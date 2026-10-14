package pagespeed

import (
	"context"
	"fmt"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

const deriveCtxCheckPages = 256

// DeriveIssues builds PageSpeed issues from persisted crawl facts.
func DeriveIssues(pageFacts []shared.PageFact, _ []shared.LinkFact) []shared.DerivedIssue {
	derivedIssues, _ := DeriveIssuesWithContext(context.Background(), pageFacts, nil)
	return derivedIssues
}

func DeriveIssuesWithContext(ctx context.Context, pageFacts []shared.PageFact, _ []shared.LinkFact) ([]shared.DerivedIssue, error) {
	var derivedIssues []shared.DerivedIssue
	for pageNumber, pageFact := range pageFacts {
		if pageNumber%deriveCtxCheckPages == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if pageFact.ResponseTimeMs > slowResponseTimeMillisecondsThreshold {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "server_responsiveness", "slow_response_time", "medium", "Page response time is slow", fmt.Sprintf("Page response time is %dms.", pageFact.ResponseTimeMs)))
		}
		if pageFact.SizeBytes > largePageSizeBytesThreshold {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "page_weight", "large_page_size", "high", "Page size is large", fmt.Sprintf("Page size is %.1fMB (recommended: under 3MB).", bytesToMegabytes(pageFact.SizeBytes))))
		} else if pageFact.SizeBytes > moderatePageSizeBytesThreshold {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "page_weight", "moderate_page_size", "medium", "Page size is moderately large", fmt.Sprintf("Page size is %.1fMB (recommended: under 1MB).", bytesToMegabytes(pageFact.SizeBytes))))
		}
	}
	return derivedIssues, nil
}

func newIssue(pageFact shared.PageFact, bucket string, issueType string, severity string, message string, details string) shared.DerivedIssue {
	return shared.DerivedIssue{
		CrawlPageID: pageFact.ID,
		URL:         pageFact.URL,
		Pillar:      PillarID,
		Bucket:      bucket,
		IssueType:   issueType,
		Severity:    severity,
		Message:     message,
		Details:     details,
	}
}

func bytesToMegabytes(sizeBytes int32) float64 {
	return float64(sizeBytes) / 1024 / 1024
}
