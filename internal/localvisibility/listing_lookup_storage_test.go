package localvisibility

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/serper"
)

// Paid listing lookup storage tests. They run only against the disposable
// database (LOCAL_SEO_TEST_DATABASE_URL) and exercise the treasury behaviour
// directly, without any paid provider call.

const listingLookupFakePlatformCredits int64 = 1000000

type listingLookupFixture struct {
	pool       *pgxpool.Pool
	ctx        context.Context
	userID     pgtype.UUID
	orgID      pgtype.UUID
	projectID  pgtype.UUID
	locationID pgtype.UUID
}

type listingLookupBudget struct {
	remaining int64
	reserved  int64
	spent     int64
}

func (f listingLookupFixture) platformBudget(t *testing.T) listingLookupBudget {
	t.Helper()
	var budget listingLookupBudget
	if err := f.pool.QueryRow(f.ctx, `SELECT remaining_credits, reserved_credits, spent_credits FROM platform_maps_credit_budget WHERE id = TRUE`).Scan(&budget.remaining, &budget.reserved, &budget.spent); err != nil {
		t.Fatalf("read platform budget: %v", err)
	}
	return budget
}

func (f listingLookupFixture) orgBudget(t *testing.T) listingLookupBudget {
	t.Helper()
	var budget listingLookupBudget
	if err := f.pool.QueryRow(f.ctx, `SELECT remaining_credits, reserved_credits, spent_credits FROM organization_maps_credit_budgets WHERE organization_id = $1`, f.orgID).Scan(&budget.remaining, &budget.reserved, &budget.spent); err != nil {
		t.Fatalf("read org budget: %v", err)
	}
	return budget
}

