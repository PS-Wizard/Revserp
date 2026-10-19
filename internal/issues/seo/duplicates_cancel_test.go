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

func canonicalRelations(evidence DuplicateEvidence) []string {
	keys := make([]string, 0, len(evidence.Relations))
	for _, relation := range evidence.Relations {
		left, right := relation.LeftPage.URL, relation.RightPage.URL
		if left > right {
			left, right = right, left
		}
		keys = append(keys, fmt.Sprintf("%s\x00%s\x00%.6f", left, right, relation.Similarity))
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
	if !gaoyaBinaryAvailable() {
		t.Skip("revserp-gaoya not on PATH (or GAOYA_PATH unset); run with the binary available for full coverage")
	}
	base := buildGaoyaBodyText(21, 500)
	pageFacts := []shared.PageFact{
		{URL: "https://example.com/base", ContentType: "text/html", Title: "Base guide", VisibleText: base},
		{URL: "https://example.com/copy", ContentType: "text/html", Title: "Copy guide", VisibleText: buildGaoyaNearCopy(base, 100, "kiwi")},
		{URL: "https://example.com/other", ContentType: "text/html", Title: "Other guide", VisibleText: buildGaoyaBodyText(22, 500)},
	}
	linkFacts := cancelTestLinks(2000)

	pureIssues, pureEvidence := DeriveIssuesWithDuplicateEvidence(pageFacts, linkFacts)
	if len(pureEvidence.Relations) == 0 {
		t.Fatal("fixture must produce near-duplicate relations")
	}
	for _, group := range pureEvidence.Groups {
		if group.IssueType == DuplicateIssueTypeNearDuplicateContent {
			t.Fatal("near-duplicate relations must not add new evidence groups")
		}
	}
	ctxIssues, ctxEvidence, err := DeriveIssuesWithDuplicateEvidenceWithContext(context.Background(), pageFacts, linkFacts)
	if err != nil {
		t.Fatalf("WithContext: %v", err)
	}
	if got, want := canonicalDerivedIssues(ctxIssues), canonicalDerivedIssues(pureIssues); fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("issue sets differ: ctx=%d pure=%d", len(got), len(want))
	}
	if got, want := canonicalRelations(ctxEvidence), canonicalRelations(pureEvidence); fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("relation sets differ: ctx=%d pure=%d", len(got), len(want))
	}
	if got, want := canonicalGroups(ctxEvidence), canonicalGroups(pureEvidence); fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("group sets differ: ctx=%d pure=%d", len(got), len(want))
	}
}

func TestNearDuplicateCancelledBeforeStart(t *testing.T) {
	t.Parallel()
	pageFacts := buildSyntheticNearDuplicateCorpus(200)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	issues, evidence, err := deriveNearDuplicateIssuesWithEvidence(ctx, pageFacts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if issues != nil || len(evidence.Groups) != 0 || len(evidence.Relations) != 0 {
		t.Fatalf("cancelled derive returned partial results: %d issues", len(issues))
	}
	expired, expireCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expireCancel()
	_, _, err = deriveNearDuplicateIssuesWithEvidence(expired, pageFacts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}

func TestSeoDeriveLinkScanCancelled(t *testing.T) {
	t.Parallel()
	pageFacts := buildSyntheticNearDuplicateCorpus(10)
	linkFacts := cancelTestLinks(200000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := DeriveIssuesWithDuplicateEvidenceWithContext(ctx, pageFacts, linkFacts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestNearDuplicateCancelledDuringGaoyaRun(t *testing.T) {
	gaoyaStubPath(t)
	t.Setenv("GAOYA_STUB_MODE", "hang")
	pageFacts := gaoyaStubTextPages(
		"https://example.com/a",
		"https://example.com/b",
		"https://example.com/c",
		"https://example.com/d",
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	issues, evidence, err := deriveNearDuplicateIssuesWithEvidence(ctx, pageFacts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if issues != nil || len(evidence.Groups) != 0 || len(evidence.Relations) != 0 {
		t.Fatalf("cancelled derive returned partial results: %d issues", len(issues))
	}
}
