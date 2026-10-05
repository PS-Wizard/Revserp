package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Admin Maps spending allowance handler tests. Validation and admin-gate tests
// need no database. Defaults and optimistic updates connect only to the
// disposable database named by LOCAL_SEO_TEST_DATABASE_URL and skip when absent.

func requireMapsSpendingAllowances(t *testing.T, pool *pgxpool.Pool, ctx context.Context) {
	t.Helper()
	for _, table := range []string{"public.organization_maps_credit_budgets", "public.platform_maps_credit_budget"} {
		var regclass string
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&regclass); err != nil || regclass == "" {
			t.Skipf("table %s is not migrated in the test database", table)
		}
	}
	var trigger bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'organization_maps_allowance_on_create')`).Scan(&trigger); err != nil || !trigger {
		t.Skip("maps spending allowance migration is not applied in the test database")
	}
}

func callAdminGetMapsBudget(t *testing.T, app *App, userID pgtype.UUID, orgID string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodGet, userID, map[string]string{"orgID": orgID}, "")
	rr := httptest.NewRecorder()
	app.handleAdminGetMapsBudget(rr, req)
	return rr
}

func callAdminPutMapsBudget(t *testing.T, app *App, userID pgtype.UUID, orgID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodPut, userID, map[string]string{"orgID": orgID}, body)
	rr := httptest.NewRecorder()
	app.handleAdminPutMapsBudget(rr, req)
	return rr
}

func callAdminGetPlatformMapsBudget(t *testing.T, app *App, userID pgtype.UUID) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodGet, userID, nil, "")
	rr := httptest.NewRecorder()
	app.handleAdminGetPlatformMapsBudget(rr, req)
	return rr
}

func callAdminPutPlatformMapsBudget(t *testing.T, app *App, userID pgtype.UUID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodPut, userID, nil, body)
	rr := httptest.NewRecorder()
	app.handleAdminPutPlatformMapsBudget(rr, req)
	return rr
}

func decodeMapsBudget(t *testing.T, rr *httptest.ResponseRecorder) mapsBudgetResponse {
	t.Helper()
	var got mapsBudgetResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode budget response: %v body=%s", err, rr.Body.String())
	}
	return got
}

func TestAdminMapsBudgetDefaults(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	requireMapsSpendingAllowances(t, fx.pool, fx.ctx)

	// Migration 90 provisions every new organization with a 500 credit allowance.
	var remaining, reserved, spent int64
	if err := fx.pool.QueryRow(fx.ctx, `SELECT remaining_credits, reserved_credits, spent_credits
		FROM organization_maps_credit_budgets WHERE organization_id = $1`, fx.orgID).Scan(&remaining, &reserved, &spent); err != nil {
		t.Fatalf("read new organization allowance: %v", err)
	}
	if remaining != 500 || reserved != 0 || spent != 0 {
		t.Fatalf("new organization allowance = %d/%d/%d, want 500/0/0", remaining, reserved, spent)
	}

	// The platform allowance is globally shared and other fixture tests mutate
	// it, so assert the migrated column default rather than the live value.
	var platformDefault string
	if err := fx.pool.QueryRow(fx.ctx, `SELECT column_default FROM information_schema.columns
		WHERE table_name = 'platform_maps_credit_budget' AND column_name = 'remaining_credits'`).Scan(&platformDefault); err != nil {
		t.Fatalf("read platform default: %v", err)
	}
	if platformDefault != "5000" {
		t.Fatalf("platform remaining_credits default = %q, want 5000", platformDefault)
	}
}

func TestAdminMapsBudgetOrganizationGetPut(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	requireMapsSpendingAllowances(t, fx.pool, fx.ctx)

	// Seed nonzero reserved/spent counters to prove an allowance change leaves
	// them untouched. They must be restored before the fixture drops the org.
	if _, err := fx.pool.Exec(fx.ctx, `UPDATE organization_maps_credit_budgets
		SET reserved_credits = 40, spent_credits = 7 WHERE organization_id = $1`, fx.orgID); err != nil {
		t.Fatalf("seed counters: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(context.Background(), `UPDATE organization_maps_credit_budgets
			SET reserved_credits = 0, spent_credits = 0 WHERE organization_id = $1`, fx.orgID)
	})

	var startRemaining int64
	if err := fx.pool.QueryRow(fx.ctx, `SELECT remaining_credits FROM organization_maps_credit_budgets
		WHERE organization_id = $1`, fx.orgID).Scan(&startRemaining); err != nil {
		t.Fatalf("read starting allowance: %v", err)
	}

	// GET computes available_credits as remaining minus reserved.
	rr := callAdminGetMapsBudget(t, fx.app, fx.ownerID, fx.orgID.String())
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", rr.Code, rr.Body.String())
	}
	wantGet := mapsBudgetResponse{RemainingCredits: startRemaining, ReservedCredits: 40, SpentCredits: 7, AvailableCredits: startRemaining - 40}
	if got := decodeMapsBudget(t, rr); got != wantGet {
		t.Fatalf("GET = %#v, want %#v", got, wantGet)
	}

	// A PUT with the observed remaining as the optimistic expectation succeeds
	// and preserves reserved/spent.
	rr = callAdminPutMapsBudget(t, fx.app, fx.ownerID, fx.orgID.String(),
		fmt.Sprintf(`{"remaining_credits":%d,"expected_remaining_credits":%d}`, startRemaining+300, startRemaining))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", rr.Code, rr.Body.String())
	}
	wantPut := mapsBudgetResponse{RemainingCredits: startRemaining + 300, ReservedCredits: 40, SpentCredits: 7, AvailableCredits: startRemaining + 260}
	if got := decodeMapsBudget(t, rr); got != wantPut {
		t.Fatalf("PUT = %#v, want %#v", got, wantPut)
	}

	// A stale expectation conflicts and leaves the row unchanged.
	rr = callAdminPutMapsBudget(t, fx.app, fx.ownerID, fx.orgID.String(),
		fmt.Sprintf(`{"remaining_credits":1,"expected_remaining_credits":%d}`, startRemaining))
	if rr.Code != http.StatusConflict {
		t.Fatalf("stale PUT status = %d, want 409 body=%s", rr.Code, rr.Body.String())
	}
	rr = callAdminGetMapsBudget(t, fx.app, fx.ownerID, fx.orgID.String())
	if got := decodeMapsBudget(t, rr); got != wantPut {
		t.Fatalf("stale PUT mutated row: %#v, want %#v", got, wantPut)
	}

	// The upper bound is inclusive: exactly 1000000000 is accepted.
	rr = callAdminPutMapsBudget(t, fx.app, fx.ownerID, fx.orgID.String(),
		fmt.Sprintf(`{"remaining_credits":1000000000,"expected_remaining_credits":%d}`, startRemaining+300))
	if rr.Code != http.StatusOK {
		t.Fatalf("boundary PUT status = %d, want 200 body=%s", rr.Code, rr.Body.String())
	}
	if got := decodeMapsBudget(t, rr); got.RemainingCredits != 1000000000 {
		t.Fatalf("boundary PUT remaining = %d, want 1000000000", got.RemainingCredits)
	}
}

func TestAdminMapsBudgetPlatformGetPut(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	requireMapsSpendingAllowances(t, fx.pool, fx.ctx)

	var original mapsBudgetResponse
	if err := fx.pool.QueryRow(fx.ctx, `SELECT remaining_credits, reserved_credits, spent_credits
		FROM platform_maps_credit_budget WHERE id = TRUE`).Scan(
		&original.RemainingCredits, &original.ReservedCredits, &original.SpentCredits); err != nil {
		t.Fatalf("read platform budget: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(context.Background(), `UPDATE platform_maps_credit_budget
			SET remaining_credits = $1, reserved_credits = $2, spent_credits = $3 WHERE id = TRUE`,
			original.RemainingCredits, original.ReservedCredits, original.SpentCredits)
	})

	wantGet := mapsBudgetResponse{RemainingCredits: original.RemainingCredits, ReservedCredits: original.ReservedCredits, SpentCredits: original.SpentCredits, AvailableCredits: original.RemainingCredits - original.ReservedCredits}
	rr := callAdminGetPlatformMapsBudget(t, fx.app, fx.ownerID)
	if rr.Code != http.StatusOK {
		t.Fatalf("platform GET status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := decodeMapsBudget(t, rr); got != wantGet {
		t.Fatalf("platform GET = %#v, want %#v", got, wantGet)
	}

	wantPut := mapsBudgetResponse{RemainingCredits: original.RemainingCredits + 25, ReservedCredits: original.ReservedCredits, SpentCredits: original.SpentCredits, AvailableCredits: original.RemainingCredits + 25 - original.ReservedCredits}
	rr = callAdminPutPlatformMapsBudget(t, fx.app, fx.ownerID,
		fmt.Sprintf(`{"remaining_credits":%d,"expected_remaining_credits":%d}`, original.RemainingCredits+25, original.RemainingCredits))
	if rr.Code != http.StatusOK {
		t.Fatalf("platform PUT status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := decodeMapsBudget(t, rr); got != wantPut {
		t.Fatalf("platform PUT = %#v, want %#v", got, wantPut)
	}

	rr = callAdminPutPlatformMapsBudget(t, fx.app, fx.ownerID,
		fmt.Sprintf(`{"remaining_credits":1,"expected_remaining_credits":%d}`, original.RemainingCredits))
	if rr.Code != http.StatusConflict {
		t.Fatalf("platform stale PUT status = %d, want 409 body=%s", rr.Code, rr.Body.String())
	}
	rr = callAdminGetPlatformMapsBudget(t, fx.app, fx.ownerID)
	if got := decodeMapsBudget(t, rr); got != wantPut {
		t.Fatalf("platform stale PUT mutated row: %#v, want %#v", got, wantPut)
	}
}

func TestAdminMapsBudgetValidation(t *testing.T) {
	app := &App{}
	var userID pgtype.UUID
	_ = userID.Scan("00000000-0000-0000-0000-000000000001")
	orgID := "00000000-0000-0000-0000-000000000002"

	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing remaining_credits", `{"expected_remaining_credits":0}`},
		{"missing expected_remaining_credits", `{"remaining_credits":10}`},
		{"negative remaining_credits", `{"remaining_credits":-1,"expected_remaining_credits":0}`},
		{"null remaining_credits", `{"remaining_credits":null,"expected_remaining_credits":0}`},
		{"remaining_credits above maximum", `{"remaining_credits":1000000001,"expected_remaining_credits":0}`},
		{"non-integer remaining_credits", `{"remaining_credits":1.5,"expected_remaining_credits":0}`},
		{"unexpected field", `{"remaining_credits":10,"expected_remaining_credits":0,"extra":1}`},
		{"null body", `null`},
		{"invalid json", `{"remaining_credits":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rr := callAdminPutMapsBudget(t, app, userID, orgID, tc.body); rr.Code != http.StatusBadRequest {
				t.Errorf("organization PUT status = %d, want 400; body = %s", rr.Code, rr.Body.String())
			}
			if rr := callAdminPutPlatformMapsBudget(t, app, userID, tc.body); rr.Code != http.StatusBadRequest {
				t.Errorf("platform PUT status = %d, want 400; body = %s", rr.Code, rr.Body.String())
			}
		})
	}

	if rr := callAdminPutMapsBudget(t, app, userID, "not-a-uuid", `{"remaining_credits":10,"expected_remaining_credits":0}`); rr.Code != http.StatusBadRequest {
		t.Errorf("invalid organization id status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

func TestAdminMapsBudgetRequiresPlatformAdmin(t *testing.T) {
	app := &App{}
	var nonAdmin pgtype.UUID
	_ = nonAdmin.Scan("00000000-0000-0000-0000-000000000001")
	orgID := "00000000-0000-0000-0000-000000000002"
	const validBody = `{"remaining_credits":10,"expected_remaining_credits":0}`

	for _, tc := range []struct {
		name string
		call func() *httptest.ResponseRecorder
	}{
		{"organization GET", func() *httptest.ResponseRecorder {
			req := localVisibilityRequest(t, http.MethodGet, nonAdmin, map[string]string{"orgID": orgID}, "")
			rr := httptest.NewRecorder()
			app.platformAdminOnly(app.handleAdminGetMapsBudget)(rr, req)
			return rr
		}},
		{"organization PUT", func() *httptest.ResponseRecorder {
			req := localVisibilityRequest(t, http.MethodPut, nonAdmin, map[string]string{"orgID": orgID}, validBody)
			rr := httptest.NewRecorder()
			app.platformAdminOnly(app.handleAdminPutMapsBudget)(rr, req)
			return rr
		}},
		{"platform GET", func() *httptest.ResponseRecorder {
			req := localVisibilityRequest(t, http.MethodGet, nonAdmin, nil, "")
			rr := httptest.NewRecorder()
			app.platformAdminOnly(app.handleAdminGetPlatformMapsBudget)(rr, req)
			return rr
		}},
		{"platform PUT", func() *httptest.ResponseRecorder {
			req := localVisibilityRequest(t, http.MethodPut, nonAdmin, nil, validBody)
			rr := httptest.NewRecorder()
			app.platformAdminOnly(app.handleAdminPutPlatformMapsBudget)(rr, req)
			return rr
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rr := tc.call(); rr.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403; body = %s", rr.Code, rr.Body.String())
			}
		})
	}
}
