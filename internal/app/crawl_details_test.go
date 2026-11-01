package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func newCrawlDetailsFixtures(t *testing.T) (*App, pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('crawl-details-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}

	var outsiderID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('crawl-details-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&outsiderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, outsiderID) })

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'crawl-details-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now()) RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatal(err)
	}
	return &App{DB: pool, Queries: queries}, userID, outsiderID, crawlID
}

func callCrawlDetails(t *testing.T, app *App, userID, crawlID pgtype.UUID) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/crawls/"+crawlID.String()+"/details", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	app.handleGetCrawlDetails(rr, req)
	return rr
}

func TestHandleGetCrawlDetailsSeeded(t *testing.T) {
	app, userID, _, crawlID := newCrawlDetailsFixtures(t)
	queries, pool, ctx := app.Queries, app.DB, context.Background()

	insertPage := func(url string, status *int32, soft404 bool, fetchError *string) {
		t.Helper()
		var statusParam pgtype.Int4
		if status != nil {
			statusParam = pgtype.Int4{Int32: *status, Valid: true}
		}
		var fetchParam pgtype.Text
		if fetchError != nil {
			fetchParam = pgtype.Text{String: *fetchError, Valid: true}
		}
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code, soft_404, fetch_error) VALUES ($1,$2,$3,$4,$5)`,
			crawlID, url, statusParam, soft404, fetchParam); err != nil {
			t.Fatalf("insert page %s: %v", url, err)
		}
	}
	intPtr := func(v int32) *int32 { return &v }
	strPtr := func(v string) *string { return &v }

	insertPage("https://x/ok", intPtr(200), false, nil)
	insertPage("https://x/ok2", intPtr(200), true, nil)
	insertPage("https://x/a", intPtr(404), false, nil)
	insertPage("https://x/b.pdf", nil, false, strPtr("response body https://x/b.pdf exceeds 10485760 byte limit"))
	insertPage("https://x/c", intPtr(500), false, strPtr("connection reset"))

	insertLink := func(source, target string, internal bool, targetStatus *int32, nofollow bool) {
		t.Helper()
		var statusParam pgtype.Int4
		if targetStatus != nil {
			statusParam = pgtype.Int4{Int32: *targetStatus, Valid: true}
		}
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_links (crawl_id, source_url, target_url, is_internal, target_status, nofollow) VALUES ($1,$2,$3,$4,$5,$6)`,
			crawlID, source, target, internal, statusParam, nofollow); err != nil {
			t.Fatalf("insert link %s -> %s: %v", source, target, err)
		}
	}
	insertLink("https://x/ok", "https://x/a", true, intPtr(200), false)
	insertLink("https://x/ok", "https://x/missing", true, intPtr(404), false)
	insertLink("https://x/ok", "https://x/other", true, intPtr(200), true)
	insertLink("https://x/ok", "https://ext1.example/p1", false, nil, false)
	insertLink("https://x/ok", "https://ext2.example/q", false, nil, true)
	insertLink("https://x/ok2", "HTTPS://EXT1.example/p2", false, nil, false)
	// No stored target_status: broken via the 404 page /a (trailing slash normalizes away).
	insertLink("https://x/ok2", "https://x/a/", true, nil, false)

	rr := callCrawlDetails(t, app, userID, crawlID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rr.Code, rr.Body.String())
	}

	var decoded struct {
		CrawlID     string `json:"crawl_id"`
		StatusCodes []struct {
			Code  *int32 `json:"code"`
			Count int64  `json:"count"`
		} `json:"status_codes"`
		Soft404Count int64 `json:"soft_404_count"`
		FailedPages  struct {
			Total int64 `json:"total"`
			Items []struct {
				URL        string  `json:"url"`
				StatusCode *int32  `json:"status_code"`
				Reason     *string `json:"reason"`
			} `json:"items"`
		} `json:"failed_pages"`
		Links struct {
			Internal              int64 `json:"internal"`
			External              int64 `json:"external"`
			Nofollow              int64 `json:"nofollow"`
			BrokenInternal        int64 `json:"broken_internal"`
			UniqueExternalDomains int64 `json:"unique_external_domains"`
		} `json:"links"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if decoded.CrawlID != crawlID.String() {
		t.Errorf("crawl_id = %q, want %q", decoded.CrawlID, crawlID.String())
	}

	wantCodes := []struct {
		code  *int32
		count int64
	}{{intPtr(200), 2}, {intPtr(404), 1}, {intPtr(500), 1}, {nil, 1}}
	if len(decoded.StatusCodes) != len(wantCodes) {
		t.Fatalf("status_codes = %+v, want %d entries", decoded.StatusCodes, len(wantCodes))
	}
	for i, want := range wantCodes {
		got := decoded.StatusCodes[i]
		if (got.Code == nil) != (want.code == nil) || (got.Code != nil && *got.Code != *want.code) || got.Count != want.count {
			gotCode, wantCode := "null", "null"
			if got.Code != nil {
				gotCode = itoa32(*got.Code)
			}
			if want.code != nil {
				wantCode = itoa32(*want.code)
			}
			t.Errorf("status_codes[%d] = {%s,%d}, want {%s,%d}", i, gotCode, got.Count, wantCode, want.count)
		}
	}

	if decoded.Soft404Count != 1 {
		t.Errorf("soft_404_count = %d, want 1", decoded.Soft404Count)
	}

	if decoded.FailedPages.Total != 3 {
		t.Errorf("failed total = %d, want 3", decoded.FailedPages.Total)
	}
	if len(decoded.FailedPages.Items) != 3 {
		t.Fatalf("failed items len = %d, want 3", len(decoded.FailedPages.Items))
	}
	// fetch_error rows first (NULL status sorts before 500), then 404 without reason.
	if decoded.FailedPages.Items[0].URL != "https://x/b.pdf" ||
		decoded.FailedPages.Items[0].StatusCode != nil ||
		decoded.FailedPages.Items[0].Reason == nil ||
		*decoded.FailedPages.Items[0].Reason != "response body https://x/b.pdf exceeds 10485760 byte limit" {
		t.Errorf("failed items[0] = %+v, want b.pdf with null status and byte-limit reason", decoded.FailedPages.Items[0])
	}
	if decoded.FailedPages.Items[1].URL != "https://x/c" ||
		decoded.FailedPages.Items[1].StatusCode == nil || *decoded.FailedPages.Items[1].StatusCode != 500 ||
		decoded.FailedPages.Items[1].Reason == nil || *decoded.FailedPages.Items[1].Reason != "connection reset" {
		t.Errorf("failed items[1] = %+v, want x/c with 500 and connection reset", decoded.FailedPages.Items[1])
	}
	if decoded.FailedPages.Items[2].URL != "https://x/a" ||
		decoded.FailedPages.Items[2].StatusCode == nil || *decoded.FailedPages.Items[2].StatusCode != 404 ||
		decoded.FailedPages.Items[2].Reason != nil {
		t.Errorf("failed items[2] = %+v, want x/a with 404 and null reason", decoded.FailedPages.Items[2])
	}

	if decoded.Links.Internal != 4 || decoded.Links.External != 3 || decoded.Links.Nofollow != 2 ||
		decoded.Links.BrokenInternal != 2 || decoded.Links.UniqueExternalDomains != 2 {
		t.Errorf("links = %+v, want {4 3 2 2 2}", decoded.Links)
	}

	// The query layer must agree with the handler totals.
	if total, err := queries.CountCrawlDetailsFailedPages(ctx, crawlID); err != nil || total != 3 {
		t.Errorf("CountCrawlDetailsFailedPages = %d,%v, want 3,nil", total, err)
	}
	if stats, err := queries.GetCrawlDetailsLinkStats(ctx, crawlID); err != nil ||
		stats.InternalCount != 4 || stats.ExternalCount != 3 || stats.NofollowCount != 2 ||
		stats.BrokenInternalCount != 2 || stats.UniqueExternalDomains != 2 {
		t.Errorf("GetCrawlDetailsLinkStats = %+v,%v, want {4 3 2 2 2},nil", stats, err)
	}
	_ = queries
}

func TestHandleGetCrawlDetailsEmpty(t *testing.T) {
	app, userID, _, crawlID := newCrawlDetailsFixtures(t)

	rr := callCrawlDetails(t, app, userID, crawlID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{`"status_codes":[]`, `"items":[]`, `"soft_404_count":0`, `"total":0`,
		`"internal":0`, `"external":0`, `"nofollow":0`, `"broken_internal":0`, `"unique_external_domains":0`} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q: %s", want, body)
		}
	}
}

func TestHandleGetCrawlDetailsForeignCrawl(t *testing.T) {
	app, _, outsiderID, crawlID := newCrawlDetailsFixtures(t)

	rr := callCrawlDetails(t, app, outsiderID, crawlID)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404, body = %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), crawlID.String()) {
		t.Errorf("response leaks crawl id: %s", rr.Body.String())
	}
}

func itoa32(v int32) string {
	return strconv.FormatInt(int64(v), 10)
}
