package aitools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestParseListIssuesArgs(t *testing.T) {
	if _, err := parseListIssuesArgs(json.RawMessage(`{"pillar":"bogus"}`)); err == nil {
		t.Error("expected error for invalid pillar")
	}
	if _, err := parseListIssuesArgs(json.RawMessage(`{"severity":"bogus"}`)); err == nil {
		t.Error("expected error for invalid severity")
	}
	parsed, err := parseListIssuesArgs(json.RawMessage(`{"pillar":"seo","severity":"high","issue_type":"missing_title","bucket":"serp_metadata","limit":10}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Pillar != "seo" || parsed.Severity != "high" || parsed.IssueType != "missing_title" || parsed.Bucket != "serp_metadata" || parsed.Limit != 10 {
		t.Errorf("unexpected parsed args: %+v", parsed)
	}
	if _, err := parseListIssuesArgs(nil); err != nil {
		t.Errorf("empty args should be valid, got: %v", err)
	}

	withURLs, err := parseListIssuesArgs(json.RawMessage(`{"urls":["https://example.com/a","https://example.com/b"]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(withURLs.URLs) != 2 || withURLs.URLs[0] != "https://example.com/a" || withURLs.URLs[1] != "https://example.com/b" {
		t.Errorf("expected urls to be parsed, got %+v", withURLs.URLs)
	}
}

func TestParseListIssuesArgs_CapsURLs(t *testing.T) {
	urls := make([]string, maxListIssuesURLs+10)
	for i := range urls {
		urls[i] = "https://example.com/p"
	}
	raw, err := json.Marshal(map[string]any{"urls": urls})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := parseListIssuesArgs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.URLs) != maxListIssuesURLs {
		t.Errorf("expected urls capped to %d, got %d", maxListIssuesURLs, len(parsed.URLs))
	}
}

type fakeIssueLister struct {
	gotListArg  sqlc.ListCrawlIssuesFilteredForUserParams
	gotCountArg sqlc.CountCrawlIssuesFilteredForUserParams
	rows        []sqlc.ListCrawlIssuesFilteredForUserRow
	total       int64
}

func (f *fakeIssueLister) ListCrawlIssuesFilteredForUser(_ context.Context, arg sqlc.ListCrawlIssuesFilteredForUserParams) ([]sqlc.ListCrawlIssuesFilteredForUserRow, error) {
	f.gotListArg = arg
	return f.rows, nil
}

func (f *fakeIssueLister) CountCrawlIssuesFilteredForUser(_ context.Context, arg sqlc.CountCrawlIssuesFilteredForUserParams) (int64, error) {
	f.gotCountArg = arg
	return f.total, nil
}

func TestExecListIssues_UsesScopeIDsAndForwardsFilters(t *testing.T) {
	crawlID := testUUID(5)
	userID := testUUID(6)
	fake := &fakeIssueLister{
		rows: []sqlc.ListCrawlIssuesFilteredForUserRow{
			{Url: "https://example.com/a", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high", Message: "missing title"},
		},
		total: 3,
	}

	result, err := execListIssues(context.Background(), crawlID, userID, listIssuesArgs{Pillar: "seo", Severity: "high"}, fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.gotListArg.CrawlID != crawlID || fake.gotListArg.UserID != userID {
		t.Fatalf("expected list query to use scope IDs, got %+v", fake.gotListArg)
	}
	if fake.gotCountArg.CrawlID != crawlID || fake.gotCountArg.UserID != userID {
		t.Fatalf("expected count query to use scope IDs, got %+v", fake.gotCountArg)
	}
	if fake.gotListArg.Column3 != "seo" || fake.gotListArg.Column6 != "high" {
		t.Errorf("expected filters to be forwarded, got %+v", fake.gotListArg)
	}
	if fake.gotListArg.Column8 != nil || fake.gotCountArg.Column7 != nil {
		t.Errorf("expected empty urls to leave params unchanged, got list=%v count=%v", fake.gotListArg.Column8, fake.gotCountArg.Column7)
	}

	var output listIssuesOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("could not unmarshal result content: %v", err)
	}
	if output.TotalMatching != 3 || len(output.Issues) != 1 {
		t.Errorf("unexpected output: %+v", output)
	}
}

func TestExecListIssues_ForwardsURLsFilter(t *testing.T) {
	fake := &fakeIssueLister{}
	urls := []string{"https://example.com/a", "https://example.com/b"}
	if _, err := execListIssues(context.Background(), testUUID(1), testUUID(2), listIssuesArgs{IssueType: "missing_title", URLs: urls}, fake); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gotList := fake.gotListArg.Column8
	if len(gotList) != 2 || gotList[0] != urls[0] || gotList[1] != urls[1] {
		t.Errorf("expected urls forwarded to list params, got %+v", fake.gotListArg.Column8)
	}
	gotCount := fake.gotCountArg.Column7
	if len(gotCount) != 2 || gotCount[0] != urls[0] || gotCount[1] != urls[1] {
		t.Errorf("expected urls forwarded to count params, got %+v", fake.gotCountArg.Column7)
	}
}

func TestExecListIssues_ClampsLimit(t *testing.T) {
	fake := &fakeIssueLister{}
	if _, err := execListIssues(context.Background(), testUUID(1), testUUID(2), listIssuesArgs{Limit: 1000}, fake); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.gotListArg.Limit != maxListIssuesLimit {
		t.Errorf("expected limit clamped to %d, got %d", maxListIssuesLimit, fake.gotListArg.Limit)
	}
}
