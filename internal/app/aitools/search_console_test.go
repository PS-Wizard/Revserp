package aitools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func searchConsoleToolFor(t *testing.T, read SearchConsoleReader) Tool {
	t.Helper()
	tool := searchConsoleTool(read)
	if tool.Def.Name != "get_search_console_data" {
		t.Fatalf("tool name = %q", tool.Def.Name)
	}
	return tool
}

func TestSearchConsoleToolRejectsUnknownReport(t *testing.T) {
	called := false
	tool := searchConsoleToolFor(t, func(context.Context, Scope, SearchConsoleQuery) (SearchConsoleReport, error) {
		called = true
		return SearchConsoleReport{}, nil
	})

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"report":"everything"}`), Scope{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if called {
		t.Error("reader ran for an invalid report")
	}
	if !strings.Contains(result.Content, "invalid report") {
		t.Errorf("content = %q, want an invalid report message", result.Content)
	}
}

func TestSearchConsoleToolRequiresReport(t *testing.T) {
	tool := searchConsoleToolFor(t, func(context.Context, Scope, SearchConsoleQuery) (SearchConsoleReport, error) {
		t.Error("reader ran without a report argument")
		return SearchConsoleReport{}, nil
	})

	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`), Scope{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result.Content, "invalid report") {
		t.Errorf("content = %q, want an invalid report message", result.Content)
	}
}

func TestSearchConsoleToolClampsLimitAndSearch(t *testing.T) {
	var got SearchConsoleQuery
	tool := searchConsoleToolFor(t, func(_ context.Context, _ Scope, query SearchConsoleQuery) (SearchConsoleReport, error) {
		got = query
		return SearchConsoleReport{Content: "{}", Summary: "ok"}, nil
	})

	longSearch := strings.Repeat("a", maxSearchConsoleSearch+40)
	args := `{"report":"top_queries","limit":9999,"search":"` + longSearch + `"}`
	if _, err := tool.Execute(context.Background(), json.RawMessage(args), Scope{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if got.Limit != maxSearchConsoleLimit {
		t.Errorf("limit = %d, want %d", got.Limit, maxSearchConsoleLimit)
	}
	if len(got.Search) != maxSearchConsoleSearch {
		t.Errorf("search length = %d, want %d", len(got.Search), maxSearchConsoleSearch)
	}
}

func TestSearchConsoleToolDefaultsLimit(t *testing.T) {
	var got SearchConsoleQuery
	tool := searchConsoleToolFor(t, func(_ context.Context, _ Scope, query SearchConsoleQuery) (SearchConsoleReport, error) {
		got = query
		return SearchConsoleReport{Content: "{}", Summary: "ok"}, nil
	})

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"report":"summary"}`), Scope{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Limit != defaultSearchConsoleLimit {
		t.Errorf("limit = %d, want %d", got.Limit, defaultSearchConsoleLimit)
	}
}

// A project without Search Console must not look like a broken tool: the reason
// comes back as ordinary content so the model tells the user to connect.
func TestSearchConsoleToolReportsUnavailableAsContentNotError(t *testing.T) {
	tool := searchConsoleToolFor(t, func(context.Context, Scope, SearchConsoleQuery) (SearchConsoleReport, error) {
		return SearchConsoleReport{Unavailable: "Google Search Console is not connected for this organization."}, nil
	})

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"report":"summary"}`), Scope{})
	if err != nil {
		t.Fatalf("Execute returned an error for a not-connected project: %v", err)
	}
	if !strings.Contains(result.Content, "not connected") {
		t.Errorf("content = %q, want the not-connected reason", result.Content)
	}
	if result.Summary != "search console not connected" {
		t.Errorf("summary = %q", result.Summary)
	}
}

func TestSearchConsoleToolReportsMissingDependency(t *testing.T) {
	tool := searchConsoleToolFor(t, nil)

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"report":"summary"}`), Scope{}); err == nil {
		t.Error("Execute succeeded with a nil reader, want an unavailable error")
	}
}

func TestSearchConsoleToolPassesReportThrough(t *testing.T) {
	for report := range validSearchConsoleReports {
		t.Run(report, func(t *testing.T) {
			var got SearchConsoleQuery
			tool := searchConsoleToolFor(t, func(_ context.Context, _ Scope, query SearchConsoleQuery) (SearchConsoleReport, error) {
				got = query
				return SearchConsoleReport{Content: `{"ok":true}`, Summary: "ok"}, nil
			})

			result, err := tool.Execute(context.Background(), json.RawMessage(`{"report":"`+report+`","days":90}`), Scope{})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got.Report != report {
				t.Errorf("report = %q, want %q", got.Report, report)
			}
			if got.Days != 90 {
				t.Errorf("days = %d, want 90", got.Days)
			}
			if result.Content != `{"ok":true}` {
				t.Errorf("content = %q", result.Content)
			}
		})
	}
}

// The tool must never accept a tenant identifier; scope is server-injected.
func TestSearchConsoleToolSchemaHasNoTenantArguments(t *testing.T) {
	tool := searchConsoleToolFor(t, func(context.Context, Scope, SearchConsoleQuery) (SearchConsoleReport, error) {
		return SearchConsoleReport{}, nil
	})

	var schema struct {
		Properties           map[string]any `json:"properties"`
		AdditionalProperties bool           `json:"additionalProperties"`
		Required             []string       `json:"required"`
	}
	if err := json.Unmarshal(tool.Def.Schema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}

	for _, forbidden := range []string{"project_id", "crawl_id", "org_id", "user_id", "site_url", "project"} {
		if _, ok := schema.Properties[forbidden]; ok {
			t.Errorf("schema exposes tenant argument %q", forbidden)
		}
	}
	if schema.AdditionalProperties {
		t.Error("schema allows additional properties")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "report" {
		t.Errorf("required = %v, want [report]", schema.Required)
	}
}

// The schema enum and the accepted report set must not drift apart: a report
// the model can see but the tool rejects reads to the model as a missing
// capability, which is how the country/device gap first showed up.
func TestSearchConsoleSchemaEnumMatchesAcceptedReports(t *testing.T) {
	tool := searchConsoleToolFor(t, func(context.Context, Scope, SearchConsoleQuery) (SearchConsoleReport, error) {
		return SearchConsoleReport{Content: "{}", Summary: "ok"}, nil
	})

	var schema struct {
		Properties struct {
			Report struct {
				Enum []string `json:"enum"`
			} `json:"report"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(tool.Def.Schema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}

	enum := schema.Properties.Report.Enum
	if len(enum) != len(validSearchConsoleReports) {
		t.Errorf("schema enum has %d reports, accepted set has %d", len(enum), len(validSearchConsoleReports))
	}
	for _, report := range enum {
		if _, ok := validSearchConsoleReports[report]; !ok {
			t.Errorf("schema advertises report %q that the tool rejects", report)
		}
	}
	for report := range validSearchConsoleReports {
		if !slices.Contains(enum, report) {
			t.Errorf("tool accepts report %q that the schema does not advertise", report)
		}
	}
}

func TestRegistryIncludesSearchConsoleTool(t *testing.T) {
	registry := NewRegistry(Deps{})
	if _, ok := registry.Get("get_search_console_data"); !ok {
		t.Fatal("get_search_console_data is not registered")
	}
}
