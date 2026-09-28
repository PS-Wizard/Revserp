package competitorcrawls

import (
	"encoding/json"
	"testing"

	"github.com/ps-wizard/revserp/internal/crawler"
)

func TestCompetitorConfigSnapshotSetsFrontierFlags(t *testing.T) {
	raw, err := competitorConfigSnapshot([]byte(`{"honour_robots_txt":true}`), 15)
	if err != nil {
		t.Fatalf("competitorConfigSnapshot: %v", err)
	}

	snapshot, _, err := crawler.NormalizeConfigSnapshot(raw)
	if err != nil {
		t.Fatalf("NormalizeConfigSnapshot: %v", err)
	}
	if snapshot.SkipSitemapSeed {
		t.Fatal("expected skip_sitemap_seed false (copied from parent)")
	}
	if snapshot.ForceFullCrawl {
		t.Fatal("expected force_full_crawl false so later competitor crawls can reuse a baseline")
	}
	if !snapshot.HonourRobotsTxt {
		t.Fatal("expected honour_robots_txt copied from parent")
	}
	if snapshot.MaxDepth != competitorMaxDepth {
		t.Fatalf("max_depth = %d, want %d", snapshot.MaxDepth, competitorMaxDepth)
	}
	if snapshot.MaxPages == nil || *snapshot.MaxPages != 15 {
		t.Fatalf("max_pages = %#v, want 15", snapshot.MaxPages)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if v, ok := decoded["skip_sitemap_seed"]; ok && v != false {
		t.Fatalf("raw skip_sitemap_seed = %v", decoded["skip_sitemap_seed"])
	}

	rawSeed, err := competitorConfigSnapshot([]byte(`{"honour_robots_txt":true,"skip_sitemap_seed":true}`), 15)
	if err != nil {
		t.Fatalf("competitorConfigSnapshot seeded: %v", err)
	}
	seeded, _, err := crawler.NormalizeConfigSnapshot(rawSeed)
	if err != nil {
		t.Fatalf("NormalizeConfigSnapshot seeded: %v", err)
	}
	if !seeded.SkipSitemapSeed {
		t.Fatal("expected skip_sitemap_seed true copied from parent")
	}
}

func TestIsParentCompetitorUniqueViolation(t *testing.T) {
	if isParentCompetitorUniqueViolation(nil) {
		t.Fatal("nil error must not match")
	}
}
