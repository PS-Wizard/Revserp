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
	defaultListIssuesLimit = 25
	maxListIssuesLimit     = 50
)

var validPillars = map[string]struct{}{"seo": {}, "aeo": {}, "pagespeed": {}}
var validSeverities = map[string]struct{}{"high": {}, "medium": {}, "low": {}}

// issueLister is the narrow DB port list_issues depends on.
type issueLister interface {
	ListCrawlIssuesFilteredForUser(ctx context.Context, arg sqlc.ListCrawlIssuesFilteredForUserParams) ([]sqlc.ListCrawlIssuesFilteredForUserRow, error)
	CountCrawlIssuesFilteredForUser(ctx context.Context, arg sqlc.CountCrawlIssuesFilteredForUserParams) (int64, error)
}

func listIssuesTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "list_issues",
			Description: "List issue rows found in the current crawl, optionally filtered by pillar, bucket, issue_type, and severity. Returns up to 50 rows (default 25) plus the total matching count.",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pillar": {"type": "string", "enum": ["seo", "aeo", "pagespeed"], "description": "Restrict results to one scoring pillar."},
    "bucket": {"type": "string", "description": "Restrict results to one bucket id within a pillar, e.g. serp_metadata, answerability, server_responsiveness."},
    "issue_type": {"type": "string", "description": "Restrict results to one issue type id, e.g. missing_title."},
    "severity": {"type": "string", "enum": ["high", "medium", "low"], "description": "Restrict results to one severity level."},
    "limit": {"type": "integer", "description": "Max rows to return (default 25, max 50)."}
  },
  "additionalProperties": false
}`),
		},
		Execute: func(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
			parsedArgs, err := parseListIssuesArgs(args)
			if err != nil {
				return jsonResult(errorOutput{Error: err.Error()}, "invalid arguments")
			}
			return execListIssues(ctx, s.CrawlID, s.UserID, parsedArgs, s.Queries)
		},
	}
}

type listIssuesArgs struct {
	Pillar    string `json:"pillar"`
	Bucket    string `json:"bucket"`
	IssueType string `json:"issue_type"`
	Severity  string `json:"severity"`
	Limit     int    `json:"limit"`
}

func parseListIssuesArgs(args json.RawMessage) (listIssuesArgs, error) {
	var parsed listIssuesArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return listIssuesArgs{}, fmt.Errorf("could not parse arguments: %w", err)
		}
	}
	if parsed.Pillar != "" {
		if _, ok := validPillars[parsed.Pillar]; !ok {
			return listIssuesArgs{}, fmt.Errorf("invalid pillar %q, expected one of seo, aeo, pagespeed", parsed.Pillar)
		}
	}
	if parsed.Severity != "" {
		if _, ok := validSeverities[parsed.Severity]; !ok {
			return listIssuesArgs{}, fmt.Errorf("invalid severity %q, expected one of high, medium, low", parsed.Severity)
		}
	}
	return parsed, nil
}

type issueRowOutput struct {
	URL       string `json:"url"`
	Pillar    string `json:"pillar"`
	Bucket    string `json:"bucket"`
	IssueType string `json:"issue_type"`
	Severity  string `json:"severity"`
	Message   string `json:"message"`
	Details   string `json:"details,omitempty"`
}

type listIssuesOutput struct {
	TotalMatching int64            `json:"total_matching"`
	Issues        []issueRowOutput `json:"issues"`
}

type errorOutput struct {
	Error string `json:"error"`
}

func execListIssues(ctx context.Context, crawlID pgtype.UUID, userID pgtype.UUID, args listIssuesArgs, reader issueLister) (Result, error) {
	limit := clampLimit(args.Limit, defaultListIssuesLimit, maxListIssuesLimit)

	total, err := reader.CountCrawlIssuesFilteredForUser(ctx, sqlc.CountCrawlIssuesFilteredForUserParams{
		CrawlID: crawlID,
		UserID:  userID,
		Column3: args.Pillar,
		Column4: args.Bucket,
		Column5: args.IssueType,
		Column6: args.Severity,
	})
	if err != nil {
		return Result{}, err
	}

	rows, err := reader.ListCrawlIssuesFilteredForUser(ctx, sqlc.ListCrawlIssuesFilteredForUserParams{
		CrawlID: crawlID,
		UserID:  userID,
		Column3: args.Pillar,
		Column4: args.Bucket,
		Column5: args.IssueType,
		Column6: args.Severity,
		Column7: "",
		Limit:   limit,
	})
	if err != nil {
		return Result{}, err
	}

	output := listIssuesOutput{TotalMatching: total, Issues: make([]issueRowOutput, 0, len(rows))}
	for _, row := range rows {
		output.Issues = append(output.Issues, issueRowOutput{
			URL:       row.Url,
			Pillar:    row.Pillar,
			Bucket:    row.Bucket,
			IssueType: row.IssueType,
			Severity:  row.Severity,
			Message:   capText(row.Message, 400),
			Details:   capText(row.Details, 400),
		})
	}

	return jsonResult(output, fmt.Sprintf("%d issues (%d matching total)", len(output.Issues), total))
}
