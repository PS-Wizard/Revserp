package competitorgaps

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func TestHopsFromHomeExcludesUnreachable(t *testing.T) {
	pages := []graphPage{
		{URL: "https://example.com/", StatusCode: 200, ContentType: "text/html"},
		{URL: "https://example.com/child", StatusCode: 200, ContentType: "text/html"},
		{URL: "https://example.com/child/grand", StatusCode: 200, ContentType: "text/html"},
		{URL: "https://example.com/sitemap-orphan", StatusCode: 200, ContentType: "text/html"},
	}
	links := []graphLink{
		{SourceURL: "https://example.com/", TargetURL: "https://example.com/child"},
		{SourceURL: "https://example.com/child", TargetURL: "https://example.com/child/grand"},
	}

	hops := hopsFromHome(pages, links, "https://example.com/")
	if hops["https://example.com"] != 0 {
		t.Fatalf("homepage hop = %d, want 0", hops["https://example.com"])
	}
	if hops["https://example.com/child"] != 1 {
		t.Fatalf("child hop = %d, want 1", hops["https://example.com/child"])
	}
	if hops["https://example.com/child/grand"] != 2 {
		t.Fatalf("grandchild hop = %d, want 2", hops["https://example.com/child/grand"])
	}
	if _, ok := hops["https://example.com/sitemap-orphan"]; ok {
		t.Fatalf("sitemap-style orphan should be unreachable")
	}
	if maxHop(hops) != 2 {
		t.Fatalf("radius = %d, want 2", maxHop(hops))
	}
}

func TestHopsFallbackToSeedURL(t *testing.T) {
	pages := []graphPage{
		{URL: "https://example.com/docs", StatusCode: 200, ContentType: "text/html"},
		{URL: "https://example.com/docs/guide", StatusCode: 200, ContentType: "text/html"},
	}
	links := []graphLink{
		{SourceURL: "https://example.com/docs", TargetURL: "https://example.com/docs/guide"},
	}
	hops := hopsFromHome(pages, links, "https://example.com/docs")
	if hops["https://example.com/docs"] != 0 {
		t.Fatalf("seed homepage hop = %d, want 0", hops["https://example.com/docs"])
	}
	if hops["https://example.com/docs/guide"] != 1 {
		t.Fatalf("child hop = %d, want 1", hops["https://example.com/docs/guide"])
	}
}

func TestHopsSkipWhenHomepageMissing(t *testing.T) {
	pages := []graphPage{
		{URL: "https://example.com/about", StatusCode: 200, ContentType: "text/html"},
	}
	hops := hopsFromHome(pages, nil, "")
	if len(hops) != 0 {
		t.Fatalf("hops = %#v, want empty when homepage cannot be found", hops)
	}
}

func TestRadiusComesFromCompetitorNotParent(t *testing.T) {
	parent := crawlSnapshot{
		SeedURL: "https://yoursite.com/",
		Pages: []graphPage{
			scoreablePage("https://yoursite.com/", 100),
			scoreablePage("https://yoursite.com/a", 100),
			scoreablePage("https://yoursite.com/a/b", 100),
			scoreablePage("https://yoursite.com/a/b/c", 100),
		},
		Links: []graphLink{
			{SourceURL: "https://yoursite.com/", TargetURL: "https://yoursite.com/a"},
			{SourceURL: "https://yoursite.com/a", TargetURL: "https://yoursite.com/a/b"},
			{SourceURL: "https://yoursite.com/a/b", TargetURL: "https://yoursite.com/a/b/c"},
		},
	}
	competitor := crawlSnapshot{
		SeedURL: "https://competitor.com/",
		Pages: []graphPage{
			scoreablePage("https://competitor.com/", 80),
			scoreablePage("https://competitor.com/blog", 80),
		},
		Links: []graphLink{
			{SourceURL: "https://competitor.com/", TargetURL: "https://competitor.com/blog"},
		},
	}

	report := buildReport(parent, competitor)
	if report.Radius != 1 {
		t.Fatalf("radius = %d, want 1 from competitor max hop", report.Radius)
	}
	if report.YourPages != 2 {
		t.Fatalf("your_pages = %d, want 2 (parent hops 0 and 1 only)", report.YourPages)
	}
	if report.TheirPages != 2 {
		t.Fatalf("their_pages = %d, want 2", report.TheirPages)
	}
}

