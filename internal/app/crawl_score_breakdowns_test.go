package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestNormalizeScoreBreakdownWorkStatus(t *testing.T) {
	tests := []struct {
		input  string
		want   string
		wantOK bool
	}{
		{"", "all", true},
		{"all", "all", true},
		{"ALL", "all", true},
		{"  all  ", "all", true},
		{"needs_action", "needs_action", true},
		{"NEEDS_ACTION", "needs_action", true},
		{"marked_done", "marked_done", true},
		{"MARKED_DONE", "marked_done", true},
		{"  marked_done  ", "marked_done", true},
		{"invalid", "", false},
		{"needs-action", "", false},
		{"fixed", "", false},
	}
	for _, tt := range tests {
		got, ok := normalizeScoreBreakdownWorkStatus(tt.input)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("normalizeScoreBreakdownWorkStatus(%q) = (%q,%v), want (%q,%v)", tt.input, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestDuplicateGroupWorkKeyStable(t *testing.T) {
	members := []sqlc.CrawlIssueGroupMember{
		{Url: "https://example.com/b"},
		{Url: "https://example.com/a"},
		{Url: "https://example.com/c"},
	}
	got := duplicateGroupWorkKey(members)
	urls := []string{"https://example.com/a", "https://example.com/b", "https://example.com/c"}
	sort.Strings(urls)
	sum := sha256.Sum256([]byte(strings.Join(urls, "\n")))
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("duplicateGroupWorkKey = %q, want %q", got, want)
	}
	shuffled := []sqlc.CrawlIssueGroupMember{
		{Url: "https://example.com/c"},
		{Url: "https://example.com/a"},
		{Url: "https://example.com/b"},
	}
	if duplicateGroupWorkKey(shuffled) != want {
		t.Fatalf("duplicateGroupWorkKey not stable across order")
	}
	other := []sqlc.CrawlIssueGroupMember{
		{Url: "https://example.com/a"},
		{Url: "https://example.com/b"},
	}
	if duplicateGroupWorkKey(other) == want {
		t.Fatalf("different member set should produce different key")
	}
}

func TestIsSitewideWorkspaceIssue(t *testing.T) {
	sitewide := []string{"weak_open_graph_coverage", "missing_website_schema", "missing_org_identity_schema", "missing_about_page", "missing_contact_page", "missing_policy_page", "missing_llms_txt", "homepage_missing_org_contact_trust_signals"}
	for _, it := range sitewide {
		if !isSitewideWorkspaceIssue(it) {
			t.Errorf("isSitewideWorkspaceIssue(%q) = false, want true", it)
		}
	}
	if isSitewideWorkspaceIssue("missing_title") {
		t.Errorf("isSitewideWorkspaceIssue for page issue should be false")
	}
	if isSitewideWorkspaceIssue("exact_duplicate_content") {
		t.Errorf("group issue should not be sitewide")
	}
}

func TestHandleListScoreBreakdownIssueURLsInvalidWorkStatus(t *testing.T) {
	app := &App{}
	var uid pgtype.UUID
	_ = uid.Scan("00000000-0000-0000-0000-000000000001")
	var crawlID pgtype.UUID
	_ = crawlID.Scan("00000000-0000-0000-0000-000000000002")

	req := httptest.NewRequest(http.MethodGet, "/crawls/"+crawlID.String()+"/score-breakdown/seo/serp_metadata/missing_title/urls?work_status=invalid", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID.String())
	routeCtx.URLParams.Add("pillar", "seo")
	routeCtx.URLParams.Add("bucket", "serp_metadata")
	routeCtx.URLParams.Add("issueType", "missing_title")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: uid}})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	app.handleListScoreBreakdownIssueURLs(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "work_status") {
		t.Fatalf("body should mention work_status, got %q", body)
	}
}
