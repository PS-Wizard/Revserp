package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
)

// tenantIsolationTenant holds one user's full fixture chain:
// user -> org (membership) -> project -> crawl -> page -> issue -> link.
type tenantIsolationTenant struct {
	email     string
	userID    pgtype.UUID
	orgID     pgtype.UUID
	projectID pgtype.UUID
	crawlID   pgtype.UUID
	pageID    pgtype.UUID
	issueID   pgtype.UUID
	linkID    pgtype.UUID
	apiKey    string // raw key value
}

func newTenantIsolationTenant(t *testing.T, pool *pgxpool.Pool, ctx context.Context, suffix string) tenantIsolationTenant {
	t.Helper()

	name := fmt.Sprintf("tenant-iso-test-%s-%d", suffix, time.Now().UnixNano())
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', $1, $2) RETURNING id`,
		name, name+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user %s: %v", suffix, err)
	}

	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name) VALUES ($1) RETURNING id`,
		name+"-org").Scan(&orgID); err != nil {
		t.Fatalf("create org %s: %v", suffix, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`,
		orgID, userID); err != nil {
		t.Fatalf("add member %s: %v", suffix, err)
	}

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (organization_id, name, base_url) VALUES ($1, $2, 'https://example.com') RETURNING id`,
		orgID, name+"-project").Scan(&projectID); err != nil {
		t.Fatalf("create project %s: %v", suffix, err)
	}

	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO crawls (project_id, requested_by_user_id, source, status) VALUES ($1, $2, 'manual', 'completed') RETURNING id`,
		projectID, userID).Scan(&crawlID); err != nil {
		t.Fatalf("create crawl %s: %v", suffix, err)
	}

	var pageID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1, $2, 200) RETURNING id`,
		crawlID, fmt.Sprintf("https://example.com/%s-page", suffix)).Scan(&pageID); err != nil {
		t.Fatalf("create crawl page %s: %v", suffix, err)
	}

	var issueID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO crawl_issues (crawl_id, url, severity, pillar, bucket, issue_type, message, details)
		 VALUES ($1, $2, 'low', 'seo', 'technical_seo', 'missing_lang', 'msg', '{}') RETURNING id`,
		crawlID, fmt.Sprintf("https://example.com/%s-issue", suffix)).Scan(&issueID); err != nil {
		t.Fatalf("create crawl issue %s: %v", suffix, err)
	}

	var linkID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO crawl_links (crawl_id, source_url, target_url) VALUES ($1, $2, $3) RETURNING id`,
		crawlID,
		fmt.Sprintf("https://example.com/%s", suffix),
		fmt.Sprintf("https://example.com/%s-target", suffix)).Scan(&linkID); err != nil {
		t.Fatalf("create crawl link %s: %v", suffix, err)
	}

	ten := tenantIsolationTenant{
		email:     name + "@example.com",
		userID:    userID,
		orgID:     orgID,
		projectID: projectID,
		crawlID:   crawlID,
		pageID:    pageID,
		issueID:   issueID,
		linkID:    linkID,
	}

	// Deleting the fixture orgs and users removes the whole chain via ON DELETE
	// CASCADE (projects -> crawls -> pages/issues/links, api_keys, memberships);
	// crawls.requested_by_user_id is ON DELETE SET NULL so user deletion is safe.
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `DELETE FROM organizations WHERE id = $1`, ten.orgID)
		_, _ = pool.Exec(cleanup, `DELETE FROM users WHERE id = $1`, ten.userID)
	})

	return ten
}

func newTenantAPIKey(t *testing.T, app *App, ctx context.Context, userID pgtype.UUID, name string) string {
	t.Helper()
	raw, prefix, hash, err := app.APIKeyManager.GenerateAPIKey()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO api_keys (user_id, name, token_prefix, token_hash) VALUES ($1, $2, $3, $4)`,
		userID, name, prefix, hash); err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	return raw
}

