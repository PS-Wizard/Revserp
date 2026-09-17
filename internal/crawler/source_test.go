package crawler

import "testing"

func TestIsManualLikeSource(t *testing.T) {
	manualLike := []string{"manual", "auto", "mcp"}
	for _, source := range manualLike {
		if !IsManualLikeSource(source) {
			t.Errorf("IsManualLikeSource(%q) = false, want true", source)
		}
	}
	for _, source := range []string{"competitor", "", "unknown"} {
		if IsManualLikeSource(source) {
			t.Errorf("IsManualLikeSource(%q) = true, want false", source)
		}
	}
}
