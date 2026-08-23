package aichattools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	readIssueWorkDefaultLimit = 25
	readIssueWorkMaxLimit     = 50
)

var validReadIssueWorkPillars = []string{"seo", "aeo", "pagespeed"}
var validReadIssueWorkStatuses = []string{"open", "awaiting_verification", "not_verified", "still_open", "fixed", "no_longer_detected"}

const readIssueWorkSchema = `{
  "type": "object",
  "properties": {
    "status": {"type": "string", "enum": ["open","awaiting_verification","not_verified","still_open","fixed","no_longer_detected"]},
    "pillar": {"type": "string", "enum": ["seo","aeo","pagespeed"]},
    "bucket": {"type": "string"},
    "issue_type": {"type": "string"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "default": 25},
    "offset": {"type": "integer", "minimum": 0, "default": 0}
  },
  "additionalProperties": false
}`

type workItemsReader interface {
	ReadIssueWorkItems(ctx context.Context, arg sqlc.ReadIssueWorkItemsParams) ([]sqlc.ReadIssueWorkItemsRow, error)
}
type workDimensionsReader interface {
	ListDistinctCrawlIssueDimensions(ctx context.Context, arg sqlc.ListDistinctCrawlIssueDimensionsParams) ([]sqlc.ListDistinctCrawlIssueDimensionsRow, error)
}
type workLatestCrawlReader interface {
	GetLatestCompletedCrawlForProject(ctx context.Context, projectID pgtype.UUID) (pgtype.UUID, error)
}
type workPreviousCrawlReader interface {
	GetPreviousCompletedCrawlID(ctx context.Context, currentCrawlID pgtype.UUID) (pgtype.UUID, error)
}
type workDiffReader interface {
	ListIssueWorkspaceDiff(ctx context.Context, arg sqlc.ListIssueWorkspaceDiffParams) ([]sqlc.ListIssueWorkspaceDiffRow, error)
}
type workCrawlMetaReader interface {
	GetCrawlByIDForUser(ctx context.Context, arg sqlc.GetCrawlByIDForUserParams) (sqlc.GetCrawlByIDForUserRow, error)
}

type readIssueWorkExecutor struct {
	workItems     workItemsReader
	dimensions    workDimensionsReader
	latestCrawl   workLatestCrawlReader
	previousCrawl workPreviousCrawlReader
	diffReader    workDiffReader
	crawlMeta     workCrawlMetaReader
}

func readIssueWorkTool() Tool {
	return Tool{
		Def: Def{
			Name:        "read_issue_work",
			Label:       "Read fix work",
			Description: "Read the project's fix-work queue merged with no-longer-detected disappearances. Returns work items by collapsed status (open, awaiting_verification, not_verified, still_open, fixed) plus verified disappearances (no_longer_detected) derived from the last two completed crawls. Use status/pillar/bucket/issue_type filters and limit/offset paging.",
			Schema:      json.RawMessage(readIssueWorkSchema),
		},
		Execute: executeReadIssueWork,
	}
}

func executeReadIssueWork(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.Queries == nil {
		return Result{}, errors.New("read_issue_work: scope has no queries")
	}
	exec := readIssueWorkExecutor{
		workItems:     s.Queries,
		dimensions:    s.Queries,
		latestCrawl:   s.Queries,
		previousCrawl: s.Queries,
		diffReader:    s.Queries,
		crawlMeta:     s.Queries,
	}
	return exec.run(ctx, args, s.ProjectID, s.CrawlID, s.UserID, s.RowBudget)
}

type readIssueWorkArgs struct {
	Status    string
	Pillar    string
	Bucket    string
	IssueType string
	Limit     int
	Offset    int
}

type readIssueWorkResponse struct {
	TotalMatching int                    `json:"total_matching"`
	Breakdown     readIssueWorkBreakdown `json:"breakdown"`
	Items         []readIssueWorkItem    `json:"items"`
	NextOffset    *int                   `json:"next_offset"`
	HasMore       bool                   `json:"has_more"`
}

type readIssueWorkBreakdown struct {
	Open                 int `json:"open"`
	AwaitingVerification int `json:"awaiting_verification"`
	NotVerified          int `json:"not_verified"`
	StillOpen            int `json:"still_open"`
	Fixed                int `json:"fixed"`
	NoLongerDetected     int `json:"no_longer_detected"`
}

