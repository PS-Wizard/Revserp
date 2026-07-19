package aitools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestParseRecommendedFixArgs(t *testing.T) {
	if _, err := parseRecommendedFixArgs(json.RawMessage(`{}`)); err == nil {
		t.Error("expected error when issue_type is missing")
	}
	parsed, err := parseRecommendedFixArgs(json.RawMessage(`{"issue_type":"missing_title","url":"https://example.com/"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.IssueType != "missing_title" || parsed.URL != "https://example.com/" {
		t.Errorf("unexpected parsed args: %+v", parsed)
	}
}

func TestExecGetRecommendedFix_UsesScopeIDsAndKnownIssueType(t *testing.T) {
	crawlID := testUUID(7)
	userID := testUUID(8)
	fake := &fakeIssueLister{
		rows: []sqlc.ListCrawlIssuesFilteredForUserRow{
			{Url: "https://example.com/a", Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Severity: "high", Message: "missing title"},
		},
	}

	result, err := execGetRecommendedFix(context.Background(), crawlID, userID, recommendedFixArgs{IssueType: "missing_title"}, fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.gotListArg.CrawlID != crawlID || fake.gotListArg.UserID != userID {
		t.Fatalf("expected query to use scope IDs, got %+v", fake.gotListArg)
	}
	if fake.gotListArg.Limit != 1 {
		t.Errorf("expected limit 1, got %d", fake.gotListArg.Limit)
	}

	var output recommendedFixOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("could not unmarshal result content: %v", err)
	}
	if !output.Found || output.RecommendedFix == "" {
		t.Errorf("expected a found recommended fix, got %+v", output)
	}
}

func TestExecGetRecommendedFix_UnknownIssueType(t *testing.T) {
	fake := &fakeIssueLister{rows: nil}

	result, err := execGetRecommendedFix(context.Background(), testUUID(1), testUUID(2), recommendedFixArgs{IssueType: "not_a_real_issue_type"}, fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output recommendedFixOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("could not unmarshal result content: %v", err)
	}
	if output.Found {
		t.Errorf("expected found=false for unknown issue type, got %+v", output)
	}
}
