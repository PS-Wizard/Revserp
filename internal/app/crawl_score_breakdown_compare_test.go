package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Compare URL change types must accept the explicit not_verified and
// no_longer_detected classifications while keeping the legacy resolved value
// valid for older API consumers.
func TestNormalizeCompareURLChangeType(t *testing.T) {
	valid := map[string]string{
		"":                    "",
		"all":                 "",
		"new":                 "new",
		"resolved":            "resolved",
		"no_longer_detected":  "no_longer_detected",
		"not_verified":        "not_verified",
		"changed":             "changed",
		"unchanged":           "unchanged",
		"improved":            "improved",
		"regressed":           "regressed",
		"  NOT_VERIFIED  ":    "not_verified",
		"No_Longer_Detected ": "no_longer_detected",
	}
	for input, want := range valid {
		if got, ok := normalizeCompareURLChangeType(input); !ok || got != want {
			t.Errorf("normalizeCompareURLChangeType(%q) = (%q, %v), want (%q, true)", input, got, ok, want)
		}
	}
	for _, invalid := range []string{"fixed", "gone", "verified"} {
		if _, ok := normalizeCompareURLChangeType(invalid); ok {
			t.Errorf("normalizeCompareURLChangeType(%q) accepted invalid value", invalid)
		}
	}
}

// The include_excluded flag defaults to false, parses bools, and rejects
// anything else so legacy requests without the flag stay identical.
func TestParseIncludeExcludedParam(t *testing.T) {
	valid := map[string]bool{
		"":         false,
		"  ":       false,
		"true":     true,
		"TRUE":     true,
		"1":        true,
		"false":    false,
		"0":        false,
		"  true  ": true,
	}
	for input, want := range valid {
		if got, ok := parseIncludeExcludedParam(input); !ok || got != want {
			t.Errorf("parseIncludeExcludedParam(%q) = (%v, %v), want (%v, true)", input, got, ok, want)
		}
	}
	for _, invalid := range []string{"yes", "maybe", "2", "truee"} {
		if _, ok := parseIncludeExcludedParam(invalid); ok {
			t.Errorf("parseIncludeExcludedParam(%q) accepted invalid value", invalid)
		}
	}
}

func TestHandleListCompareIssueURLsRejectsBadIncludeExcluded(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest(http.MethodGet, "/?change_type=resolved&include_excluded=maybe", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("baselineCrawlID", "00000000-0000-0000-0000-000000000001")
	routeCtx.URLParams.Add("currentCrawlID", "00000000-0000-0000-0000-000000000002")
	routeCtx.URLParams.Add("pillar", "seo")
	routeCtx.URLParams.Add("bucket", "serp_metadata")
	routeCtx.URLParams.Add("issueType", "missing_title")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	rr := httptest.NewRecorder()
	app.handleListScoreBreakdownCompareIssueURLs(rr, req.WithContext(ctx))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "include_excluded") {
		t.Errorf("error body should name include_excluded, got %q", body)
	}
}
