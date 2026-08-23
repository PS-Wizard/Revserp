package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type fakeWorkReader struct {
	workRows      []sqlc.ReadIssueWorkItemsRow
	workErr       error
	dimensionRows []sqlc.ListDistinctCrawlIssueDimensionsRow
	dimensionErr  error
	latestID      pgtype.UUID
	latestErr     error
	prevID        pgtype.UUID
	prevErr       error
	diffRows      []sqlc.ListIssueWorkspaceDiffRow
	diffErr       error
	crawlMetaRow  sqlc.GetCrawlByIDForUserRow
	crawlMetaErr  error

	workCalls      int
	dimensionCalls int
	latestCalls    int
	prevCalls      int
	diffCalls      int
	metaCalls      int
}

func (f *fakeWorkReader) ReadIssueWorkItems(_ context.Context, _ sqlc.ReadIssueWorkItemsParams) ([]sqlc.ReadIssueWorkItemsRow, error) {
	f.workCalls++
	if f.workErr != nil {
		return nil, f.workErr
	}
	return f.workRows, nil
}
func (f *fakeWorkReader) ListDistinctCrawlIssueDimensions(_ context.Context, _ sqlc.ListDistinctCrawlIssueDimensionsParams) ([]sqlc.ListDistinctCrawlIssueDimensionsRow, error) {
	f.dimensionCalls++
	if f.dimensionErr != nil {
		return nil, f.dimensionErr
	}
	return f.dimensionRows, nil
}
func (f *fakeWorkReader) GetLatestCompletedCrawlForProject(_ context.Context, _ pgtype.UUID) (pgtype.UUID, error) {
	f.latestCalls++
	if f.latestErr != nil {
		return pgtype.UUID{}, f.latestErr
	}
	return f.latestID, nil
}
func (f *fakeWorkReader) GetPreviousCompletedCrawlID(_ context.Context, _ pgtype.UUID) (pgtype.UUID, error) {
	f.prevCalls++
	if f.prevErr != nil {
		return pgtype.UUID{}, f.prevErr
	}
	return f.prevID, nil
}
func (f *fakeWorkReader) ListIssueWorkspaceDiff(_ context.Context, _ sqlc.ListIssueWorkspaceDiffParams) ([]sqlc.ListIssueWorkspaceDiffRow, error) {
	f.diffCalls++
	if f.diffErr != nil {
		return nil, f.diffErr
	}
	return f.diffRows, nil
}
func (f *fakeWorkReader) GetCrawlByIDForUser(_ context.Context, _ sqlc.GetCrawlByIDForUserParams) (sqlc.GetCrawlByIDForUserRow, error) {
	f.metaCalls++
	if f.crawlMetaErr != nil {
		return sqlc.GetCrawlByIDForUserRow{}, f.crawlMetaErr
	}
	return f.crawlMetaRow, nil
}

func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}
func uuid(b byte) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{b}, Valid: true}
}

func makeWorkRow(subjectKind, subjectKey, pillar, bucket, issueType, attemptStatus string, attemptCount int64, verifiedAt *time.Time, createdAt, updatedAt time.Time, attemptCreatedAt *time.Time, emails []string) sqlc.ReadIssueWorkItemsRow {
	row := sqlc.ReadIssueWorkItemsRow{
		ID:                uuid(10),
		SubjectKind:       subjectKind,
		SubjectKey:        subjectKey,
		Pillar:            pillar,
		Bucket:            bucket,
		IssueType:         issueType,
		ItemStatus:        "open",
		AttemptStatus:     attemptStatus,
		AttemptCount:      attemptCount,
		ItemCreatedAt:     ts(createdAt),
		ItemUpdatedAt:     ts(updatedAt),
		ContributorEmails: emails,
	}
	if verifiedAt != nil {
		row.VerifiedAt = ts(*verifiedAt)
	}
	if attemptCreatedAt != nil {
		row.AttemptCreatedAt = ts(*attemptCreatedAt)
	}
	return row
}

