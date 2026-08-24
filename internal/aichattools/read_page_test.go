package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type fakePageContentReader struct {
	row    sqlc.GetCrawlPageContentByURLForUserRow
	err    error
	calls  int
	params []sqlc.GetCrawlPageContentByURLForUserParams
}

func (f *fakePageContentReader) GetCrawlPageContentByURLForUser(_ context.Context, arg sqlc.GetCrawlPageContentByURLForUserParams) (sqlc.GetCrawlPageContentByURLForUserRow, error) {
	f.calls++
	f.params = append(f.params, arg)
	return f.row, f.err
}

func TestReadPageDefinitionAndArgs(t *testing.T) {
	tool := readPageTool()
	if tool.Def.Name != "read_page" || tool.Def.Label == "" || !json.Valid(tool.Def.Schema) {
		t.Fatalf("invalid definition: %+v", tool.Def)
	}
	var schema struct {
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
	}
	if err := json.Unmarshal(tool.Def.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Required) != 2 || schema.AdditionalProperties {
		t.Fatalf("schema = %+v", schema)
	}

	valid, err := parseReadPageArgs(json.RawMessage(`{"url":"https://example.com/a","mode":"content"}`))
	if err != nil || valid.URL != "https://example.com/a" || valid.Mode != "content" {
		t.Fatalf("valid args = %+v, %v", valid, err)
	}
	for _, raw := range []string{
		`{}`,
		`{"url":"https://example.com"}`,
		`{"url":"https://example.com","mode":"bad"}`,
		`{"url":"https://example.com","mode":"metadata","cursor":"x"}`,
		`{"url":"https://example.com","mode":"content","cursor":"x"}`,
		`{"url":"https://example.com","mode":"content","extra":true}`,
		`{"url":"https://example.com","url":"https://example.org","mode":"content"}`,
		`{"url":"https://example.com","mode":"content"} true`,
	} {
		if _, err := parseReadPageArgs(json.RawMessage(raw)); err == nil {
			t.Errorf("parseReadPageArgs(%s) succeeded", raw)
		}
	}
}

func TestReadPageMetadataDoesNotLoadOrSpendContent(t *testing.T) {
	row := testPageRow(false, nil)
	row.Title = pgtype.Text{String: "Title", Valid: true}
	row.WordCount = pgtype.Int4{Int32: 42, Valid: true}
	reader := &fakePageContentReader{row: row}
	pageBudget := NewPageContentBudget(1000, 1)
	rowBudget := NewBudget(2)
	exec := readPageExecutor{pages: reader}

	result, err := exec.run(context.Background(), json.RawMessage(`{"url":"https://example.com/a","mode":"metadata"}`), testPageScope(rowBudget, pageBudget))
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || reader.params[0].IncludeContent {
		t.Fatalf("query calls=%d params=%+v", reader.calls, reader.params)
	}
	if pageBudget.RemainingBytes() != 1000 || !pageBudget.TryRegisterPage("later") {
		t.Fatal("metadata mode touched the page-content budget")
	}
	if rowBudget.Remaining() != 1 {
		t.Fatalf("row budget = %d", rowBudget.Remaining())
	}
	var response readPageMetadataResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatal(err)
	}
	if response.Mode != "metadata" || response.Page.Title != "Title" || response.Page.WordCount == nil || *response.Page.WordCount != 42 {
		t.Fatalf("response = %+v", response)
	}
}

