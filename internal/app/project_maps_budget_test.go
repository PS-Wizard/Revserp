package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Project Maps spending allowance handler tests. These cover request
// validation and route registration only; the database-backed membership,
// allowance arithmetic, and authorization paths live in the integration test
// owned by the location layer 4 effort.

func TestProjectMapsBudgetRouteRegistered(t *testing.T) {
	router, ok := (&App{}).Router().(chi.Router)
	if !ok {
		t.Fatal("app router does not expose chi routes")
	}
	found := map[string]bool{}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		found[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	if !found["GET /projects/{projectID}/maps-budget"] {
		t.Fatal("GET /projects/{projectID}/maps-budget is not registered")
	}
}

func TestProjectMapsBudgetInvalidProjectID(t *testing.T) {
	app := &App{}
	var userID pgtype.UUID
	_ = userID.Scan("00000000-0000-0000-0000-000000000001")
	req := localVisibilityRequest(t, http.MethodGet, userID, map[string]string{"projectID": "not-a-uuid"}, "")
	rr := httptest.NewRecorder()
	app.handleGetProjectMapsBudget(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}
