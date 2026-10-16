package app

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type comparisonExcludedIssueType struct {
	Pillar           string `json:"pillar"`
	Bucket           string `json:"bucket"`
	IssueType        string `json:"issue_type"`
	ExcludedURLCount int    `json:"excluded_url_count"`
}

func parseComparisonExcludedURLPatterns(rawConfigSnapshot []byte) ([]string, error) {
	if len(strings.TrimSpace(string(rawConfigSnapshot))) == 0 {
		return nil, nil
	}
	var snapshot struct {
		ExcludedURLPatterns []string `json:"excluded_url_patterns"`
	}
	if err := json.Unmarshal(rawConfigSnapshot, &snapshot); err != nil {
		return nil, err
	}
	return snapshot.ExcludedURLPatterns, nil
}

func countComparisonExcludedIssueTypes(candidates []sqlc.ListComparisonExcludedCandidateURLsRow, isExcluded func(string) bool) []comparisonExcludedIssueType {
	seen := make(map[sqlc.ListComparisonExcludedCandidateURLsRow]struct{}, len(candidates))
	counts := make(map[comparisonExcludedIssueType]int)
	for _, candidate := range candidates {
		if _, dup := seen[candidate]; dup {
			continue
		}
		seen[candidate] = struct{}{}
		if !isExcluded(candidate.Url) {
			continue
		}
		key := comparisonExcludedIssueType{Pillar: candidate.Pillar, Bucket: candidate.Bucket, IssueType: candidate.IssueType}
		counts[key]++
	}
	excluded := make([]comparisonExcludedIssueType, 0, len(counts))
	for key, count := range counts {
		key.ExcludedURLCount = count
		excluded = append(excluded, key)
	}
	sort.Slice(excluded, func(i, j int) bool {
		if excluded[i].Pillar != excluded[j].Pillar {
			return excluded[i].Pillar < excluded[j].Pillar
		}
		if excluded[i].Bucket != excluded[j].Bucket {
			return excluded[i].Bucket < excluded[j].Bucket
		}
		return excluded[i].IssueType < excluded[j].IssueType
	})
	return excluded
}

func loadComparisonExcludedIssueTypes(ctx context.Context, queries *sqlc.Queries, userID, baselineCrawlID, currentCrawlID pgtype.UUID, currentConfigSnapshot []byte) ([]comparisonExcludedIssueType, error) {
	patterns, err := parseComparisonExcludedURLPatterns(currentConfigSnapshot)
	if err != nil {
		return nil, err
	}
	matcher, err := crawler.CompileExcludedURLPatterns(patterns)
	if err != nil {
		return nil, err
	}
	if matcher == nil {
		return nil, nil
	}
	candidates, err := queries.ListComparisonExcludedCandidateURLs(ctx, sqlc.ListComparisonExcludedCandidateURLsParams{
		BaselineCrawlID: baselineCrawlID,
		UserID:          userID,
		CurrentCrawlID:  currentCrawlID,
	})
	if err != nil {
		return nil, err
	}
	return countComparisonExcludedIssueTypes(candidates, matcher.Excludes), nil
}

// loadComparisonExcludedURLs returns the sorted baseline URLs the current crawl
// skipped via its exclusion patterns: missing-page, absent-current-issue
// candidates only, so current issues never mislabel as excluded.
func loadComparisonExcludedURLs(ctx context.Context, queries *sqlc.Queries, userID, baselineCrawlID, currentCrawlID pgtype.UUID, pillar, bucket, issueType string, currentConfigSnapshot []byte) ([]string, error) {
	patterns, err := parseComparisonExcludedURLPatterns(currentConfigSnapshot)
	if err != nil {
		return nil, err
	}
	matcher, err := crawler.CompileExcludedURLPatterns(patterns)
	if err != nil {
		return nil, err
	}
	if matcher == nil {
		return nil, nil
	}
	candidates, err := queries.ListComparisonExcludedCandidateURLs(ctx, sqlc.ListComparisonExcludedCandidateURLsParams{
		BaselineCrawlID: baselineCrawlID,
		UserID:          userID,
		Pillar:          pillar,
		Bucket:          bucket,
		IssueType:       issueType,
		CurrentCrawlID:  currentCrawlID,
	})
	if err != nil {
		return nil, err
	}
	return filterComparisonExcludedURLs(candidates, matcher.Excludes), nil
}

func filterComparisonExcludedURLs(candidates []sqlc.ListComparisonExcludedCandidateURLsRow, isExcluded func(string) bool) []string {
	seen := make(map[string]struct{}, len(candidates))
	var excluded []string
	for _, candidate := range candidates {
		if _, dup := seen[candidate.Url]; dup {
			continue
		}
		seen[candidate.Url] = struct{}{}
		if !isExcluded(candidate.Url) {
			continue
		}
		excluded = append(excluded, candidate.Url)
	}
	sort.Strings(excluded)
	return excluded
}
