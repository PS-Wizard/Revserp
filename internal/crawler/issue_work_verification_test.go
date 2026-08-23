package crawler

import "testing"

func TestClassifyIssueWorkVerification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		observed         bool
		requiredEvidence bool
		issuePresent     bool
		wantAttempt      string
		wantWorkItem     string
	}{
		{name: "fixed", observed: true, requiredEvidence: true, wantAttempt: "fixed", wantWorkItem: "fixed"},
		{name: "still open", observed: true, requiredEvidence: true, issuePresent: true, wantAttempt: "still_open", wantWorkItem: "open"},
		{name: "page missing", requiredEvidence: true, wantAttempt: "not_verified", wantWorkItem: "awaiting_verification"},
		{name: "required evidence missing", observed: true, wantAttempt: "not_verified", wantWorkItem: "awaiting_verification"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			attempt, workItem := classifyIssueWorkVerification(test.observed, test.requiredEvidence, test.issuePresent)
			if attempt != test.wantAttempt || workItem != test.wantWorkItem {
				t.Fatalf("got (%q, %q), want (%q, %q)", attempt, workItem, test.wantAttempt, test.wantWorkItem)
			}
		})
	}
}