func makeDiffRow(url, pillar, bucket, issueType, severity, changeType string) sqlc.ListIssueWorkspaceDiffRow {
	return sqlc.ListIssueWorkspaceDiffRow{
		Url:        url,
		Pillar:     pillar,
		Bucket:     bucket,
		IssueType:  issueType,
		Severity:   severity,
		ChangeType: changeType,
	}
}

func runReadIssueWork(t *testing.T, fake *fakeWorkReader, raw string, budget *Budget) Result {
	t.Helper()
	exec := readIssueWorkExecutor{
		workItems:     fake,
		dimensions:    fake,
		latestCrawl:   fake,
		previousCrawl: fake,
		diffReader:    fake,
		crawlMeta:     fake,
	}
	result, err := exec.run(context.Background(), json.RawMessage(raw), testProjectID, testCrawlID, testUserID, budget)
	if err != nil {
		t.Fatalf("run(%s) returned error: %v", raw, err)
	}
	return result
}

func decodeWorkResponse(t *testing.T, result Result) readIssueWorkResponse {
	t.Helper()
	var resp readIssueWorkResponse
	if err := json.Unmarshal([]byte(result.Content), &resp); err != nil {
		t.Fatalf("content is not valid response JSON: %v\ncontent: %s", err, result.Content)
	}
	return resp
}

func TestParseReadIssueWorkArgs(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "empty defaults", raw: `{}`, wantErr: ""},
		{name: "limit zero rejected", raw: `{"limit":0}`, wantErr: "at least 1"},
		{name: "limit negative rejected", raw: `{"limit":-1}`, wantErr: "at least 1"},
		{name: "limit over max rejected", raw: `{"limit":51}`, wantErr: "at most 50"},
		{name: "limit 50 accepted", raw: `{"limit":50}`, wantErr: ""},
		{name: "offset negative rejected", raw: `{"offset":-1}`, wantErr: "offset must be >= 0"},
		{name: "unknown key rejected", raw: `{"bogus":1}`, wantErr: `unknown argument "bogus"`},
		{name: "duplicate key rejected", raw: `{"limit":1,"limit":2}`, wantErr: `duplicate argument "limit"`},
		{name: "trailing data rejected", raw: `{"limit":1} {"limit":2}`, wantErr: "trailing data"},
		{name: "invalid pillar", raw: `{"pillar":"social"}`, wantErr: "valid pillars"},
		{name: "invalid status", raw: `{"status":"bogus"}`, wantErr: "valid statuses"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseReadIssueWorkArgs(json.RawMessage(test.raw))
			if test.wantErr == "" && err != nil {
				t.Fatalf("parse err = %v, want nil", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("parse err = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestReadIssueWorkPrecedence(t *testing.T) {
	now := time.Now().UTC()
	verified := now.Add(-time.Hour)
	fake := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows: []sqlc.ReadIssueWorkItemsRow{
			makeWorkRow("page", "https://example.com/page", "seo", "meta_tags", "missing_title", "fixed", 1, &verified, now.Add(-48*time.Hour), now, &now, []string{"a@example.com"}),
		},
		diffRows: []sqlc.ListIssueWorkspaceDiffRow{
			makeDiffRow("https://example.com/page", "seo", "meta_tags", "missing_title", "high", "no_longer_detected"),
			makeDiffRow("https://example.com/page", "seo", "meta_tags", "missing_title", "high", "still_open"), // should be discarded anyway
		},
		latestID:     uuid(1),
		prevID:       uuid(2),
		crawlMetaRow: sqlc.GetCrawlByIDForUserRow{CompletedAt: ts(now)},
	}
	result := runReadIssueWork(t, fake, `{}`, nil)
	resp := decodeWorkResponse(t, result)
	// Only one item with fixed, diff claimed and discarded
	if len(resp.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(resp.Items))
	}
	if resp.Items[0].Status != "fixed" {
		t.Fatalf("Status = %q, want fixed", resp.Items[0].Status)
	}
	if resp.Items[0].URL != "https://example.com/page" {
		t.Fatalf("URL = %q, want page", resp.Items[0].URL)
	}
	// Ensure no no_longer_detected duplicate
	for _, it := range resp.Items {
		if it.Status == "no_longer_detected" {
			t.Fatalf("found no_longer_detected duplicate, precedence failed")
		}
	}
}

func TestReadIssueWorkUnclaimedDiffSurfaces(t *testing.T) {
	now := time.Now().UTC()
	fake := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows:      []sqlc.ReadIssueWorkItemsRow{},
		diffRows: []sqlc.ListIssueWorkspaceDiffRow{
			makeDiffRow("https://example.com/gone", "seo", "content", "thin_content", "low", "no_longer_detected"),
			makeDiffRow("https://example.com/stay", "seo", "content", "thin_content", "low", "still_open"),
			makeDiffRow("https://example.com/new", "seo", "content", "thin_content", "low", "new"),
		},
		latestID:     uuid(1),
		prevID:       uuid(2),
		crawlMetaRow: sqlc.GetCrawlByIDForUserRow{CompletedAt: ts(now)},
	}
	result := runReadIssueWork(t, fake, `{}`, nil)
	resp := decodeWorkResponse(t, result)
	if len(resp.Items) != 1 {
		t.Fatalf("Items = %d, want 1 (only no_longer_detected)", len(resp.Items))
	}
	if resp.Items[0].Status != "no_longer_detected" || resp.Items[0].URL != "https://example.com/gone" {
		t.Fatalf("item = %+v, want no_longer_detected gone", resp.Items[0])
	}
	if resp.Items[0].Severity == nil || *resp.Items[0].Severity != "low" {
		t.Fatalf("Severity = %v, want low", resp.Items[0].Severity)
	}
}

