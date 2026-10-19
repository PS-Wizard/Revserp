package seo

import (
	"context"
	"fmt"
	"testing"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// buildSyntheticNearDuplicateCorpus builds a large workload of templated pages
// for cancellation and scale tests. Section templates repeat across page
// groups; it makes no claim about which pairs the 0.9 MinHash engine matches.
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

// buildGaoyaCopyCorpus builds base pages each with a one-word near-copy plus
// unrelated pages, all with varied main bodies for the 0.9 threshold.
func buildGaoyaCopyCorpus(bases int, unrelated int) []shared.PageFact {
	pageFacts := make([]shared.PageFact, 0, 2*bases+unrelated)
	for base := 0; base < bases; base++ {
		body := buildGaoyaBodyText(int64(base+1), 500)
		pageFacts = append(pageFacts, shared.PageFact{
			URL:         fmt.Sprintf("https://example.com/guide-%d", base),
			Title:       fmt.Sprintf("Guide %d", base),
			VisibleText: body,
		}, shared.PageFact{
			URL:         fmt.Sprintf("https://example.com/guide-%d-copy", base),
			Title:       fmt.Sprintf("Guide %d copy", base),
			VisibleText: buildGaoyaNearCopy(body, 100+base, "kiwi"),
		})
	}
	for other := 0; other < unrelated; other++ {
		pageFacts = append(pageFacts, shared.PageFact{
			URL:         fmt.Sprintf("https://example.com/other-%d", other),
			Title:       fmt.Sprintf("Other %d", other),
			VisibleText: buildGaoyaBodyText(int64(1000+other), 500),
		})
	}
	return pageFacts
}

func TestGaoyaNearDuplicateFindsMainBodyCopies(t *testing.T) {
	if !gaoyaBinaryAvailable() {
		t.Skip("revserp-gaoya not on PATH (or GAOYA_PATH unset); run with the binary available for full coverage")
	}
	const bases = 10
	pageFacts := buildGaoyaCopyCorpus(bases, 10)
	if err := EnrichPageFactsWithContentFingerprintsWithContext(context.Background(), pageFacts); err != nil {
		t.Fatalf("fingerprints: %v", err)
	}

	_, evidence, err := deriveNearDuplicateIssuesWithEvidence(context.Background(), pageFacts)
	if err != nil {
		t.Fatalf("deriveNearDuplicateIssuesWithEvidence: %v", err)
	}
	paired := make(map[string]string)
	for _, relation := range evidence.Relations {
		paired[relation.LeftPage.URL] = relation.RightPage.URL
	}
	for base := 0; base < bases; base++ {
		wantLeft := fmt.Sprintf("https://example.com/guide-%d", base)
		wantRight := fmt.Sprintf("https://example.com/guide-%d-copy", base)
		if paired[wantLeft] != wantRight {
			t.Fatalf("base %d paired with %q, want %q (all relations: %v)", base, paired[wantLeft], wantRight, gaoyaRelationURLs(evidence))
		}
	}
	if len(evidence.Relations) != bases {
		t.Fatalf("got %d relations, want exactly %d (no unrelated pairs)", len(evidence.Relations), bases)
	}
}

func BenchmarkGaoyaNearDuplicateDetection(b *testing.B) {
	if !gaoyaBinaryAvailable() {
		b.Skip("revserp-gaoya not on PATH (or GAOYA_PATH unset)")
	}
	pageFacts := buildGaoyaCopyCorpus(2, 2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := runGaoyaNearDuplicateDetection(context.Background(), pageFacts); err != nil {
			b.Fatalf("runGaoyaNearDuplicateDetection: %v", err)
		}
	}
}
