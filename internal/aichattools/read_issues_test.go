package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

var (
	testCrawlID = pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	testUserID  = pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
)

// fakeIssueReader implements all read_issues reader interfaces without a database.
type fakeIssueReader struct {
	total         int64
	countErr      error
	breakdownRows []sqlc.BreakdownCrawlIssuesFilteredForUserRow
	breakdownErr  error
	dimensionRows []sqlc.ListDistinctCrawlIssueDimensionsRow
	dimensionErr  error
	rows          []sqlc.ListCrawlIssuesFilteredForUserRow
	listErr       error

	countCalls      int
	listCalls       int
	breakdownCalls  int
	dimensionCalls  int
	lastListParams  sqlc.ListCrawlIssuesFilteredForUserParams
	lastCountParams sqlc.CountCrawlIssuesFilteredForUserParams
}

func (f *fakeIssueReader) ListCrawlIssuesFilteredForUser(_ context.Context, arg sqlc.ListCrawlIssuesFilteredForUserParams) ([]sqlc.ListCrawlIssuesFilteredForUserRow, error) {
	f.listCalls++
	f.lastListParams = arg
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.rows, nil
}

func (f *fakeIssueReader) CountCrawlIssuesFilteredForUser(_ context.Context, arg sqlc.CountCrawlIssuesFilteredForUserParams) (int64, error) {
	f.countCalls++
	f.lastCountParams = arg
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.total, nil
}

func (f *fakeIssueReader) BreakdownCrawlIssuesFilteredForUser(_ context.Context, _ sqlc.BreakdownCrawlIssuesFilteredForUserParams) ([]sqlc.BreakdownCrawlIssuesFilteredForUserRow, error) {
	f.breakdownCalls++
	if f.breakdownErr != nil {
		return nil, f.breakdownErr
	}
	return f.breakdownRows, nil
}

func (f *fakeIssueReader) ListDistinctCrawlIssueDimensions(_ context.Context, _ sqlc.ListDistinctCrawlIssueDimensionsParams) ([]sqlc.ListDistinctCrawlIssueDimensionsRow, error) {
	f.dimensionCalls++
	if f.dimensionErr != nil {
		return nil, f.dimensionErr
	}
	return f.dimensionRows, nil
}

func defaultDimensions() []sqlc.ListDistinctCrawlIssueDimensionsRow {
	return []sqlc.ListDistinctCrawlIssueDimensionsRow{
		{Pillar: "seo", Bucket: "meta_tags", IssueType: "missing_title"},
		{Pillar: "seo", Bucket: "content", IssueType: "thin_content"},
	}
}

func makeIssueRow(issueType, severity, message, details string) sqlc.ListCrawlIssuesFilteredForUserRow {
	return sqlc.ListCrawlIssuesFilteredForUserRow{
		ID:        pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
		Url:       "https://example.com/page",
		Pillar:    "seo",
		Bucket:    "meta_tags",
		IssueType: issueType,
		Severity:  severity,
		Message:   message,
		Details:   details,
	}
}

func runReadIssues(t *testing.T, fake *fakeIssueReader, raw string, budget *Budget) Result {
	t.Helper()
	exec := readIssuesExecutor{
		lister:     fake,
		counter:    fake,
		breakdown:  fake,
		dimensions: fake,
	}
	result, err := exec.run(context.Background(), json.RawMessage(raw), testCrawlID, testUserID, budget)
	if err != nil {
		t.Fatalf("run(%s) returned error: %v", raw, err)
	}
	return result
}

