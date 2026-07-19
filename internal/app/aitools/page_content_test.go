package aitools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func pgTextValid(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}

type fakePageReader struct {
	gotArg sqlc.GetCrawlPageByURLForUserParams
	row    sqlc.GetCrawlPageByURLForUserRow
	err    error
}

func (f *fakePageReader) GetCrawlPageByURLForUser(_ context.Context, arg sqlc.GetCrawlPageByURLForUserParams) (sqlc.GetCrawlPageByURLForUserRow, error) {
	f.gotArg = arg
	return f.row, f.err
}

func TestParsePageContentArgs(t *testing.T) {
	if _, err := parsePageContentArgs(json.RawMessage(`{}`)); err == nil {
		t.Error("expected error when url is missing")
	}
	parsed, err := parsePageContentArgs(json.RawMessage(`{"url":"https://example.com/"}`))
	if err != nil || parsed.URL != "https://example.com/" {
		t.Errorf("unexpected parse result: %+v, err=%v", parsed, err)
	}
}

func TestExecGetPageContent_UsesScopeIDsAndCapsVisibleText(t *testing.T) {
	crawlID := testUUID(9)
	userID := testUUID(10)
	longText := strings.Repeat("x", maxPageVisibleTextChars+500)
	fake := &fakePageReader{
		row: sqlc.GetCrawlPageByURLForUserRow{
			Url:         "https://example.com/a",
			H2Headings:  []byte(`["Heading A"]`),
			H3Headings:  []byte(`[]`),
			VisibleText: pgTextValid(longText),
		},
	}

	result, err := execGetPageContent(context.Background(), crawlID, userID, pageContentArgs{URL: "https://example.com/a"}, fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.gotArg.CrawlID != crawlID || fake.gotArg.UserID != userID {
		t.Fatalf("expected query to use scope IDs, got %+v", fake.gotArg)
	}

	var output pageContentOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("could not unmarshal result content: %v", err)
	}
	if !output.Found {
		t.Fatalf("expected found=true, got %+v", output)
	}
	if len(output.VisibleText) >= len(longText) {
		t.Errorf("expected visible text to be capped, got length %d", len(output.VisibleText))
	}
	if !strings.Contains(output.VisibleText, "truncated") {
		t.Errorf("expected truncation marker in visible text")
	}
	if len(output.H2Headings) != 1 || output.H2Headings[0] != "Heading A" {
		t.Errorf("expected h2 headings to be parsed, got %+v", output.H2Headings)
	}
}

func TestExecGetPageContent_NotFoundInCurrentCrawl(t *testing.T) {
	fake := &fakePageReader{err: pgx.ErrNoRows}

	result, err := execGetPageContent(context.Background(), testUUID(1), testUUID(2), pageContentArgs{URL: "https://other.example/"}, fake)
	if err != nil {
		t.Fatalf("no-rows should not be a Go error, got: %v", err)
	}
	if result.Summary != "page not found in current crawl" {
		t.Errorf("unexpected summary: %q", result.Summary)
	}
}