func TestReadPageContentFormatsSemanticBlocksWithoutHTML(t *testing.T) {
	raw := json.RawMessage(`[{"tag":"h2","text":"Heading","html":"Heading"},{"tag":"p","text":"Bold link","html":"<strong>Bold</strong> <a href=\"/next\">link</a>"},{"tag":"ul","text":"One Two","html":"<li>One</li><li><em>Two</em></li>"}]`)
	row := testPageRow(true, raw)
	reader := &fakePageContentReader{row: row}
	exec := readPageExecutor{pages: reader}
	budget := NewPageContentBudget(96<<10, 5)

	result, err := exec.run(context.Background(), json.RawMessage(`{"url":"https://example.com/a","mode":"content"}`), testPageScope(NewBudget(5), budget))
	if err != nil {
		t.Fatal(err)
	}
	if !reader.params[0].IncludeContent {
		t.Fatal("content mode did not request content_blocks")
	}
	if strings.Contains(result.Content, "<strong>") || strings.Contains(result.Content, "<li>") {
		t.Fatalf("response leaked raw HTML: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"type":"heading"`) || !strings.Contains(result.Content, `**Bold**`) || !strings.Contains(result.Content, `[link](https://example.com/next)`) || !strings.Contains(result.Content, `"type":"unordered_list"`) {
		t.Fatalf("response lost semantic content: %s", result.Content)
	}
	if got := budget.RemainingBytes(); got != (96<<10)-len(result.Content) {
		t.Fatalf("remaining bytes = %d, want %d", got, (96<<10)-len(result.Content))
	}
}

func TestReadPageContentPaginationIsBoundedAndRepeatable(t *testing.T) {
	huge := strings.Repeat("🙂word ", 9000)
	raw, err := json.Marshal([]map[string]string{{"tag": "p", "text": huge, "html": huge}})
	if err != nil {
		t.Fatal(err)
	}
	row := testPageRow(true, raw)
	reader := &fakePageContentReader{row: row}
	exec := readPageExecutor{pages: reader}
	budget := NewPageContentBudget(96<<10, 1)
	scope := testPageScope(NewBudget(10), budget)

	first, err := exec.run(context.Background(), json.RawMessage(`{"url":"https://example.com/a","mode":"content"}`), scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Content) > readPageResultLimit || !utf8Valid(first.Content) {
		t.Fatalf("first result size/UTF-8 = %d/%v", len(first.Content), utf8Valid(first.Content))
	}
	var firstResponse readPageContentResponse
	if err := json.Unmarshal([]byte(first.Content), &firstResponse); err != nil {
		t.Fatal(err)
	}
	if !firstResponse.Content.HasMore || firstResponse.Content.NextCursor == "" || strings.Contains(firstResponse.Content.NextCursor, "{") {
		t.Fatalf("first page has no opaque continuation: %+v", firstResponse.Content)
	}
	beforeSecond := budget.RemainingBytes()
	args, _ := json.Marshal(map[string]string{"url": "https://example.com/a", "mode": "content", "cursor": firstResponse.Content.NextCursor})
	second, err := exec.run(context.Background(), args, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Content) > readPageResultLimit || !utf8Valid(second.Content) {
		t.Fatalf("second result size/UTF-8 = %d/%v", len(second.Content), utf8Valid(second.Content))
	}
	if reader.calls != 2 {
		t.Fatalf("same-page continuation was denied; calls=%d", reader.calls)
	}
	if budget.RemainingBytes() != beforeSecond-len(second.Content) {
		t.Fatal("second page did not spend its exact serialized size")
	}
}

func TestReadPageUniquePageAndRowLimitsAvoidDB(t *testing.T) {
	row := testPageRow(false, nil)
	reader := &fakePageContentReader{row: row}
	exec := readPageExecutor{pages: reader}
	pageBudget := NewPageContentBudget(1000, 1)
	scope := testPageScope(NewBudget(5), pageBudget)

	if _, err := exec.run(context.Background(), json.RawMessage(`{"url":"https://example.com/a","mode":"content"}`), scope); err != nil {
		t.Fatal(err)
	}
	denied, err := exec.run(context.Background(), json.RawMessage(`{"url":"https://example.com/b","mode":"content"}`), scope)
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || !strings.Contains(denied.Content, "limit_reached") {
		t.Fatalf("unique-page denial = calls %d, %s", reader.calls, denied.Content)
	}

	zeroReader := &fakePageContentReader{row: row}
	zeroExec := readPageExecutor{pages: zeroReader}
	limited, err := zeroExec.run(context.Background(), json.RawMessage(`{"url":"https://example.com/a","mode":"metadata"}`), testPageScope(NewBudget(0), nil))
	if err != nil {
		t.Fatal(err)
	}
	if zeroReader.calls != 0 || !strings.Contains(limited.Content, "limit_reached") {
		t.Fatalf("row denial = calls %d, %s", zeroReader.calls, limited.Content)
	}
}

func TestReadPageInvalidCursorAndLowByteBudgetMakeNoProgress(t *testing.T) {
	t.Run("invalid cursor avoids database", func(t *testing.T) {
		reader := &fakePageContentReader{row: testPageRow(true, nil)}
		budget := NewPageContentBudget(1000, 1)
		result, err := (&readPageExecutor{pages: reader}).run(context.Background(), json.RawMessage(`{"url":"https://example.com/a","mode":"content","cursor":"invalid"}`), testPageScope(NewBudget(1), budget))
		if err != nil {
			t.Fatal(err)
		}
		if reader.calls != 0 || !strings.Contains(result.Content, "cursor") || !budget.TryRegisterPage("unused") {
			t.Fatalf("invalid cursor result=%s calls=%d", result.Content, reader.calls)
		}
	})

	t.Run("small remaining budget spends nothing", func(t *testing.T) {
		raw, err := json.Marshal([]map[string]string{{"tag": "p", "text": strings.Repeat("content ", 3000)}})
		if err != nil {
			t.Fatal(err)
		}
		reader := &fakePageContentReader{row: testPageRow(true, raw)}
		budget := NewPageContentBudget(1000, 1)
		result, err := (&readPageExecutor{pages: reader}).run(context.Background(), json.RawMessage(`{"url":"https://example.com/a","mode":"content"}`), testPageScope(NewBudget(1), budget))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Content, "limit_reached") || budget.RemainingBytes() != 1000 {
			t.Fatalf("low-budget result=%s remaining=%d", result.Content, budget.RemainingBytes())
		}
	})
}

