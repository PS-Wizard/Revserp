package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/localvisibility"
	"github.com/ps-wizard/revserp/internal/locationsetup"
)

func TestLocationInitializationReadyResponse(t *testing.T) {
	response := newLocationInitializationResponse(locationsetup.Setup{})
	if response.Status != "ready" || response.Differences != "" || len(response.Steps) != 7 {
		t.Fatalf("ready response = %#v", response)
	}
	for i, step := range response.Steps {
		if step.ID != locationsetup.OrderedSteps[i] || step.Status != "pending" {
			t.Fatalf("ready step = %#v", step)
		}
	}
}

func TestLocationInitializationPreservesParallelStepsAndSkipReasons(t *testing.T) {
	setup := locationsetup.Setup{
		Status: "running", Differences: "No equipment sales.",
		Steps:   map[string]string{"questions": "completed", "ai_visibility": "running", "map_queries": "completed", "maps_visibility": "skipped"},
		Reasons: map[string]string{"maps_visibility": "insufficient credits"},
	}
	response := newLocationInitializationResponse(setup)
	if response.Differences != setup.Differences {
		t.Fatal("differences were lost")
	}
	if response.Steps[5].Status != "running" || response.Steps[6].Status != "skipped" || response.Steps[6].Reason != "insufficient credits" {
		t.Fatalf("parallel steps = %#v", response.Steps)
	}
}

func TestLocationInitializationCreditEstimate(t *testing.T) {
	for _, count := range []int{0, 1, 3, localvisibility.MapQueryStarterLimit} {
		got, err := locationInitializationCreditEstimate(count)
		if err != nil {
			t.Fatal(err)
		}
		pricedCount := count
		if pricedCount == 0 {
			pricedCount = localvisibility.MapQueryStarterLimit
		}
		if want := int32(pricedCount * localvisibility.GridPointCount * localvisibility.MapsCreditsPerCall); got != want {
			t.Fatalf("count %d estimate %d, want %d", count, got, want)
		}
	}
	if _, err := locationInitializationCreditEstimate(-1); err == nil {
		t.Fatal("negative count must fail")
	}
}

func TestLocationInitializationAPIReadyAndMembership(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	location := createUnboundLocation(t, fx, "Branch setup")
	for _, tc := range []struct {
		user pgtype.UUID
		want int
	}{{fx.ownerID, http.StatusOK}, {fx.memberID, http.StatusOK}, {fx.outsider, http.StatusNotFound}} {
		rr := httptest.NewRecorder()
		fx.app.handleGetLocationInitialization(rr, localVisibilityRequest(t, http.MethodGet, tc.user, map[string]string{"projectID": fx.projectID.String(), "locationID": location.ID}, ""))
		if rr.Code != tc.want {
			t.Fatalf("GET status %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
		}
		if tc.want == http.StatusOK {
			var response locationInitializationResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Status != "ready" || response.EstimatedMapsCredits == nil {
				t.Fatalf("ready response = %#v", response)
			}
		}
	}
}

func TestLocationInitializationAPIMemberCannotStart(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	location := createUnboundLocation(t, fx, "Member cannot initialize")
	rr := httptest.NewRecorder()
	fx.app.handleStartLocationInitialization(rr, localVisibilityRequest(t, http.MethodPost, fx.memberID, map[string]string{"projectID": fx.projectID.String(), "locationID": location.ID}, `{}`))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("POST status %d, want 403: %s", rr.Code, rr.Body.String())
	}
}

func TestLocationInitializationAPIStartIsIdempotent(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	location := createUnboundLocation(t, fx, "Owner initializes once")
	params := map[string]string{"projectID": fx.projectID.String(), "locationID": location.ID}
	for _, body := range []string{`{"differences":"No equipment sales."}`, `{"differences":"Do not replace this."}`} {
		rr := httptest.NewRecorder()
		fx.app.handleStartLocationInitialization(rr, localVisibilityRequest(t, http.MethodPost, fx.ownerID, params, body))
		if rr.Code != http.StatusOK {
			t.Fatalf("start status %d: %s", rr.Code, rr.Body.String())
		}
		var response locationInitializationResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Status != "running" || response.Differences != "No equipment sales." || response.Steps[1].Status != "running" {
			t.Fatalf("setup response = %#v", response)
		}
	}
	var jobs int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM ai_worker_jobs WHERE project_id = $1 AND location_id = $2 AND job_type = 'location_profile_generation'`, fx.projectID, location.ID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("profile jobs = %d, want 1", jobs)
	}
}

func TestLocationMapsRegenerationWaitsForProfile(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	location := createUnboundLocation(t, fx, "Preparing branch")
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO location_business_profiles (project_id, location_id, brand_name, website_url) VALUES ($1,$2,'Branch','https://example.com')`, fx.projectID, location.ID); err != nil {
		t.Fatal(err)
	}
	params := map[string]string{"projectID": fx.projectID.String(), "locationID": location.ID}
	start := httptest.NewRecorder()
	fx.app.handleStartLocationInitialization(start, localVisibilityRequest(t, http.MethodPost, fx.ownerID, params, `{}`))
	if start.Code != http.StatusOK {
		t.Fatalf("start status %d: %s", start.Code, start.Body.String())
	}
	rr := httptest.NewRecorder()
	fx.app.handleRegenerateLocationMapsQueries(rr, localVisibilityRequest(t, http.MethodPost, fx.ownerID, params, `{}`))
	if rr.Code != http.StatusConflict {
		t.Fatalf("regenerate status %d, want 409: %s", rr.Code, rr.Body.String())
	}
	var jobs int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT COUNT(*) FROM ai_worker_jobs WHERE project_id = $1 AND location_id = $2 AND job_type = 'map_query_generation'`, fx.projectID, location.ID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("Maps generation started before profile adaptation: %d jobs", jobs)
	}
}