func (f listingLookupFixture) lookupCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM local_listing_lookups WHERE location_id=$1`, f.locationID).Scan(&count); err != nil {
		t.Fatalf("count lookups: %v", err)
	}
	return count
}

func newListingLookupFixture(t *testing.T) listingLookupFixture {
	t.Helper()
	pool, ctx := newLocalRunTestPool(t)
	var regclass string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.local_listing_lookups')::text`).Scan(&regclass); err != nil || regclass == "" {
		t.Skip("local_listing_lookups is not migrated in the test database")
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var userID, orgID, projectID, locationID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', $1, $2) RETURNING id`,
		"ll-user-"+suffix, "ll-user-"+suffix+"@example.invalid").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, "ll-org-"+suffix).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, userID); err != nil {
		t.Fatalf("create membership: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1, $2, 'https://ll-test.example.invalid') RETURNING id`,
		orgID, "ll-project-"+suffix).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	queriesJSON, _ := json.Marshal([]string{"a", "b", "c", "d", "e"})
	if err := pool.QueryRow(ctx, `INSERT INTO project_locations (project_id, name, latitude, longitude, queries, address, locality, query_service)
		VALUES ($1, $2, 27.6942, 85.3123, $3, '1 Test Street', 'Testville', 'coffee') RETURNING id`,
		projectID, "ll-location-"+suffix, queriesJSON).Scan(&locationID); err != nil {
		t.Fatalf("create location: %v", err)
	}

	// The org is dedicated to this fixture; the platform singleton is shared
	// with the manual UI, so only a reversible fake top-up is applied.
	if _, err := pool.Exec(ctx, `UPDATE organization_maps_credit_budgets SET remaining_credits=$1, reserved_credits=0, spent_credits=0 WHERE organization_id=$2`, listingLookupFakePlatformCredits, orgID); err != nil {
		t.Fatalf("fund org budget: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE platform_maps_credit_budget SET remaining_credits = remaining_credits + $1 WHERE id = TRUE`, listingLookupFakePlatformCredits); err != nil {
		t.Fatalf("fund platform budget: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		var spent, reserved int64
		_ = pool.QueryRow(cleanupCtx, `SELECT spent_credits, reserved_credits FROM organization_maps_credit_budgets WHERE organization_id=$1`, orgID).Scan(&spent, &reserved)
		// Delete only this fixture's data so the location deletion guard
		// cannot block teardown and no other org is touched.
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM local_listing_lookups WHERE location_id=$1`, locationID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM local_visibility_runs WHERE location_id=$1`, locationID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM projects WHERE id=$1`, projectID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(cleanupCtx, `UPDATE platform_maps_credit_budget
			SET remaining_credits = remaining_credits - $1 + $2,
			    reserved_credits = GREATEST(reserved_credits - $3, 0),
			    spent_credits = GREATEST(spent_credits - $2, 0)
			WHERE id = TRUE`, listingLookupFakePlatformCredits, spent, reserved)
	})

	return listingLookupFixture{pool: pool, ctx: ctx, userID: userID, orgID: orgID, projectID: projectID, locationID: locationID}
}

func listingLookupTestSelection(key string) ListingLookupSelection {
	return ListingLookupSelection{Query: "Test Cafe Testville", CandidateKey: key, Latitude: 27.6942, Longitude: 85.3123}
}

func listingLookupPlace(placeID string, lat, lon float64) serper.MapsListingPlace {
	return serper.MapsListingPlace{PlaceID: placeID, Title: "Test Cafe", Address: "1 Test Street", Latitude: &lat, Longitude: &lon}
}

func listingLookupPlaceNoCoords(placeID string) serper.MapsListingPlace {
	return serper.MapsListingPlace{PlaceID: placeID, Title: "No Coords Cafe"}
}

func TestListingLookupReserveAndRecordChargesBothAllowances(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	basePlatform := f.platformBudget(t)
	baseOrg := f.orgBudget(t)

	lookup, created, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-a"))
	if err != nil {
		t.Fatalf("reserve lookup: %v", err)
	}
	if !created {
		t.Fatalf("first reserve created = false, want true")
	}
	if lookup.Status != "running" || lookup.ExpectedCredits != 3 || lookup.ReservedCredits != 3 || lookup.CreditsUsed != 0 || lookup.CreditKnown {
		t.Fatalf("reserved lookup = %#v", lookup)
	}
	if lookup.CandidateKey != "candidate-a" || lookup.Query != "Test Cafe Testville" {
		t.Fatalf("reserved lookup query/key = %q/%q", lookup.Query, lookup.CandidateKey)
	}
	if got := f.platformBudget(t); got.reserved-basePlatform.reserved != 3 || got.spent != basePlatform.spent {
		t.Fatalf("platform after reserve = %#v, want reserved +3", got)
	}
	if got := f.orgBudget(t); got.reserved-baseOrg.reserved != 3 || got.spent != baseOrg.spent {
		t.Fatalf("org after reserve = %#v, want reserved +3", got)
	}

	recorded, err := store.RecordListingLookup(f.ctx, lookup.ID, serper.MapsListingResponse{Credits: 3, Places: []serper.MapsListingPlace{listingLookupPlace("real-place", 27.6942, 85.3123)}}, nil)
	if err != nil {
		t.Fatalf("record lookup: %v", err)
	}
	if recorded.Status != "completed" || recorded.CreditsUsed != 3 || recorded.ReservedCredits != 0 || !recorded.CreditKnown {
		t.Fatalf("recorded lookup = %#v", recorded)
	}
	if got := f.platformBudget(t); got.spent-basePlatform.spent != 3 || got.reserved != basePlatform.reserved {
		t.Fatalf("platform after record = %#v, want spent +3 reserved back", got)
	}
	if got := f.orgBudget(t); got.spent-baseOrg.spent != 3 || got.reserved != baseOrg.reserved {
		t.Fatalf("org after record = %#v, want spent +3 reserved back", got)
	}
}

func TestListingLookupUnfundedOrgBlocksReservation(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	if _, err := f.pool.Exec(f.ctx, `UPDATE organization_maps_credit_budgets SET remaining_credits=0 WHERE organization_id=$1`, f.orgID); err != nil {
		t.Fatalf("exhaust org allowance: %v", err)
	}
	basePlatform := f.platformBudget(t)

	if _, _, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-a")); !errors.Is(err, ErrMapsBudgetUnavailable) {
		t.Fatalf("reserve error = %v, want ErrMapsBudgetUnavailable", err)
	}
	if got := f.platformBudget(t); got != basePlatform {
		t.Fatalf("unfunded reserve leaked a platform reservation: %#v -> %#v", basePlatform, got)
	}
	if count := f.lookupCount(t); count != 0 {
		t.Fatalf("lookup rows = %d, want 0", count)
	}
}

func TestListingLookupEmptyCompletedVsCIDOnlyFailure(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	base := f.orgBudget(t)
	lat, lon := 27.6942, 85.3123
	for _, tc := range []struct {
		name       string
		key        string
		places     []serper.MapsListingPlace
		wantStatus string
		wantError  bool
	}{
		// A genuine empty places array is a completed no-match, not a failure.
		{"genuine empty places array completes", "candidate-empty", []serper.MapsListingPlace{}, "completed", false},
		// Results carrying only a CID have no bindable placeId and must fail while
		// still settling the actual charge.
		{"cid-only results fail without a bindable placeId", "candidate-cid", []serper.MapsListingPlace{{CID: "1234567890", Title: "CID Only Cafe", Latitude: &lat, Longitude: &lon}}, "failed", true},
		// A placeId without map coordinates is not bindable for a 3-credit candidate.
		{"place id without coordinates fails", "candidate-nocoords", []serper.MapsListingPlace{listingLookupPlaceNoCoords("lonely-place")}, "failed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup, created, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection(tc.key))
			if err != nil {
				t.Fatalf("reserve lookup: %v", err)
			}
			if !created {
				t.Fatalf("different candidate created = false, want true")
			}
			recorded, err := store.RecordListingLookup(f.ctx, lookup.ID, serper.MapsListingResponse{Credits: 3, Places: tc.places}, nil)
			if err != nil {
				t.Fatalf("record lookup: %v", err)
			}
			if recorded.Status != tc.wantStatus || recorded.CreditsUsed != 3 || recorded.ReservedCredits != 0 || !recorded.CreditKnown {
				t.Fatalf("lookup = %#v, want status %q with charge settled", recorded, tc.wantStatus)
			}
			if recorded.Error.Valid != tc.wantError {
				t.Fatalf("lookup error = %v, want error presence %v", recorded.Error, tc.wantError)
			}
			if tc.wantError && !strings.Contains(recorded.Error.String, "bindable placeId") {
				t.Fatalf("lookup error = %q, want bindable placeId message", recorded.Error.String)
			}
		})
	}
	// All three lookups were charged even though two produced no bindable listing.
	if got := f.orgBudget(t); got.spent-base.spent != 9 || got.reserved != base.reserved {
		t.Fatalf("lookup charges org budget = %#v, want spent +9 reserved back", got)
	}
}

func TestListingLookupChargedProviderErrorIsFailed(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	base := f.orgBudget(t)
	lookup, _, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-a"))
	if err != nil {
		t.Fatalf("reserve lookup: %v", err)
	}
	recorded, err := store.RecordListingLookup(f.ctx, lookup.ID, serper.MapsListingResponse{Credits: 3}, errors.New("status 500: provider error"))
	if err != nil {
		t.Fatalf("record lookup: %v", err)
	}
	if recorded.Status != "failed" || recorded.CreditsUsed != 3 || recorded.ReservedCredits != 0 || !recorded.CreditKnown || !recorded.Error.Valid {
		t.Fatalf("charged error lookup = %#v, want failed with spend recorded", recorded)
	}
	if got := f.orgBudget(t); got.spent-base.spent != 3 || got.reserved != base.reserved {
		t.Fatalf("charged error org budget = %#v, want spent +3 reserved back", got)
	}
}

func TestListingLookupPriceMismatchFailsWithActualSpend(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	base := f.orgBudget(t)
	lookup, _, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-a"))
	if err != nil {
		t.Fatalf("reserve lookup: %v", err)
	}
	recorded, err := store.RecordListingLookup(f.ctx, lookup.ID, serper.MapsListingResponse{Credits: 5, Places: []serper.MapsListingPlace{listingLookupPlace("real-place", 27.6942, 85.3123)}}, nil)
	if err != nil {
		t.Fatalf("record lookup: %v", err)
	}
	if recorded.Status != "failed" || recorded.CreditsUsed != 5 || recorded.ReservedCredits != 0 || !recorded.CreditKnown {
		t.Fatalf("mismatch lookup = %#v, want failed with actual spend 5", recorded)
	}
	if got := f.orgBudget(t); got.spent-base.spent != 5 || got.reserved != base.reserved {
		t.Fatalf("mismatch org budget = %#v, want spent +5 reserved back", got)
	}
}

func TestListingLookupUnknownChargeHoldsReservation(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	base := f.orgBudget(t)
	basePlatform := f.platformBudget(t)
	for _, tc := range []struct {
		name string
		key  string
		err  error
	}{
		{"transport failure", "candidate-a", errors.New("connection reset")},
		{"decode failure", "candidate-b", errors.New("serper listing lookup: decode response: unexpected end of JSON input")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup, _, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection(tc.key))
			if err != nil {
				t.Fatalf("reserve lookup: %v", err)
			}
			recorded, err := store.RecordListingLookup(f.ctx, lookup.ID, serper.MapsListingResponse{}, tc.err)
			if err != nil {
				t.Fatalf("record lookup: %v", err)
			}
			if recorded.Status != "uncertain" || recorded.CreditKnown || recorded.CreditsUsed != 0 || recorded.ReservedCredits != 3 {
				t.Fatalf("unknown lookup = %#v, want uncertain with reservation held", recorded)
			}
			// Delete the unresolved row so the next subtest can reserve again.
			if _, err := f.pool.Exec(f.ctx, `DELETE FROM local_listing_lookups WHERE id=$1`, lookup.ID); err != nil {
				t.Fatalf("delete unresolved lookup: %v", err)
			}
		})
	}
	// Both unknown charges keep their reservation in the treasury; only the
	// evidence rows were removed to free the unsettled slot between subtests.
	if got := f.orgBudget(t); got.reserved-base.reserved != 6 || got.spent != base.spent {
		t.Fatalf("unknown charges org budget = %#v, want two held reservations", got)
	}
	if got := f.platformBudget(t); got.reserved-basePlatform.reserved != 6 || got.spent != basePlatform.spent {
		t.Fatalf("unknown charges platform budget = %#v, want two held reservations", got)
	}
}

func TestListingLookupDuplicateActiveIsConflict(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	if _, _, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-a")); err != nil {
		t.Fatalf("first reserve lookup: %v", err)
	}
	basePlatform := f.platformBudget(t)

	_, _, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-b"))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("second reserve error = %v, want unique violation", err)
	}
	if got := f.platformBudget(t); got != basePlatform {
		t.Fatalf("conflicting reserve leaked a reservation: %#v -> %#v", basePlatform, got)
	}
}

func TestListingLookupUncertainBlocksDifferentCandidate(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	lookup, _, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-a"))
	if err != nil {
		t.Fatalf("reserve lookup: %v", err)
	}
	if _, err := store.RecordListingLookup(f.ctx, lookup.ID, serper.MapsListingResponse{}, errors.New("connection reset")); err != nil {
		t.Fatalf("record uncertain: %v", err)
	}
	basePlatform := f.platformBudget(t)
	_, _, err = store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-b"))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("uncertain reserve error = %v, want unique violation", err)
	}
	if got := f.platformBudget(t); got != basePlatform {
		t.Fatalf("uncertain reserve leaked a reservation: %#v -> %#v", basePlatform, got)
	}
}

func TestListingLookupDedupSameCandidateReturnsWithoutNewCharge(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	lookup, _, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-a"))
	if err != nil {
		t.Fatalf("reserve lookup: %v", err)
	}
	if _, err := store.RecordListingLookup(f.ctx, lookup.ID, serper.MapsListingResponse{Credits: 3, Places: []serper.MapsListingPlace{{CID: "123", Title: "CID only"}}}, nil); err != nil {
		t.Fatalf("record failed lookup: %v", err)
	}
	basePlatform := f.platformBudget(t)
	baseOrg := f.orgBudget(t)

	again, created, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-a"))
	if err != nil {
		t.Fatalf("dedup reserve: %v", err)
	}
	if created {
		t.Fatalf("dedup created = true, want false")
	}
	if again.ID != lookup.ID || again.Status != "failed" {
		t.Fatalf("dedup lookup = %#v, want original failed row", again)
	}
	if got := f.platformBudget(t); got != basePlatform {
		t.Fatalf("dedup changed platform budget: %#v -> %#v", basePlatform, got)
	}
	if got := f.orgBudget(t); got != baseOrg {
		t.Fatalf("dedup changed org budget: %#v -> %#v", baseOrg, got)
	}
	if count := f.lookupCount(t); count != 1 {
		t.Fatalf("lookup rows = %d, want 1", count)
	}
}

func TestListingLookupDifferentCandidateReservesAgain(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	first, _, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-a"))
	if err != nil {
		t.Fatalf("reserve first: %v", err)
	}
	if _, err := store.RecordListingLookup(f.ctx, first.ID, serper.MapsListingResponse{Credits: 3}, nil); err != nil {
		t.Fatalf("record first: %v", err)
	}
	basePlatform := f.platformBudget(t)
	baseOrg := f.orgBudget(t)

	second, created, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-b"))
	if err != nil {
		t.Fatalf("reserve second: %v", err)
	}
	if !created {
		t.Fatalf("different candidate created = false, want true")
	}
	if second.ID == first.ID || second.CandidateKey != "candidate-b" || second.ExpectedCredits != 3 || second.ReservedCredits != 3 {
		t.Fatalf("second lookup = %#v", second)
	}
	if got := f.platformBudget(t); got.reserved-basePlatform.reserved != 3 {
		t.Fatalf("platform after second reserve = %#v, want reserved +3", got)
	}
	if got := f.orgBudget(t); got.reserved-baseOrg.reserved != 3 {
		t.Fatalf("org after second reserve = %#v, want reserved +3", got)
	}
	if count := f.lookupCount(t); count != 2 {
		t.Fatalf("lookup rows = %d, want 2", count)
	}
}

func TestListingLookupHistoricalOneCreditSettlesAtOne(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	queries := sqlc.New(f.pool)
	base := f.orgBudget(t)
	basePlatform := f.platformBudget(t)
	if _, err := queries.ReservePlatformMapsCredits(f.ctx, 1); err != nil {
		t.Fatalf("reserve platform 1: %v", err)
	}
	if _, err := queries.ReserveOrganizationMapsCredits(f.ctx, sqlc.ReserveOrganizationMapsCreditsParams{OrganizationID: f.orgID, Credits: 1}); err != nil {
		t.Fatalf("reserve org 1: %v", err)
	}
	historical, err := queries.CreateLocationListingLookup(f.ctx, sqlc.CreateLocationListingLookupParams{
		LocationID:      f.locationID,
		Query:           "Old Query",
		ExpectedCredits: 1,
		CandidateKey:    "",
		SourceLatitude:  27.6942,
		SourceLongitude: 85.3123,
	})
	if err != nil {
		t.Fatalf("create historical lookup: %v", err)
	}
	// Old 1-credit policy needs only a placeId; coordinates were not required.
	recorded, err := store.RecordListingLookup(f.ctx, historical.ID, serper.MapsListingResponse{Credits: 1, Places: []serper.MapsListingPlace{listingLookupPlaceNoCoords("old-place")}}, nil)
	if err != nil {
		t.Fatalf("record historical: %v", err)
	}
	if recorded.Status != "completed" || recorded.CreditsUsed != 1 || recorded.ReservedCredits != 0 || recorded.ExpectedCredits != 1 || !recorded.CreditKnown {
		t.Fatalf("historical lookup = %#v, want completed at frozen price 1", recorded)
	}
	if got := f.orgBudget(t); got.spent-base.spent != 1 || got.reserved != base.reserved {
		t.Fatalf("historical org budget = %#v, want spent +1 reserved back", got)
	}
	if got := f.platformBudget(t); got.spent-basePlatform.spent != 1 || got.reserved != basePlatform.reserved {
		t.Fatalf("historical platform budget = %#v, want spent +1 reserved back", got)
	}
}

func TestListingLookupRecordIsIdempotent(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	lookup, _, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, listingLookupTestSelection("candidate-a"))
	if err != nil {
		t.Fatalf("reserve lookup: %v", err)
	}
	recorded, err := store.RecordListingLookup(f.ctx, lookup.ID, serper.MapsListingResponse{Credits: 3, Places: []serper.MapsListingPlace{listingLookupPlace("real-place", 27.6942, 85.3123)}}, nil)
	if err != nil {
		t.Fatalf("record lookup: %v", err)
	}
	if recorded.Status != "completed" || recorded.CreditsUsed != 3 {
		t.Fatalf("recorded lookup = %#v", recorded)
	}

	basePlatform := f.platformBudget(t)
	baseOrg := f.orgBudget(t)

	again, err := store.RecordListingLookup(f.ctx, lookup.ID, serper.MapsListingResponse{Credits: 3, Places: []serper.MapsListingPlace{listingLookupPlace("real-place", 27.6942, 85.3123)}}, nil)
	if err != nil {
		t.Fatalf("second record lookup: %v", err)
	}
	if again.Status != "completed" || again.CreditsUsed != 3 || again.ReservedCredits != 0 {
		t.Fatalf("second record changed the lookup = %#v", again)
	}
	if got := f.platformBudget(t); got != basePlatform {
		t.Fatalf("second record double-spent platform: %#v -> %#v", basePlatform, got)
	}
	if got := f.orgBudget(t); got != baseOrg {
		t.Fatalf("second record double-spent org: %#v -> %#v", baseOrg, got)
	}
}

func TestListingLookupBoundLocationDoesNotReserve(t *testing.T) {
	fixture := newListingLookupFixture(t)
	if _, err := fixture.pool.Exec(fixture.ctx, `UPDATE project_locations SET place_id='bound-test-place' WHERE id=$1`, fixture.locationID); err != nil {
		t.Fatal(err)
	}
	before := fixture.platformBudget(t)
	_, _, err := (ListingLookupStore{Pool: fixture.pool}).ReserveListingLookup(fixture.ctx, fixture.userID, fixture.projectID, fixture.locationID, listingLookupTestSelection("candidate-a"))
	if !errors.Is(err, ErrListingAlreadyBound) {
		t.Fatalf("expected bound-listing conflict, got %v", err)
	}
	if after := fixture.platformBudget(t); after != before {
		t.Fatalf("bound listing changed the platform allowance: %+v -> %+v", before, after)
	}
	var count int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM local_listing_lookups WHERE location_id=$1`, fixture.locationID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("bound listing created lookup rows: %d %v", count, err)
	}
}

func TestListingLookupMissingCoordinatesRefusesNewCandidate(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	basePlatform := f.platformBudget(t)
	baseOrg := f.orgBudget(t)
	bad := []ListingLookupSelection{
		{Query: "Test Cafe Testville", CandidateKey: "candidate-a", Latitude: math.NaN(), Longitude: 85.3123},
		{Query: "Test Cafe Testville", CandidateKey: "candidate-b", Latitude: 200, Longitude: 85.3123},
		{Query: "   ", CandidateKey: "candidate-c", Latitude: 27.6942, Longitude: 85.3123},
		{Query: "Test Cafe Testville", CandidateKey: "   ", Latitude: 27.6942, Longitude: 85.3123},
	}
	for i, sel := range bad {
		if _, _, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, sel); err == nil {
			t.Fatalf("bad selection %d accepted, want validation error", i)
		}
	}
	if got := f.platformBudget(t); got != basePlatform {
		t.Fatalf("invalid selection changed platform budget: %#v -> %#v", basePlatform, got)
	}
	if got := f.orgBudget(t); got != baseOrg {
		t.Fatalf("invalid selection changed org budget: %#v -> %#v", baseOrg, got)
	}
	if count := f.lookupCount(t); count != 0 {
		t.Fatalf("lookup rows = %d, want 0", count)
	}
}

func TestListingLookupExplicitZeroCoordinatesValid(t *testing.T) {
	f := newListingLookupFixture(t)
	store := ListingLookupStore{Pool: f.pool}
	zero := ListingLookupSelection{Query: "Null Island Shop", CandidateKey: "candidate-zero", Latitude: 0, Longitude: 0}
	var probeLat, probeLon float64
	probe := serper.MapsListingPlace{Latitude: &probeLat, Longitude: &probeLon}
	if !probe.HasMapCoordinates() {
		t.Fatalf("explicit 0,0 must satisfy HasMapCoordinates")
	}
	lookup, created, err := store.ReserveListingLookup(f.ctx, f.userID, f.projectID, f.locationID, zero)
	if err != nil {
		t.Fatalf("reserve zero lookup: %v", err)
	}
	if !created || lookup.ExpectedCredits != 3 || lookup.ReservedCredits != 3 {
		t.Fatalf("zero lookup = %#v, want fresh 3-credit reservation", lookup)
	}
	recorded, err := store.RecordListingLookup(f.ctx, lookup.ID, serper.MapsListingResponse{Credits: 3, Places: []serper.MapsListingPlace{listingLookupPlace("zero-place", 0, 0)}}, nil)
	if err != nil {
		t.Fatalf("record zero lookup: %v", err)
	}
	if recorded.Status != "completed" || recorded.CreditsUsed != 3 || recorded.ReservedCredits != 0 {
		t.Fatalf("zero lookup = %#v, want completed with charge settled", recorded)
	}
}