func TestReadPageUnavailableMissingAndMalformed(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		reader := &fakePageContentReader{row: testPageRow(false, nil)}
		budget := NewPageContentBudget(1000, 5)
		result, err := (&readPageExecutor{pages: reader}).run(context.Background(), json.RawMessage(`{"url":"https://example.com/a","mode":"content"}`), testPageScope(NewBudget(5), budget))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Content, `"status":"unavailable"`) || !strings.Contains(result.Content, "Page content is not available for this crawl.") || strings.Contains(strings.ToLower(result.Content), "recrawl") {
			t.Fatalf("unavailable response = %s", result.Content)
		}
		if budget.RemainingBytes() != 1000 {
			t.Fatal("unavailable content spent bytes")
		}
	})

	t.Run("missing", func(t *testing.T) {
		reader := &fakePageContentReader{err: pgx.ErrNoRows}
		rowBudget := NewBudget(1)
		result, err := (&readPageExecutor{pages: reader}).run(context.Background(), json.RawMessage(`{"url":"https://example.com/missing","mode":"metadata"}`), testPageScope(rowBudget, nil))
		if err != nil || !strings.Contains(result.Content, "No page with that exact URL") || rowBudget.Remaining() != 1 {
			t.Fatalf("missing result=%+v err=%v budget=%d", result, err, rowBudget.Remaining())
		}
	})

	t.Run("malformed blocks", func(t *testing.T) {
		reader := &fakePageContentReader{row: testPageRow(true, json.RawMessage(`not-json`))}
		_, err := (&readPageExecutor{pages: reader}).run(context.Background(), json.RawMessage(`{"url":"https://example.com/a","mode":"content"}`), testPageScope(NewBudget(1), nil))
		if err == nil || !strings.Contains(err.Error(), "format content blocks") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("database error", func(t *testing.T) {
		reader := &fakePageContentReader{err: errors.New("boom")}
		_, err := (&readPageExecutor{pages: reader}).run(context.Background(), json.RawMessage(`{"url":"https://example.com/a","mode":"metadata"}`), testPageScope(NewBudget(1), nil))
		if err == nil || !strings.Contains(err.Error(), "read page") {
			t.Fatalf("error = %v", err)
		}
	})
}

func testPageRow(available bool, blocks []byte) sqlc.GetCrawlPageContentByURLForUserRow {
	return sqlc.GetCrawlPageContentByURLForUserRow{
		CrawlID:          pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		Url:              "https://example.com/a",
		ContentBlocks:    blocks,
		ContentAvailable: available,
	}
}

func testPageScope(rows *Budget, pages *PageContentBudget) Scope {
	return Scope{
		CrawlID:           pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		UserID:            pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		RowBudget:         rows,
		PageContentBudget: pages,
	}
}

func utf8Valid(value string) bool {
	return utf8.ValidString(value)
}
