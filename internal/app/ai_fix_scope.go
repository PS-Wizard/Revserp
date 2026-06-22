package app

import (
	"fmt"

	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

// resolveAIFixScope validates the selected Miller-column scope against the persisted breakdown.
func resolveAIFixScope(snapshot issueshared.ScoreBreakdownSnapshot, requestBody aiFixRequest) (issueshared.PillarScoreBreakdown, []issueshared.BucketScoreBreakdown, []issueshared.IssueTypeScoreBreakdown, error) {
	for _, pillar := range snapshot.Pillars {
		if pillar.ID != requestBody.PillarID {
			continue
		}

		bucketByID := make(map[string]issueshared.BucketScoreBreakdown, len(pillar.Buckets))
		for _, bucket := range pillar.Buckets {
			bucketByID[bucket.ID] = bucket
		}

		buckets := make([]issueshared.BucketScoreBreakdown, 0, len(requestBody.BucketIDs))
		for _, bucketID := range requestBody.BucketIDs {
			bucket, ok := bucketByID[bucketID]
			if !ok {
				return issueshared.PillarScoreBreakdown{}, nil, nil, fmt.Errorf("invalid bucket_id: %s", bucketID)
			}
			buckets = append(buckets, bucket)
		}

		selectedIssues := make([]issueshared.IssueTypeScoreBreakdown, 0)
		if len(requestBody.IssueTypeIDs) == 0 {
			for _, bucket := range buckets {
				selectedIssues = append(selectedIssues, bucket.Issues...)
			}
			return pillar, buckets, selectedIssues, nil
		}

		issueTypeIDSet := make(map[string]struct{}, len(requestBody.IssueTypeIDs))
		for _, issueTypeID := range requestBody.IssueTypeIDs {
			issueTypeIDSet[issueTypeID] = struct{}{}
		}
		for _, bucket := range buckets {
			for _, issue := range bucket.Issues {
				if _, ok := issueTypeIDSet[issue.ID]; ok {
					selectedIssues = append(selectedIssues, issue)
				}
			}
		}
		if len(selectedIssues) == 0 {
			return issueshared.PillarScoreBreakdown{}, nil, nil, fmt.Errorf("invalid issue_type_ids for selected buckets")
		}

		return pillar, buckets, selectedIssues, nil
	}

	return issueshared.PillarScoreBreakdown{}, nil, nil, fmt.Errorf("invalid pillar_id")
}

// shouldRequestSpecificMetadataFixes returns true when exact copy suggestions are better than generic guidance.
func shouldRequestSpecificMetadataFixes(issues []issueshared.IssueTypeScoreBreakdown) bool {
	metadataIssueTypes := map[string]struct{}{
		"missing_title":              {},
		"title_too_long":             {},
		"title_too_short":            {},
		"duplicate_title":            {},
		"missing_meta_description":   {},
		"meta_description_too_long":  {},
		"meta_description_too_short": {},
		"duplicate_meta_description": {},
	}
	for _, issue := range issues {
		if _, ok := metadataIssueTypes[issue.ID]; !ok {
			return false
		}
	}
	return len(issues) > 0
}

func hasTitleMetadataIssue(issues []issueshared.IssueTypeScoreBreakdown) bool {
	titleIssueTypes := map[string]struct{}{
		"missing_title":   {},
		"title_too_long":  {},
		"title_too_short": {},
		"duplicate_title": {},
	}
	for _, issue := range issues {
		if _, ok := titleIssueTypes[issue.ID]; ok {
			return true
		}
	}
	return false
}

func hasMetaDescriptionMetadataIssue(issues []issueshared.IssueTypeScoreBreakdown) bool {
	metaDescriptionIssueTypes := map[string]struct{}{
		"missing_meta_description":   {},
		"meta_description_too_long":  {},
		"meta_description_too_short": {},
		"duplicate_meta_description": {},
	}
	for _, issue := range issues {
		if _, ok := metaDescriptionIssueTypes[issue.ID]; ok {
			return true
		}
	}
	return false
}
