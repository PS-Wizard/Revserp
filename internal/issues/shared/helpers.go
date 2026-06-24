package shared

import (
	"sort"
	"strings"
)

type IssueGroup struct {
	AffectedURLs map[string]struct{}
	Severity     string
	RowCount     int32
	Message      string
	Details      string
}

// BuildIssueGroupsByBucket groups unique affected URLs and max severity per issue type inside one pillar bucket.
func BuildIssueGroupsByBucket(pillarID string, crawlIssueSignals []CrawlIssueSignal) map[string]map[string]*IssueGroup {
	issueGroupsByBucket := make(map[string]map[string]*IssueGroup)
	for _, crawlIssueSignal := range crawlIssueSignals {
		if strings.TrimSpace(crawlIssueSignal.URL) == "" || crawlIssueSignal.Pillar != pillarID || strings.TrimSpace(crawlIssueSignal.Bucket) == "" {
			continue
		}
		if _, exists := issueGroupsByBucket[crawlIssueSignal.Bucket]; !exists {
			issueGroupsByBucket[crawlIssueSignal.Bucket] = make(map[string]*IssueGroup)
		}
		if _, exists := issueGroupsByBucket[crawlIssueSignal.Bucket][crawlIssueSignal.IssueType]; !exists {
			issueGroupsByBucket[crawlIssueSignal.Bucket][crawlIssueSignal.IssueType] = &IssueGroup{AffectedURLs: make(map[string]struct{})}
		}

		issueGroup := issueGroupsByBucket[crawlIssueSignal.Bucket][crawlIssueSignal.IssueType]
		previousSeverity := issueGroup.Severity
		issueGroup.AffectedURLs[crawlIssueSignal.URL] = struct{}{}
		issueGroup.RowCount++
		issueGroup.Severity = MaxSeverity(issueGroup.Severity, crawlIssueSignal.Severity)
		if SeverityRank(issueGroup.Severity) > SeverityRank(previousSeverity) {
			issueGroup.Message = crawlIssueSignal.Message
			issueGroup.Details = crawlIssueSignal.Details
		}
		if strings.TrimSpace(issueGroup.Message) == "" {
			issueGroup.Message = crawlIssueSignal.Message
		}
		if strings.TrimSpace(issueGroup.Details) == "" {
			issueGroup.Details = crawlIssueSignal.Details
		}
	}
	return issueGroupsByBucket
}

// CountScoreablePages returns the number of HTML-like pages eligible for crawl scoring.
func CountScoreablePages(crawlPageSignals []CrawlPageSignal) int {
	totalScoredPages := 0
	for _, crawlPageSignal := range crawlPageSignals {
		if IsScoreablePage(crawlPageSignal) {
			totalScoredPages++
		}
	}
	return totalScoredPages
}

// IsScoreablePage reports whether the page should count toward crawl-level scoring.
func IsScoreablePage(crawlPageSignal CrawlPageSignal) bool {
	return IsScoreableContentType(crawlPageSignal.ContentType)
}

// IsScoreableContentType reports whether one page content type should be scored or analyzed as HTML.
func IsScoreableContentType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if contentType == "" {
		return true
	}
	return strings.Contains(contentType, "text/html")
}

// SortedBucketIDs returns stable bucket ids sorted by descending weight then label.
func SortedBucketIDs(bucketWeights map[string]float64) []string {
	bucketIDs := make([]string, 0, len(bucketWeights))
	for bucketID := range bucketWeights {
		bucketIDs = append(bucketIDs, bucketID)
	}
	sort.Slice(bucketIDs, func(leftIndex int, rightIndex int) bool {
		leftBucketID := bucketIDs[leftIndex]
		rightBucketID := bucketIDs[rightIndex]
		if bucketWeights[leftBucketID] == bucketWeights[rightBucketID] {
			return HumanizeIdentifier(leftBucketID) < HumanizeIdentifier(rightBucketID)
		}
		return bucketWeights[leftBucketID] > bucketWeights[rightBucketID]
	})
	return bucketIDs
}

// MaxSeverity returns the stronger of two severities.
func MaxSeverity(currentSeverity string, candidateSeverity string) string {
	if SeverityRank(candidateSeverity) > SeverityRank(currentSeverity) {
		return candidateSeverity
	}
	return currentSeverity
}

// SeverityRank orders severities from weakest to strongest.
func SeverityRank(severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// NewIssue constructs one derived issue with all standard fields populated.
func NewIssue(pageFact PageFact, pillarID string, bucket string, issueType string, severity string, message string, details string) DerivedIssue {
	return DerivedIssue{
		CrawlPageID: pageFact.ID,
		URL:         pageFact.URL,
		Pillar:      pillarID,
		Bucket:      bucket,
		IssueType:   issueType,
		Severity:    severity,
		Message:     message,
		Details:     details,
	}
}

// HumanizeIdentifier formats one internal identifier for display.
func HumanizeIdentifier(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "_")
	for partIndex, part := range parts {
		switch strings.ToLower(part) {
		case "seo":
			parts[partIndex] = "SEO"
		case "aeo":
			parts[partIndex] = "AEO"
		case "og":
			parts[partIndex] = "OG"
		case "psi":
			parts[partIndex] = "PSI"
		case "h1", "h2", "h3":
			parts[partIndex] = strings.ToUpper(part)
		default:
			if part == "" {
				continue
			}
			parts[partIndex] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}
