package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func putOnboarding(t *testing.T, fixture sessionFixture, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/me/onboarding", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: "revserp_session", Value: fixture.rawCookie})
	response := httptest.NewRecorder()
	fixture.app.Router().ServeHTTP(response, request)
	return response
}

func getOnboarding(t *testing.T, fixture sessionFixture) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/me/onboarding", nil)
	request.AddCookie(&http.Cookie{Name: "revserp_session", Value: fixture.rawCookie})
	response := httptest.NewRecorder()
	fixture.app.Router().ServeHTTP(response, request)
	return response
}

func decodeOnboarding(t *testing.T, response *httptest.ResponseRecorder) onboardingState {
	t.Helper()
	var state onboardingState
	if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode onboarding state: %v (body %q)", err, response.Body.String())
	}
	return state
}

func equalOnboarding(a, b onboardingState) bool {
	return a.Version == b.Version && a.Status == b.Status && a.Checkpoint == b.Checkpoint &&
		stringPointerValue(a.ProjectID) == stringPointerValue(b.ProjectID) &&
		stringPointerValue(a.CrawlID) == stringPointerValue(b.CrawlID) &&
		stringPointerValue(a.CompletedAt) == stringPointerValue(b.CompletedAt)
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func onboardingTestProject(t *testing.T, fixture sessionFixture, name string) (pgtype.UUID, pgtype.UUID) {
	t.Helper()
	var orgID pgtype.UUID
	if err := fixture.pool.QueryRow(fixture.ctx,
		`INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name+"-org").Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	if _, err := fixture.pool.Exec(fixture.ctx,
		`INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, fixture.userID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	var projectID pgtype.UUID
	if err := fixture.pool.QueryRow(fixture.ctx,
		`INSERT INTO projects (organization_id, name, base_url) VALUES ($1, $2, 'https://93.184.216.34') RETURNING id`,
		orgID, name).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return orgID, projectID
}

func onboardingTestCrawl(t *testing.T, fixture sessionFixture, projectID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var crawlID pgtype.UUID
	if err := fixture.pool.QueryRow(fixture.ctx,
		`INSERT INTO crawls (project_id, status) VALUES ($1, 'completed') RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatalf("create crawl: %v", err)
	}
	return crawlID
}

func TestOnboardingMigrationDefaults(t *testing.T) {
	fixture := newSessionFixture(t)
	var raw []byte
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT onboarding_state FROM users WHERE id = $1`, fixture.userID).Scan(&raw); err != nil {
		t.Fatalf("load onboarding state: %v", err)
	}
	state, err := parseOnboardingState(raw)
	if err != nil {
		t.Fatalf("parse onboarding state: %v", err)
	}
	if state.Version != 1 || state.Status != "not_started" || state.Checkpoint != "new_project" {
		t.Fatalf("new user state = %+v, want version 1 not_started new_project", state)
	}
	if state.ProjectID != nil || state.CrawlID != nil || state.CompletedAt != nil {
		t.Fatalf("new user state has unexpected IDs: %+v", state)
	}
}

func TestOnboardingRequiresAuth(t *testing.T) {
	fixture := newSessionFixture(t)
	router := fixture.app.Router()

	unauthenticated := httptest.NewRecorder()
	router.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/me/onboarding", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("GET without session = %d, want 401", unauthenticated.Code)
	}

	unauthenticatedPut := httptest.NewRecorder()
	router.ServeHTTP(unauthenticatedPut, httptest.NewRequest(http.MethodPut, "/me/onboarding", strings.NewReader(`{}`)))
	if unauthenticatedPut.Code != http.StatusUnauthorized {
		t.Fatalf("PUT without session = %d, want 401", unauthenticatedPut.Code)
	}
}

func TestOnboardingSuspendedIsForbidden(t *testing.T) {
	fixture := newSessionFixture(t)
	if _, err := fixture.pool.Exec(fixture.ctx,
		`UPDATE users SET status = 'suspended', suspended_at = now() WHERE id = $1`, fixture.userID); err != nil {
		t.Fatalf("suspend user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(),
			`UPDATE users SET status = 'active', suspended_at = NULL WHERE id = $1`, fixture.userID)
	})
	if response := getOnboarding(t, fixture); response.Code != http.StatusForbidden {
		t.Fatalf("suspended GET = %d, want 403 (body %s)", response.Code, response.Body.String())
	}
	if response := putOnboarding(t, fixture, `{"version":1,"status":"skipped","checkpoint":"new_project"}`); response.Code != http.StatusForbidden {
		t.Fatalf("suspended PUT = %d, want 403 (body %s)", response.Code, response.Body.String())
	}
}

