package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func gscEffectiveSource(value any) string {
	if effective, ok := value.(*locationGSCEffectiveBinding); ok && effective != nil {
		return effective.Source
	}
	return ""
}

func analyticsEffectiveSource(value any) string {
	if effective, ok := value.(*locationGoogleAnalyticsEffectiveBinding); ok && effective != nil {
		return effective.Source
	}
	return ""
}

// TestLocationBindingResponseConfigured pins `configured` to exactly the
// presence of an explicit location row: absence stays false even when the
// parent binding still resolves an effective source.
func TestLocationBindingResponseConfigured(t *testing.T) {
	accountID := pgtype.UUID{Bytes: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Valid: true}
	accounts := []sqlc.GoogleConnection{testGoogleAccount(accountID.String(), "sub-1", "owner@example.com")}
	projectGSC := sqlc.ProjectGscConnection{GoogleConnectionID: accountID, SiteUrl: "https://project.example/"}
	projectAnalytics := sqlc.ProjectGoogleAnalyticsConnection{GoogleConnectionID: accountID, PropertyID: "111", PropertyDisplayName: "Project"}

	t.Run("gsc", func(t *testing.T) {
		tests := []struct {
			name           string
			binding        locationGSCBinding
			hasBinding     bool
			wantConfigured bool
			wantMode       string
			wantSource     string
		}{
			{"absent is not configured even with parent binding", locationGSCBinding{}, false, false, "inherit", "project"},
			{"explicit inherit row is configured", locationGSCBinding{Mode: "inherit"}, true, true, "inherit", "project"},
			{"explicit off row is configured", locationGSCBinding{Mode: "off"}, true, true, "off", ""},
			{"explicit custom row is configured", locationGSCBinding{Mode: "custom", GoogleConnectionID: accountID, SiteURL: pgText("https://branch.example/")}, true, true, "custom", "location"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				response := newLocationGSCBindingResponse(accounts, projectGSC, true, test.binding, test.hasBinding)
				if got := response["configured"]; got != test.wantConfigured {
					t.Fatalf("configured = %v, want %v", got, test.wantConfigured)
				}
				if got := response["mode"]; got != test.wantMode {
					t.Fatalf("mode = %v, want %v", got, test.wantMode)
				}
				if got := gscEffectiveSource(response["effective"]); got != test.wantSource {
					t.Fatalf("effective source = %q, want %q", got, test.wantSource)
				}
			})
		}
	})

	t.Run("analytics", func(t *testing.T) {
		tests := []struct {
			name           string
			binding        locationGoogleAnalyticsBinding
			hasBinding     bool
			wantConfigured bool
			wantMode       string
			wantSource     string
		}{
			{"absent is not configured even with parent selection", locationGoogleAnalyticsBinding{}, false, false, "inherit", "project"},
			{"explicit inherit row is configured", locationGoogleAnalyticsBinding{Mode: "inherit"}, true, true, "inherit", "project"},
			{"explicit off row is configured", locationGoogleAnalyticsBinding{Mode: "off"}, true, true, "off", ""},
			{"explicit custom row is configured", locationGoogleAnalyticsBinding{Mode: "custom", GoogleConnectionID: accountID, PropertyID: pgText("111")}, true, true, "custom", "location"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				response := newLocationGoogleAnalyticsBindingResponse(accounts, projectAnalytics, true, test.binding, test.hasBinding)
				if got := response["configured"]; got != test.wantConfigured {
					t.Fatalf("configured = %v, want %v", got, test.wantConfigured)
				}
				if got := response["mode"]; got != test.wantMode {
					t.Fatalf("mode = %v, want %v", got, test.wantMode)
				}
				if got := analyticsEffectiveSource(response["effective"]); got != test.wantSource {
					t.Fatalf("effective source = %q, want %q", got, test.wantSource)
				}
			})
		}
	})
}

// recordingGoogleAccountDB captures the single Exec a delete helper runs.
type recordingGoogleAccountDB struct {
	execSQL  string
	execArgs []any
}

func (db *recordingGoogleAccountDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	db.execSQL = sql
	db.execArgs = args
	return pgconn.CommandTag{}, nil
}

func (db *recordingGoogleAccountDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return nil, errors.New("recordingGoogleAccountDB does not implement Query")
}

func (db *recordingGoogleAccountDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return nil
}