func TestParseReadIssuesArgs(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    readIssuesArgs
		wantErr string
	}{
		{name: "empty object defaults", raw: `{}`, want: readIssuesArgs{Limit: 25}},
		{name: "empty input defaults", raw: ``, want: readIssuesArgs{Limit: 25}},
		{name: "full args normalized", raw: `{"pillars":["SEO","aeo"],"bucket":"meta_tags","issue_type":"missing_title","severity":"HIGH","urls":["https://a.test"],"limit":40,"offset":10}`,
			want: readIssuesArgs{Pillars: []string{"seo", "aeo"}, Bucket: "meta_tags", IssueType: "missing_title", Severity: "high", URLs: []string{"https://a.test"}, Limit: 40, Offset: 10}},
		{name: "limit clamped to max", raw: `{"limit":100}`, want: readIssuesArgs{Limit: 50}},
		{name: "limit zero rejected", raw: `{"limit":0}`, wantErr: "at least 1"},
		{name: "limit negative rejected", raw: `{"limit":-3}`, wantErr: "at least 1"},
		{name: "limit float rejected", raw: `{"limit":1.5}`, wantErr: "must be an integer"},
		{name: "offset negative rejected", raw: `{"offset":-1}`, wantErr: "offset must be >= 0"},
		{name: "unknown key rejected", raw: `{"pillars":["seo"],"bogus":1}`, wantErr: `unknown argument "bogus"`},
		{name: "duplicate key rejected", raw: `{"limit":1,"limit":2}`, wantErr: `duplicate argument "limit"`},
		{name: "trailing data rejected", raw: `{"limit":1} {"limit":2}`, wantErr: "trailing data"},
		{name: "non-object rejected", raw: `[1,2]`, wantErr: "must be a JSON object"},
		{name: "null rejected", raw: `null`, wantErr: "must be a JSON object"},
		{name: "pillars non-array rejected", raw: `{"pillars":1}`, wantErr: "array of strings"},
		{name: "pillars empty rejected", raw: `{"pillars":[]}`, wantErr: "must not be empty"},
		{name: "pillars invalid value rejected", raw: `{"pillars":["social"]}`, wantErr: "valid pillars: seo, aeo, pagespeed"},
		{name: "pillars deduplicated", raw: `{"pillars":["seo","SEO","aeo"]}`, want: readIssuesArgs{Pillars: []string{"seo", "aeo"}, Limit: 25}},
		{name: "urls non-array rejected", raw: `{"urls":"https://a.test"}`, wantErr: "array of strings"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseReadIssuesArgs(json.RawMessage(test.raw))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseReadIssuesArgs(%s) error = %v, want containing %q", test.raw, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseReadIssuesArgs(%s) error = %v", test.raw, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseReadIssuesArgs(%s) = %+v, want %+v", test.raw, got, test.want)
			}
		})
	}
}

func decodeResponse(t *testing.T, result Result) readIssuesResponse {
	t.Helper()
	var response readIssuesResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("content is not valid response JSON: %v\ncontent: %s", err, result.Content)
	}
	return response
}

func TestReadIssuesPaging(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		total       int64
		rows        int
		wantNext    int
		wantHasMore bool
		wantSummary string
	}{
		{name: "first page has more", raw: `{}`, total: 100, rows: 25, wantNext: 25, wantHasMore: true, wantSummary: "25 issues shown (100 matching total)"},
		{name: "last page ends exactly", raw: `{"offset":95}`, total: 100, rows: 5, wantNext: 100, wantHasMore: false, wantSummary: "5 issues shown (100 matching total)"},
		{name: "offset beyond total", raw: `{"offset":150}`, total: 100, rows: 0, wantNext: 150, wantHasMore: false, wantSummary: "0 issues shown (100 matching total)"},
		{name: "single issue summary", raw: `{}`, total: 1, rows: 1, wantNext: 1, wantHasMore: false, wantSummary: "1 issue shown (1 matching total)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeIssueReader{
				total:         test.total,
				dimensionRows: defaultDimensions(),
			}
			for range test.rows {
				fake.rows = append(fake.rows, makeIssueRow("missing_title", "high", "msg", "det"))
			}
			result := runReadIssues(t, fake, test.raw, nil)
			response := decodeResponse(t, result)
			if response.TotalMatching != test.total {
				t.Fatalf("TotalMatching = %d, want %d", response.TotalMatching, test.total)
			}
			if response.NextOffset != test.wantNext {
				t.Fatalf("NextOffset = %d, want %d", response.NextOffset, test.wantNext)
			}
			if response.HasMore != test.wantHasMore {
				t.Fatalf("HasMore = %v, want %v", response.HasMore, test.wantHasMore)
			}
			if result.Summary != test.wantSummary {
				t.Fatalf("Summary = %q, want %q", result.Summary, test.wantSummary)
			}
		})
	}
}

