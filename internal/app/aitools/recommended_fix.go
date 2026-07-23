package aitools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueengine "github.com/ps-wizard/revserp/internal/issues"
)

func recommendedFixTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "get_recommended_fix",
			Description: "Get the deterministic, ground-truth recommended fix for one issue type found in the current crawl, grounded in an actual affected row. Treat the result as ground truth; adapt or explain it rather than inventing a fix from scratch. The recommended fix for an issue type is the same pattern across pages - call this ONCE per issue_type (optionally with a representative url) and apply it to the affected URLs given to you; do not call it once per URL.",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "issue_type": {"type": "string", "description": "The issue type id to get a recommended fix for, e.g. missing_title."},
    "url": {"type": "string", "description": "Optional: restrict to the affected row for this specific URL."}
  },
  "required": ["issue_type"],
  "additionalProperties": false
}`),
		},
		Execute: func(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
			parsedArgs, err := parseRecommendedFixArgs(args)
			if err != nil {
				return jsonResult(errorOutput{Error: err.Error()}, "invalid arguments")
			}
			return execGetRecommendedFix(ctx, s.CrawlID, s.UserID, parsedArgs, s.Queries)
		},
	}
}

type recommendedFixArgs struct {
	IssueType string `json:"issue_type"`
	URL       string `json:"url"`
}

func parseRecommendedFixArgs(args json.RawMessage) (recommendedFixArgs, error) {
	var parsed recommendedFixArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return recommendedFixArgs{}, fmt.Errorf("could not parse arguments: %w", err)
		}
	}
	if parsed.IssueType == "" {
		return recommendedFixArgs{}, fmt.Errorf("issue_type is required")
	}
	return parsed, nil
}

type recommendedFixOutput struct {
	Found          bool   `json:"found"`
	IssueType      string `json:"issue_type,omitempty"`
	Pillar         string `json:"pillar,omitempty"`
	Bucket         string `json:"bucket,omitempty"`
	Severity       string `json:"severity,omitempty"`
	URL            string `json:"url,omitempty"`
	Message        string `json:"message,omitempty"`
	RecommendedFix string `json:"recommended_fix,omitempty"`
}

func execGetRecommendedFix(ctx context.Context, crawlID pgtype.UUID, userID pgtype.UUID, args recommendedFixArgs, reader issueLister) (Result, error) {
	rows, err := reader.ListCrawlIssuesFilteredForUser(ctx, sqlc.ListCrawlIssuesFilteredForUserParams{
		CrawlID: crawlID,
		UserID:  userID,
		Column3: "",
		Column4: "",
		Column5: args.IssueType,
		Column6: "",
		Column7: args.URL,
		Limit:   1,
	})
	if err != nil {
		return Result{}, err
	}
	if len(rows) == 0 {
		return jsonResult(recommendedFixOutput{Found: false}, "no matching issue found in current crawl for that issue_type/url")
	}

	row := rows[0]
	fix := issueengine.RecommendedFix(row.Pillar, row.Bucket, row.IssueType, row.Message, row.Details)
	output := recommendedFixOutput{
		Found:          true,
		IssueType:      row.IssueType,
		Pillar:         row.Pillar,
		Bucket:         row.Bucket,
		Severity:       row.Severity,
		URL:            row.Url,
		Message:        capText(row.Message, 400),
		RecommendedFix: fix,
	}
	return jsonResult(output, "recommended fix for "+row.IssueType)
}
