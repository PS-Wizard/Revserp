package app

import (
	"errors"
	"strings"
	"testing"
)

func TestAutoCrawlFrequencyBounds(t *testing.T) {
	if defaultAutoCrawlFrequencyDays != 7 {
		t.Fatalf("defaultAutoCrawlFrequencyDays = %d, want 7 for new settings", defaultAutoCrawlFrequencyDays)
	}
	if maxAutoCrawlFrequencyDays != 60 {
		t.Fatalf("maxAutoCrawlFrequencyDays = %d, want 60", maxAutoCrawlFrequencyDays)
	}
	for _, days := range []int32{1, 7, 14, 30, 60} {
		if err := checkAutoCrawlFrequencyDays(days); err != nil {
			t.Fatalf("checkAutoCrawlFrequencyDays(%d): %v, want acceptance", days, err)
		}
	}
	for _, days := range []int32{0, -1, 61, 90} {
		err := checkAutoCrawlFrequencyDays(days)
		if err == nil {
			t.Fatalf("checkAutoCrawlFrequencyDays(%d) succeeded, want rejection", days)
		}
		if !strings.Contains(err.Error(), "frequency_days must be between 1 and 60") {
			t.Fatalf("checkAutoCrawlFrequencyDays(%d) = %q, want the 1..60 range", days, err)
		}
		var badReq *autoCrawlBadRequestError
		if !errors.As(err, &badReq) {
			t.Fatalf("checkAutoCrawlFrequencyDays(%d) = %T, want HTTP 400 mapping", days, err)
		}
	}
}