func TestReadIssuesBreakdownShaping(t *testing.T) {
	fake := &fakeIssueReader{
		total: 17,
		breakdownRows: []sqlc.BreakdownCrawlIssuesFilteredForUserRow{
			{Pillar: "seo", Bucket: "meta_tags", IssueType: "missing_title", Severity: "high", IssueCount: 5},
			{Pillar: "seo", Bucket: "meta_tags", IssueType: "missing_meta_description", Severity: "medium", IssueCount: 2},
			{Pillar: "seo", Bucket: "content", IssueType: "thin_content", Severity: "low", IssueCount: 7},
			{Pillar: "seo", Bucket: "meta_tags", IssueType: "missing_title", Severity: "low", IssueCount: 3},
		},
		dimensionRows: defaultDimensions(),
	}
	response := decodeResponse(t, runReadIssues(t, fake, `{}`, nil))

	if len(response.Breakdown.ByBucket) != 2 {
		t.Fatalf("ByBucket = %d groups, want 2", len(response.Breakdown.ByBucket))
	}
	top := response.Breakdown.ByBucket[0]
	if top.Bucket != "meta_tags" || top.Label != "Meta Tags" || top.Pillar != "seo" || top.Count != 10 {
		t.Fatalf("top bucket = %+v, want meta_tags/Meta Tags/seo/10", top)
	}
	if top.Severities.High != 5 || top.Severities.Medium != 2 || top.Severities.Low != 3 {
		t.Fatalf("top bucket severities = %+v, want high=5 medium=2 low=3", top.Severities)
	}
	second := response.Breakdown.ByBucket[1]
	if second.Severities.High != 0 || second.Severities.Medium != 0 || second.Severities.Low != 7 {
		t.Fatalf("second bucket severities = %+v, want high=0 medium=0 low=7", second.Severities)
	}

	if len(response.Breakdown.ByIssueType) != 3 {
		t.Fatalf("ByIssueType = %d groups, want 3", len(response.Breakdown.ByIssueType))
	}
	topType := response.Breakdown.ByIssueType[0]
	if topType.IssueType != "missing_title" || topType.Label != "Missing Title" || topType.Count != 8 {
		t.Fatalf("top issue type = %+v, want missing_title/Missing Title/8", topType)
	}
}

func TestReadIssuesBreakdownTopNCap(t *testing.T) {
	fake := &fakeIssueReader{
		total:         325,
		dimensionRows: defaultDimensions(),
	}
	for i := 1; i <= 25; i++ {
		fake.breakdownRows = append(fake.breakdownRows, sqlc.BreakdownCrawlIssuesFilteredForUserRow{
			Pillar:     "seo",
			Bucket:     "bucket_" + fmt.Sprintf("%02d", i),
			IssueType:  "issue_" + fmt.Sprintf("%02d", i),
			Severity:   "high",
			IssueCount: int64(i),
		})
	}
	response := decodeResponse(t, runReadIssues(t, fake, `{}`, nil))

	if len(response.Breakdown.ByBucket) != 20 {
		t.Fatalf("ByBucket = %d groups, want capped at 20", len(response.Breakdown.ByBucket))
	}
	if len(response.Breakdown.ByIssueType) != 20 {
		t.Fatalf("ByIssueType = %d groups, want capped at 20", len(response.Breakdown.ByIssueType))
	}
	if response.Breakdown.ByBucket[0].Bucket != "bucket_25" || response.Breakdown.ByBucket[0].Count != 25 {
		t.Fatalf("top bucket = %+v, want bucket_25/25", response.Breakdown.ByBucket[0])
	}
	for i := 1; i < len(response.Breakdown.ByBucket); i++ {
		if response.Breakdown.ByBucket[i-1].Count < response.Breakdown.ByBucket[i].Count {
			t.Fatalf("ByBucket not sorted by count desc at %d: %d < %d", i, response.Breakdown.ByBucket[i-1].Count, response.Breakdown.ByBucket[i].Count)
		}
	}
}

func TestReadIssuesFixFoldingAndTruncation(t *testing.T) {
	fake := &fakeIssueReader{
		total:         2,
		dimensionRows: defaultDimensions(),
		rows: []sqlc.ListCrawlIssuesFilteredForUserRow{
			makeIssueRow("missing_title", "high", strings.Repeat("m", 300), strings.Repeat("d", 300)),
			{ID: pgtype.UUID{Bytes: [16]byte{4}, Valid: true}, Url: "https://example.com/other", Pillar: "seo", Bucket: "content", IssueType: "custom_check", Severity: "low", Message: "missing x", Details: "check y"},
		},
	}
	response := decodeResponse(t, runReadIssues(t, fake, `{}`, nil))
	if len(response.Issues) != 2 {
		t.Fatalf("Issues = %d rows, want 2", len(response.Issues))
	}

	first := response.Issues[0]
	if want := "Add a descriptive <title> tag that reflects the page intent and primary query."; first.RecommendedFix != want {
		t.Fatalf("RecommendedFix = %q, want canonical %q", first.RecommendedFix, want)
	}
	if want := strings.Repeat("m", 250) + "\u2026"; first.Message != want {
		t.Fatalf("Message = %d runes, want capped at 250 with marker", len([]rune(first.Message)))
	}
	if want := strings.Repeat("d", 250) + "\u2026"; first.Details != want {
		t.Fatalf("Details not capped with marker: %d runes", len([]rune(first.Details)))
	}

	second := response.Issues[1]
	if !strings.Contains(second.RecommendedFix, "Review Custom Check in Content") || !strings.Contains(second.RecommendedFix, "missing x check y") {
		t.Fatalf("fallback RecommendedFix = %q, want formula with message and details", second.RecommendedFix)
	}
	if second.Message != "missing x" || second.Details != "check y" {
		t.Fatalf("short text was truncated: %q / %q", second.Message, second.Details)
	}
}

