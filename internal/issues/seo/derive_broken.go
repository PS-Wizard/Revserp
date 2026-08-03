package seo

import (
	"fmt"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// DeriveBrokenPageIssues derives issues for pages that have no usable content:
// hard HTTP errors, soft 404s, and pages that could not be fetched.
//
// These pages are deliberately kept out of the normal content derivation. A 404
// has no title, meta description, or H1, so running the content rules over it
// would report four or five separate content problems for what is one problem —
// the page is broken. This emits only that one problem.
func DeriveBrokenPageIssues(pageFacts []shared.PageFact) []shared.DerivedIssue {
	var derivedIssues []shared.DerivedIssue
	for _, pageFact := range pageFacts {
		switch {
		case pageFact.FetchError != "":
			derivedIssues = append(derivedIssues, newIssue(pageFact, "technical_seo", "fetch_failed", "high",
				"Page could not be fetched",
				fmt.Sprintf("The crawler could not retrieve this page: %s.", pageFact.FetchError)))

		case pageFact.Soft404:
			derivedIssues = append(derivedIssues, newIssue(pageFact, "technical_seo", "soft_404", "high",
				"Page returns a not-found message with a success status",
				fmt.Sprintf("Page answered HTTP %d but serves the site's \"not found\" content. Search engines treat this as a soft 404 and may drop the URL without reporting an error. Return a real 404 or 410 status for URLs that do not exist.", pageFact.StatusCode)))

		case pageFact.StatusCode >= 500:
			derivedIssues = append(derivedIssues, newIssue(pageFact, "technical_seo", "server_error_status", "high",
				"Page returned a server error",
				fmt.Sprintf("Page returned HTTP %d.", pageFact.StatusCode)))

		case pageFact.StatusCode >= 400:
			derivedIssues = append(derivedIssues, newIssue(pageFact, "technical_seo", "client_error_status", "high",
				"Page returned a client error",
				fmt.Sprintf("Page returned HTTP %d.", pageFact.StatusCode)))
		}
	}
	return derivedIssues
}
