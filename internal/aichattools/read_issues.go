package aichattools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

const (
	readIssuesDefaultLimit = 25
	readIssuesMaxLimit     = 50
	readIssuesMaxURLs      = 25
	readIssuesMaxTextLen   = 250
	readIssuesTopN         = 20
)

var (
	validReadIssuesPillars    = []string{"seo", "aeo", "pagespeed"}
	validReadIssuesSeverities = []string{"high", "medium", "low"}
)

const readIssuesSchema = `{
  "type": "object",
  "properties": {
    "pillar": {"type": "string", "enum": ["seo", "aeo", "pagespeed"]},
    "bucket": {"type": "string", "description": "Bucket id or human label, e.g. \"meta_tags\" or \"Meta Tags\""},
    "issue_type": {"type": "string", "description": "Issue type id, e.g. \"missing_title\""},
    "severity": {"type": "string", "enum": ["high", "medium", "low"]},
    "urls": {"type": "array", "items": {"type": "string"}, "maxItems": 25},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "default": 25},
    "offset": {"type": "integer", "minimum": 0, "default": 0}
  },
  "additionalProperties": false
}`

// issueLister pages one crawl's issues through the user-membership join.
type issueLister interface {
	ListCrawlIssuesFilteredForUser(ctx context.Context, arg sqlc.ListCrawlIssuesFilteredForUserParams) ([]sqlc.ListCrawlIssuesFilteredForUserRow, error)
}

// issueCounter counts one crawl's issues through the user-membership join.
type issueCounter interface {
	CountCrawlIssuesFilteredForUser(ctx context.Context, arg sqlc.CountCrawlIssuesFilteredForUserParams) (int64, error)
}

// issueBreakdownReader reads per-group issue counts through the user-membership join.
type issueBreakdownReader interface {
	BreakdownCrawlIssuesFilteredForUser(ctx context.Context, arg sqlc.BreakdownCrawlIssuesFilteredForUserParams) ([]sqlc.BreakdownCrawlIssuesFilteredForUserRow, error)
}

// issueDimensionReader reads the distinct pillar/bucket/issue_type values one crawl has.
type issueDimensionReader interface {
	ListDistinctCrawlIssueDimensions(ctx context.Context, arg sqlc.ListDistinctCrawlIssueDimensionsParams) ([]sqlc.ListDistinctCrawlIssueDimensionsRow, error)
}

// readIssuesExecutor runs one read_issues call against narrow reader interfaces
// so tests can substitute fakes without a database.
type readIssuesExecutor struct {
	lister     issueLister
	counter    issueCounter
	breakdown  issueBreakdownReader
	dimensions issueDimensionReader
}

func readIssuesTool() Tool {
	return Tool{
		Def: Def{
			Name:        "read_issues",
			Label:       "Read issues",
			Description: "Read crawl issues of the current crawl with optional filters and paging. Returns matching totals, a breakdown of top buckets and issue types, and issue rows with deterministic recommended fixes.",
			Schema:      json.RawMessage(readIssuesSchema),
		},
		Execute: executeReadIssues,
	}
}

// executeReadIssues adapts the tool contract to the narrow executors.
func executeReadIssues(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.Queries == nil {
		return Result{}, errors.New("read_issues: scope has no queries")
	}
	exec := readIssuesExecutor{
		lister:     s.Queries,
		counter:    s.Queries,
		breakdown:  s.Queries,
		dimensions: s.Queries,
	}
	return exec.run(ctx, args, s.CrawlID, s.UserID, s.RowBudget)
}

// readIssuesArgs is the raw, unvalidated argument set.
type readIssuesArgs struct {
	Pillar    string
	Bucket    string
	IssueType string
	Severity  string
	URLs      []string
	Limit     int
	Offset    int
}

// readIssuesResponse is the JSON the model sees.
type readIssuesResponse struct {
	TotalMatching int64               `json:"total_matching"`
	Breakdown     readIssuesBreakdown `json:"breakdown"`
	Issues        []readIssuesIssue   `json:"issues"`
	NextOffset    int                 `json:"next_offset"`
	HasMore       bool                `json:"has_more"`
}

type readIssuesBreakdown struct {
	ByBucket    []readIssuesBucketGroup    `json:"by_bucket"`
	ByIssueType []readIssuesIssueTypeGroup `json:"by_issue_type"`
}

type readIssuesBucketGroup struct {
	Bucket     string                   `json:"bucket"`
	Label      string                   `json:"label"`
	Pillar     string                   `json:"pillar"`
	Count      int64                    `json:"count"`
	Severities readIssuesSeverityCounts `json:"severities"`
}