func TestReadIssuesFilterValidation(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantErr  string
		wantNoDB bool
	}{
		{name: "invalid pillar", raw: `{"pillars":["social"]}`, wantErr: "valid pillars: seo, aeo, pagespeed", wantNoDB: true},
		{name: "invalid severity", raw: `{"severity":"critical"}`, wantErr: "valid severities: high, medium, low", wantNoDB: true},
		{name: "negative offset", raw: `{"offset":-1}`, wantErr: "offset must be >= 0", wantNoDB: true},
		{name: "unknown bucket", raw: `{"bucket":"nope"}`, wantErr: "unknown bucket \"nope\"; valid buckets: content, meta_tags"},
		{name: "unknown issue type", raw: `{"issue_type":"nope"}`, wantErr: "unknown issue_type \"nope\"; valid issue types: missing_title, thin_content"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeIssueReader{dimensionRows: defaultDimensions()}
			result := runReadIssues(t, fake, test.raw, nil)
			if !strings.Contains(result.Content, test.wantErr) {
				t.Fatalf("Content = %q, want containing %q", result.Content, test.wantErr)
			}
			if test.wantNoDB && (fake.dimensionCalls != 0 || fake.countCalls != 0 || fake.listCalls != 0) {
				t.Fatalf("validation error hit the database: %+v", fake)
			}
		})
	}
}

func TestReadIssuesBucketLabelResolution(t *testing.T) {
	tests := []struct {
		name       string
		bucket     string
		wantBucket string
	}{
		{name: "id exact", bucket: "meta_tags", wantBucket: "meta_tags"},
		{name: "id case-insensitive", bucket: "META_TAGS", wantBucket: "meta_tags"},
		{name: "human label", bucket: "Meta Tags", wantBucket: "meta_tags"},
		{name: "label case-insensitive", bucket: "meta tags", wantBucket: "meta_tags"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeIssueReader{
				total:         0,
				dimensionRows: defaultDimensions(),
			}
			runReadIssues(t, fake, `{"bucket":`+strconv.Quote(test.bucket)+`}`, nil)
			if got := fake.lastCountParams.Column4; got != test.wantBucket {
				t.Fatalf("count filter bucket = %v, want %q", got, test.wantBucket)
			}
			if got := fake.lastListParams.Column4; got != test.wantBucket {
				t.Fatalf("list filter bucket = %v, want %q", got, test.wantBucket)
			}
		})
	}
}

func TestReadIssuesIssueTypeAcceptance(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantType  string
		wantError bool
	}{
		{name: "id exact", raw: `{"issue_type":"missing_title"}`, wantType: "missing_title"},
		{name: "id case-insensitive", raw: `{"issue_type":"MISSING_TITLE"}`, wantType: "missing_title"},
		{name: "human label", raw: `{"issue_type":"Missing Title"}`, wantType: "missing_title"},
		{name: "label case-insensitive", raw: `{"issue_type":"missing title"}`, wantType: "missing_title"},
		{name: "pillar-scoped id", raw: `{"pillars":["aeo"],"issue_type":"missing_citations"}`, wantType: "missing_citations"},
		{name: "unknown", raw: `{"issue_type":"nope"}`, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeIssueReader{
				total: 0,
				dimensionRows: []sqlc.ListDistinctCrawlIssueDimensionsRow{
					{Pillar: "seo", Bucket: "meta_tags", IssueType: "missing_title"},
					{Pillar: "aeo", Bucket: "answerability", IssueType: "missing_citations"},
				},
			}
			result := runReadIssues(t, fake, test.raw, nil)
			if test.wantError {
				if !strings.Contains(result.Content, "unknown issue_type") {
					t.Fatalf("Content = %q, want unknown issue_type error", result.Content)
				}
				return
			}
			if got := fake.lastCountParams.Column5; got != test.wantType {
				t.Fatalf("count filter issue_type = %v, want %q", got, test.wantType)
			}
			if got := fake.lastListParams.Column5; got != test.wantType {
				t.Fatalf("list filter issue_type = %v, want %q", got, test.wantType)
			}
		})
	}
}

