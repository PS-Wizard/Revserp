package app

import "testing"

// Compare URL change types must accept the explicit not_verified and
// no_longer_detected classifications while keeping the legacy resolved value
// valid for older API consumers.
func TestNormalizeCompareURLChangeType(t *testing.T) {
	valid := map[string]string{
		"":                    "",
		"all":                 "",
		"new":                 "new",
		"resolved":            "resolved",
		"no_longer_detected":  "no_longer_detected",
		"not_verified":        "not_verified",
		"changed":             "changed",
		"unchanged":           "unchanged",
		"improved":            "improved",
		"regressed":           "regressed",
		"  NOT_VERIFIED  ":    "not_verified",
		"No_Longer_Detected ": "no_longer_detected",
	}
	for input, want := range valid {
		if got, ok := normalizeCompareURLChangeType(input); !ok || got != want {
			t.Errorf("normalizeCompareURLChangeType(%q) = (%q, %v), want (%q, true)", input, got, ok, want)
		}
	}
	for _, invalid := range []string{"fixed", "gone", "verified"} {
		if _, ok := normalizeCompareURLChangeType(invalid); ok {
			t.Errorf("normalizeCompareURLChangeType(%q) accepted invalid value", invalid)
		}
	}
}