// TestDeleteLocationGoogleBindingTargetsOnlyOwnRow proves each delete helper
// removes only the location's own row: never the shared account or the parent
// project selection.
func TestDeleteLocationGoogleBindingTargetsOnlyOwnRow(t *testing.T) {
	locationID := pgtype.UUID{Bytes: uuid.MustParse("33333333-3333-3333-3333-333333333333"), Valid: true}
	tests := []struct {
		name   string
		remove func(context.Context, googleAccountDB, pgtype.UUID) error
		table  string
	}{
		{"gsc", deleteLocationGSCBinding, "location_gsc_connections"},
		{"analytics", deleteLocationGoogleAnalyticsBinding, "location_google_analytics_connections"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := &recordingGoogleAccountDB{}
			if err := test.remove(context.Background(), db, locationID); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if !strings.Contains(db.execSQL, "DELETE FROM "+test.table) {
				t.Fatalf("delete must target %s, got %q", test.table, db.execSQL)
			}
			if !strings.Contains(db.execSQL, "WHERE location_id = $1") {
				t.Fatalf("delete must scope by location_id, got %q", db.execSQL)
			}
			if strings.Contains(db.execSQL, "google_connections") || strings.Contains(db.execSQL, "project_") {
				t.Fatalf("delete must not touch shared account or parent binding, got %q", db.execSQL)
			}
			if len(db.execArgs) != 1 {
				t.Fatalf("delete args = %v, want exactly the location id", db.execArgs)
			}
			if arg, ok := db.execArgs[0].(pgtype.UUID); !ok || !uuidEqual(arg, locationID) {
				t.Fatalf("delete arg = %v, want the addressed location id", db.execArgs[0])
			}
		})
	}
}

func callDeleteLocationGoogleBinding(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID, service string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodDelete, userID, map[string]string{"projectID": projectID, "locationID": locationID}, "")
	rr := httptest.NewRecorder()
	if service == "gsc" {
		app.handleDeleteLocationGSCBinding(rr, req)
	} else {
		app.handleDeleteLocationGoogleAnalyticsBinding(rr, req)
	}
	return rr
}

func callGetLocationGSCBinding(t *testing.T, app *App, userID pgtype.UUID, projectID, locationID string) *httptest.ResponseRecorder {
	t.Helper()
	req := localVisibilityRequest(t, http.MethodGet, userID, map[string]string{"projectID": projectID, "locationID": locationID}, "")
	rr := httptest.NewRecorder()
	app.handleGetLocationGSCBinding(rr, req)
	return rr
}