// TestReadIssuesIssueTypeNotConfusedByBucketVocabulary is the regression test for
// the bug where issue_type validation checked the bucket vocabulary, so every
// real issue type was rejected while bucket ids were wrongly accepted.
func TestReadIssuesIssueTypeNotConfusedByBucketVocabulary(t *testing.T) {
	fake := &fakeIssueReader{
		total: 0,
		dimensionRows: []sqlc.ListDistinctCrawlIssueDimensionsRow{
			{Pillar: "pagespeed", Bucket: "server_responsiveness", IssueType: "slow_response_time"},
			{Pillar: "pagespeed", Bucket: "page_weight", IssueType: "large_page_size"},
		},
	}

	// A real issue type must be accepted even though it is not a bucket id.
	result := runReadIssues(t, fake, `{"issue_type":"slow_response_time"}`, nil)
	if strings.Contains(result.Content, "unknown issue_type") {
		t.Fatalf("Content = %q, want accepted", result.Content)
	}
	if got := fake.lastCountParams.Column5; got != "slow_response_time" {
		t.Fatalf("count filter issue_type = %v, want slow_response_time", got)
	}

	// A bucket id must not be accepted as an issue type.
	result = runReadIssues(t, fake, `{"issue_type":"server_responsiveness"}`, nil)
	if !strings.Contains(result.Content, "unknown issue_type") {
		t.Fatalf("Content = %q, want unknown issue_type error", result.Content)
	}
}

func TestReadIssuesURLsCap(t *testing.T) {
	fake := &fakeIssueReader{dimensionRows: defaultDimensions()}
	urls := make([]string, 30)
	for i := range urls {
		urls[i] = "https://example.com/" + strconv.Itoa(i)
	}
	payload, err := json.Marshal(map[string]any{"urls": urls})
	if err != nil {
		t.Fatal(err)
	}
	runReadIssues(t, fake, string(payload), nil)
	if got := len(fake.lastListParams.Column8); got != 25 {
		t.Fatalf("list filter urls = %d entries, want truncated to 25", got)
	}
}

func TestReadIssuesLimitOffsetForwarded(t *testing.T) {
	fake := &fakeIssueReader{dimensionRows: defaultDimensions()}
	runReadIssues(t, fake, `{"limit":40,"offset":10}`, nil)
	if fake.lastListParams.Limit != 40 || fake.lastListParams.Offset != 10 {
		t.Fatalf("list params = limit %d offset %d, want 40/10", fake.lastListParams.Limit, fake.lastListParams.Offset)
	}
}
func TestReadIssuesPillarsForwarded(t *testing.T) {
	fake := &fakeIssueReader{dimensionRows: defaultDimensions()}
	runReadIssues(t, fake, `{"pillars":["aeo","SEO"]}`, nil)
	want := []string{"aeo", "seo"}
	if !reflect.DeepEqual(fake.lastCountParams.Column3, want) {
		t.Fatalf("count pillar filter = %v, want %v", fake.lastCountParams.Column3, want)
	}
	if !reflect.DeepEqual(fake.lastListParams.Column3, want) {
		t.Fatalf("list pillar filter = %v, want %v", fake.lastListParams.Column3, want)
	}
	if fake.lastListParams.Limit != 25 {
		t.Fatalf("limit = %d, want 25 (single-element default)", fake.lastListParams.Limit)
	}
}

func TestReadIssuesCombinedLimitClamp(t *testing.T) {
	fake := &fakeIssueReader{dimensionRows: defaultDimensions()}
	runReadIssues(t, fake, `{"pillars":["seo","aeo"],"limit":50}`, nil)
	if fake.lastListParams.Limit != readIssuesCombinedMaxRows {
		t.Fatalf("combined limit = %d, want clamped to %d", fake.lastListParams.Limit, readIssuesCombinedMaxRows)
	}
}

func TestReadIssuesSinglePillarLimitUnclamped(t *testing.T) {
	fake := &fakeIssueReader{dimensionRows: defaultDimensions()}
	runReadIssues(t, fake, `{"pillars":["aeo"],"limit":50}`, nil)
	if fake.lastListParams.Limit != 50 {
		t.Fatalf("single-pillar limit = %d, want 50 (no clamp)", fake.lastListParams.Limit)
	}
}

