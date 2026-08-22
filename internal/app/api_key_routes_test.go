package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
)

func TestV1RoutesRejectWrites(t *testing.T) {
	a := &App{APIKeyManager: internalauth.NewAPIKeyManager(nil)}
	router := a.Router()
	paths := []string{
		"/v1/me",
		"/v1/organizations/00000000-0000-0000-0000-000000000001/projects",
		"/v1/projects/00000000-0000-0000-0000-000000000001",
		"/v1/projects/00000000-0000-0000-0000-000000000001/crawls",
		"/v1/projects/00000000-0000-0000-0000-000000000001/bucket-trends",
		"/v1/projects/00000000-0000-0000-0000-000000000001/score-potential",
		"/v1/crawls/00000000-0000-0000-0000-000000000001",
		"/v1/crawls/00000000-0000-0000-0000-000000000001/score-breakdown",
		"/v1/crawls/00000000-0000-0000-0000-000000000001/page-health",
		"/v1/crawls/00000000-0000-0000-0000-000000000001/pages",
		"/v1/crawls/00000000-0000-0000-0000-000000000001/issues",
		"/v1/crawls/00000000-0000-0000-0000-000000000001/links",
		"/v1/crawls/00000000-0000-0000-0000-000000000001/site-graph",
		"/v1/crawl-pages/00000000-0000-0000-0000-000000000001",
		"/v1/crawl-issues/00000000-0000-0000-0000-000000000001",
		"/v1/crawl-links/00000000-0000-0000-0000-000000000001",
	}

	for _, path := range paths {
		t.Run("GET "+path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("GET status = %d, want %d", response.Code, http.StatusUnauthorized)
			}
		})
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			t.Run(method+" "+path, func(t *testing.T) {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(method, path, nil))
				if response.Code != http.StatusMethodNotAllowed {
					t.Fatalf("%s status = %d, want %d", method, response.Code, http.StatusMethodNotAllowed)
				}
			})
		}
	}
}
