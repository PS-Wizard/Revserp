package crawler

import (
	"encoding/json"
	"testing"
)

// Progress persists as plain JSON: a stage plus actual completed affected
// rows. No percent, no ETA, no totals.
func TestCrawlAnalysisProgressSerializesCounts(t *testing.T) {
	encoded, err := json.Marshal(CrawlAnalysisProgress{Stage: AnalysisStageCheckingLinks, LinksResolved: 640000})
	if err != nil {
		t.Fatalf("marshal progress: %v", err)
	}
	if string(encoded) != `{"stage":"checking_links","links_resolved":640000}` {
		t.Fatalf("progress JSON = %s, want exact stage/count shape", encoded)
	}
}

func TestCrawlAnalysisProgressOmitsZeroCount(t *testing.T) {
	encoded, err := json.Marshal(CrawlAnalysisProgress{Stage: AnalysisStageCheckingLinks})
	if err != nil {
		t.Fatalf("marshal progress: %v", err)
	}
	if string(encoded) != `{"stage":"checking_links"}` {
		t.Fatalf("progress JSON = %s, want stage only", encoded)
	}
}

func TestCrawlAnalysisProgressRoundTrips(t *testing.T) {
	var decoded CrawlAnalysisProgress
	if err := json.Unmarshal([]byte(`{"stage":"deriving_issues"}`), &decoded); err != nil {
		t.Fatalf("unmarshal legacy progress without count: %v", err)
	}
	if decoded.Stage != AnalysisStageDerivingIssues || decoded.LinksResolved != 0 {
		t.Fatalf("decoded progress = %+v, want stage only", decoded)
	}
}