// TestScratchDeleteLocationGoogleBindingIsolatesOwnRow proves the owner-only
// DELETE removes only the addressed location binding, leaving the shared
// account, parent selection, sibling location binding and stored reports intact,
// and that the next GET reports configured false. Scratch only: it skips
// without LOCATION_SCRATCH_TEST_DATABASE_URL and never touches the app DB.
func TestScratchDeleteLocationGoogleBindingIsolatesOwnRow(t *testing.T) {
	app, pool, ctx, orgID, userID, projectID, locationID := scratchAppFixture(t)

	var accountID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO google_connections (organization_id, connected_by_user_id, encrypted_refresh_token, scope)
		VALUES ($1, $2, 'scratch-refresh', 'https://www.googleapis.com/auth/webmasters.readonly https://www.googleapis.com/auth/analytics.readonly')
		RETURNING id`, orgID, userID).Scan(&accountID); err != nil {
		t.Fatalf("create shared account: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_gsc_connections (project_id, google_connection_id, site_url)
		VALUES ($1, $2, 'https://scratch.example/')`, projectID, accountID); err != nil {
		t.Fatalf("create parent gsc selection: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_google_analytics_connections (project_id, google_connection_id, property_id, property_display_name)
		VALUES ($1, $2, '111', 'Scratch')`, projectID, accountID); err != nil {
		t.Fatalf("create parent analytics selection: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO location_gsc_connections (location_id, mode, google_connection_id, site_url)
		VALUES ($1, 'custom', $2, 'https://scratch.example/branch')`, locationID, accountID); err != nil {
		t.Fatalf("create location gsc binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO location_google_analytics_connections (location_id, mode, google_connection_id, property_id)
		VALUES ($1, 'custom', $2, '111')`, locationID, accountID); err != nil {
		t.Fatalf("create location analytics binding: %v", err)
	}

	var siblingLocationID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO project_locations (project_id, name, latitude, longitude)
		VALUES ($1, 'sibling', 27.7001, 85.3188) RETURNING id`, projectID).Scan(&siblingLocationID); err != nil {
		t.Fatalf("create sibling location: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO location_gsc_connections (location_id, mode, google_connection_id, site_url)
		VALUES ($1, 'custom', $2, 'https://scratch.example/sibling')`, siblingLocationID, accountID); err != nil {
		t.Fatalf("create sibling gsc binding: %v", err)
	}

	subject := fmt.Sprintf("scratchapp-member-%d-%d", time.Now().UnixNano(), orgID.Bytes[0])
	var memberID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test',$1,$2) RETURNING id`,
		subject, subject+"@example.com").Scan(&memberID); err != nil {
		t.Fatalf("create member user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'member')`, orgID, memberID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM organization_members WHERE user_id = $1`, memberID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, memberID)
	})

	countRows := func(query string, arg pgtype.UUID) int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, query, arg).Scan(&count); err != nil {
			t.Fatalf("count: %v", err)
		}
		return count
	}

	// A non-owner member cannot delete; the binding must survive.
	rr := callDeleteLocationGoogleBinding(t, app, memberID, projectID.String(), locationID.String(), "gsc")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("member delete gsc = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	if got := countRows(`SELECT count(*) FROM location_gsc_connections WHERE location_id = $1`, locationID); got != 1 {
		t.Fatalf("forbidden delete changed bindings: %d", got)
	}

	// Owner deletes GSC: only this location's binding goes.
	rr = callDeleteLocationGoogleBinding(t, app, userID, projectID.String(), locationID.String(), "gsc")
	if rr.Code != http.StatusOK {
		t.Fatalf("owner delete gsc = %d: %s", rr.Code, rr.Body.String())
	}
	if got := countRows(`SELECT count(*) FROM location_gsc_connections WHERE location_id = $1`, locationID); got != 0 {
		t.Fatalf("location gsc binding rows after delete = %d, want 0", got)
	}
	if got := countRows(`SELECT count(*) FROM project_gsc_connections WHERE project_id = $1`, projectID); got != 1 {
		t.Fatalf("parent gsc selection rows = %d, want 1 (preserved)", got)
	}
	if got := countRows(`SELECT count(*) FROM google_connections WHERE id = $1`, accountID); got != 1 {
		t.Fatalf("shared account rows = %d, want 1 (preserved)", got)
	}
	if got := countRows(`SELECT count(*) FROM location_gsc_connections WHERE location_id = $1`, siblingLocationID); got != 1 {
		t.Fatalf("sibling location binding rows = %d, want 1 (preserved)", got)
	}
	if got := countRows(`SELECT count(*) FROM location_google_analytics_connections WHERE location_id = $1`, locationID); got != 1 {
		t.Fatalf("analytics binding rows after gsc delete = %d, want 1 (untouched)", got)
	}

	// The next GET reports the binding as not configured.
	rr = callGetLocationGSCBinding(t, app, userID, projectID.String(), locationID.String())
	if rr.Code != http.StatusOK {
		t.Fatalf("get gsc binding = %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode binding: %v", err)
	}
	if configured, _ := body["configured"].(bool); configured {
		t.Fatalf("configured after delete = %v, want false", body["configured"])
	}

	// Owner deletes Analytics independently; the shared account and parent
	// analytics selection stay.
	rr = callDeleteLocationGoogleBinding(t, app, userID, projectID.String(), locationID.String(), "analytics")
	if rr.Code != http.StatusOK {
		t.Fatalf("owner delete analytics = %d: %s", rr.Code, rr.Body.String())
	}
	if got := countRows(`SELECT count(*) FROM location_google_analytics_connections WHERE location_id = $1`, locationID); got != 0 {
		t.Fatalf("location analytics binding rows after delete = %d, want 0", got)
	}
	if got := countRows(`SELECT count(*) FROM project_google_analytics_connections WHERE project_id = $1`, projectID); got != 1 {
		t.Fatalf("parent analytics selection rows = %d, want 1 (preserved)", got)
	}
	if got := countRows(`SELECT count(*) FROM google_connections WHERE id = $1`, accountID); got != 1 {
		t.Fatalf("shared account rows after analytics delete = %d, want 1 (preserved)", got)
	}
}