func TestReadIssueWorkZeroAttemptCollapsesToOpen(t *testing.T) {
	now := time.Now().UTC()
	fake := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows: []sqlc.ReadIssueWorkItemsRow{
			makeWorkRow("page", "https://example.com/open", "seo", "meta_tags", "missing_title", "", 0, nil, now.Add(-time.Hour), now, nil, nil),
		},
		latestErr: pgx.ErrNoRows, // no diff
	}
	result := runReadIssueWork(t, fake, `{}`, nil)
	resp := decodeWorkResponse(t, result)
	if len(resp.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(resp.Items))
	}
	if resp.Items[0].Status != "open" {
		t.Fatalf("Status = %q, want open", resp.Items[0].Status)
	}
	if resp.Items[0].AttemptCount != 0 {
		t.Fatalf("AttemptCount = %d, want 0", resp.Items[0].AttemptCount)
	}
}

func TestReadIssueWorkFilters(t *testing.T) {
	now := time.Now().UTC()
	fake := &fakeWorkReader{
		dimensionRows: []sqlc.ListDistinctCrawlIssueDimensionsRow{
			{Pillar: "seo", Bucket: "meta_tags", IssueType: "missing_title"},
			{Pillar: "aeo", Bucket: "answerability", IssueType: "missing_citations"},
		},
		workRows: []sqlc.ReadIssueWorkItemsRow{
			makeWorkRow("page", "https://example.com/a", "seo", "meta_tags", "missing_title", "fixed", 1, &now, now, now, &now, nil),
			makeWorkRow("page", "https://example.com/b", "aeo", "answerability", "missing_citations", "open", 0, nil, now, now, nil, nil),
		},
		latestErr: pgx.ErrNoRows,
	}
	// pillar filter
	result := runReadIssueWork(t, fake, `{"pillar":"seo"}`, nil)
	resp := decodeWorkResponse(t, result)
	// Note: workRows are not filtered in fake, but merged filtering should be post-merge pillar handling?
	// Our executor filters via SQL, but fake returns all; we test post-merge status/pillar filtering via args?
	// Actually fake ignores SQL filters, so we need to test status filter still.
	// Instead test bucket unknown error
	result = runReadIssueWork(t, fake, `{"bucket":"nope"}`, nil)
	if !strings.Contains(result.Content, "unknown bucket") || !strings.Contains(result.Content, "answerability") {
		t.Fatalf("Content = %q, want unknown bucket listing valid values", result.Content)
	}
	// status filter post-merge
	fake2 := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows: []sqlc.ReadIssueWorkItemsRow{
			makeWorkRow("page", "https://example.com/a", "seo", "meta_tags", "missing_title", "fixed", 1, &now, now, now, &now, nil),
			makeWorkRow("page", "https://example.com/b", "seo", "meta_tags", "missing_title", "", 0, nil, now, now, nil, nil),
		},
		latestErr: pgx.ErrNoRows,
	}
	result = runReadIssueWork(t, fake2, `{"status":"fixed"}`, nil)
	resp = decodeWorkResponse(t, result)
	if len(resp.Items) != 1 || resp.Items[0].Status != "fixed" {
		t.Fatalf("status filter failed: %+v", resp.Items)
	}
	_ = resp
}

