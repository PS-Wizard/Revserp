package issues

import (
	"slices"

	"github.com/ps-wizard/revserp/internal/issues/aeo"
	pagespeed "github.com/ps-wizard/revserp/internal/issues/page_speed"
	"github.com/ps-wizard/revserp/internal/issues/seo"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// DeriveIssues builds backend issue rows from persisted crawl facts.
func DeriveIssues(pageFacts []shared.PageFact, linkFacts []shared.LinkFact) []shared.DerivedIssue {
	var derivedIssues []shared.DerivedIssue
	scoreablePageFacts := make([]shared.PageFact, 0, len(pageFacts))
	for _, pageFact := range pageFacts {
		if !shared.IsScoreableContentType(pageFact.ContentType) {
			continue
		}
		scoreablePageFacts = append(scoreablePageFacts, pageFact)
	}
	derivedIssues = append(derivedIssues, seo.DeriveIssues(slices.Clone(scoreablePageFacts), linkFacts)...)
	derivedIssues = append(derivedIssues, aeo.DeriveIssues(scoreablePageFacts, linkFacts)...)
	derivedIssues = append(derivedIssues, pagespeed.DeriveIssues(scoreablePageFacts, linkFacts)...)
	return derivedIssues
}