func TestIssueCoverageIgnoresInternalLinkingAndSitewide(t *testing.T) {
	pages := []graphPage{
		scoreablePage("https://example.com/", 10),
		scoreablePage("https://example.com/a", 10),
		scoreablePage("https://example.com/b", 10),
		scoreablePage("https://example.com/c", 10),
	}
	issues := []pageIssue{
		{URL: "https://example.com/", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title"},
		{URL: "https://example.com/a", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_meta_description"},
		{URL: "https://example.com/b", Pillar: "seo", Bucket: "internal_linking", IssueType: "orphan_like_page"},
		{URL: "https://example.com/", Pillar: "aeo", Bucket: "trust", IssueType: "missing_about_page"},
	}

	got := bucketCoveragePercent(pages, issues, "serp_metadata")
	if got != 50 {
		t.Fatalf("serp_metadata coverage = %d, want 50", got)
	}
	if bucketCoveragePercent(pages, issues, "aeo") != 0 {
		t.Fatalf("sitewide missing_about_page must not count toward AEO page coverage")
	}
}

func TestPresenceMissingAboutPage(t *testing.T) {
	you := []pageIssue{{IssueType: "missing_about_page"}}
	them := []pageIssue{}
	rows := presenceRows(you, them, pgtype.Bool{}, pgtype.Bool{})
	if len(rows) == 0 || rows[0].ID != "about" {
		t.Fatalf("first presence row = %#v, want about", rows)
	}
	if rows[0].You {
		t.Fatalf("about you = true, want false when missing_about_page is present")
	}
	if !rows[0].Them {
		t.Fatalf("about them = false, want true when missing_about_page is absent")
	}
}

func TestLlmsTxtPresenceFallsBackToIssue(t *testing.T) {
	if presenceValue("llms_txt", "missing_llms_txt", nil, pgtype.Bool{Bool: true, Valid: true}) != true {
		t.Fatalf("has_llms_txt true should report presence")
	}
	if presenceValue("llms_txt", "missing_llms_txt", nil, pgtype.Bool{Bool: false, Valid: true}) != false {
		t.Fatalf("has_llms_txt false should report absence")
	}
	if presenceValue("llms_txt", "missing_llms_txt", []pageIssue{{IssueType: "missing_llms_txt"}}, pgtype.Bool{}) {
		t.Fatalf("null has_llms_txt with missing_llms_txt should be false")
	}
	if !presenceValue("llms_txt", "missing_llms_txt", nil, pgtype.Bool{}) {
		t.Fatalf("null has_llms_txt without missing_llms_txt should be true")
	}
}

func TestMedianWordCounts(t *testing.T) {
	odd := medianInt([]int{1, 100, 3})
	if odd == nil || *odd != 3 {
		t.Fatalf("odd median = %v, want 3", ptrVal(odd))
	}
	even := medianInt([]int{10, 30})
	if even == nil || *even != 20 {
		t.Fatalf("even median = %v, want 20", ptrVal(even))
	}
	if medianInt(nil) != nil {
		t.Fatalf("empty median should be nil")
	}
}

func TestPSIMetricsFromStoredResults(t *testing.T) {
	perf := 87
	lcp := 2.4
	raw := []byte(`[{"url":"https://example.com/","mobile":{"success":true,"performance_score":87,"metrics":{"largest_contentful_paint":2.4},"strategy":"mobile"},"analysis_date":"2026-01-01T00:00:00Z"}]`)
	got := psiMetricsFromResults(raw)
	if got == nil || got.Performance == nil || *got.Performance != perf || got.LCP == nil || *got.LCP != lcp {
		t.Fatalf("psi = %#v, want performance %d lcp %v", got, perf, lcp)
	}
	if psiMetricsFromResults([]byte(`[{"mobile":{"success":false,"error":"timeout"}}]`)) != nil {
		t.Fatalf("unsuccessful PSI should be null")
	}
	if psiMetricsFromResults(nil) != nil {
		t.Fatalf("missing PSI should be null")
	}
}

func TestHomepageSignalsEmptyWhenNoHomePath(t *testing.T) {
	got := homepageSignals([]graphPage{{URL: "https://example.com/about", Title: "About", WordCount: 40}})
	if got.URL != "" || got.Title || got.WordCount != 0 {
		t.Fatalf("homepage signals = %#v, want empty url and zeroed fields", got)
	}
}

func TestSpreadRowsPageAddressablePercents(t *testing.T) {
	youSlice := []graphPage{
		scoreablePage("https://yoursite.com/", 10),
		scoreablePage("https://yoursite.com/a", 10),
		scoreablePage("https://yoursite.com/b", 10),
		scoreablePage("https://yoursite.com/c", 10),
	}
	themSlice := []graphPage{
		scoreablePage("https://competitor.com/", 10),
		scoreablePage("https://competitor.com/a", 10),
		scoreablePage("https://competitor.com/b", 10),
		scoreablePage("https://competitor.com/c", 10),
	}
	youIssues := []pageIssue{
		{URL: "https://yoursite.com/", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title"},
		{URL: "https://yoursite.com/a", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title"},
		{URL: "https://yoursite.com/b", Pillar: "seo", Bucket: "internal_linking", IssueType: "orphan_like_page"},
		{URL: "https://yoursite.com/", Pillar: "aeo", Bucket: "trust", IssueType: "missing_about_page"},
	}

	rows := spreadRows(youSlice, themSlice, youIssues, nil)
	if len(rows) != 1 {
		t.Fatalf("spread = %#v, want only missing_title", rows)
	}
	row := rows[0]
	if row.ID != "missing_title" {
		t.Fatalf("id = %q, want missing_title", row.ID)
	}
	if row.Label != "Missing Title" {
		t.Fatalf("label = %q, want Missing Title", row.Label)
	}
	if row.Pillar != "seo" {
		t.Fatalf("pillar = %q, want seo", row.Pillar)
	}
	if row.You != 50 {
		t.Fatalf("you = %v, want 50", row.You)
	}
	if row.Them != 0 {
		t.Fatalf("them = %v, want 0", row.Them)
	}
}

func TestSpreadRowsSortsByWidestGapThenLabel(t *testing.T) {
	youSlice := []graphPage{
		scoreablePage("https://yoursite.com/", 10),
		scoreablePage("https://yoursite.com/a", 10),
	}
	themSlice := []graphPage{
		scoreablePage("https://competitor.com/", 10),
		scoreablePage("https://competitor.com/a", 10),
	}
	youIssues := []pageIssue{
		{URL: "https://yoursite.com/", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title"},
		{URL: "https://yoursite.com/a", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title"},
		{URL: "https://yoursite.com/", Pillar: "seo", Bucket: "content_structure", IssueType: "missing_h1"},
		{URL: "https://yoursite.com/", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_meta_description"},
	}

	rows := spreadRows(youSlice, themSlice, youIssues, nil)
	if len(rows) != 3 {
		t.Fatalf("spread len = %d, want 3", len(rows))
	}
	if rows[0].ID != "missing_title" {
		t.Fatalf("first = %q, want missing_title (widest gap)", rows[0].ID)
	}
	if rows[1].ID != "missing_h1" || rows[2].ID != "missing_meta_description" {
		t.Fatalf("tied gaps = %q, %q, want missing_h1 then missing_meta_description by label", rows[1].ID, rows[2].ID)
	}
}

func TestPageHealthHistogram(t *testing.T) {
	slice := []graphPage{
		scoreablePage("https://example.com/", 10),
		scoreablePage("https://example.com/a", 10),
		scoreablePage("https://example.com/b", 10),
		scoreablePage("https://example.com/c", 10),
	}
	issues := []pageIssue{
		{URL: "https://example.com/", Pillar: "seo", Bucket: "internal_linking", IssueType: "orphan_like_page"},
		{URL: "https://example.com/", Pillar: "aeo", Bucket: "trust", IssueType: "missing_about_page"},
		{URL: "https://example.com/b", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title"},
		{URL: "https://example.com/b", Pillar: "seo", Bucket: "content_structure", IssueType: "missing_h1"},
	}
	for i := 0; i < 25; i++ {
		issues = append(issues, pageIssue{
			URL:       "https://example.com/c",
			Pillar:    "seo",
			Bucket:    "serp_metadata",
			IssueType: "missing_title",
		})
	}

	got := pageHealthSide(slice, issues)
	if got.TotalPages != 4 {
		t.Fatalf("total_pages = %d, want 4", got.TotalPages)
	}
	if len(got.Buckets) != PageHealthBuckets {
		t.Fatalf("buckets len = %d, want %d", len(got.Buckets), PageHealthBuckets)
	}
	if got.Buckets[0] != 2 || got.Buckets[2] != 1 || got.Buckets[20] != 1 {
		t.Fatalf("buckets = %#v, want [0]=2 [2]=1 [20]=1", got.Buckets)
	}
}

func TestBuildReportSpreadAndPageHealth(t *testing.T) {
	parent := crawlSnapshot{
		SeedURL: "https://yoursite.com/",
		Pages: []graphPage{
			scoreablePage("https://yoursite.com/", 10),
			scoreablePage("https://yoursite.com/a", 10),
		},
		Links: []graphLink{
			{SourceURL: "https://yoursite.com/", TargetURL: "https://yoursite.com/a"},
		},
		Issues: []pageIssue{
			{URL: "https://yoursite.com/", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title"},
			{URL: "https://yoursite.com/a", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title"},
		},
	}
	competitor := crawlSnapshot{
		SeedURL: "https://competitor.com/",
		Pages: []graphPage{
			scoreablePage("https://competitor.com/", 10),
			scoreablePage("https://competitor.com/a", 10),
		},
		Links: []graphLink{
			{SourceURL: "https://competitor.com/", TargetURL: "https://competitor.com/a"},
		},
	}

	report := buildReport(parent, competitor)
	if report.Version != ReportVersion {
		t.Fatalf("version = %q, want %q", report.Version, ReportVersion)
	}
	if len(report.Spread) == 0 {
		t.Fatalf("spread empty, want missing_title")
	}
	if report.Spread[0].ID != "missing_title" || report.Spread[0].You != 100 || report.Spread[0].Them != 0 {
		t.Fatalf("spread[0] = %#v, want missing_title you=100 them=0", report.Spread[0])
	}
	if len(report.PageHealth.You.Buckets) != PageHealthBuckets || len(report.PageHealth.Them.Buckets) != PageHealthBuckets {
		t.Fatalf("page health buckets you=%d them=%d, want %d", len(report.PageHealth.You.Buckets), len(report.PageHealth.Them.Buckets), PageHealthBuckets)
	}
	if report.PageHealth.You.TotalPages != 2 || report.PageHealth.Them.TotalPages != 2 {
		t.Fatalf("page health totals you=%d them=%d, want 2", report.PageHealth.You.TotalPages, report.PageHealth.Them.TotalPages)
	}
}

func TestIssueAndPresenceRowContract(t *testing.T) {
	report := buildReport(crawlSnapshot{}, crawlSnapshot{})
	if report.Version != ReportVersion {
		t.Fatalf("version = %q, want %q", report.Version, ReportVersion)
	}
	wantIssues := []string{"serp_metadata", "content_structure", "content_quality", "indexability", "technical_seo", "media_optimization", "aeo"}
	if len(report.Issues) != len(wantIssues) {
		t.Fatalf("issues len = %d, want %d", len(report.Issues), len(wantIssues))
	}
	for i, id := range wantIssues {
		if report.Issues[i].ID != id {
			t.Fatalf("issues[%d].id = %q, want %q", i, report.Issues[i].ID, id)
		}
	}
	wantPresence := []string{"about", "contact", "policy", "llms_txt", "organization_schema", "website_schema", "homepage_trust"}
	if len(report.Presence) != len(wantPresence) {
		t.Fatalf("presence len = %d, want %d", len(report.Presence), len(wantPresence))
	}
	for i, id := range wantPresence {
		if report.Presence[i].ID != id {
			t.Fatalf("presence[%d].id = %q, want %q", i, report.Presence[i].ID, id)
		}
	}
}

func TestUnscoreableReachablePagesAreNotSliced(t *testing.T) {
	pages := []graphPage{
		scoreablePage("https://example.com/", 10),
		{URL: "https://example.com/broken", StatusCode: 404, ContentType: "text/html"},
	}
	links := []graphLink{{SourceURL: "https://example.com/", TargetURL: "https://example.com/broken"}}
	hops := hopsFromHome(pages, links, "https://example.com/")
	if hops["https://example.com/broken"] != 1 {
		t.Fatalf("broken page should still set hop distance")
	}
	sliced := slicePages(pages, hops, maxHop(hops))
	if len(sliced) != 1 || sliced[0].URL != "https://example.com/" {
		t.Fatalf("slice = %#v, want only the scoreable homepage", sliced)
	}
}

func scoreablePage(pageURL string, wordCount int) graphPage {
	return graphPage{
		URL:         pageURL,
		StatusCode:  200,
		ContentType: "text/html",
		WordCount:   wordCount,
	}
}

func TestSliceRescoreDropsPagesPastCompetitorRadius(t *testing.T) {
	parent := crawlSnapshot{
		ID:      "parent",
		SeedURL: "https://yoursite.com/",
		Pages: []graphPage{
			scoreablePage("https://yoursite.com/", 100),
			scoreablePage("https://yoursite.com/a", 100),
			scoreablePage("https://yoursite.com/a/b", 100),
			scoreablePage("https://yoursite.com/a/b/c", 100),
		},
		Links: []graphLink{
			{SourceURL: "https://yoursite.com/", TargetURL: "https://yoursite.com/a"},
			{SourceURL: "https://yoursite.com/a", TargetURL: "https://yoursite.com/a/b"},
			{SourceURL: "https://yoursite.com/a/b", TargetURL: "https://yoursite.com/a/b/c"},
		},
		Issues: []pageIssue{
			{URL: "https://yoursite.com/a/b/c", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"},
		},
	}
	competitor := crawlSnapshot{
		ID:      "competitor",
		SeedURL: "https://competitor.com/",
		Pages: []graphPage{
			scoreablePage("https://competitor.com/", 80),
			scoreablePage("https://competitor.com/blog", 80),
		},
		Links: []graphLink{
			{SourceURL: "https://competitor.com/", TargetURL: "https://competitor.com/blog"},
		},
	}

	report := buildReport(parent, competitor)
	if report.YouBreakdown.TotalScoredPages != 2 {
		t.Fatalf("you total_scored_pages = %d, want 2 hop-matched pages", report.YouBreakdown.TotalScoredPages)
	}
	if report.ThemBreakdown.TotalScoredPages != 2 {
		t.Fatalf("them total_scored_pages = %d, want 2", report.ThemBreakdown.TotalScoredPages)
	}
	if len(report.YourSlice) != 2 || report.YourSlice[0].Hop != 0 {
		t.Fatalf("your_slice = %#v, want 2 pages starting at hop 0", report.YourSlice)
	}
	if breakdownHasIssue(report.YouBreakdown, "missing_title") {
		t.Fatalf("deep-page missing_title leaked into hop-1 slice score")
	}
}

func TestSliceRescoreDropsRatioDerivedOpenGraph(t *testing.T) {
	parent := crawlSnapshot{
		ID:      "parent",
		SeedURL: "https://yoursite.com/",
		Pages:   []graphPage{scoreablePage("https://yoursite.com/", 40)},
		Issues: []pageIssue{
			{URL: "https://yoursite.com/", Pillar: "aeo", Bucket: "trust", IssueType: "weak_open_graph_coverage", Severity: "high"},
			{URL: "https://yoursite.com/", Pillar: "aeo", Bucket: "experience", IssueType: "missing_about_page", Severity: "medium"},
		},
	}
	competitor := crawlSnapshot{
		ID:      "competitor",
		SeedURL: "https://competitor.com/",
		Pages:   []graphPage{scoreablePage("https://competitor.com/", 40)},
	}

	report := buildReport(parent, competitor)
	if breakdownHasIssue(report.YouBreakdown, "weak_open_graph_coverage") {
		t.Fatalf("weak_open_graph_coverage must be dropped from slice scores")
	}
	if !breakdownHasIssue(report.YouBreakdown, "missing_about_page") {
		t.Fatalf("missing_about_page should still count as a binary sitewide fact")
	}
}

func TestSliceRescoreReusesStoredPSI(t *testing.T) {
	psi := []byte(`[{"url":"https://yoursite.com/","mobile":{"success":true,"performance_score":91,"metrics":{"largest_contentful_paint":2.4},"strategy":"mobile"}}]`)
	parent := crawlSnapshot{
		ID:         "parent",
		SeedURL:    "https://yoursite.com/",
		PSIResults: psi,
		Pages:      []graphPage{scoreablePage("https://yoursite.com/", 40)},
	}
	competitor := crawlSnapshot{
		ID:      "competitor",
		SeedURL: "https://competitor.com/",
		Pages:   []graphPage{scoreablePage("https://competitor.com/", 40)},
	}

	report := buildReport(parent, competitor)
	got := bucketScore(report.YouBreakdown, "pagespeed", "psi_cwv")
	if got != 91 {
		t.Fatalf("psi_cwv bucket = %d, want stored PSI 91", got)
	}
}

func breakdownHasIssue(snapshot shared.ScoreBreakdownSnapshot, issueType string) bool {
	for _, pillar := range snapshot.Pillars {
		for _, bucket := range pillar.Buckets {
			for _, issue := range bucket.Issues {
				if issue.ID == issueType {
					return true
				}
			}
		}
	}
	return false
}

func bucketScore(snapshot shared.ScoreBreakdownSnapshot, pillarID, bucketID string) int32 {
	for _, pillar := range snapshot.Pillars {
		if pillar.ID != pillarID {
			continue
		}
		for _, bucket := range pillar.Buckets {
			if bucket.ID == bucketID {
				return bucket.Score
			}
		}
	}
	return 0
}

func ptrVal(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
