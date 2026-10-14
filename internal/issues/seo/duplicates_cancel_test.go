package seo

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

type pairVerificationCancelContext struct {
	context.Context
	checks      atomic.Int64
	cancelAfter int64
	cancel      context.CancelFunc
}

func (ctx *pairVerificationCancelContext) Err() error {
	if ctx.checks.Add(1) == ctx.cancelAfter {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

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
	t.Parallel()
	pageFacts := buildSyntheticNearDuplicateCorpus(120)
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

func TestNearDuplicateCancelledDuringVerify(t *testing.T) {
	pageFacts := buildSyntheticNearDuplicateCorpus(1200)
	if err := EnrichPageFactsWithContentFingerprintsWithContext(context.Background(), pageFacts); err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	candidatePairs, err := buildNearDuplicateCandidatePairs(context.Background(), pageFacts)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	profiles, err := buildNearDuplicateProfiles(context.Background(), pageFacts)
	if err != nil {
		t.Fatalf("profiles: %v", err)
	}
	pairList := make([][2]int, 0, len(candidatePairs))
	for pair := range candidatePairs {
		pairList = append(pairList, pair)
	}
	workerCount := min(runtime.NumCPU(), len(pairList))
	for len(pairList) < workerCount*deriveCtxCheckPairs*2 {
		pairList = append(pairList, pairList...)
	}
	t.Logf("verify workload: pages=%d pairs=%d", len(pageFacts), len(pairList))

	beforeGoroutines := runtime.NumGoroutine()
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &pairVerificationCancelContext{
		Context:     baseCtx,
		cancelAfter: int64(workerCount + 2),
		cancel:      cancel,
	}
	start := time.Now()
	matches, err := verifyNearDuplicatePairs(ctx, pageFacts, profiles, pairList)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if matches != nil {
		t.Fatalf("cancelled verify returned %d partial matches", len(matches))
	}
	if elapsed > 30*time.Second {
		t.Fatalf("cancelled verify took %s, workers did not stop promptly", elapsed)
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > beforeGoroutines && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if leaked := runtime.NumGoroutine() - beforeGoroutines; leaked > 0 {
		t.Fatalf("verify leaked %d goroutines after cancellation", leaked)
	}
}
