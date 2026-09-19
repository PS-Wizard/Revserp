package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectsetup"
)

func TestProjectSetupRoutesRegistered(t *testing.T) {
	app := &App{Config: config.Config{}}
	want := map[string]bool{
		http.MethodGet + " /projects/{projectID}/setup":  false,
		http.MethodPost + " /projects/{projectID}/setup": false,
	}
	if err := chi.Walk(app.Router().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		if _, ok := want[key]; ok {
			want[key] = true
		}
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	for key, found := range want {
		if !found {
			t.Errorf("%s is not registered", key)
		}
	}
}

func TestNewProjectSetupResponseMapping(t *testing.T) {
	setup := sqlc.ProjectSetup{
		ID:                   mustUUID(t, "11111111-1111-1111-1111-111111111111"),
		OrganizationID:       mustUUID(t, "22222222-2222-2222-2222-222222222222"),
		ProjectID:            mustUUID(t, "33333333-3333-3333-3333-333333333333"),
		RequestedByUserID:    mustUUID(t, "44444444-4444-4444-4444-444444444444"),
		CrawlID:              mustUUID(t, "55555555-5555-5555-5555-555555555555"),
		Status:               projectSetupStatusCompleted,
		Error:                pgtype.Text{String: "boom", Valid: true},
		FailedStep:           pgtype.Text{String: projectSetupStatusProfileGeneration, Valid: true},
		VisibilitySkipReason: pgtype.Text{},
		CreatedAt:            pgtype.Timestamptz{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Valid: true},
		UpdatedAt:            pgtype.Timestamptz{Time: time.Date(2026, 1, 2, 4, 5, 6, 0, time.UTC), Valid: true},
		CompletedAt:          pgtype.Timestamptz{Time: time.Date(2026, 1, 2, 4, 5, 6, 0, time.UTC), Valid: true},
	}

	response := newProjectSetupResponse(setup)
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)

	for _, want := range []string{
		`"id":"11111111-1111-1111-1111-111111111111"`,
		`"organization_id":"22222222-2222-2222-2222-222222222222"`,
		`"project_id":"33333333-3333-3333-3333-333333333333"`,
		`"requested_by_user_id":"44444444-4444-4444-4444-444444444444"`,
		`"status":"completed"`,
		`"crawl_id":"55555555-5555-5555-5555-555555555555"`,
		`"error":"boom"`,
		`"failed_step":"profile_generation"`,
		`"created_at":"2026-01-02T03:04:05Z"`,
		`"completed_at":"2026-01-02T04:05:06Z"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("response %s missing %s", body, want)
		}
	}
	// Optional fields must stay absent when unset.
	if strings.Contains(body, "visibility_skip_reason") {
		t.Errorf("response %s should omit unset visibility_skip_reason", body)
	}
}

func TestNewReadyProjectSetupParams(t *testing.T) {
	orgID := mustUUID(t, "11111111-1111-1111-1111-111111111111")
	projectID := mustUUID(t, "22222222-2222-2222-2222-222222222222")
	userID := mustUUID(t, "33333333-3333-3333-3333-333333333333")

	if projectSetupStatusReady != projectsetup.StatusReady {
		t.Fatalf("projectSetupStatusReady = %q, want the shared %q", projectSetupStatusReady, projectsetup.StatusReady)
	}
	params := newReadyProjectSetupParams(orgID, projectID, userID)
	if params.Status != projectSetupStatusReady {
		t.Fatalf("status = %q, want %q", params.Status, projectSetupStatusReady)
	}
	if params.OrganizationID != orgID || params.ProjectID != projectID || params.RequestedByUserID != userID {
		t.Fatalf("params = %+v, want org/project/user preserved", params)
	}
}

func TestWriteProjectSetupStartError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		want    int
		handled bool
	}{
		{"setup not found", projectsetup.ErrSetupNotFound, http.StatusNotFound, true},
		{"invalid failed step", projectsetup.ErrInvalidFailedStep, http.StatusConflict, true},
		{"resume conflict", projectsetup.ErrResumeConflict, http.StatusConflict, true},
		{"unexpected", errors.New("boom"), 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			if got := writeProjectSetupStartError(rr, test.err); got != test.handled {
				t.Fatalf("handled = %v, want %v", got, test.handled)
			}
			if !test.handled {
				if rr.Body.Len() != 0 {
					t.Fatalf("unexpected error wrote body %s", rr.Body.String())
				}
				return
			}
			if rr.Code != test.want {
				t.Fatalf("status = %d, want %d", rr.Code, test.want)
			}
		})
	}
}
