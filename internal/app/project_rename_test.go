package app


import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestRenameProjectRouteRegistered(t *testing.T) {
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
	if !found["PATCH /projects/{projectID}"] {
		t.Fatal("PATCH /projects/{projectID} is not registered")
	}
}

func TestRenameProjectRequestValidation(t *testing.T) {
	const projectID = "11111111-1111-1111-1111-111111111111"
	var userID pgtype.UUID
	if err := userID.Scan("00000000-0000-0000-0000-000000000001"); err != nil {
		t.Fatalf("scan user id: %v", err)
	}
	cases := []struct {
		name      string
		projectID string
		body      string
		want      int
	}{
		{name: "malformed project id", projectID: "not-a-uuid", body: `{"name":"New"}`, want: http.StatusBadRequest},
		{name: "malformed json", projectID: projectID, body: `{"name":`, want: http.StatusBadRequest},
		{name: "empty name", projectID: projectID, body: `{"name":""}`, want: http.StatusBadRequest},
		{name: "whitespace name", projectID: projectID, body: `{"name":"   "}`, want: http.StatusBadRequest},
		{name: "missing name", projectID: projectID, body: `{}`, want: http.StatusBadRequest},
		{name: "base_url rejected", projectID: projectID, body: `{"name":"New","base_url":"https://example.com"}`, want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{}
			req := localVisibilityRequest(t, http.MethodPatch, userID, map[string]string{"projectID": tc.projectID}, tc.body)
			rr := httptest.NewRecorder()
			app.handleRenameProject(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}
