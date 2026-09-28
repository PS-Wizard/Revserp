package worker

import (
	"testing"
)

// The admin override wins when valid; garbage that the CHECK constraint
// should have stopped fails loudly instead of silently using env.
func TestEffectiveCrawlPageWorkerCount(t *testing.T) {
	for _, stored := range []int32{1, 4, 12, 100} {
		if got, err := effectiveCrawlPageWorkerCount(stored); err != nil || got != int(stored) {
			t.Errorf("effectiveCrawlPageWorkerCount(%d) = %d, %v; want %d, nil", stored, got, err, stored)
		}
	}
	for _, stored := range []int32{-100, -1, 0, 101, 1000} {
		if got, err := effectiveCrawlPageWorkerCount(stored); err == nil {
			t.Errorf("effectiveCrawlPageWorkerCount(%d) = %d, nil; want an error", stored, got)
		}
	}
}
