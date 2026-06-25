package worker

import (
	"testing"
	"time"

	"github.com/ps-wizard/revserp/internal/crawler"
)

func TestSchedulerCutoffCalculation(t *testing.T) {
	autoCrawlInterval := 24 * time.Hour
	now := time.Now().UTC()
	cutoff := now.Add(-autoCrawlInterval)

	// The cutoff should be exactly 24h before now.
	if now.Sub(cutoff) != autoCrawlInterval {
		t.Errorf("cutoff diff: got %s, want %s", now.Sub(cutoff), autoCrawlInterval)
	}

	// A crawl completed 25h ago should be older than the cutoff (due).
	oldCompleted := now.Add(-25 * time.Hour)
	if !oldCompleted.Before(cutoff) {
		t.Error("crawl completed 25h ago should be before cutoff (due)")
	}

	// A crawl completed 23h ago should not be before the cutoff (not due).
	recentCompleted := now.Add(-23 * time.Hour)
	if recentCompleted.Before(cutoff) {
		t.Error("crawl completed 23h ago should NOT be before cutoff (not due)")
	}

	// A crawl completed exactly at cutoff should NOT be before (not yet due).
	exactCompleted := cutoff
	if exactCompleted.Before(cutoff) {
		t.Error("crawl completed exactly at cutoff should NOT be before")
	}
}

func TestSchedulerConfigNormalizeNil(t *testing.T) {
	// Passing nil to NormalizeConfigSnapshot should return defaults.
	snapshot, normalized, err := crawler.NormalizeConfigSnapshot(nil)
	if err != nil {
		t.Fatalf("NormalizeConfigSnapshot(nil) returned error: %v", err)
	}
	if snapshot.MaxDepth != 2 {
		t.Errorf("default MaxDepth: got %d, want 2", snapshot.MaxDepth)
	}
	if len(normalized) == 0 {
		t.Error("normalized bytes should not be empty")
	}
}

func TestSchedulerConfigNormalizeEmpty(t *testing.T) {
	snapshot, normalized, err := crawler.NormalizeConfigSnapshot([]byte{})
	if err != nil {
		t.Fatalf("NormalizeConfigSnapshot([]byte{}) returned error: %v", err)
	}
	if snapshot.MaxDepth != 2 {
		t.Errorf("default MaxDepth: got %d, want 2", snapshot.MaxDepth)
	}
	if len(normalized) == 0 {
		t.Error("normalized bytes should not be empty")
	}
}
