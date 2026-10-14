package config

import (
	"testing"
	"time"
)

func TestLoadCrawlTimeoutDefaultsAndOverrides(t *testing.T) {
	for _, test := range []struct {
		name            string
		crawlTimeout    string
		analysisTimeout string
		wantCrawl       time.Duration
		wantAnalysis    time.Duration
	}{
		{"defaults", "", "", 60 * time.Minute, 30 * time.Minute},
		{"analysis override", "", "45m", 60 * time.Minute, 45 * time.Minute},
		{"shorter analysis override", "", "15m", 60 * time.Minute, 15 * time.Minute},
		{"independent overrides", "90m", "40m", 90 * time.Minute, 40 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("CRAWL_TIMEOUT", test.crawlTimeout)
			t.Setenv("CRAWL_ANALYSIS_TIMEOUT", test.analysisTimeout)
			cfg := Load()
			if cfg.CrawlTimeout != test.wantCrawl {
				t.Errorf("CrawlTimeout = %s, want %s", cfg.CrawlTimeout, test.wantCrawl)
			}
			if cfg.AnalysisTimeout != test.wantAnalysis {
				t.Errorf("AnalysisTimeout = %s, want %s", cfg.AnalysisTimeout, test.wantAnalysis)
			}
		})
	}
}
