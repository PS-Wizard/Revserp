package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	// maxPageVisibleTextChars is a safety char cap on a returned word window.
	maxPageVisibleTextChars = 6000
	defaultPageWords        = 50
	maxPageWords            = 300
)

// pageReader is the narrow DB port get_page_content depends on.
type pageReader interface {
	GetCrawlPageByURLForUser(ctx context.Context, arg sqlc.GetCrawlPageByURLForUserParams) (sqlc.GetCrawlPageByURLForUserRow, error)
}

func pageContentTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "get_page_content",
			Description: "Get the crawled content for one page URL in the current crawl: title, meta description, H1/H2/H3 headings, canonical URL, robots directive, JSON-LD, and total word count are always returned. The visible body text is returned as a small WINDOW: pass max_words (default 50, max 300) and offset (word index, default 0) to page through it, using has_more/total_words to decide whether to fetch the next window. Use sparingly: prefer list_issues / get_recommended_fix for fixes (their rows already include the affected field values). Only read body text when you truly need surrounding copy, request the smallest window that answers the question, and never bulk-read pages to survey a site.",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {"type": "string", "description": "The exact page URL to fetch, as it appears in the crawl."},
    "max_words": {"type": "integer", "minimum": 1, "description": "Max words of visible body text to return (default 50, max 300)."},
    "offset": {"type": "integer", "minimum": 0, "description": "Word index to start the visible-text window at (default 0). Page forward using has_more."}
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
	URL      string `json:"url"`
	MaxWords int    `json:"max_words"`
	Offset   int    `json:"offset"`
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
	if parsed.MaxWords <= 0 {
		parsed.MaxWords = defaultPageWords
	}
	if parsed.MaxWords > maxPageWords {
		parsed.MaxWords = maxPageWords
	}
	if parsed.Offset < 0 {
		parsed.Offset = 0
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
	TotalWords      int      `json:"total_words,omitempty"`
	WordOffset      int      `json:"word_offset,omitempty"`
	WordsReturned   int      `json:"words_returned,omitempty"`
	HasMore         bool     `json:"has_more,omitempty"`
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

	// Return only a small window of the visible body text so a single read
	// can't flood the context; the model pages through with offset/max_words.
	maxWords := args.MaxWords
	if maxWords <= 0 {
		maxWords = defaultPageWords
	}
	if maxWords > maxPageWords {
		maxWords = maxPageWords
	}
	words := strings.Fields(textValue(row.VisibleText))
	total := len(words)
	offset := args.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + maxWords
	if end > total {
		end = total
	}
	window := strings.Join(words[offset:end], " ")

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
		VisibleText:     capText(window, maxPageVisibleTextChars),
		TotalWords:      total,
		WordOffset:      offset,
		WordsReturned:   end - offset,
		HasMore:         end < total,
	}
	return jsonResult(output, fmt.Sprintf("page content for %s (words %d-%d of %d)", row.Url, offset, end, total))
}
