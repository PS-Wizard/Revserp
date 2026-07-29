package worker

import (
	"testing"
	"time"
)

// A reclaim cutoff at or below CrawlTimeout fails crawls that are still running
// inside their own budget. The recurring sweep regressed exactly this way once,
// using a flat 15 minutes against a 30-minute timeout, so pin the invariant for
// the default and for CRAWL_TIMEOUT overrides in both directions.
func TestReclaimCutoffAlwaysExceedsCrawlTimeout(t *testing.T) {
	for _, crawlTimeout := range []time.Duration{
		time.Minute,
		15 * time.Minute,
		defaultCrawlTimeout,
		2 * time.Hour,
		24 * time.Hour,
	} {
		w := &Worker{cfg: Config{CrawlTimeout: crawlTimeout}}

		for _, floor := range []time.Duration{0, staleRunningCrawlAge} {
			if got := w.reclaimCutoff(floor); got <= crawlTimeout {
				t.Errorf(
					"reclaimCutoff(%v) = %v with CrawlTimeout %v; must exceed the timeout or a live crawl is failed mid-run",
					floor, got, crawlTimeout,
				)
			}
		}
	}
}

func TestReclaimCutoffRespectsFloor(t *testing.T) {
	w := &Worker{cfg: Config{CrawlTimeout: defaultCrawlTimeout}}

	if got := w.reclaimCutoff(staleRunningCrawlAge); got != staleRunningCrawlAge {
		t.Errorf("startup cutoff = %v, want the %v floor", got, staleRunningCrawlAge)
	}

	want := defaultCrawlTimeout + staleRunningCrawlGrace
	if got := w.reclaimCutoff(0); got != want {
		t.Errorf("periodic cutoff = %v, want %v", got, want)
	}
}