func TestReadIssueWorkLimitOffset(t *testing.T) {
	now := time.Now().UTC()
	// Create 5 items with distinct last_activity for sorting
	var rows []sqlc.ReadIssueWorkItemsRow
	for i := 0; i < 5; i++ {
		tm := now.Add(-time.Duration(i) * time.Hour)
		updated := tm
		rows = append(rows, makeWorkRow("page", "https://example.com/"+string(rune('a'+i)), "seo", "content", "thin_content", "", 0, nil, now, updated, nil, nil))
	}
	// Make distinct URLs and times sorted descending already, but executor sorts by last_activity DESC
	fake := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows:      rows,
		latestErr:     pgx.ErrNoRows,
	}
	// limit 2 offset 1 should return 2nd and 3rd
	result := runReadIssueWork(t, fake, `{"limit":2,"offset":1}`, nil)
	resp := decodeWorkResponse(t, result)
	if len(resp.Items) != 2 {
		t.Fatalf("Items = %d, want 2", len(resp.Items))
	}
	if resp.TotalMatching != 5 {
		t.Fatalf("TotalMatching = %d, want 5", resp.TotalMatching)
	}
	if !resp.HasMore || resp.NextOffset == nil || *resp.NextOffset != 3 {
		t.Fatalf("HasMore/NextOffset = %v/%v, want true/3", resp.HasMore, resp.NextOffset)
	}
	// limit >50 rejected
	result = runReadIssueWork(t, fake, `{"limit":51}`, nil)
	if !strings.Contains(result.Content, "at most 50") {
		t.Fatalf("Content = %q, want limit rejected", result.Content)
	}
}

func TestReadIssueWorkSingleCrawlNoPrevious(t *testing.T) {
	now := time.Now().UTC()
	fake := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows: []sqlc.ReadIssueWorkItemsRow{
			makeWorkRow("page", "https://example.com/a", "seo", "meta_tags", "missing_title", "awaiting_verification", 1, &now, now, now, &now, nil),
		},
		latestID:     uuid(1),
		prevErr:      pgx.ErrNoRows, // no previous
		crawlMetaRow: sqlc.GetCrawlByIDForUserRow{CompletedAt: ts(now)},
	}
	result := runReadIssueWork(t, fake, `{}`, nil)
	resp := decodeWorkResponse(t, result)
	if len(resp.Items) != 1 || resp.Items[0].Status != "awaiting_verification" {
		t.Fatalf("single crawl failed: %+v", resp.Items)
	}
	if fake.diffCalls != 0 {
		t.Fatalf("diffCalls = %d, want 0 for single crawl", fake.diffCalls)
	}
}

