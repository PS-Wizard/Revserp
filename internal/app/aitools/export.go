package aitools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ps-wizard/revserp/internal/ai"
)

// ExportAction identifies a frontend export using only server-authorized scope.
type ExportAction struct {
	Kind      string `json:"kind"`
	Format    string `json:"format"`
	ProjectID string `json:"project_id"`
	CrawlID   string `json:"crawl_id"`
}

func exportAuditTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "export_audit",
			Description: "Export the current audit as a PDF.",
			Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
		Execute: func(_ context.Context, args json.RawMessage, s Scope) (Result, error) {
			var parsed struct{}
			if err := strictObject(args, &parsed); err != nil {
				return Result{}, errors.New("arguments must be exactly an empty object")
			}
			return exportResult("audit", "pdf", s)
		},
	}
}

func exportCrawlTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "export_crawl",
			Description: "Export the current crawl score breakdown. Use csv or xlsx only; map Excel, spreadsheet, and misspellings such as xsls or xslx to xlsx.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"format":{"type":"string","enum":["csv","xlsx"]}},"required":["format"],"additionalProperties":false}`),
		},
		Execute: func(_ context.Context, args json.RawMessage, s Scope) (Result, error) {
			var parsed struct {
				Format string `json:"format"`
			}
			if err := strictObject(args, &parsed, "format"); err != nil || (parsed.Format != "csv" && parsed.Format != "xlsx") {
				return Result{}, errors.New("format must be csv or xlsx")
			}
			return exportResult("crawl", parsed.Format, s)
		},
	}
}

func exportResult(kind, format string, s Scope) (Result, error) {
	if !s.ProjectID.Valid || !s.CrawlID.Valid || s.ProjectID.String() == "00000000-0000-0000-0000-000000000000" || s.CrawlID.String() == "00000000-0000-0000-0000-000000000000" {
		return Result{}, errors.New("current project and crawl are required")
	}
	action := &ExportAction{Kind: kind, Format: format, ProjectID: s.ProjectID.String(), CrawlID: s.CrawlID.String()}
	content, _ := json.Marshal(action)
	return Result{Content: string(content), Summary: "export requested", ExportAction: action}, nil
}