func tenantGet(t *testing.T, handler http.Handler, path, rawKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestAPIKeyTenantIsolationIntegration proves through the real /v1 Router that an
// API key for user A cannot read any resource owned by user B's organization,
// while still reading every equivalent resource it owns.
//
// Fixture limitation: rows are inserted directly via SQL with minimal columns;
// they are not produced by the real create flows, which also write derived data
// (e.g. score breakdowns, auto-crawl schedules). That does not affect the
// ownership joins under test.
func TestAPIKeyTenantIsolationIntegration(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)

	app := &App{DB: pool, Queries: queries, APIKeyManager: internalauth.NewAPIKeyManager(queries)}
	router := app.Router()

	a := newTenantIsolationTenant(t, pool, ctx, "a")
	b := newTenantIsolationTenant(t, pool, ctx, "b")
	a.apiKey = newTenantAPIKey(t, app, ctx, a.userID, "tenant-iso-a")

	t.Run("user A cannot read user B organization resources", func(t *testing.T) {
		cases := []struct {
			name string
			path string
			want int
		}{
			{"organization projects list", "/v1/organizations/" + b.orgID.String() + "/projects", http.StatusForbidden},
			{"project by id", "/v1/projects/" + b.projectID.String(), http.StatusNotFound},
			{"project crawls list", "/v1/projects/" + b.projectID.String() + "/crawls", http.StatusForbidden},
			{"crawl by id", "/v1/crawls/" + b.crawlID.String(), http.StatusNotFound},
			{"crawl pages list", "/v1/crawls/" + b.crawlID.String() + "/pages", http.StatusForbidden},
			{"crawl issues list", "/v1/crawls/" + b.crawlID.String() + "/issues", http.StatusForbidden},
			{"crawl links list", "/v1/crawls/" + b.crawlID.String() + "/links", http.StatusForbidden},
			{"crawl page by id", "/v1/crawl-pages/" + b.pageID.String(), http.StatusNotFound},
			{"crawl issue by id", "/v1/crawl-issues/" + b.issueID.String(), http.StatusNotFound},
			{"crawl link by id", "/v1/crawl-links/" + b.linkID.String(), http.StatusNotFound},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				rec := tenantGet(t, router, tc.path, a.apiKey)
				if rec.Code != tc.want {
					t.Errorf("GET %s with A's key status = %d, want %d, body = %s",
						tc.path, rec.Code, tc.want, rec.Body.String())
				}
				// Never leak B's identifiers in a rejected response body.
				if strings.Contains(rec.Body.String(), b.projectID.String()) ||
					strings.Contains(rec.Body.String(), b.crawlID.String()) {
					t.Errorf("response leaks user B resource ids: %s", rec.Body.String())
				}
			})
		}
	})

	t.Run("user A can read its own resources", func(t *testing.T) {
		cases := []struct {
			name     string
			path     string
			mustHave []string // ids that must appear in the response body
		}{
			{"organization projects list", "/v1/organizations/" + a.orgID.String() + "/projects", []string{a.projectID.String()}},
			{"project by id", "/v1/projects/" + a.projectID.String(), []string{a.projectID.String()}},
			{"project crawls list", "/v1/projects/" + a.projectID.String() + "/crawls", []string{a.crawlID.String()}},
			{"crawl by id", "/v1/crawls/" + a.crawlID.String(), []string{a.crawlID.String()}},
			{"crawl pages list", "/v1/crawls/" + a.crawlID.String() + "/pages", []string{a.pageID.String()}},
			{"crawl issues list", "/v1/crawls/" + a.crawlID.String() + "/issues", []string{a.issueID.String()}},
			{"crawl links list", "/v1/crawls/" + a.crawlID.String() + "/links", []string{a.linkID.String()}},
			{"crawl page by id", "/v1/crawl-pages/" + a.pageID.String(), []string{a.pageID.String()}},
			{"crawl issue by id", "/v1/crawl-issues/" + a.issueID.String(), []string{a.issueID.String()}},
			{"crawl link by id", "/v1/crawl-links/" + a.linkID.String(), []string{a.linkID.String()}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				rec := tenantGet(t, router, tc.path, a.apiKey)
				if rec.Code != http.StatusOK {
					t.Fatalf("GET %s with owner's key status = %d, want 200, body = %s",
						tc.path, rec.Code, rec.Body.String())
				}
				var parsed map[string]json.RawMessage
				if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				for _, want := range tc.mustHave {
					if !strings.Contains(rec.Body.String(), want) {
						t.Errorf("GET %s response missing own resource id %s: %s", tc.path, want, rec.Body.String())
					}
				}
			})
		}
	})
}
