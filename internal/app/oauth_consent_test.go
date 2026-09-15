package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
)

// The consent endpoints proxy Supabase, so these tests stay on the paths that
// must be rejected before any upstream call happens.

const consentTestAuthorizationID = "11111111-2222-3333-4444-555555555555"

// newConsentRequest builds a request carrying a chi path parameter, which is how
// the handler reads the authorization id.
func newConsentRequest(method string, authorizationID string, body string) *http.Request {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, "/oauth/authorizations/"+authorizationID+"/consent", reader)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("authorizationID", authorizationID)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func decodeErrorBody(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (body=%q)", err, response.Body.String())
	}
	return body["error"]
}

func TestGetOAuthAuthorizationRejectsInvalidID(t *testing.T) {
	for _, authorizationID := range []string{"", "not-a-uuid", "12345"} {
		t.Run("id="+authorizationID, func(t *testing.T) {
			app := &App{}
			response := httptest.NewRecorder()
			app.handleGetOAuthAuthorization(response, newConsentRequest(http.MethodGet, authorizationID, ""))

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if message := decodeErrorBody(t, response); !strings.Contains(message, "uuid") {
				t.Fatalf("error = %q, want it to mention a uuid", message)
			}
		})
	}
}

func TestPostOAuthConsentRejectsInvalidID(t *testing.T) {
	app := &App{}
	response := httptest.NewRecorder()
	app.handlePostOAuthConsent(response, newConsentRequest(http.MethodPost, "not-a-uuid", `{"action":"approve"}`))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestPostOAuthConsentRejectsInvalidAction(t *testing.T) {
	for _, action := range []string{"", "maybe", "APPROVE", "approve deny"} {
		t.Run("action="+action, func(t *testing.T) {
			app := &App{}
			response := httptest.NewRecorder()
			body, err := json.Marshal(map[string]string{"action": action})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			app.handlePostOAuthConsent(response, newConsentRequest(http.MethodPost, consentTestAuthorizationID, string(body)))

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
			}
			if message := decodeErrorBody(t, response); !strings.Contains(message, "approve or deny") {
				t.Fatalf("error = %q, want it to name the accepted actions", message)
			}
		})
	}
}

func TestConsentRoutesRequireSession(t *testing.T) {
	// No cookie means the session middleware rejects the request before any
	// database work, so a manager without a pool is enough here.
	app := &App{SessionManager: internalauth.NewSessionManager(nil, nil, nil, "revserp_session", "", time.Hour, false)}
	router := app.Router()

	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "/oauth/authorizations/"+consentTestAuthorizationID, nil),
		httptest.NewRequest(http.MethodPost, "/oauth/authorizations/"+consentTestAuthorizationID+"/consent", strings.NewReader(`{"action":"approve"}`)),
	}
	for _, request := range requests {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want %d", request.Method, request.URL.Path, response.Code, http.StatusUnauthorized)
		}
	}
}
