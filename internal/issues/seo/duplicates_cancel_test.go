package seo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func cancelTestLinks(count int) []shared.LinkFact {
	linkFacts := make([]shared.LinkFact, 0, count)
	for i := 0; i < count; i++ {
		linkFacts = append(linkFacts, shared.LinkFact{
			SourceURL:    fmt.Sprintf("https://example.com/page-%d", i%500),
			TargetURL:    fmt.Sprintf("https://example.com/target-%d", i),
			TargetStatus: 200,
		})
	}
	return linkFacts
}

func canonicalDerivedIssues(issues []shared.DerivedIssue) []string {
	keys := make([]string, 0, len(issues))
	for _, issue := range issues {
		keys = append(keys, issue.IssueType+"\x00"+issue.URL+"\x00"+issue.Message+"\x00"+issue.Details)
	}
	sort.Strings(keys)
	return keys
}

func canonicalGroups(evidence DuplicateEvidence) []string {
	keys := make([]string, 0, len(evidence.Groups))
	for _, group := range evidence.Groups {
		members := make([]string, 0, len(group.Members))
		for _, member := range group.Members {
			members = append(members, member.URL)
		}
		sort.Strings(members)
		keys = append(keys, group.IssueType+"\x00"+fmt.Sprintf("%q", members))
	}
	sort.Strings(keys)
	return keys
}

func TestDeriveWithContextMatchesPure(t *testing.T) {
	pageFacts := []shared.PageFact{
		{URL: "https://example.com/base", ContentType: "text/html", Title: "Harbor planning guide", VisibleText: "Check tides and weather before departure."},
		{URL: "https://example.com/copy", ContentType: "text/html", Title: "Harbor planning guide", VisibleText: "Check tides and weather before departure."},
		{URL: "https://example.com/variant", ContentType: "text/html", Title: "Harbor planning guide", VisibleText: "Check tides and weather before arrival."},
	}
	linkFacts := cancelTestLinks(2000)
	pureIssues, pureEvidence := DeriveIssuesWithDuplicateEvidence(pageFacts, linkFacts)
	exactGroups := 0
	for _, group := range pureEvidence.Groups {
		if group.IssueType == DuplicateIssueTypeExactDuplicateContent {
			exactGroups++
			if len(group.Members) != 2 {
				t.Fatalf("exact group members = %d, want 2", len(group.Members))
			}
		}
	}
	if exactGroups != 1 {
		t.Fatalf("exact groups = %d, want 1", exactGroups)
	}
	ctxIssues, ctxEvidence, err := DeriveIssuesWithDuplicateEvidenceWithContext(context.Background(), pageFacts, linkFacts)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := canonicalDerivedIssues(ctxIssues), canonicalDerivedIssues(pureIssues); fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("issue sets differ: ctx=%d pure=%d", len(got), len(want))
	}
	if got, want := canonicalGroups(ctxEvidence), canonicalGroups(pureEvidence); fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("group sets differ: ctx=%d pure=%d", len(got), len(want))
	}
}

func TestExactDuplicateDerivationHonorsCancelledContext(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, expireCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expireCancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"cancelled", cancelled, context.Canceled},
		{"expired", expired, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, pages := range [][]shared.PageFact{nil, {{ContentSHA256: "same"}, {ContentSHA256: "same"}}} {
				issues, evidence, err := deriveDuplicateContentIssuesWithEvidence(tc.ctx, pages)
				if !errors.Is(err, tc.want) {
					t.Fatalf("error = %v, want %v", err, tc.want)
				}
				if len(issues) != 0 || len(evidence.Groups) != 0 {
					t.Fatal("cancelled derive returned partial results")
				}
			}
		})
	}
}

func TestSeoDeriveLinkScanCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := DeriveIssuesWithDuplicateEvidenceWithContext(ctx, nil, cancelTestLinks(2000))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