type readIssuesSeverityCounts struct {
	High   int64 `json:"high"`
	Medium int64 `json:"medium"`
	Low    int64 `json:"low"`
}

type readIssuesIssueTypeGroup struct {
	IssueType string `json:"issue_type"`
	Label     string `json:"label"`
	Count     int64  `json:"count"`
}

type readIssuesIssue struct {
	URL            string `json:"url"`
	Pillar         string `json:"pillar"`
	Bucket         string `json:"bucket"`
	IssueType      string `json:"issue_type"`
	Severity       string `json:"severity"`
	Message        string `json:"message"`
	Details        string `json:"details"`
	RecommendedFix string `json:"recommended_fix"`
}

func (e *readIssuesExecutor) run(ctx context.Context, raw json.RawMessage, crawlID, userID pgtype.UUID, budget *Budget) (Result, error) {
	if budget != nil && budget.Remaining() == 0 {
		return Result{
			Content: "The row budget for this turn is exhausted. Do not call read_issues again; synthesize your answer from the data you already have.",
			Summary: "row budget reached",
		}, nil
	}

	args, err := parseReadIssuesArgs(raw)
	if err != nil {
		return Result{Content: "read_issues error: " + err.Error()}, nil
	}
	if args.Pillar != "" && !slices.Contains(validReadIssuesPillars, args.Pillar) {
		return Result{Content: fmt.Sprintf("read_issues error: unknown pillar %q; valid pillars: %s", args.Pillar, strings.Join(validReadIssuesPillars, ", "))}, nil
	}
	if args.Severity != "" && !slices.Contains(validReadIssuesSeverities, args.Severity) {
		return Result{Content: fmt.Sprintf("read_issues error: unknown severity %q; valid severities: %s", args.Severity, strings.Join(validReadIssuesSeverities, ", "))}, nil
	}
	if args.Offset < 0 {
		return Result{Content: "read_issues error: offset must be >= 0"}, nil
	}
	if len(args.URLs) > readIssuesMaxURLs {
		args.URLs = args.URLs[:readIssuesMaxURLs]
	}

	dimensions, err := e.dimensions.ListDistinctCrawlIssueDimensions(ctx, sqlc.ListDistinctCrawlIssueDimensionsParams{CrawlID: crawlID, UserID: userID})
	if err != nil {
		return Result{}, fmt.Errorf("read_issues: list dimensions: %w", err)
	}
	if args.Bucket != "" {
		resolved, err := resolveReadIssuesBucket(args.Bucket, dimensions, args.Pillar)
		if err != nil {
			return Result{Content: "read_issues error: " + err.Error()}, nil
		}
		args.Bucket = resolved
	}
	if args.IssueType != "" {
		if err := validateReadIssuesIssueType(args.IssueType, dimensions, args.Pillar); err != nil {
			return Result{Content: "read_issues error: " + err.Error()}, nil
		}
	}

	total, err := e.counter.CountCrawlIssuesFilteredForUser(ctx, sqlc.CountCrawlIssuesFilteredForUserParams{
		CrawlID: crawlID,
		UserID:  userID,
		Column3: args.Pillar,
		Column4: args.Bucket,
		Column5: args.IssueType,
		Column6: args.Severity,
		Column7: args.URLs,
	})
	if err != nil {
		return Result{}, fmt.Errorf("read_issues: count issues: %w", err)
	}

	breakdownRows, err := e.breakdown.BreakdownCrawlIssuesFilteredForUser(ctx, sqlc.BreakdownCrawlIssuesFilteredForUserParams{
		CrawlID: crawlID,
		UserID:  userID,
		Column3: args.Pillar,
		Column4: args.Bucket,
		Column5: args.IssueType,
		Column6: args.Severity,
		Column7: "",
		Column8: args.URLs,
	})
	if err != nil {
		return Result{}, fmt.Errorf("read_issues: breakdown: %w", err)
	}

	rows, err := e.lister.ListCrawlIssuesFilteredForUser(ctx, sqlc.ListCrawlIssuesFilteredForUserParams{
		CrawlID: crawlID,
		UserID:  userID,
		Column3: args.Pillar,
		Column4: args.Bucket,
		Column5: args.IssueType,
		Column6: args.Severity,
		Column7: "",
		Column8: args.URLs,
		Limit:   int32(args.Limit),
		Offset:  int32(args.Offset),
	})
	if err != nil {
		return Result{}, fmt.Errorf("read_issues: list issues: %w", err)
	}

	if budget != nil {
		budget.Spend(len(rows))
	}

	nextOffset := args.Offset + len(rows)
	response := readIssuesResponse{
		TotalMatching: total,
		Breakdown:     shapeReadIssuesBreakdown(breakdownRows),
		Issues:        shapeReadIssuesIssues(rows),
		NextOffset:    nextOffset,
		HasMore:       int64(nextOffset) < total,
	}
	content, err := json.Marshal(response)
	if err != nil {
		return Result{}, fmt.Errorf("read_issues: marshal response: %w", err)
	}
	return Result{
		Content: string(content),
		Summary: readIssuesSummary(len(rows), total),
	}, nil
}

