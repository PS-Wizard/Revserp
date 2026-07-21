package aitools

import (
	"context"
	"strings"
	"testing"
)

func TestExportAuditTool(t *testing.T) {
	project, crawl := testUUID(1), testUUID(2)
	result, err := exportAuditTool().Execute(context.Background(), []byte(`{}`), Scope{ProjectID: project, CrawlID: crawl})
	if err != nil || result.ExportAction == nil || *result.ExportAction != (ExportAction{Kind: "audit", Format: "pdf", ProjectID: project.String(), CrawlID: crawl.String()}) {
		t.Fatalf("got %+v, %v", result, err)
	}
	for _, args := range []string{`[]`, `null`, `{`, `{} {}`, `{"x":1}`, `{"project_id":"other"}`, `{"crawl_id":"other"}`, `{"x":1,"x":2}`} {
		if _, err := exportAuditTool().Execute(context.Background(), []byte(args), Scope{ProjectID: project, CrawlID: crawl}); err == nil {
			t.Errorf("expected rejection for %s", args)
		}
	}
	for _, scope := range []Scope{{}, {ProjectID: project}, {ProjectID: project, CrawlID: testUUID(0)}} {
		if _, err := exportAuditTool().Execute(context.Background(), []byte(`{}`), scope); err == nil {
			t.Error("expected invalid scope rejection")
		}
	}
}

func TestExportCrawlTool(t *testing.T) {
	project, crawl := testUUID(1), testUUID(2)
	for _, format := range []string{"csv", "xlsx"} {
		result, err := exportCrawlTool().Execute(context.Background(), []byte(`{"format":"`+format+`"}`), Scope{ProjectID: project, CrawlID: crawl})
		if err != nil || result.ExportAction == nil || result.ExportAction.Kind != "crawl" || result.ExportAction.Format != format || result.ExportAction.ProjectID != project.String() || result.ExportAction.CrawlID != crawl.String() {
			t.Errorf("%s: got %+v, %v", format, result, err)
		}
	}
	for _, args := range []string{`{}`, `[]`, `{`, `{"format":null}`, `{"format":"xls"}`, `{"format":"xsls"}`, `{"format":"xslx"}`, `{"format":"excel"}`, `{"format":"csv","extra":1}`, `{"format":"csv","project_id":"other"}`, `{"format":"csv","crawl_id":"other"}`, `{"format":"csv","filename":"x"}`, `{"format":"csv","url":"https://example.com"}`, `{"format":"csv","format":"xlsx"}`, `{"format":"csv"} {}`} {
		if _, err := exportCrawlTool().Execute(context.Background(), []byte(args), Scope{ProjectID: project, CrawlID: crawl}); err == nil {
			t.Errorf("expected rejection for %s", args)
		}
	}
	if _, err := exportCrawlTool().Execute(context.Background(), []byte(`{"format":"csv"}`), Scope{ProjectID: project}); err == nil {
		t.Error("expected invalid scope rejection")
	}
}

func TestExportSchemas(t *testing.T) {
	registry := NewRegistry(Deps{})
	for _, name := range []string{"export_audit", "export_crawl"} {
		tool, ok := registry.Get(name)
		if !ok || strings.Contains(string(tool.Def.Schema), "id") || strings.Contains(string(tool.Def.Schema), "url") {
			t.Errorf("invalid schema for %s: %s", name, tool.Def.Schema)
		}
	}
}