func TestReadIssuesBucketValidUnderAnyRequestedPillar(t *testing.T) {
	fake := &fakeIssueReader{
		total: 0,
		dimensionRows: []sqlc.ListDistinctCrawlIssueDimensionsRow{
			{Pillar: "seo", Bucket: "meta_tags", IssueType: "missing_title"},
			{Pillar: "aeo", Bucket: "answerability", IssueType: "missing_citations"},
		},
	}
	// The bucket exists under aeo; requesting seo alone must reject it.
	result := runReadIssues(t, fake, `{"pillars":["seo"],"bucket":"answerability"}`, nil)
	if !strings.Contains(result.Content, "unknown bucket") {
		t.Fatalf("Content = %q, want unknown bucket error", result.Content)
	}
	// Requesting seo+aeo together accepts the bucket.
	result = runReadIssues(t, fake, `{"pillars":["seo","aeo"],"bucket":"answerability"}`, nil)
	if strings.Contains(result.Content, "unknown bucket") {
		t.Fatalf("Content = %q, want accepted under seo+aeo", result.Content)
	}
}

func TestReadIssuesRowBudget(t *testing.T) {
	t.Run("spend down", func(t *testing.T) {
		fake := &fakeIssueReader{
			total:         3,
			dimensionRows: defaultDimensions(),
		}
		for range 3 {
			fake.rows = append(fake.rows, makeIssueRow("missing_title", "high", "msg", "det"))
		}
		budget := NewBudget(10)
		result := runReadIssues(t, fake, `{}`, budget)
		if budget.Remaining() != 7 {
			t.Fatalf("budget remaining = %d, want 7", budget.Remaining())
		}
		if result.Summary != "3 issues shown (3 matching total)" {
			t.Fatalf("Summary = %q, want normal summary", result.Summary)
		}
	})

	t.Run("overspend clamps", func(t *testing.T) {
		fake := &fakeIssueReader{
			total:         30,
			dimensionRows: defaultDimensions(),
		}
		for range 5 {
			fake.rows = append(fake.rows, makeIssueRow("missing_title", "high", "msg", "det"))
		}
		budget := NewBudget(3)
		runReadIssues(t, fake, `{}`, budget)
		if budget.Remaining() != 0 {
			t.Fatalf("budget remaining = %d, want 0", budget.Remaining())
		}
	})

	t.Run("stub at zero", func(t *testing.T) {
		fake := &fakeIssueReader{dimensionRows: defaultDimensions()}
		result := runReadIssues(t, fake, `{}`, NewBudget(0))
		if !strings.Contains(result.Content, "row budget") {
			t.Fatalf("Content = %q, want row budget stub", result.Content)
		}
		if result.Summary != "row budget reached" {
			t.Fatalf("Summary = %q, want row budget reached", result.Summary)
		}
		if fake.dimensionCalls != 0 || fake.countCalls != 0 || fake.listCalls != 0 {
			t.Fatalf("stub result hit the database: %+v", fake)
		}
	})

	t.Run("nil budget uncapped", func(t *testing.T) {
		fake := &fakeIssueReader{
			total:         1,
			dimensionRows: defaultDimensions(),
			rows:          []sqlc.ListCrawlIssuesFilteredForUserRow{makeIssueRow("missing_title", "high", "msg", "det")},
		}
		result := runReadIssues(t, fake, `{}`, nil)
		if result.Summary != "1 issue shown (1 matching total)" {
			t.Fatalf("Summary = %q, want normal summary", result.Summary)
		}
	})
}

func TestReadIssuesDatabaseErrorsPropagate(t *testing.T) {
	fake := &fakeIssueReader{
		dimensionRows: defaultDimensions(),
		listErr:       errors.New("boom"),
	}
	exec := readIssuesExecutor{lister: fake, counter: fake, breakdown: fake, dimensions: fake}
	_, err := exec.run(context.Background(), json.RawMessage(`{}`), testCrawlID, testUserID, nil)
	if err == nil || !strings.Contains(err.Error(), "list issues") {
		t.Fatalf("run() error = %v, want wrapped list error", err)
	}
}

func TestExecuteReadIssuesRequiresQueries(t *testing.T) {
	_, err := executeReadIssues(context.Background(), json.RawMessage(`{}`), Scope{})
	if err == nil || !strings.Contains(err.Error(), "no queries") {
		t.Fatalf("executeReadIssues error = %v, want no-queries error", err)
	}
}