func readIssuesSummary(shown int, total int64) string {
	noun := "issues"
	if shown == 1 {
		noun = "issue"
	}
	return fmt.Sprintf("%d %s shown (%d matching total)", shown, noun, total)
}

// parseReadIssuesArgs parses the tool arguments strictly: unknown keys,
// duplicate keys, and trailing data are rejected. Empty input yields defaults.
func parseReadIssuesArgs(raw json.RawMessage) (readIssuesArgs, error) {
	args := readIssuesArgs{Limit: readIssuesDefaultLimit}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for key, value := range fields {
		switch key {
		case "pillar":
			if err := json.Unmarshal(value, &args.Pillar); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.Pillar = strings.ToLower(strings.TrimSpace(args.Pillar))
		case "bucket":
			if err := json.Unmarshal(value, &args.Bucket); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.Bucket = strings.TrimSpace(args.Bucket)
		case "issue_type":
			if err := json.Unmarshal(value, &args.IssueType); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.IssueType = strings.TrimSpace(args.IssueType)
		case "severity":
			if err := json.Unmarshal(value, &args.Severity); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.Severity = strings.ToLower(strings.TrimSpace(args.Severity))
		case "urls":
			if err := json.Unmarshal(value, &args.URLs); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
		case "limit":
			if err := json.Unmarshal(value, &args.Limit); err != nil {
				return args, fmt.Errorf("argument %q must be an integer", key)
			}
			if args.Limit < 1 {
				return args, fmt.Errorf("argument %q must be at least 1", key)
			}
			if args.Limit > readIssuesMaxLimit {
				args.Limit = readIssuesMaxLimit
			}
		case "offset":
			if err := json.Unmarshal(value, &args.Offset); err != nil {
				return args, fmt.Errorf("argument %q must be an integer", key)
			}
			if args.Offset < 0 {
				return args, errors.New("offset must be >= 0")
			}
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	return args, nil
}

// strictJSONFields decodes one JSON object into raw field values while
// rejecting non-objects, duplicate keys, and trailing data.
func strictJSONFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("invalid arguments JSON: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("arguments must be a JSON object")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid arguments JSON: %w", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("argument keys must be strings")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("duplicate argument %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("invalid value for argument %q: %w", key, err)
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("invalid arguments JSON: %w", err)
	}
	if extra, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("invalid arguments JSON: %w", err)
		}
		return nil, fmt.Errorf("trailing data after arguments object: %v", extra)
	}
	return fields, nil
}

// resolveReadIssuesBucket resolves a bucket id or human label (case-insensitively)
// to the canonical id, or returns an error listing the valid buckets.
func resolveReadIssuesBucket(raw string, dimensions []sqlc.ListDistinctCrawlIssueDimensionsRow, pillar string) (string, error) {
	target := strings.ToLower(strings.TrimSpace(raw))
	valid := validReadIssuesDimensions(dimensions, pillar)
	for _, bucket := range valid {
		if strings.ToLower(bucket) == target || strings.ToLower(shared.HumanizeIdentifier(bucket)) == target {
			return bucket, nil
		}
	}
	return "", fmt.Errorf("unknown bucket %q; valid buckets: %s", raw, strings.Join(valid, ", "))
}

// validateReadIssuesIssueType rejects an issue type id that the crawl does not have,
// listing the valid ones.
func validateReadIssuesIssueType(raw string, dimensions []sqlc.ListDistinctCrawlIssueDimensionsRow, pillar string) error {
	if slices.Contains(validReadIssuesDimensions(dimensions, pillar), raw) {
		return nil
	}
	validIssueTypes := make([]string, 0)
	for _, dimension := range dimensions {
		if pillar != "" && dimension.Pillar != pillar {
			continue
		}
		validIssueTypes = append(validIssueTypes, dimension.IssueType)
	}
	validIssueTypes = uniqueSorted(validIssueTypes)
	return fmt.Errorf("unknown issue_type %q; valid issue types: %s", raw, strings.Join(validIssueTypes, ", "))
}