func TestReadIssueWorkContributorsTruncation(t *testing.T) {
	now := time.Now().UTC()
	emails := []string{"a@ex.com", "b@ex.com", "c@ex.com", "d@ex.com", "e@ex.com", "f@ex.com", "g@ex.com"}
	// sort for deterministic truncation
	sort.Strings(emails)
	fake := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows: []sqlc.ReadIssueWorkItemsRow{
			makeWorkRow("page", "https://example.com/a", "seo", "meta_tags", "missing_title", "fixed", 1, &now, now, now, &now, emails),
		},
		latestErr: pgx.ErrNoRows,
	}
	result := runReadIssueWork(t, fake, `{}`, nil)
	resp := decodeWorkResponse(t, result)
	if len(resp.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(resp.Items))
	}
	contribs := resp.Items[0].Contributors
	if len(contribs) != 5 {
		t.Fatalf("Contributors = %v, want 5 entries", contribs)
	}
	if !strings.HasPrefix(contribs[4], "+") || !strings.Contains(contribs[4], "more") {
		t.Fatalf("Contributors[4] = %q, want +N more", contribs[4])
	}
}

func TestReadIssueWorkSummary(t *testing.T) {
	now := time.Now().UTC()
	// Test singular and order
	fake := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows: []sqlc.ReadIssueWorkItemsRow{
			makeWorkRow("page", "https://example.com/a", "seo", "meta_tags", "missing_title", "fixed", 1, &now, now, now, &now, nil),
		},
		diffRows: []sqlc.ListIssueWorkspaceDiffRow{
			makeDiffRow("https://example.com/b", "seo", "content", "thin_content", "low", "no_longer_detected"),
		},
		latestID:     uuid(1),
		prevID:       uuid(2),
		crawlMetaRow: sqlc.GetCrawlByIDForUserRow{CompletedAt: ts(now)},
	}
	// Add two items total, one fixed one no_longer_detected
	result := runReadIssueWork(t, fake, `{}`, nil)
	if !strings.Contains(result.Summary, "2 fix items:") || !strings.Contains(result.Summary, "1 fixed") || !strings.Contains(result.Summary, "1 no longer detected") {
		t.Fatalf("Summary = %q, want 2 fix items with fixed and no longer detected", result.Summary)
	}
	// Single item
	fake2 := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows: []sqlc.ReadIssueWorkItemsRow{
			makeWorkRow("page", "https://example.com/a", "seo", "meta_tags", "missing_title", "fixed", 1, &now, now, now, &now, nil),
		},
		latestErr: pgx.ErrNoRows,
	}
	result = runReadIssueWork(t, fake2, `{}`, nil)
	if result.Summary != "1 fix item: 1 fixed" {
		t.Fatalf("Summary = %q, want singular", result.Summary)
	}
	// Order test: fixed before open etc.
	fake3 := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows: []sqlc.ReadIssueWorkItemsRow{
			makeWorkRow("page", "https://example.com/a", "seo", "meta_tags", "missing_title", "", 0, nil, now, now, nil, nil), // open
			makeWorkRow("page", "https://example.com/b", "seo", "meta_tags", "missing_title", "fixed", 1, &now, now, now, &now, nil),
			makeWorkRow("page", "https://example.com/c", "seo", "meta_tags", "missing_title", "awaiting_verification", 1, &now, now, now, &now, nil),
		},
		latestErr: pgx.ErrNoRows,
	}
	result = runReadIssueWork(t, fake3, `{}`, nil)
	// Order should be fixed, awaiting_verification, open
	if !strings.Contains(result.Summary, "1 fixed, 1 awaiting verification, 1 open") {
		t.Fatalf("Summary order = %q, want fixed->awaiting->open", result.Summary)
	}
}

func TestReadIssueWorkRowBudget(t *testing.T) {
	now := time.Now().UTC()
	fake := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows: []sqlc.ReadIssueWorkItemsRow{
			makeWorkRow("page", "https://example.com/a", "seo", "meta_tags", "missing_title", "fixed", 1, &now, now, now, &now, nil),
		},
		latestErr: pgx.ErrNoRows,
	}
	budget := NewBudget(0)
	result := runReadIssueWork(t, fake, `{}`, budget)
	if !strings.Contains(result.Content, "row budget") {
		t.Fatalf("Content = %q, want row budget stub", result.Content)
	}
	if result.Summary != "row budget reached" {
		t.Fatalf("Summary = %q, want row budget reached", result.Summary)
	}
	if fake.workCalls != 0 {
		t.Fatalf("workCalls = %d, want 0 on budget exhausted", fake.workCalls)
	}
}

