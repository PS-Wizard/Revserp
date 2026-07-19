package aitools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	defaultListPagesLimit = 25
	maxListPagesLimit     = 100
)

// pageLister is the narrow DB port list_pages depends on.
type pageLister interface {
	ListCrawlPagesFilteredForUser(ctx context.Context, arg sqlc.ListCrawlPagesFilteredForUserParams) ([]sqlc.ListCrawlPagesFilteredForUserRow, error)
	CountCrawlPagesFilteredForUser(ctx context.Context, arg sqlc.CountCrawlPagesFilteredForUserParams) (int64, error)
}

func listPagesTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "list_pages",
			Description: "List pages crawled in the current crawl, optionally filtered by a substring match on the URL. Returns url, title, and word count for up to 100 pages (default 25) plus the total matching count.",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "filter": {"type": "string", "description": "Substring to match against page URLs (case-insensitive)."},
    "limit": {"type": "integer", "description": "Max rows to return (default 25, max 100)."}
  },
  "additionalProperties": false
}`),
		},
		Execute: func(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
			parsedArgs, err := parseListPagesArgs(args)
			if err != nil {
				return jsonResult(errorOutput{Error: err.Error()}, "invalid arguments")
			}
			return execListPages(ctx, s.CrawlID, s.UserID, parsedArgs, s.Queries)
		},
	}
}

type listPagesArgs struct {
	Filter string `json:"filter"`
	Limit  int    `json:"limit"`
}

func parseListPagesArgs(args json.RawMessage) (listPagesArgs, error) {
	var parsed listPagesArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return listPagesArgs{}, fmt.Errorf("could not parse arguments: %w", err)
		}
	}
	return parsed, nil
}

type pageRowOutput struct {
	URL       string `json:"url"`
	Title     string `json:"title,omitempty"`
	WordCount int32  `json:"word_count,omitempty"`
}

type listPagesOutput struct {
	TotalMatching int64           `json:"total_matching"`
	Pages         []pageRowOutput `json:"pages"`
}

func execListPages(ctx context.Context, crawlID pgtype.UUID, userID pgtype.UUID, args listPagesArgs, reader pageLister) (Result, error) {
	limit := clampLimit(args.Limit, defaultListPagesLimit, maxListPagesLimit)

	total, err := reader.CountCrawlPagesFilteredForUser(ctx, sqlc.CountCrawlPagesFilteredForUserParams{
		CrawlID: crawlID,
		UserID:  userID,
		Column3: args.Filter,
	})
	if err != nil {
		return Result{}, err
	}

	rows, err := reader.ListCrawlPagesFilteredForUser(ctx, sqlc.ListCrawlPagesFilteredForUserParams{
		CrawlID: crawlID,
		UserID:  userID,
		Column3: args.Filter,
		Limit:   limit,
	})
	if err != nil {
		return Result{}, err
	}

	output := listPagesOutput{TotalMatching: total, Pages: make([]pageRowOutput, 0, len(rows))}
	for _, row := range rows {
		output.Pages = append(output.Pages, pageRowOutput{
			URL:       row.Url,
			Title:     textValue(row.Title),
			WordCount: row.WordCount.Int32,
		})
	}

	return jsonResult(output, fmt.Sprintf("%d pages (%d matching total)", len(output.Pages), total))
}