// validReadIssuesDimensions lists the bucket ids for the crawl, optionally scoped
// to one pillar, sorted and deduplicated.
func validReadIssuesDimensions(dimensions []sqlc.ListDistinctCrawlIssueDimensionsRow, pillar string) []string {
	buckets := make([]string, 0)
	for _, dimension := range dimensions {
		if pillar != "" && dimension.Pillar != pillar {
			continue
		}
		buckets = append(buckets, dimension.Bucket)
	}
	return uniqueSorted(buckets)
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	return slices.Compact(values)
}

// shapeReadIssuesBreakdown folds the group rows into top-N bucket and issue type
// lists with severity counts.
func shapeReadIssuesBreakdown(rows []sqlc.BreakdownCrawlIssuesFilteredForUserRow) readIssuesBreakdown {
	type bucketAgg struct {
		bucket     string
		pillar     string
		count      int64
		severities readIssuesSeverityCounts
	}
	bucketAggs := map[string]*bucketAgg{}
	issueTypeCounts := map[string]int64{}
	for _, row := range rows {
		agg, ok := bucketAggs[row.Bucket]
		if !ok {
			agg = &bucketAgg{bucket: row.Bucket, pillar: row.Pillar}
			bucketAggs[row.Bucket] = agg
		}
		agg.count += row.IssueCount
		switch row.Severity {
		case "high":
			agg.severities.High += row.IssueCount
		case "medium":
			agg.severities.Medium += row.IssueCount
		case "low":
			agg.severities.Low += row.IssueCount
		}
		issueTypeCounts[row.IssueType] += row.IssueCount
	}

	bucketGroups := make([]readIssuesBucketGroup, 0, len(bucketAggs))
	for _, agg := range bucketAggs {
		bucketGroups = append(bucketGroups, readIssuesBucketGroup{
			Bucket:     agg.bucket,
			Label:      shared.HumanizeIdentifier(agg.bucket),
			Pillar:     agg.pillar,
			Count:      agg.count,
			Severities: agg.severities,
		})
	}
	sort.Slice(bucketGroups, func(i, j int) bool {
		if bucketGroups[i].Count != bucketGroups[j].Count {
			return bucketGroups[i].Count > bucketGroups[j].Count
		}
		if bucketGroups[i].Label != bucketGroups[j].Label {
			return bucketGroups[i].Label < bucketGroups[j].Label
		}
		return bucketGroups[i].Bucket < bucketGroups[j].Bucket
	})
	bucketGroups = bucketGroups[:min(len(bucketGroups), readIssuesTopN)]

	issueTypeGroups := make([]readIssuesIssueTypeGroup, 0, len(issueTypeCounts))
	for issueType, count := range issueTypeCounts {
		issueTypeGroups = append(issueTypeGroups, readIssuesIssueTypeGroup{
			IssueType: issueType,
			Label:     shared.HumanizeIdentifier(issueType),
			Count:     count,
		})
	}
	sort.Slice(issueTypeGroups, func(i, j int) bool {
		if issueTypeGroups[i].Count != issueTypeGroups[j].Count {
			return issueTypeGroups[i].Count > issueTypeGroups[j].Count
		}
		if issueTypeGroups[i].Label != issueTypeGroups[j].Label {
			return issueTypeGroups[i].Label < issueTypeGroups[j].Label
		}
		return issueTypeGroups[i].IssueType < issueTypeGroups[j].IssueType
	})
	issueTypeGroups = issueTypeGroups[:min(len(issueTypeGroups), readIssuesTopN)]

	return readIssuesBreakdown{ByBucket: bucketGroups, ByIssueType: issueTypeGroups}
}

// shapeReadIssuesIssues folds the recommended fix into each row before capping
// message and details.
func shapeReadIssuesIssues(rows []sqlc.ListCrawlIssuesFilteredForUserRow) []readIssuesIssue {
	rowsOut := make([]readIssuesIssue, 0, len(rows))
	for _, row := range rows {
		fix := issues.RecommendedFix(row.Pillar, row.Bucket, row.IssueType, row.Message, row.Details)
		rowsOut = append(rowsOut, readIssuesIssue{
			URL:            row.Url,
			Pillar:         row.Pillar,
			Bucket:         row.Bucket,
			IssueType:      row.IssueType,
			Severity:       row.Severity,
			Message:        capReadIssuesText(row.Message),
			Details:        capReadIssuesText(row.Details),
			RecommendedFix: fix,
		})
	}
	return rowsOut
}

// capReadIssuesText caps text at readIssuesMaxTextLen runes, appending a
// truncation marker when anything was cut.
func capReadIssuesText(text string) string {
	if utf8.RuneCountInString(text) <= readIssuesMaxTextLen {
		return text
	}
	return string([]rune(text)[:readIssuesMaxTextLen]) + "\u2026"
}
