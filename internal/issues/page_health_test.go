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

func TestCalculatePageHealthScores(t *testing.T) {
	baseConfig := DefaultScoringConfig()

	tests := []struct {
		name   string
		pages  []PageHealthPageSignal
		issues []PageHealthIssueSignal
		config shared.ScoringConfig
	}{
		{
			name:   "clean=100",
			pages:  []PageHealthPageSignal{{CrawlPageID: newPageUUID(), StatusCode: 200, ContentType: "text/html"}},
			config: baseConfig,
		},
		{
			name:   "broken=0 status 404",
			pages:  []PageHealthPageSignal{{CrawlPageID: newPageUUID(), StatusCode: 404, ContentType: "text/html"}},
			config: baseConfig,
		},
		{
			name:   "broken=0 soft404",
			pages:  []PageHealthPageSignal{{CrawlPageID: newPageUUID(), StatusCode: 200, ContentType: "text/html", Soft404: true}},
			config: baseConfig,
		},
		{
			name:   "broken=0 fetch error",
			pages:  []PageHealthPageSignal{{CrawlPageID: newPageUUID(), StatusCode: 200, ContentType: "text/html", FetchError: "timeout"}},
			config: baseConfig,
		},
		{
			name:   "non-HTML omitted",
			pages:  []PageHealthPageSignal{{CrawlPageID: newPageUUID(), StatusCode: 200, ContentType: "application/pdf"}},
			config: baseConfig,
		},
		{
			name:   "broken non-HTML still 0",
			pages:  []PageHealthPageSignal{{CrawlPageID: newPageUUID(), StatusCode: 500, ContentType: "application/pdf"}},
			config: baseConfig,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scores := CalculatePageHealthScores(tc.pages, tc.issues, tc.config)
			switch tc.name {
			case "clean=100":
				if len(scores) != 1 || scores[0].HealthScore != 100 {
					t.Fatalf("expected 100, got %+v", scores)
				}
			case "broken=0 status 404", "broken=0 soft404", "broken=0 fetch error", "broken non-HTML still 0":
				if len(scores) != 1 || scores[0].HealthScore != 0 {
					t.Fatalf("expected 0, got %+v", scores)
				}
			case "non-HTML omitted":
				if len(scores) != 0 {
					t.Fatalf("expected omitted (0 results), got %+v", scores)
				}
			}
		})
	}

	t.Run("issue formula", func(t *testing.T) {
		pageID := newPageUUID()
		pages := []PageHealthPageSignal{{CrawlPageID: pageID, StatusCode: 200, ContentType: "text/html"}}
		issues := []PageHealthIssueSignal{{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"}}
		scores := CalculatePageHealthScores(pages, issues, DefaultScoringConfig())
		if len(scores) != 1 || scores[0].HealthScore != 88 {
			t.Fatalf("expected 88, got %+v", scores)
		}
	})

	t.Run("same-bucket soft sum", func(t *testing.T) {
		pageID := newPageUUID()
		pages := []PageHealthPageSignal{{CrawlPageID: pageID, StatusCode: 200, ContentType: "text/html"}}
		issues := []PageHealthIssueSignal{
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"},
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "duplicate_title", Severity: "high"},
		}
		scores := CalculatePageHealthScores(pages, issues, DefaultScoringConfig())
		if len(scores) != 1 || scores[0].HealthScore != 83 {
			t.Fatalf("expected 83, got %+v", scores)
		}
	})

	t.Run("cross-bucket sum", func(t *testing.T) {
		pageID := newPageUUID()
		pages := []PageHealthPageSignal{{CrawlPageID: pageID, StatusCode: 200, ContentType: "text/html"}}
		issues := []PageHealthIssueSignal{
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"},
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "content_quality", IssueType: "thin_content", Severity: "medium"},
		}
		scores := CalculatePageHealthScores(pages, issues, DefaultScoringConfig())
		if len(scores) != 1 || scores[0].HealthScore != 81 {
			t.Fatalf("expected 81, got %+v", scores)
		}
	})

	t.Run("sitewide excluded", func(t *testing.T) {
		pageID := newPageUUID()
		pages := []PageHealthPageSignal{{CrawlPageID: pageID, StatusCode: 200, ContentType: "text/html"}}
		issues := []PageHealthIssueSignal{{CrawlPageID: pageID, Pillar: "aeo", Bucket: "trust", IssueType: "missing_website_schema", Severity: "high"}}
		scores := CalculatePageHealthScores(pages, issues, DefaultScoringConfig())
		if len(scores) != 1 || scores[0].HealthScore != 100 {
			t.Fatalf("expected 100 sitewide excluded, got %+v", scores)
		}
	})

	t.Run("google_psi excluded", func(t *testing.T) {
		pageID := newPageUUID()
		pages := []PageHealthPageSignal{{CrawlPageID: pageID, StatusCode: 200, ContentType: "text/html"}}
		issues := []PageHealthIssueSignal{
			{CrawlPageID: pageID, Pillar: "pagespeed", Bucket: "psi_cwv", IssueType: "google_psi_lcp", Severity: "high"},
			{CrawlPageID: pageID, Pillar: "pagespeed", Bucket: "psi_cwv", IssueType: "google_psi_mobile_performance", Severity: "high"},
		}
		scores := CalculatePageHealthScores(pages, issues, DefaultScoringConfig())
		if len(scores) != 1 || scores[0].HealthScore != 100 {
			t.Fatalf("expected 100 google_psi excluded, got %+v", scores)
		}
	})

	t.Run("config edits change score", func(t *testing.T) {
		pageID := newPageUUID()
		pages := []PageHealthPageSignal{{CrawlPageID: pageID, StatusCode: 200, ContentType: "text/html"}}
		issues := []PageHealthIssueSignal{{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"}}
		custom := DefaultScoringConfig()
		if _, ok := custom.Pillars["seo"]; ok {
			p := custom.Pillars["seo"]
			newMap := make(map[string]float64, len(p.IssuePenaltyByType))
			for k, v := range p.IssuePenaltyByType {
				newMap[k] = v
			}
			newMap["missing_title"] = 20
			p.IssuePenaltyByType = newMap
			custom.Pillars["seo"] = p
		}
		scores := CalculatePageHealthScores(pages, issues, custom)
		if len(scores) != 1 || scores[0].HealthScore != 80 {
			t.Fatalf("expected 80 with custom penalty, got %+v", scores)
		}
		custom2 := DefaultScoringConfig()
		custom2.SeverityMultipliers = map[string]float64{"high": 0.5, "medium": 0.6, "low": 0.3}
		scores2 := CalculatePageHealthScores(pages, issues, custom2)
		if len(scores2) != 1 || scores2[0].HealthScore != 94 {
			t.Fatalf("expected 94 with custom multiplier, got %+v", scores2)
		}
		pageID2 := newPageUUID()
		pages2 := []PageHealthPageSignal{{CrawlPageID: pageID2, StatusCode: 200, ContentType: "text/html"}}
		issues2 := []PageHealthIssueSignal{
			{CrawlPageID: pageID2, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"},
			{CrawlPageID: pageID2, Pillar: "seo", Bucket: "serp_metadata", IssueType: "duplicate_title", Severity: "high"},
		}
		custom3 := DefaultScoringConfig()
		custom3.SoftSumDecay = 0.1
		scores3 := CalculatePageHealthScores(pages2, issues2, custom3)
		if len(scores3) != 1 || scores3[0].HealthScore != 87 {
			t.Fatalf("expected 87 with custom decay, got %+v", scores3)
		}
	})

	t.Run("duplicate dedup retains largest", func(t *testing.T) {
		pageID := newPageUUID()
		pages := []PageHealthPageSignal{{CrawlPageID: pageID, StatusCode: 200, ContentType: "text/html"}}
		issues := []PageHealthIssueSignal{
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"},
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "low"},
			{CrawlPageID: pageID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"},
		}
		scores := CalculatePageHealthScores(pages, issues, DefaultScoringConfig())
		if len(scores) != 1 || scores[0].HealthScore != 88 {
			t.Fatalf("expected 88 deduped, got %+v", scores)
		}
	})

	t.Run("invalid page ID ignored", func(t *testing.T) {
		pageID := newPageUUID()
		pages := []PageHealthPageSignal{{CrawlPageID: pageID, StatusCode: 200, ContentType: "text/html"}}
		issues := []PageHealthIssueSignal{{CrawlPageID: pgtype.UUID{Valid: false}, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"}}
		scores := CalculatePageHealthScores(pages, issues, DefaultScoringConfig())
		if len(scores) != 1 || scores[0].HealthScore != 100 {
			t.Fatalf("expected 100 invalid ID ignored, got %+v", scores)
		}
	})

	t.Run("deterministic page input order", func(t *testing.T) {
		idA := newPageUUID()
		idB := newPageUUID()
		pages := []PageHealthPageSignal{{CrawlPageID: idB, StatusCode: 200, ContentType: "text/html"}, {CrawlPageID: idA, StatusCode: 200, ContentType: "text/html"}}
		issues := []PageHealthIssueSignal{{CrawlPageID: idA, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high"}}
		scores := CalculatePageHealthScores(pages, issues, DefaultScoringConfig())
		if len(scores) != 2 {
			t.Fatalf("expected 2 scores, got %d", len(scores))
		}
		if scores[0].CrawlPageID != idB || scores[1].CrawlPageID != idA {
			t.Fatalf("expected input order B,A, got %v %v", scores[0].CrawlPageID, scores[1].CrawlPageID)
		}
		if scores[0].HealthScore != 100 || scores[1].HealthScore != 88 {
			t.Fatalf("expected scores 100,88 got %d,%d", scores[0].HealthScore, scores[1].HealthScore)
		}
	})
}
