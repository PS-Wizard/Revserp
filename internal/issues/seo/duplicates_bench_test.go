package seo

import (
	"fmt"
	"testing"
	"time"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// buildSyntheticNearDuplicateCorpus mimics a real site: shared nav/footer
// boilerplate across all pages, section templates shared within page groups
// (the medium-frequency shingles that drive candidate-pair blowup), and a
// small unique tail per page.
func buildSyntheticNearDuplicateCorpus(pageCount int) []shared.PageFact {
	boilerplate := ""
	for i := 0; i < 120; i++ {
		boilerplate += fmt.Sprintf("navword%d footword%d ", i, i)
	}
	templates := make([]string, 12)
	for t := range templates {
		body := ""
		for i := 0; i < 140; i++ {
			body += fmt.Sprintf("section%dterm%d cluster%dword%d ", t, i, t, i)
		}
		templates[t] = body
	}
	pageFacts := make([]shared.PageFact, pageCount)
	for p := range pageFacts {
		tail := ""
		for i := 0; i < 40; i++ {
			tail += fmt.Sprintf("uniq%dp%d ", p, i)
		}
		tmpl := templates[p%len(templates)]
		visible := boilerplate + tmpl + tail
		pageFacts[p] = shared.PageFact{
			URL:         fmt.Sprintf("https://example.com/page-%d", p),
			ContentType: "text/html",
			Title:       fmt.Sprintf("Section %d Page", p%len(templates)),
			H1:          fmt.Sprintf("Section %d Heading", p%len(templates)),
			VisibleText: visible,
		}
	}
	return pageFacts
}

func TestNearDuplicateOldVsNew(t *testing.T) {
	pageFacts := buildSyntheticNearDuplicateCorpus(300)
	EnrichPageFactsWithContentFingerprints(pageFacts)
	candidatePairs := buildNearDuplicateCandidatePairs(pageFacts)

	pairList := make([][2]int, 0, len(candidatePairs))
	for pair := range candidatePairs {
		pairList = append(pairList, pair)
	}

	// Old serial path: rebuild trigrams per pair via the reference function.
	oldStart := time.Now()
	oldMatches := map[[2]int]struct{}{}
	for _, pair := range pairList {
		left := pageFacts[pair[0]]
		right := pageFacts[pair[1]]
		if left.ContentSHA256 == "" || right.ContentSHA256 == "" || left.ContentSHA256 == right.ContentSHA256 {
			continue
		}
		if calculateNearDuplicateContentSimilarity(left, right) >= nearDuplicateContentSimilarityThreshold {
			oldMatches[pair] = struct{}{}
		}
	}
	oldElapsed := time.Since(oldStart)

	// New path: precompute profiles once + parallel verify.
	newStart := time.Now()
	profiles := buildNearDuplicateProfiles(pageFacts)
	newMatchList := verifyNearDuplicatePairs(pageFacts, profiles, pairList)
	newElapsed := time.Since(newStart)

	newMatches := map[[2]int]struct{}{}
	for _, pair := range newMatchList {
		newMatches[pair] = struct{}{}
	}

	if len(oldMatches) != len(newMatches) {
		t.Fatalf("match count differs: old=%d new=%d", len(oldMatches), len(newMatches))
	}
	for pair := range oldMatches {
		if _, ok := newMatches[pair]; !ok {
			t.Fatalf("new path missing pair present in old: %v", pair)
		}
	}

	t.Logf("pages=%d candidate_pairs=%d matches=%d | old(serial)=%s new(parallel+cached)=%s speedup=%.1fx",
		len(pageFacts), len(pairList), len(oldMatches),
		oldElapsed.Round(time.Millisecond), newElapsed.Round(time.Millisecond),
		float64(oldElapsed)/float64(newElapsed))
}
