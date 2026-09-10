package issues

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func newPageUUID() pgtype.UUID {
	u := uuid.New()
	var b [16]byte = u
	return pgtype.UUID{Bytes: b, Valid: true}
}

func htmlPage(id pgtype.UUID) PageHealthPageSignal {
	return PageHealthPageSignal{CrawlPageID: id, StatusCode: 200, ContentType: "text/html"}
}

func findPillar(t *testing.T, score PageHealthScore, pillarID string) PageHealthPillarScore {
	t.Helper()
	for _, pillar := range score.Pillars {
		if pillar.ID == pillarID {
			return pillar
		}
	}
	t.Fatalf("pillar %q not found in %+v", pillarID, score.Pillars)
	return PageHealthPillarScore{}
}

func findBucket(t *testing.T, pillar PageHealthPillarScore, bucketID string) PageHealthBucketScore {
	t.Helper()
	for _, bucket := range pillar.Buckets {
		if bucket.ID == bucketID {
			return bucket
		}
	}
	t.Fatalf("bucket %q not found in pillar %+v", bucketID, pillar)
	return PageHealthBucketScore{}
}

func TestCalculatePageHealthScores(t *testing.T) {
	t.Run("clean html scores 100 everywhere", func(t *testing.T) {
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(newPageUUID())}, nil, DefaultScoringConfig())
		if len(scores) != 1 {
			t.Fatalf("expected 1 score, got %d", len(scores))
		}
		score := scores[0]
		if score.HealthScore != 100 {
			t.Fatalf("expected overall 100, got %d", score.HealthScore)
		}
		wantPillarOrder := []string{"seo", "aeo", "pagespeed"}
		if len(score.Pillars) != len(wantPillarOrder) {
			t.Fatalf("expected %d pillars, got %+v", len(wantPillarOrder), score.Pillars)
		}
		for index, pillarID := range wantPillarOrder {
			pillar := score.Pillars[index]
			if pillar.ID != pillarID || pillar.Score != 100 {
				t.Fatalf("pillar %d: expected %s=100, got %s=%d", index, pillarID, pillar.ID, pillar.Score)
			}
			for _, bucket := range pillar.Buckets {
				if bucket.ID == "psi_cwv" {
					t.Fatalf("pillar %s: psi_cwv bucket must be omitted, got %+v", pillarID, pillar.Buckets)
				}
				if bucket.Score != 100 {
					t.Fatalf("bucket %s: expected 100, got %d", bucket.ID, bucket.Score)
				}
			}
		}
	})

	t.Run("bucket order matches SortedBucketIDs minus psi_cwv", func(t *testing.T) {
		config := DefaultScoringConfig()
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(newPageUUID())}, nil, config)
		for _, pillar := range scores[0].Pillars {
			wantOrder := make([]string, 0, len(pillar.Buckets))
			for _, bucketID := range shared.SortedBucketIDs(config.Pillars[pillar.ID].BucketWeights) {
				if bucketID != "psi_cwv" {
					wantOrder = append(wantOrder, bucketID)
				}
			}
			if len(pillar.Buckets) != len(wantOrder) {
				t.Fatalf("pillar %s: bucket count mismatch", pillar.ID)
			}
			for index, bucket := range pillar.Buckets {
				if bucket.ID != wantOrder[index] {
					t.Fatalf("pillar %s: bucket %d expected %s, got %s", pillar.ID, index, wantOrder[index], bucket.ID)
				}
			}
		}
	})

	t.Run("pagespeed psi_cwv omitted with normalized remaining weights", func(t *testing.T) {
		// slow_response_time high = 12 penalty -> server_responsiveness 88;
		// remaining weights 0.40 + 0.30 = 0.70, so pillar = (88*0.40 + 100*0.30)/0.70 = 93.14 -> 93;
		// overall = 0.65*100 + 0.20*100 + 0.15*93 = 98.95 -> 99.
		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{{CrawlPageID: pageID, Pillar: "pagespeed", Bucket: "server_responsiveness", IssueType: "slow_response_time", Severity: "high"}}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, DefaultScoringConfig())
		score := scores[0]
		if score.HealthScore != 99 {
			t.Fatalf("expected overall 99, got %d", score.HealthScore)
		}
		psiPillar := findPillar(t, score, "pagespeed")
		if len(psiPillar.Buckets) != 2 {
			t.Fatalf("expected 2 pagespeed buckets without psi_cwv, got %+v", psiPillar.Buckets)
		}
		if psiPillar.Score != 93 {
			t.Fatalf("expected normalized pagespeed pillar 93, got %d", psiPillar.Score)
		}
		if got := findBucket(t, psiPillar, "server_responsiveness").Score; got != 88 {
			t.Fatalf("expected server_responsiveness 88, got %d", got)
		}
		if got := findBucket(t, psiPillar, "page_weight").Score; got != 100 {
			t.Fatalf("expected page_weight 100, got %d", got)
		}
	})
	t.Run("hard 404 is zero breakdown", func(t *testing.T) {
		scores := CalculatePageHealthScores([]PageHealthPageSignal{{CrawlPageID: newPageUUID(), StatusCode: 404, ContentType: "text/html"}}, nil, DefaultScoringConfig())
		if len(scores) != 1 || scores[0].HealthScore != 0 {
			t.Fatalf("expected one zero score, got %+v", scores)
		}
		assertZeroBreakdown(t, scores[0])
	})

	t.Run("soft 404 is zero breakdown", func(t *testing.T) {
		scores := CalculatePageHealthScores([]PageHealthPageSignal{{CrawlPageID: newPageUUID(), StatusCode: 200, ContentType: "text/html", Soft404: true}}, nil, DefaultScoringConfig())
		if len(scores) != 1 || scores[0].HealthScore != 0 {
			t.Fatalf("expected one zero score, got %+v", scores)
		}
		assertZeroBreakdown(t, scores[0])
	})

	t.Run("broken non-HTML is zero breakdown", func(t *testing.T) {
		scores := CalculatePageHealthScores([]PageHealthPageSignal{{CrawlPageID: newPageUUID(), StatusCode: 500, ContentType: "application/pdf"}}, nil, DefaultScoringConfig())
		if len(scores) != 1 || scores[0].HealthScore != 0 {
			t.Fatalf("expected one zero score, got %+v", scores)
		}
		assertZeroBreakdown(t, scores[0])
	})

	t.Run("soft 404 non-HTML is zero breakdown", func(t *testing.T) {
		scores := CalculatePageHealthScores([]PageHealthPageSignal{{CrawlPageID: newPageUUID(), StatusCode: 200, ContentType: "application/pdf", Soft404: true}}, nil, DefaultScoringConfig())
		if len(scores) != 1 || scores[0].HealthScore != 0 {
			t.Fatalf("expected one zero score, got %+v", scores)
		}
		assertZeroBreakdown(t, scores[0])
	})

	t.Run("fetch error pages are unscored", func(t *testing.T) {
		scores := CalculatePageHealthScores([]PageHealthPageSignal{{CrawlPageID: newPageUUID(), StatusCode: 200, ContentType: "text/html", FetchError: "timeout"}}, nil, DefaultScoringConfig())
		if len(scores) != 0 {
			t.Fatalf("expected no score for fetch error, got %+v", scores)
		}
	})

	t.Run("healthy non-HTML pages are unscored", func(t *testing.T) {
		scores := CalculatePageHealthScores([]PageHealthPageSignal{{CrawlPageID: newPageUUID(), StatusCode: 200, ContentType: "application/pdf"}}, nil, DefaultScoringConfig())
		if len(scores) != 0 {
			t.Fatalf("expected no score for non-HTML, got %+v", scores)
		}
	})

	t.Run("exact weighted behavior: single seo issue", func(t *testing.T) {
		// missing_title high = 12 penalty -> serp_metadata (weight 0.20) bucket 88;
		// seo pillar = 0.20*88 + 0.80*100 = 97.6 -> 98;
		// overall = 0.65*98 + 0.20*100 + 0.15*100 = 98.7 -> 99.
		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"}}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, DefaultScoringConfig())
		score := scores[0]
		if score.HealthScore != 99 {
			t.Fatalf("expected overall 99, got %d", score.HealthScore)
		}
		seoPillar := findPillar(t, score, "seo")
		if seoPillar.Score != 98 {
			t.Fatalf("expected seo pillar 98, got %d", seoPillar.Score)
		}
		if got := findBucket(t, seoPillar, "serp_metadata").Score; got != 88 {
			t.Fatalf("expected serp_metadata 88, got %d", got)
		}
		if got := findBucket(t, seoPillar, "indexability").Score; got != 100 {
			t.Fatalf("expected untouched bucket 100, got %d", got)
		}
	})

	t.Run("exact weighted behavior: same-bucket soft sum", func(t *testing.T) {
		// missing_title 12 + duplicate_title 10 decayed 0.5 -> bucket penalty 17 -> 83;
		// seo = 0.20*83 + 0.80*100 = 96.6 -> 97; overall = 0.65*97 + 35 = 98.05 -> 98.
		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"},
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "duplicate_title", Severity: "high"},
		}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, DefaultScoringConfig())
		score := scores[0]
		if score.HealthScore != 98 {
			t.Fatalf("expected overall 98, got %d", score.HealthScore)
		}
		seoPillar := findPillar(t, score, "seo")
		if seoPillar.Score != 97 {
			t.Fatalf("expected seo pillar 97, got %d", seoPillar.Score)
		}
		if got := findBucket(t, seoPillar, "serp_metadata").Score; got != 83 {
			t.Fatalf("expected serp_metadata 83, got %d", got)
		}
	})

	t.Run("exact weighted behavior: cross-bucket sum", func(t *testing.T) {
		// serp_metadata 88; thin_content medium = 12*0.6 = 7.2 -> content_quality 93;
		// seo = 0.20*88 + 0.20*93 + 0.60*100 = 96.2 -> 96; overall = 0.65*96 + 35 = 97.4 -> 97.
		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"},
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "content_quality", IssueType: "thin_content", Severity: "medium"},
		}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, DefaultScoringConfig())
		score := scores[0]
		if score.HealthScore != 97 {
			t.Fatalf("expected overall 97, got %d", score.HealthScore)
		}
		seoPillar := findPillar(t, score, "seo")
		if seoPillar.Score != 96 {
			t.Fatalf("expected seo pillar 96, got %d", seoPillar.Score)
		}
	})

	t.Run("sitewide issues excluded", func(t *testing.T) {
		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{{CrawlPageID: pageID, Pillar: "aeo", Bucket: "trust", IssueType: "missing_website_schema", Severity: "high"}}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, DefaultScoringConfig())
		if scores[0].HealthScore != 100 {
			t.Fatalf("expected 100 sitewide excluded, got %+v", scores)
		}
	})

	t.Run("google_psi origin issues excluded", func(t *testing.T) {
		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{
			{CrawlPageID: pageID, Pillar: "pagespeed", Bucket: "psi_cwv", IssueType: "google_psi_lcp", Severity: "high"},
			{CrawlPageID: pageID, Pillar: "pagespeed", Bucket: "psi_cwv", IssueType: "google_psi_mobile_performance", Severity: "high"},
		}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, DefaultScoringConfig())
		if scores[0].HealthScore != 100 {
			t.Fatalf("expected 100 google_psi excluded, got %+v", scores)
		}
	})

	t.Run("duplicate dedup retains largest penalty", func(t *testing.T) {
		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "low"},
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"},
		}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, DefaultScoringConfig())
		seoPillar := findPillar(t, scores[0], "seo")
		if got := findBucket(t, seoPillar, "serp_metadata").Score; got != 88 {
			t.Fatalf("expected deduped bucket 88, got %d", got)
		}
	})

	t.Run("custom penalties change bucket score", func(t *testing.T) {
		// custom missing_title penalty 20 -> serp_metadata 80; seo = 0.20*80+0.80*100 = 96;
		// overall = 0.65*96 + 35 = 97.4 -> 97.
		custom := DefaultScoringConfig()
		seoConfig := custom.Pillars["seo"]
		seoConfig.IssuePenaltyByType = cloneFloatMap(seoConfig.IssuePenaltyByType)
		seoConfig.IssuePenaltyByType["missing_title"] = 20
		custom.Pillars["seo"] = seoConfig

		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"}}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, custom)
		if scores[0].HealthScore != 97 {
			t.Fatalf("expected overall 97 with custom penalty, got %+v", scores[0])
		}
		if got := findBucket(t, findPillar(t, scores[0], "seo"), "serp_metadata").Score; got != 80 {
			t.Fatalf("expected serp_metadata 80, got %d", got)
		}
	})

	t.Run("custom severity multipliers", func(t *testing.T) {
		// high multiplier 0.5 -> penalty 6 -> serp_metadata 94; seo = 0.20*94+0.80*100 = 98.8 -> 99;
		// overall = 0.65*99 + 35 = 99.35 -> 99.
		custom := DefaultScoringConfig()
		custom.SeverityMultipliers = map[string]float64{"high": 0.5, "medium": 0.6, "low": 0.3}

		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"}}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, custom)
		if scores[0].HealthScore != 99 {
			t.Fatalf("expected overall 99 with custom multiplier, got %+v", scores[0])
		}
		if got := findBucket(t, findPillar(t, scores[0], "seo"), "serp_metadata").Score; got != 94 {
			t.Fatalf("expected serp_metadata 94, got %d", got)
		}
	})

	t.Run("custom soft sum decay", func(t *testing.T) {
		// decay 0.1: penalty 12 + 10*0.1 = 13 -> serp_metadata 87; seo = 0.20*87+0.80*100 = 97.4 -> 97;
		// overall = 0.65*97 + 35 = 98.05 -> 98.
		custom := DefaultScoringConfig()
		custom.SoftSumDecay = 0.1

		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"},
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "duplicate_title", Severity: "high"},
		}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, custom)
		if scores[0].HealthScore != 98 {
			t.Fatalf("expected overall 98 with custom decay, got %+v", scores[0])
		}
		if got := findBucket(t, findPillar(t, scores[0], "seo"), "serp_metadata").Score; got != 87 {
			t.Fatalf("expected serp_metadata 87, got %d", got)
		}
	})

	t.Run("custom bucket weights", func(t *testing.T) {
		// serp_metadata weight raised to 0.5; weights normalize to sum 1, so
		// seo = (88*0.5 + 100*0.8)/1.3 = 95.38 -> 95; overall = 0.65*95 + 35 = 96.75 -> 97.
		custom := DefaultScoringConfig()
		seoConfig := custom.Pillars["seo"]
		seoConfig.BucketWeights = cloneFloatMap(seoConfig.BucketWeights)
		seoConfig.BucketWeights["serp_metadata"] = 0.5
		custom.Pillars["seo"] = seoConfig

		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"}}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, custom)
		if scores[0].HealthScore != 97 {
			t.Fatalf("expected overall 97 with custom bucket weight, got %+v", scores[0])
		}
		if got := findPillar(t, scores[0], "seo").Score; got != 95 {
			t.Fatalf("expected seo pillar 95, got %d", got)
		}
	})

	t.Run("custom overall weights normalized", func(t *testing.T) {
		// Weights summing to 0.5 normalize to the default 0.65/0.20/0.15 split -> overall 99.
		custom := DefaultScoringConfig()
		custom.OverallWeights = map[string]float64{"seo": 0.325, "aeo": 0.10, "pagespeed": 0.075}

		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"}}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, custom)
		if scores[0].HealthScore != 99 {
			t.Fatalf("expected normalized overall 99, got %d", scores[0].HealthScore)
		}
	})

	t.Run("custom overall weights reweight pillars", func(t *testing.T) {
		// All weight on seo: overall = seo pillar score 98.
		custom := DefaultScoringConfig()
		custom.OverallWeights = map[string]float64{"seo": 1.0, "aeo": 0, "pagespeed": 0}

		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"}}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, custom)
		if scores[0].HealthScore != 98 {
			t.Fatalf("expected overall 98, got %d", scores[0].HealthScore)
		}
	})

	t.Run("invalid page ID issue ignored", func(t *testing.T) {
		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{{CrawlPageID: pgtype.UUID{Valid: false}, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"}}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, DefaultScoringConfig())
		if scores[0].HealthScore != 100 {
			t.Fatalf("expected 100 invalid ID ignored, got %+v", scores)
		}
	})

	t.Run("issue for unknown pillar ignored", func(t *testing.T) {
		pageID := newPageUUID()
		issues := []PageHealthIssueSignal{{CrawlPageID: pageID, Pillar: "unknown_pillar", Bucket: "bucket", IssueType: "missing_title", Severity: "high"}}
		scores := CalculatePageHealthScores([]PageHealthPageSignal{htmlPage(pageID)}, issues, DefaultScoringConfig())
		if scores[0].HealthScore != 100 {
			t.Fatalf("expected 100 unknown pillar ignored, got %+v", scores)
		}
	})

	t.Run("deterministic page input order", func(t *testing.T) {
		idA := newPageUUID()
		idB := newPageUUID()
		pages := []PageHealthPageSignal{htmlPage(idB), htmlPage(idA)}
		issues := []PageHealthIssueSignal{{CrawlPageID: idA, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"}}
		scores := CalculatePageHealthScores(pages, issues, DefaultScoringConfig())
		if len(scores) != 2 {
			t.Fatalf("expected 2 scores, got %d", len(scores))
		}
		if scores[0].CrawlPageID != idB || scores[1].CrawlPageID != idA {
			t.Fatalf("expected input order B,A, got %v %v", scores[0].CrawlPageID, scores[1].CrawlPageID)
		}
		if scores[0].HealthScore != 100 || scores[1].HealthScore != 99 {
			t.Fatalf("expected scores 100,99 got %d,%d", scores[0].HealthScore, scores[1].HealthScore)
		}
	})
}

func assertZeroBreakdown(t *testing.T, score PageHealthScore) {
	t.Helper()
	if score.HealthScore != 0 {
		t.Fatalf("expected overall 0, got %d", score.HealthScore)
	}
	if len(score.Pillars) != 3 {
		t.Fatalf("expected 3 pillars in zero breakdown, got %+v", score.Pillars)
	}
	for _, pillar := range score.Pillars {
		if pillar.Score != 0 {
			t.Fatalf("pillar %s: expected 0, got %d", pillar.ID, pillar.Score)
		}
		if len(pillar.Buckets) == 0 {
			t.Fatalf("pillar %s: expected buckets, got none", pillar.ID)
		}
		for _, bucket := range pillar.Buckets {
			if bucket.Score != 0 {
				t.Fatalf("bucket %s/%s: expected 0, got %d", pillar.ID, bucket.ID, bucket.Score)
			}
		}
	}
}
