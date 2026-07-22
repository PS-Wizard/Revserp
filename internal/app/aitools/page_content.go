package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const maxPageVisibleTextChars = 6000

// pageReader is the narrow DB port get_page_content depends on.
type pageReader interface {
	GetCrawlPageByURLForUser(ctx context.Context, arg sqlc.GetCrawlPageByURLForUserParams) (sqlc.GetCrawlPageByURLForUserRow, error)
}

func pageContentTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "get_page_content",
			Description: "Get the crawled content for one page URL in the current crawl: title, meta description, H1/H2/H3 headings, canonical URL, robots directive, JSON-LD, word count, and visible text (capped to ~6000 characters). Use sparingly: this returns a page's full text and consumes a lot of context. Call it ONLY for a specific page (or a small, explicitly named/selected set of pages) the user asked about. For issue fixes prefer list_issues / get_recommended_fix, whose rows already include the affected field values — do not read a page unless you truly need its surrounding copy, and never bulk-read pages to survey a site. Reads are capped at 8 pages per turn; if you need more, ask the user to narrow to specific pages.",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {"type": "string", "description": "The exact page URL to fetch, as it appears in the crawl."}
  },
  "required": ["url"],
  "additionalProperties": false
}`),
		},
		Execute: func(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
			parsedArgs, err := parsePageContentArgs(args)
			if err != nil {
				return jsonResult(errorOutput{Error: err.Error()}, "invalid arguments")
			}
			return execGetPageContent(ctx, s.CrawlID, s.UserID, parsedArgs, s.Queries)
		},
	}
}

type pageContentArgs struct {
	URL string `json:"url"`
}

func parsePageContentArgs(args json.RawMessage) (pageContentArgs, error) {
	var parsed pageContentArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return pageContentArgs{}, fmt.Errorf("could not parse arguments: %w", err)
		}
	}
	if parsed.URL == "" {
		return pageContentArgs{}, fmt.Errorf("url is required")
	}
	return parsed, nil
}

type pageContentOutput struct {
	Found           bool     `json:"found"`
	URL             string   `json:"url,omitempty"`
	Title           string   `json:"title,omitempty"`
	MetaDescription string   `json:"meta_description,omitempty"`
	H1              string   `json:"h1,omitempty"`
	H2Headings      []string `json:"h2_headings,omitempty"`
	H3Headings      []string `json:"h3_headings,omitempty"`
	CanonicalURL    string   `json:"canonical_url,omitempty"`
	Robots          string   `json:"robots,omitempty"`
	JSONLD          string   `json:"json_ld,omitempty"`
	WordCount       int32    `json:"word_count,omitempty"`
	VisibleText     string   `json:"visible_text,omitempty"`
}

func execGetPageContent(ctx context.Context, crawlID pgtype.UUID, userID pgtype.UUID, args pageContentArgs, reader pageReader) (Result, error) {
	row, err := reader.GetCrawlPageByURLForUser(ctx, sqlc.GetCrawlPageByURLForUserParams{
		CrawlID: crawlID,
		Url:     args.URL,
		UserID:  userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return jsonResult(pageContentOutput{Found: false}, "page not found in current crawl")
		}
		return Result{}, err
	}

	var h2Headings []string
	_ = json.Unmarshal(row.H2Headings, &h2Headings)
	var h3Headings []string
	_ = json.Unmarshal(row.H3Headings, &h3Headings)

	output := pageContentOutput{
		Found:           true,
		URL:             row.Url,
		Title:           textValue(row.Title),
		MetaDescription: textValue(row.MetaDescription),
		H1:              textValue(row.H1),
		H2Headings:      h2Headings,
		H3Headings:      h3Headings,
		CanonicalURL:    textValue(row.CanonicalUrl),
		Robots:          textValue(row.Robots),
		JSONLD:          capText(string(row.JsonLd), 2000),
		WordCount:       row.WordCount.Int32,
		VisibleText:     capText(textValue(row.VisibleText), maxPageVisibleTextChars),
	}
	return jsonResult(output, fmt.Sprintf("page content for %s (%d words)", row.Url, row.WordCount.Int32))
}
