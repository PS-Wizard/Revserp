package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ps-wizard/revserp/internal/ai"
)

const (
	defaultSearchConsoleLimit = 25
	maxSearchConsoleLimit     = 100
	maxSearchConsoleSearch    = 100
)

var validSearchConsoleReports = map[string]struct{}{
	"summary":          {},
	"top_queries":      {},
	"question_queries": {},
	"top_pages":        {},
	"countries":        {},
	"devices":          {},
	"opportunities":    {},
}

// SearchConsoleReader is the application-owned, authorized Search Console read
// path. It resolves the organization's Google connection and the project's
// selected property from Scope, so no tool argument ever names a site, a
// project, or a tenant.
type SearchConsoleReader func(context.Context, Scope, SearchConsoleQuery) (SearchConsoleReport, error)

// SearchConsoleQuery is one validated report request from the model.
type SearchConsoleQuery struct {
	Report string
	Days   int
	Search string
	Limit  int
}

// SearchConsoleReport is one report's output. Unavailable carries the reason
// Search Console could not be read for this project — no organization
// connection, no property selected, or a connection needing reauth. Those are
// ordinary states of a working system, not tool failures, so they come back as
// content the model can act on rather than as an error.
type SearchConsoleReport struct {
	Unavailable string
	Content     string
	Summary     string
}

func searchConsoleTool(read SearchConsoleReader) Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "get_search_console_data",
			Description: "Read Google Search Console performance for the current project: headline clicks/impressions/CTR/position, the real queries people find the site through, question-style queries, top landing pages, country and device breakdowns, and ranking opportunities. This is actual search demand, not crawl data — use it for keyword research, query intent, audience geography, mobile-vs-desktop questions, and traffic questions. Returns a plain explanation when Search Console is not connected.",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "report": {"type": "string", "enum": ["summary", "top_queries", "question_queries", "top_pages", "countries", "devices", "opportunities"], "description": "summary: headline metrics vs the previous period. top_queries: highest-traffic search queries. question_queries: queries phrased as questions or comparisons, for answer-intent and content-gap work. top_pages: highest-traffic landing pages. countries: traffic split by country (top 25). devices: traffic split by desktop, mobile, and tablet. opportunities: low-CTR and striking-distance queries worth optimizing."},
    "days": {"type": "integer", "minimum": 7, "maximum": 480, "description": "Size of the reporting window in days (default 180). Applies to top_queries and question_queries only; every other report covers the standard window and states its own start_date and end_date. Search Console data lags roughly 3 days."},
    "search": {"type": "string", "description": "Case-insensitive substring filter on the query text. Only applies to top_queries and question_queries."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 100, "description": "Max rows to return (default 25, max 100)."}
  },
  "required": ["report"],
  "additionalProperties": false
}`),
		},
		Execute: func(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
			parsed, err := parseSearchConsoleArgs(args)
			if err != nil {
				return jsonResult(errorOutput{Error: err.Error()}, "invalid arguments")
			}
			if read == nil {
				return Result{}, errors.New("search console access is unavailable")
			}

			report, err := read(ctx, s, parsed)
			if err != nil {
				return Result{}, err
			}
			if report.Unavailable != "" {
				return jsonResult(errorOutput{Error: report.Unavailable}, "search console not connected")
			}
			return Result{Content: report.Content, Summary: report.Summary}, nil
		},
	}
}

type searchConsoleArgs struct {
	Report string `json:"report"`
	Days   int    `json:"days"`
	Search string `json:"search"`
	Limit  int    `json:"limit"`
}

func parseSearchConsoleArgs(args json.RawMessage) (SearchConsoleQuery, error) {
	var parsed searchConsoleArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return SearchConsoleQuery{}, fmt.Errorf("could not parse arguments: %w", err)
		}
	}
	if _, ok := validSearchConsoleReports[parsed.Report]; !ok {
		return SearchConsoleQuery{}, fmt.Errorf("invalid report %q, expected one of summary, top_queries, question_queries, top_pages, opportunities", parsed.Report)
	}
	if len(parsed.Search) > maxSearchConsoleSearch {
		parsed.Search = parsed.Search[:maxSearchConsoleSearch]
	}

	return SearchConsoleQuery{
		Report: parsed.Report,
		Days:   parsed.Days,
		Search: parsed.Search,
		Limit:  int(clampLimit(parsed.Limit, defaultSearchConsoleLimit, maxSearchConsoleLimit)),
	}, nil
}