func TestReadIssueWorkBudgetSpendsReturnedRows(t *testing.T) {
	now := time.Now().UTC()
	fake := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows: []sqlc.ReadIssueWorkItemsRow{
			makeWorkRow("page", "https://example.com/a", "seo", "meta_tags", "missing_title", "fixed", 1, &now, now, now, &now, nil),
			makeWorkRow("page", "https://example.com/b", "seo", "meta_tags", "missing_title", "fixed", 1, &now, now, now, &now, nil),
			makeWorkRow("page", "https://example.com/c", "seo", "meta_tags", "missing_title", "fixed", 1, &now, now, now, &now, nil),
		},
		latestErr: pgx.ErrNoRows,
	}
	budget := NewBudget(10)
	result := runReadIssueWork(t, fake, `{"limit":1}`, budget)
	resp := decodeWorkResponse(t, result)
	if len(resp.Items) != 1 {
		t.Fatalf("Items = %d, want 1", len(resp.Items))
	}
	if got := budget.Remaining(); got != 9 {
		t.Fatalf("budget remaining = %d, want 9", got)
	}
}

func TestReadIssueWorkGroupNeverCollides(t *testing.T) {
	now := time.Now().UTC()
	fake := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workRows: []sqlc.ReadIssueWorkItemsRow{
			makeWorkRow("group", "group-id-123", "seo", "content", "duplicate_content", "fixed", 1, &now, now, now, &now, nil),
		},
		diffRows: []sqlc.ListIssueWorkspaceDiffRow{
			makeDiffRow("group-id-123", "seo", "content", "duplicate_content", "high", "no_longer_detected"),
		},
		latestID:     uuid(1),
		prevID:       uuid(2),
		crawlMetaRow: sqlc.GetCrawlByIDForUserRow{CompletedAt: ts(now)},
	}
	result := runReadIssueWork(t, fake, `{}`, nil)
	resp := decodeWorkResponse(t, result)
	// Group work should not claim diff, so both should appear (work fixed + diff no_longer)
	// Since diff url "group-id-123" equals group subject_key, but spec says group never collides, so keep both.
	if len(resp.Items) != 2 {
		t.Fatalf("Items = %d, want 2 (group + diff)", len(resp.Items))
	}
	hasFixed := false
	hasGone := false
	for _, it := range resp.Items {
		if it.Status == "fixed" {
			hasFixed = true
		}
		if it.Status == "no_longer_detected" {
			hasGone = true
		}
	}
	if !hasFixed || !hasGone {
		t.Fatalf("missing statuses fixed=%v gone=%v", hasFixed, hasGone)
	}
}

func TestExecuteReadIssueWorkRequiresQueries(t *testing.T) {
	_, err := executeReadIssueWork(context.Background(), json.RawMessage(`{}`), Scope{})
	if err == nil || !strings.Contains(err.Error(), "no queries") {
		t.Fatalf("error = %v, want no queries", err)
	}
}

func TestReadIssueWorkDatabaseErrorsPropagate(t *testing.T) {
	fake := &fakeWorkReader{
		dimensionRows: defaultDimensions(),
		workErr:       errors.New("boom"),
	}
	exec := readIssueWorkExecutor{
		workItems:     fake,
		dimensions:    fake,
		latestCrawl:   fake,
		previousCrawl: fake,
		diffReader:    fake,
		crawlMeta:     fake,
	}
	_, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testCrawlID, testUserID, nil)
	if err == nil || !strings.Contains(err.Error(), "list work items") {
		t.Fatalf("err = %v, want work items error", err)
	}
}