func TestOnboardingPersistenceReadWrite(t *testing.T) {
	fixture := newSessionFixture(t)

	response := getOnboarding(t, fixture)
	if response.Code != http.StatusOK {
		t.Fatalf("initial GET = %d (%s)", response.Code, response.Body.String())
	}
	if initial := decodeOnboarding(t, response); initial.Status != "not_started" || initial.Checkpoint != "new_project" {
		t.Fatalf("initial state = %+v, want not_started new_project", initial)
	}

	response = putOnboarding(t, fixture, `{"version":1,"status":"in_progress","checkpoint":"overview"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT = %d (%s)", response.Code, response.Body.String())
	}
	put := decodeOnboarding(t, response)
	if put.Version != 1 || put.Status != "in_progress" || put.Checkpoint != "overview" {
		t.Fatalf("PUT state = %+v", put)
	}
	if put.CompletedAt != nil {
		t.Fatalf("non-completed state carries completed_at: %+v", put)
	}

	roundtrip := decodeOnboarding(t, getOnboarding(t, fixture))
	if !equalOnboarding(roundtrip, put) {
		t.Fatalf("GET after PUT = %+v, want %+v", roundtrip, put)
	}
}

func TestOnboardingIsolationBetweenUsers(t *testing.T) {
	first := newSessionFixture(t)
	second := newSessionFixture(t)

	if response := putOnboarding(t, first, `{"version":1,"status":"in_progress","checkpoint":"score_cards"}`); response.Code != http.StatusOK {
		t.Fatalf("first PUT = %d (%s)", response.Code, response.Body.String())
	}
	other := decodeOnboarding(t, getOnboarding(t, second))
	if other.Status != "not_started" || other.Checkpoint != "new_project" {
		t.Fatalf("second user state = %+v, want untouched default", other)
	}
	if response := putOnboarding(t, second, `{"version":1,"status":"skipped","checkpoint":"overview"}`); response.Code != http.StatusOK {
		t.Fatalf("second PUT = %d (%s)", response.Code, response.Body.String())
	}
	untouched := decodeOnboarding(t, getOnboarding(t, first))
	if untouched.Status != "in_progress" || untouched.Checkpoint != "score_cards" {
		t.Fatalf("first user state = %+v, second user's write leaked across", untouched)
	}
}

func TestOnboardingValidation(t *testing.T) {
	fixture := newSessionFixture(t)
	cases := map[string]string{
		"bad version":      `{"version":2,"status":"in_progress","checkpoint":"overview"}`,
		"bad status":       `{"version":1,"status":"done","checkpoint":"overview"}`,
		"bad checkpoint":   `{"version":1,"status":"in_progress","checkpoint":"spaceship"}`,
		"empty checkpoint": `{"version":1,"status":"in_progress","checkpoint":""}`,
		"bad project id":   `{"version":1,"status":"in_progress","checkpoint":"overview","project_id":"nope"}`,
		"bad crawl id":     `{"version":1,"status":"in_progress","checkpoint":"overview","project_id":"00000000-0000-0000-0000-000000000000","crawl_id":"nope"}`,
		"crawl alone":      `{"version":1,"status":"in_progress","checkpoint":"overview","crawl_id":"00000000-0000-0000-0000-000000000000"}`,
		"unknown ids":      `{"version":1,"status":"in_progress","checkpoint":"overview","project_id":"00000000-0000-0000-0000-000000000000"}`,
		"malformed json":   `{"version":`,
	}
	for name, body := range cases {
		if response := putOnboarding(t, fixture, body); response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 400 or 404 (body %s)", name, response.Code, response.Body.String())
		}
	}
	unchanged := decodeOnboarding(t, getOnboarding(t, fixture))
	if unchanged.Status != "not_started" {
		t.Fatalf("rejected writes mutated state: %+v", unchanged)
	}
}

func TestOnboardingProjectAndCrawlAccess(t *testing.T) {
	fixture := newSessionFixture(t)
	_, projectID := onboardingTestProject(t, fixture, "onboarding-access")
	crawlID := onboardingTestCrawl(t, fixture, projectID)

	other := newSessionFixture(t)
	_, otherProjectID := onboardingTestProject(t, other, "onboarding-other")
	otherCrawlID := onboardingTestCrawl(t, other, otherProjectID)

	body := `{"version":1,"status":"in_progress","checkpoint":"waiting_for_results","project_id":"` + projectID.String() + `","crawl_id":"` + crawlID.String() + `"}`
	response := putOnboarding(t, fixture, body)
	if response.Code != http.StatusOK {
		t.Fatalf("own project+crawl PUT = %d (%s)", response.Code, response.Body.String())
	}
	stored := decodeOnboarding(t, response)
	if stringPointerValue(stored.ProjectID) != projectID.String() {
		t.Fatalf("stored project = %+v, want %s", stored, projectID.String())
	}
	if stringPointerValue(stored.CrawlID) != crawlID.String() {
		t.Fatalf("stored crawl = %+v, want %s", stored, crawlID.String())
	}
	if roundtrip := decodeOnboarding(t, getOnboarding(t, fixture)); !equalOnboarding(roundtrip, stored) {
		t.Fatalf("GET after ID PUT = %+v, want %+v", roundtrip, stored)
	}

	foreignProject := `{"version":1,"status":"in_progress","checkpoint":"overview","project_id":"` + otherProjectID.String() + `"}`
	if response := putOnboarding(t, fixture, foreignProject); response.Code != http.StatusNotFound {
		t.Fatalf("foreign project PUT = %d, want 404 (body %s)", response.Code, response.Body.String())
	}
	foreignCrawl := `{"version":1,"status":"in_progress","checkpoint":"overview","project_id":"` + projectID.String() + `","crawl_id":"` + otherCrawlID.String() + `"}`
	if response := putOnboarding(t, fixture, foreignCrawl); response.Code != http.StatusNotFound && response.Code != http.StatusBadRequest {
		t.Fatalf("foreign crawl PUT = %d, want 404 or 400 (body %s)", response.Code, response.Body.String())
	}
	_, secondProjectID := onboardingTestProject(t, fixture, "onboarding-second")
	secondCrawlID := onboardingTestCrawl(t, fixture, secondProjectID)
	crossed := `{"version":1,"status":"in_progress","checkpoint":"overview","project_id":"` + projectID.String() + `","crawl_id":"` + secondCrawlID.String() + `"}`
	if response := putOnboarding(t, fixture, crossed); response.Code != http.StatusBadRequest {
		t.Fatalf("cross-project crawl PUT = %d, want 400 (body %s)", response.Code, response.Body.String())
	}
	after := decodeOnboarding(t, getOnboarding(t, fixture))
	if !equalOnboarding(after, stored) {
		t.Fatalf("rejected ID writes mutated state: %+v, want %+v", after, stored)
	}
}

func TestOnboardingCompletionTimestamp(t *testing.T) {
	fixture := newSessionFixture(t)

	before := time.Now().UTC().Add(-time.Minute)
	response := putOnboarding(t, fixture, `{"version":1,"status":"completed","checkpoint":"finish","completed_at":"2000-01-01T00:00:00Z"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("complete PUT = %d (%s)", response.Code, response.Body.String())
	}
	completed := decodeOnboarding(t, response)
	if completed.Status != "completed" || completed.CompletedAt == nil {
		t.Fatalf("completed state = %+v, want server-owned completed_at", completed)
	}
	stamped, err := time.Parse(time.RFC3339, *completed.CompletedAt)
	if err != nil {
		t.Fatalf("completed_at %q is not RFC3339: %v", *completed.CompletedAt, err)
	}
	if stamped.Before(before) || stamped.After(time.Now().UTC().Add(time.Minute)) {
		t.Fatalf("completed_at %q is not server time; client-supplied value must be ignored", *completed.CompletedAt)
	}

	again := decodeOnboarding(t, putOnboarding(t, fixture, `{"version":1,"status":"completed","checkpoint":"finish"}`))
	if again.CompletedAt == nil || *again.CompletedAt != *completed.CompletedAt {
		t.Fatalf("repeated completed write moved completed_at from %q to %+v", *completed.CompletedAt, again)
	}
	if roundtrip := decodeOnboarding(t, getOnboarding(t, fixture)); !equalOnboarding(roundtrip, again) {
		t.Fatalf("GET after complete = %+v, want %+v", roundtrip, again)
	}

	reopened := decodeOnboarding(t, putOnboarding(t, fixture, `{"version":1,"status":"in_progress","checkpoint":"overview"}`))
	if reopened.CompletedAt != nil {
		t.Fatalf("leaving completed kept completed_at: %+v", reopened)
	}

	skipped := decodeOnboarding(t, putOnboarding(t, fixture, `{"version":1,"status":"skipped","checkpoint":"revbot"}`))
	if skipped.Checkpoint != "revbot" || skipped.CompletedAt != nil {
		t.Fatalf("skipped state = %+v, want retained checkpoint without completed_at", skipped)
	}
}