type readIssueWorkItem struct {
	SubjectKind    string   `json:"subject_kind"`
	URL            string   `json:"url"`
	Pillar         string   `json:"pillar"`
	Bucket         string   `json:"bucket"`
	IssueType      string   `json:"issue_type"`
	Status         string   `json:"status"`
	Severity       *string  `json:"severity,omitempty"`
	AttemptCount   int64    `json:"attempt_count"`
	OpenedAt       *string  `json:"opened_at,omitempty"`
	LastActivityAt *string  `json:"last_activity_at,omitempty"`
	VerifiedAt     *string  `json:"verified_at,omitempty"`
	Contributors   []string `json:"contributors"`
}

type mergedWorkItem struct {
	SubjectKind  string
	URL          string
	Pillar       string
	Bucket       string
	IssueType    string
	Status       string
	Severity     *string
	AttemptCount int64
	OpenedAt     *time.Time
	LastActivity time.Time
	VerifiedAt   *time.Time
	Contributors []string
}

func (e *readIssueWorkExecutor) run(ctx context.Context, raw json.RawMessage, projectID, crawlID, userID pgtype.UUID, budget *Budget) (Result, error) {
	if budget != nil && budget.Remaining() == 0 {
		return Result{
			Content: "The row budget for this turn is exhausted. Do not call read_issue_work again; synthesize your answer from the data you already have.",
			Summary: "row budget reached",
		}, nil
	}

	args, err := parseReadIssueWorkArgs(raw)
	if err != nil {
		return Result{Content: "read_issue_work error: " + err.Error()}, nil
	}

	// Resolve bucket/issue_type against crawl taxonomy.
	dimensions, err := e.dimensions.ListDistinctCrawlIssueDimensions(ctx, sqlc.ListDistinctCrawlIssueDimensionsParams{CrawlID: crawlID, UserID: userID})
	if err != nil {
		return Result{}, fmt.Errorf("read_issue_work: list dimensions: %w", err)
	}
	if args.Bucket != "" {
		// pillar scope for validation: single pillar if provided else all
		scopePillars := []string{}
		if args.Pillar != "" {
			scopePillars = []string{args.Pillar}
		}
		resolved, err := resolveReadIssuesBucket(args.Bucket, dimensions, scopePillars)
		if err != nil {
			return Result{Content: "read_issue_work error: " + err.Error()}, nil
		}
		args.Bucket = resolved
	}
	if args.IssueType != "" {
		scopePillars := []string{}
		if args.Pillar != "" {
			scopePillars = []string{args.Pillar}
		}
		resolved, err := resolveReadIssuesIssueType(args.IssueType, dimensions, scopePillars)
		if err != nil {
			return Result{Content: "read_issue_work error: " + err.Error()}, nil
		}
		args.IssueType = resolved
	}
	if args.Status != "" {
		// Normalize status lower? Already validated lowercased in parser.
		if !contains(validReadIssueWorkStatuses, args.Status) {
			return Result{Content: fmt.Sprintf("read_issue_work error: unknown status %q; valid statuses: %s", args.Status, strings.Join(validReadIssueWorkStatuses, ", "))}, nil
		}
	}
	if args.Pillar != "" && !contains(validReadIssueWorkPillars, args.Pillar) {
		return Result{Content: fmt.Sprintf("read_issue_work error: unknown pillar %q; valid pillars: %s", args.Pillar, strings.Join(validReadIssueWorkPillars, ", "))}, nil
	}

	// Fetch work rows and breakdown.
	workRows, err := e.workItems.ReadIssueWorkItems(ctx, sqlc.ReadIssueWorkItemsParams{
		ProjectID: projectID,
		Pillar:    args.Pillar,
		Bucket:    args.Bucket,
		IssueType: args.IssueType,
	})
	if err != nil {
		return Result{}, fmt.Errorf("read_issue_work: list work items: %w", err)
	}

	// Derive diff side only when previous crawl exists.
	var diffRows []sqlc.ListIssueWorkspaceDiffRow
	var currentCompletedAt time.Time
	var latestID pgtype.UUID
	if e.latestCrawl != nil && e.previousCrawl != nil && e.diffReader != nil {
		latestID, err = e.latestCrawl.GetLatestCompletedCrawlForProject(ctx, projectID)
		if err == nil {
			// fetch current completed_at for diff-only last_activity
			if e.crawlMeta != nil {
				if meta, err2 := e.crawlMeta.GetCrawlByIDForUser(ctx, sqlc.GetCrawlByIDForUserParams{ID: latestID, UserID: userID}); err2 == nil && meta.CompletedAt.Valid {
					currentCompletedAt = meta.CompletedAt.Time
				} else {
					// fallback to now if not found
					currentCompletedAt = time.Now().UTC()
				}
			}
			prevID, err2 := e.previousCrawl.GetPreviousCompletedCrawlID(ctx, latestID)
			if err2 == nil {
				diffRows, err = e.diffReader.ListIssueWorkspaceDiff(ctx, sqlc.ListIssueWorkspaceDiffParams{
					CurrentID:  latestID,
					BaselineID: prevID,
					UserID:     userID,
					UrlFilter:  "",
				})
				if err != nil {
					return Result{}, fmt.Errorf("read_issue_work: diff: %w", err)
				}
			} else if !errors.Is(err2, pgx.ErrNoRows) {
				return Result{}, fmt.Errorf("read_issue_work: previous crawl: %w", err2)
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return Result{}, fmt.Errorf("read_issue_work: latest crawl: %w", err)
		}
	}

	// Merge with precedence.
	merged := mergeWorkAndDiff(workRows, diffRows, currentCompletedAt)

	// Compute breakdown before status filtering.
	breakdown := breakdownFromMerged(merged)

	// Apply status filter post-merge.
	filtered := merged
	if args.Status != "" {
		tmp := make([]mergedWorkItem, 0, len(merged))
		for _, m := range merged {
			if m.Status == args.Status {
				tmp = append(tmp, m)
			}
		}
		filtered = tmp
	}

	// Sort: last_activity DESC, then url, pillar, bucket, issue_type ASC.
	sort.Slice(filtered, func(i, j int) bool {
		if !filtered[i].LastActivity.Equal(filtered[j].LastActivity) {
			return filtered[i].LastActivity.After(filtered[j].LastActivity)
		}
		if filtered[i].URL != filtered[j].URL {
			return filtered[i].URL < filtered[j].URL
		}
		if filtered[i].Pillar != filtered[j].Pillar {
			return filtered[i].Pillar < filtered[j].Pillar
		}
		if filtered[i].Bucket != filtered[j].Bucket {
			return filtered[i].Bucket < filtered[j].Bucket
		}
		return filtered[i].IssueType < filtered[j].IssueType
	})

	// ponytail: merged ceiling 500
	if len(filtered) > 500 {
		filtered = filtered[:500]
	}

	totalMatching := len(filtered)
	// Slice by offset/limit
	start := args.Offset
	if start > totalMatching {
		start = totalMatching
	}
	end := start + args.Limit
	if end > totalMatching {
		end = totalMatching
	}
	page := filtered[start:end]
	if budget != nil {
		budget.Spend(len(page))
	}

	hasMore := end < totalMatching
	var nextOffset *int
	if hasMore {
		v := end
		nextOffset = &v
	} else {
		// When not has_more, next_offset is null but we still set to total? spec says int|null; use null when no more.
		// To keep deterministic, leave nil.
	}

	items := make([]readIssueWorkItem, 0, len(page))
	for _, m := range page {
		it := readIssueWorkItem{
			SubjectKind:  m.SubjectKind,
			URL:          m.URL,
			Pillar:       m.Pillar,
			Bucket:       m.Bucket,
			IssueType:    m.IssueType,
			Status:       m.Status,
			Severity:     m.Severity,
			AttemptCount: m.AttemptCount,
			Contributors: truncateContributors(m.Contributors),
		}
		if m.OpenedAt != nil {
			s := m.OpenedAt.UTC().Format(time.RFC3339)
			it.OpenedAt = &s
		}
		if !m.LastActivity.IsZero() {
			s := m.LastActivity.UTC().Format(time.RFC3339)
			it.LastActivityAt = &s
		}
		if m.VerifiedAt != nil {
			s := m.VerifiedAt.UTC().Format(time.RFC3339)
			it.VerifiedAt = &s
		}
		if it.Contributors == nil {
			it.Contributors = []string{}
		}
		items = append(items, it)
	}

	resp := readIssueWorkResponse{
		TotalMatching: totalMatching,
		Breakdown:     breakdown,
		Items:         items,
		NextOffset:    nextOffset,
		HasMore:       hasMore,
	}
	// Ensure items not nil for JSON
	if resp.Items == nil {
		resp.Items = []readIssueWorkItem{}
	}
	content, err := json.Marshal(resp)
	if err != nil {
		return Result{}, fmt.Errorf("read_issue_work: marshal response: %w", err)
	}
	// Enforce 4KB cap sanity: if over, truncate items? For now just ensure.
	if len(content) > 4096 {
		// If over cap, reduce items iteratively? Simplified: trim to limit.
		// Keep first items until under cap.
		for len(content) > 4096 && len(resp.Items) > 0 {
			resp.Items = resp.Items[:len(resp.Items)-1]
			resp.HasMore = true
			if resp.NextOffset == nil {
				v := start + len(resp.Items)
				resp.NextOffset = &v
			} else {
				*resp.NextOffset = start + len(resp.Items)
			}
			resp.TotalMatching = totalMatching // keep original?
			content, _ = json.Marshal(resp)
		}
	}
	return Result{
		Content: string(content),
		Summary: readIssueWorkSummary(totalMatching, breakdown),
	}, nil
}

func mergeWorkAndDiff(workRows []sqlc.ReadIssueWorkItemsRow, diffRows []sqlc.ListIssueWorkspaceDiffRow, currentCompletedAt time.Time) []mergedWorkItem {
	// Build identity map for page-subject work rows.
	byIdentity := make(map[string]struct{})
	for _, w := range workRows {
		if w.SubjectKind == "page" {
			key := w.SubjectKey + "\n" + w.Pillar + "\n" + w.Bucket + "\n" + w.IssueType
			byIdentity[key] = struct{}{}
		}
	}

	merged := make([]mergedWorkItem, 0, len(workRows)+len(diffRows))

	// Add work rows with collapsed status.
	for _, w := range workRows {
		status := w.AttemptStatus
		if status == "" {
			status = "open"
		}
		var lastActivity time.Time
		if w.VerifiedAt.Valid {
			lastActivity = w.VerifiedAt.Time
		} else if w.AttemptCreatedAt.Valid {
			lastActivity = w.AttemptCreatedAt.Time
		} else {
			lastActivity = w.ItemUpdatedAt.Time
		}
		var openedAt *time.Time
		if w.ItemCreatedAt.Valid {
			t := w.ItemCreatedAt.Time
			openedAt = &t
		}
		var verifiedAt *time.Time
		if w.VerifiedAt.Valid {
			t := w.VerifiedAt.Time
			verifiedAt = &t
		}
		// Dedup contributors: sorted and truncated later.
		emails := make([]string, len(w.ContributorEmails))
		copy(emails, w.ContributorEmails)
		sort.Strings(emails)
		// Filter empty? already filtered non-null.
		sev := (*string)(nil)
		merged = append(merged, mergedWorkItem{
			SubjectKind:  w.SubjectKind,
			URL:          w.SubjectKey,
			Pillar:       w.Pillar,
			Bucket:       w.Bucket,
			IssueType:    w.IssueType,
			Status:       status,
			Severity:     sev,
			AttemptCount: w.AttemptCount,
			OpenedAt:     openedAt,
			LastActivity: lastActivity,
			VerifiedAt:   verifiedAt,
			Contributors: emails,
		})
	}

	// Add unclaimed diff rows where change_type == no_longer_detected
	for _, d := range diffRows {
		if d.ChangeType != "no_longer_detected" {
			continue
		}
		key := d.Url + "\n" + d.Pillar + "\n" + d.Bucket + "\n" + d.IssueType
		if _, claimed := byIdentity[key]; claimed {
			continue
		}
		// For group collisions, we already avoid because byIdentity only contains page keys, so group diff never claimed.
		sev := d.Severity
		// Copy to avoid alias
		s := sev
		var openedAt *time.Time // nil for diff-only
		var verifiedAt *time.Time
		lastActivity := currentCompletedAt
		if lastActivity.IsZero() {
			lastActivity = time.Now().UTC()
		}
		merged = append(merged, mergedWorkItem{
			SubjectKind:  "page",
			URL:          d.Url,
			Pillar:       d.Pillar,
			Bucket:       d.Bucket,
			IssueType:    d.IssueType,
			Status:       "no_longer_detected",
			Severity:     &s,
			AttemptCount: 0,
			OpenedAt:     openedAt,
			LastActivity: lastActivity,
			VerifiedAt:   verifiedAt,
			Contributors: []string{},
		})
	}
	return merged
}

func breakdownFromMerged(merged []mergedWorkItem) readIssueWorkBreakdown {
	var b readIssueWorkBreakdown
	for _, m := range merged {
		switch m.Status {
		case "open":
			b.Open++
		case "awaiting_verification":
			b.AwaitingVerification++
		case "not_verified":
			b.NotVerified++
		case "still_open":
			b.StillOpen++
		case "fixed":
			b.Fixed++
		case "no_longer_detected":
			b.NoLongerDetected++
		}
	}
	return b
}

func truncateContributors(emails []string) []string {
	if len(emails) == 0 {
		return []string{}
	}
	// Already sorted caller side.
	if len(emails) <= 5 {
		out := make([]string, len(emails))
		copy(out, emails)
		return out
	}
	// Max 5 entries including the "+N more" literal.
	remaining := len(emails) - 4
	out := make([]string, 0, 5)
	out = append(out, emails[:4]...)
	out = append(out, fmt.Sprintf("+%d more", remaining))
	return out
}

func readIssueWorkSummary(total int, b readIssueWorkBreakdown) string {
	// Order: fixed → no_longer_detected → still_open → awaiting_verification → not_verified → open
	type entry struct {
		key   string
		label string
		count int
	}
	entries := []entry{
		{"fixed", "fixed", b.Fixed},
		{"no_longer_detected", "no longer detected", b.NoLongerDetected},
		{"still_open", "still open", b.StillOpen},
		{"awaiting_verification", "awaiting verification", b.AwaitingVerification},
		{"not_verified", "not verified", b.NotVerified},
		{"open", "open", b.Open},
	}
	parts := []string{}
	for _, e := range entries {
		if e.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", e.count, e.label))
		}
	}
	noun := "fix items"
	if total == 1 {
		noun = "fix item"
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d %s", total, noun)
	}
	return fmt.Sprintf("%d %s: %s", total, noun, strings.Join(parts, ", "))
}

