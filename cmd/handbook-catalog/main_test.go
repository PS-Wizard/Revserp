package main

import (
	"reflect"
	"testing"
)

func TestBuildHandbookDetectorPillar(t *testing.T) {
	manifest := buildHandbookDetectorPillar(
		map[string]float64{"trust": 1, "answerability": 1},
		map[string]float64{"missing_title": 1, "homepage_missing_org_contact_trust_signals": 1, "google_psi_lcp": 1},
	)
	if !reflect.DeepEqual(manifest.Buckets, []string{"answerability", "trust"}) {
		t.Fatalf("buckets are not sorted: %v", manifest.Buckets)
	}
	if !reflect.DeepEqual(manifest.IssueTypes, []string{"google_psi_lcp", "homepage_missing_org_contact_trust_signals", "missing_title"}) {
		t.Fatalf("issue types are not sorted: %v", manifest.IssueTypes)
	}
	for _, test := range []struct{ issueType, scope string }{
		{"missing_title", "page"},
		{"homepage_missing_org_contact_trust_signals", "site"},
		{"google_psi_lcp", "origin"},
	} {
		t.Run(test.issueType, func(t *testing.T) {
			if got := manifest.Scopes[test.issueType]; got != test.scope {
				t.Fatalf("scope = %q, want %q", got, test.scope)
			}
		})
	}
}
