package app

import (
	"testing"

	issueengine "github.com/ps-wizard/revserp/internal/issues"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func locationAuditTestPages() []shared.CrawlPageSignal {
	html := "text/html"
	return []shared.CrawlPageSignal{
		{URL: "https://site.example/branch", StatusCode: 200, ContentType: html},
		{URL: "https://site.example/branch/menu", StatusCode: 200, ContentType: html},
		{URL: "https://site.example/other", StatusCode: 200, ContentType: html},
		{URL: "https://site.example/branch/broken", StatusCode: 500, ContentType: html},
	}
}

func locationAuditTestIssue(pageURL, pillar, bucket, issueType string) shared.CrawlIssueSignal {
	return shared.CrawlIssueSignal{
		URL: pageURL, Pillar: pillar, Bucket: bucket,
		Severity: "high", IssueType: issueType,
		Message: "m", Details: "d",
	}
}

func snapshotIssueTypes(snapshot shared.ScoreBreakdownSnapshot) map[string]bool {
	found := map[string]bool{}
	for _, pillar := range snapshot.Pillars {
		for _, bucket := range pillar.Buckets {
			for _, issue := range bucket.Issues {
				found[issue.ID] = true
			}
		}
	}
	return found
}

func TestDeriveLocationWebsiteAuditStates(t *testing.T) {
	config := issueengine.DefaultScoringConfig()
	pages := locationAuditTestPages()

	if got := deriveLocationWebsiteAudit(pages, nil, config, "", ""); got.State != "" || got.HasSnapshot {
		t.Fatalf("empty scope should yield no state or snapshot, got %+v", got)
	}
	if got := deriveLocationWebsiteAudit(pages, nil, config, "https://site.example/branch", "none"); got.State != "" || got.HasSnapshot {
		t.Fatalf("match none should yield no state or snapshot, got %+v", got)
	}
	got := deriveLocationWebsiteAudit(pages, nil, config, "https://site.example/nowhere", "subtree")
	if got.State != "no_matching_pages" || got.HasSnapshot {
		t.Fatalf("unmatched scope should yield no_matching_pages without snapshot, got %+v", got)
	}
	if got.Snapshot.OverallScore != 0 {
		t.Fatalf("no_matching_pages must not carry a score, got %d", got.Snapshot.OverallScore)
	}
	brokenOnly := []shared.CrawlPageSignal{{URL: "https://site.example/branch/broken", StatusCode: 500, ContentType: "text/html"}}
	got = deriveLocationWebsiteAudit(brokenOnly, nil, config, "https://site.example/branch", "subtree")
	if got.State != "no_matching_pages" || got.HasSnapshot {
		t.Fatalf("unscoreable scope should yield no_matching_pages without snapshot, got %+v", got)
	}
}

func TestDeriveLocationWebsiteAuditSubtreeBoundary(t *testing.T) {
	config := issueengine.DefaultScoringConfig()
	pages := []shared.CrawlPageSignal{
		{URL: "https://site.example/branch", StatusCode: 200, ContentType: "text/html"},
		{URL: "https://site.example/branch/menu", StatusCode: 200, ContentType: "text/html"},
		{URL: "https://site.example/branches", StatusCode: 200, ContentType: "text/html"},
		{URL: "https://other.example/branch/menu", StatusCode: 200, ContentType: "text/html"},
	}
	got := deriveLocationWebsiteAudit(pages, nil, config, "https://site.example/branch", "subtree")
	if !got.HasSnapshot || got.State != "ready" {
		t.Fatalf("expected ok snapshot, got %+v", got)
	}
	if got.MatchedPages != 2 || got.EligiblePages != 2 {
		t.Fatalf("subtree must match scope page plus segment descendants only, got matched=%d eligible=%d", got.MatchedPages, got.EligiblePages)
	}
	if got.Snapshot.TotalScoredPages != 2 {
		t.Fatalf("denominator must count eligible in-scope pages only, got %d", got.Snapshot.TotalScoredPages)
	}
}

func TestDeriveLocationWebsiteAuditIgnoresOutOfScopeIssues(t *testing.T) {
	config := issueengine.DefaultScoringConfig()
	pages := locationAuditTestPages()
	issues := []shared.CrawlIssueSignal{
		locationAuditTestIssue("https://site.example/other", "seo", "serp_metadata", "missing_title"),
	}
	got := deriveLocationWebsiteAudit(pages, issues, config, "https://site.example/branch", "subtree")
	if !got.HasSnapshot {
		t.Fatal("expected snapshot for matched scope")
	}
	if found := snapshotIssueTypes(got.Snapshot); found["missing_title"] {
		t.Fatal("out-of-scope issue leaked into branch score")
	}
	if got.Snapshot.OverallScore != 100 {
		t.Fatalf("branch without in-scope issues must score 100, got %d", got.Snapshot.OverallScore)
	}
}

func TestDeriveLocationWebsiteAuditExcludesSitewideIssues(t *testing.T) {
	config := issueengine.DefaultScoringConfig()
	pages := locationAuditTestPages()
	issues := []shared.CrawlIssueSignal{
		locationAuditTestIssue("https://site.example/branch", "seo", "serp_metadata", "missing_title"),
		locationAuditTestIssue("https://site.example/branch", "aeo", "trust", "missing_contact_page"),
		locationAuditTestIssue("https://site.example/branch", "pagespeed", "psi_cwv", "google_psi_lcp"),
	}
	got := deriveLocationWebsiteAudit(pages, issues, config, "https://site.example/branch", "exact")
	if !got.HasSnapshot {
		t.Fatal("expected snapshot for exact scope")
	}
	found := snapshotIssueTypes(got.Snapshot)
	if !found["missing_title"] {
		t.Fatal("in-scope page issue went missing from branch score")
	}
	if found["missing_contact_page"] || found["google_psi_lcp"] {
		t.Fatalf("site-wide issues must be excluded from branch score, got %v", found)
	}
	if len(got.ExcludedIssueTypes) != 2 || got.ExcludedIssueTypes[0] != "google_psi_lcp" || got.ExcludedIssueTypes[1] != "missing_contact_page" {
		t.Fatalf("excluded site-wide types must be reported sorted, got %v", got.ExcludedIssueTypes)
	}
	if len(got.UnsupportedBuckets) != 1 || got.UnsupportedBuckets[0] != "psi_cwv" {
		t.Fatalf("origin-level psi bucket must be flagged unsupported, got %v", got.UnsupportedBuckets)
	}
}

func TestDeriveLocationWebsiteAuditMatchesScorerOnEligiblePages(t *testing.T) {
	config := issueengine.DefaultScoringConfig()
	pages := locationAuditTestPages()
	issues := []shared.CrawlIssueSignal{
		locationAuditTestIssue("https://site.example/branch/menu?utm=x", "seo", "serp_metadata", "missing_title"),
		locationAuditTestIssue("https://site.example/other", "seo", "serp_metadata", "missing_title"),
	}
	got := deriveLocationWebsiteAudit(pages, issues, config, "https://site.example/branch", "subtree")
	if !got.HasSnapshot {
		t.Fatal("expected snapshot for matched scope")
	}
	eligible := []shared.CrawlPageSignal{pages[0], pages[1]}
	want := issueengine.BuildScoreBreakdownWithConfig("", eligible, []shared.CrawlIssueSignal{issues[0]}, locationWebsiteAuditAvailableConfig(config), nil)
	if got.Snapshot.OverallScore != want.OverallScore {
		t.Fatalf("branch score must come from the shared scorer over eligible pages, got %d want %d", got.Snapshot.OverallScore, want.OverallScore)
	}
	if got.Snapshot.TotalScoredPages != 2 {
		t.Fatalf("coverage denominator must be eligible in-scope pages, got %d", got.Snapshot.TotalScoredPages)
	}
}

func TestLocationWebsiteAuditScopeURL(t *testing.T) {
	scope := locationWebsiteScopeRevision{Revision: 2, Match: "none"}
	if _, usable := locationWebsiteAuditScopeURL(scope); usable {
		t.Fatal("match none must never yield a usable scope")
	}
	scope = locationWebsiteScopeRevision{Revision: 3, Match: "exact"}
	if _, usable := locationWebsiteAuditScopeURL(scope); usable {
		t.Fatal("null scope URL must never yield a usable scope")
	}
	raw := "https://site.example/branch"
	scope = locationWebsiteScopeRevision{Revision: 3, URL: &raw, Match: "subtree"}
	scopeURL, usable := locationWebsiteAuditScopeURL(scope)
	if !usable || scopeURL != raw {
		t.Fatalf("usable scope must pass through, got %q %v", scopeURL, usable)
	}
}

func TestDeriveLocationWebsiteAuditExcludesUnsupportedBucket(t *testing.T) {
	config := issueengine.DefaultScoringConfig()
	pages := []shared.CrawlPageSignal{
		{URL: "https://site.example/branch", StatusCode: 200, ContentType: "text/html", ResponseTimeMs: 2500},
	}
	got := deriveLocationWebsiteAudit(pages, nil, config, "https://site.example/branch", "exact")
	if !got.HasSnapshot {
		t.Fatal("expected snapshot for exact scope")
	}
	for _, pillar := range got.Snapshot.Pillars {
		if pillar.ID != "pagespeed" {
			continue
		}
		for _, bucket := range pillar.Buckets {
			if bucket.ID == "psi_cwv" {
				t.Fatal("unsupported psi_cwv bucket must not contribute to the branch score")
			}
		}
		if pillar.Score != 100 {
			t.Fatalf("pagespeed with only available buckets clean must score 100, got %d", pillar.Score)
		}
	}
}

func TestLocationWebsiteAuditAvailableConfigRescales(t *testing.T) {
	config := issueengine.DefaultScoringConfig()
	available := locationWebsiteAuditAvailableConfig(config)
	weights := available.Pillars["pagespeed"].BucketWeights
	if _, exists := weights["psi_cwv"]; exists {
		t.Fatal("available config must drop the unsupported bucket")
	}
	sum := 0.0
	for _, weight := range weights {
		sum += weight
	}
	if sum < 0.999 || sum > 1.001 {
		t.Fatalf("remaining pillar weights must rescale to one, got %f", sum)
	}
	if config.Pillars["pagespeed"].BucketWeights["psi_cwv"] == 0 {
		t.Fatal("rescaling must clone, never mutate the shared config")
	}
}

func TestLocationWebsiteAuditMethodChanged(t *testing.T) {
	if locationWebsiteAuditMethodChanged("", "v9-soft-sum") {
		t.Fatal("missing stored version is too fresh to have drifted")
	}
	if locationWebsiteAuditMethodChanged("v9-soft-sum", "v9-soft-sum") {
		t.Fatal("matching versions must not flag a change")
	}
	if !locationWebsiteAuditMethodChanged("v8", "v9-soft-sum") {
		t.Fatal("mismatched versions must flag a change")
	}
}