func parseReadIssueWorkArgs(raw json.RawMessage) (readIssueWorkArgs, error) {
	args := readIssueWorkArgs{Limit: readIssueWorkDefaultLimit, Offset: 0}
	if len(bytes.TrimSpace(raw)) == 0 {
		return args, nil
	}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	if fields == nil {
		return args, nil
	}
	for key, value := range fields {
		switch key {
		case "status":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			v = strings.ToLower(strings.TrimSpace(v))
			if !contains(validReadIssueWorkStatuses, v) {
				return args, fmt.Errorf("invalid status %q; valid statuses: %s", v, strings.Join(validReadIssueWorkStatuses, ", "))
			}
			args.Status = v
		case "pillar":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			v = strings.ToLower(strings.TrimSpace(v))
			if !contains(validReadIssueWorkPillars, v) {
				return args, fmt.Errorf("invalid pillar %q; valid pillars: %s", v, strings.Join(validReadIssueWorkPillars, ", "))
			}
			args.Pillar = v
		case "bucket":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.Bucket = strings.TrimSpace(v)
		case "issue_type":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.IssueType = strings.TrimSpace(v)
		case "limit":
			var v int
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be an integer", key)
			}
			if v < 1 {
				return args, fmt.Errorf("argument %q must be at least 1", key)
			}
			if v > readIssueWorkMaxLimit {
				return args, fmt.Errorf("argument %q must be at most %d", key, readIssueWorkMaxLimit)
			}
			args.Limit = v
		case "offset":
			var v int
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be an integer", key)
			}
			if v < 0 {
				return args, errors.New("offset must be >= 0")
			}
			args.Offset = v
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	return args, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
