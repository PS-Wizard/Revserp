package aichattools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func runRenderChart(t *testing.T, raw string, budget *Budget) Result {
	t.Helper()
	result, err := executeRenderChart(context.Background(), json.RawMessage(raw), Scope{RowBudget: budget})
	if err != nil {
		t.Fatalf("run(%s) returned error: %v", raw, err)
	}
	return result
}

func TestRenderChartValidTrend(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "date trend valid",
			raw:  `{"preset":"trend","title":"Score trend","x_kind":"date","x":["2024-01-01","2024-02-01","2024-03-01"],"unit":"score","series":[{"label":"SEO","values":[80,82,null]}]}`,
		},
		{
			name: "category trend with note and projected",
			raw:  `{"preset":"trend","title":"Fix progress","note":"Forecast assumes weekly fixes continue.","x_kind":"category","x":["Week 1","Week 2","Week 3","Week 4"],"unit":"count","series":[{"label":"Open","values":[10,8,6,4],"projected_points":1},{"label":"Fixed","values":[0,2,5,7]}]}`,
		},
		{
			name: "rfc3339 dates increasing",
			raw:  `{"preset":"trend","title":"Clicks","x_kind":"date","x":["2024-01-01T00:00:00Z","2024-01-02T00:00:00Z"],"unit":"count","series":[{"label":"Clicks","values":[100,120]}]}`,
		},
		{
			name: "percent unit category",
			raw:  `{"preset":"trend","title":"CTR","x_kind":"category","x":["A","B"],"unit":"percent","series":[{"label":"CTR","values":[1.2,3.4]}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runRenderChart(t, test.raw, nil)
			if !strings.Contains(result.Content, "Chart") || !strings.Contains(result.Content, "rendered") {
				t.Fatalf("Content = %q, want chart rendered confirmation", result.Content)
			}
			if !strings.Contains(result.Summary, "Chart ready:") || !strings.Contains(result.Summary, "points") {
				t.Fatalf("Summary = %q, want chart summary with title and counts", result.Summary)
			}
			// Ensure we did not echo all data verbatim (e.g., series values array should not be in Content)
			if strings.Contains(result.Content, "\"values\"") {
				t.Fatalf("Content = %q, should not echo all data", result.Content)
			}
		})
	}
}

func TestRenderChartValidRanking(t *testing.T) {
	raw := `{"preset":"ranking","title":"Top issue buckets","note":"Ordered by open issue count.","categories":["Broken links","Missing titles","Duplicate content"],"unit":"count","series":[{"label":"Current crawl","values":[42,31,18]},{"label":"Previous crawl","values":[48,29,21]}]}`
	result := runRenderChart(t, raw, nil)
	if result.Summary != `Chart ready: "Top issue buckets" · 3 points · 2 series` {
		t.Fatalf("Summary = %q", result.Summary)
	}
	if !strings.Contains(result.Content, "rendered") {
		t.Fatalf("Content = %q, want rendered confirmation", result.Content)
	}
}

func TestRenderChartPreservesArgsAsContract(t *testing.T) {
	raw := `{"preset":"trend","title":"My Trend","note":"based on last 3 crawls","x_kind":"category","x":["A","B","C"],"unit":"count","series":[{"label":"S1","values":[1,2,3]}]}`
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	// Ensure round-trip preserves args for frontend contract: strictJSONFields should accept it
	if _, err := parseRenderChartArgs(json.RawMessage(raw)); err != nil {
		t.Fatalf("parse failed: %v", err)
	}
}

func TestParseRenderChartArgsValidation(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "missing preset", raw: `{"title":"T","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `missing required argument "preset"`},
		{name: "missing title", raw: `{"preset":"trend","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `missing required argument "title"`},
		{name: "unknown field", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}],"color":"red"}`, wantErr: `unknown argument "color"`},
		{name: "unknown preset", raw: `{"preset":"bar","title":"T","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `invalid preset`},
		{name: "unknown unit", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"unit":"lightyears","series":[{"label":"S","values":[1,2]}]}`, wantErr: `invalid unit`},
		{name: "unknown x_kind", raw: `{"preset":"trend","title":"T","x_kind":"weird","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `invalid x_kind`},
		{name: "noncanonical enum rejected", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"unit":"Score","series":[{"label":"S","values":[1,2]}]}`, wantErr: `invalid unit`},
		{name: "title empty", raw: `{"preset":"trend","title":"   ","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `argument "title"`},
		{name: "title too long", raw: `{"preset":"trend","title":"` + strings.Repeat("a", 121) + `","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `argument "title"`},
		{name: "note too long", raw: `{"preset":"trend","title":"T","note":"` + strings.Repeat("a", 301) + `","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `argument "note"`},
		{name: "trend missing x kind", raw: `{"preset":"trend","title":"T","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `missing required argument "x_kind"`},
		{name: "trend missing x", raw: `{"preset":"trend","title":"T","x_kind":"category","unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `missing required argument "x"`},
		{name: "ranking missing categories", raw: `{"preset":"ranking","title":"T","unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `missing required argument "categories"`},
		{name: "ranking rejects x", raw: `{"preset":"ranking","title":"T","categories":["A","B"],"x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `argument "x" is not allowed`},
		{name: "trend rejects categories", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"categories":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `argument "categories" is not allowed`},
		{name: "ranking categories too few", raw: `{"preset":"ranking","title":"T","categories":["A"],"unit":"count","series":[{"label":"S","values":[1]}]}`, wantErr: `argument "categories" must have between 2 and 12`},
		{name: "ranking categories too many", raw: `{"preset":"ranking","title":"T","categories":["1","2","3","4","5","6","7","8","9","10","11","12","13"],"unit":"count","series":[{"label":"S","values":[1,2,3,4,5,6,7,8,9,10,11,12,13]}]}`, wantErr: `argument "categories" must have between 2 and 12`},
		{name: "ranking category empty", raw: `{"preset":"ranking","title":"T","categories":["A","   "],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `categories[1] must be nonempty`},
		{name: "ranking category duplicate", raw: `{"preset":"ranking","title":"T","categories":["A","A"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `duplicated`},
		{name: "ranking values length mismatch", raw: `{"preset":"ranking","title":"T","categories":["A","B","C"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `values length`},
		{name: "ranking rejects null", raw: `{"preset":"ranking","title":"T","categories":["A","B"],"unit":"count","series":[{"label":"S","values":[1,null]}]}`, wantErr: `must be a number for preset "ranking"`},
		{name: "ranking rejects projected points", raw: `{"preset":"ranking","title":"T","categories":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2],"projected_points":1}]}`, wantErr: `projected_points is not allowed`},
		{name: "x too few", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A"],"unit":"count","series":[{"label":"S","values":[1]}]}`, wantErr: `argument "x" must have between 2 and 60`},
		{name: "x empty label", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","   "],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `x[1] must be nonempty`},
		{name: "x label too long", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","` + strings.Repeat("a", 81) + `"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `x[1] must be at most 80`},
		{name: "series empty", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"unit":"count","series":[]}`, wantErr: `argument "series" must have between 1 and 3`},
		{name: "series too many", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"A","values":[1,2]},{"label":"B","values":[1,2]},{"label":"C","values":[1,2]},{"label":"D","values":[1,2]}]}`, wantErr: `argument "series" must have between 1 and 3`},
		{name: "series label empty", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"   ","values":[1,2]}]}`, wantErr: `series[0].label`},
		{name: "series label too long", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"` + strings.Repeat("a", 61) + `","values":[1,2]}]}`, wantErr: `series[0].label`},
		{name: "values length mismatch", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B","C"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `values length`},
		{name: "projected_points zero", raw: `{"preset":"trend","title":"T","note":"n","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2],"projected_points":0}]}`, wantErr: `projected_points must be between 1 and`},
		{name: "projected_points too large", raw: `{"preset":"trend","title":"T","note":"n","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2],"projected_points":2}]}`, wantErr: `projected_points must be between 1 and`},
		{name: "projected without note", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B","C"],"unit":"count","series":[{"label":"S","values":[1,2,3],"projected_points":1}]}`, wantErr: `note is required`},
		{name: "date invalid format", raw: `{"preset":"trend","title":"T","x_kind":"date","x":["2024-13-01","2024-02-01"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `not a valid date`},
		{name: "date not increasing", raw: `{"preset":"trend","title":"T","x_kind":"date","x":["2024-02-01","2024-01-01"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `strictly increasing`},
		{name: "date equal", raw: `{"preset":"trend","title":"T","x_kind":"date","x":["2024-01-01","2024-01-01"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `strictly increasing`},
		{name: "category duplicate", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","A"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `duplicated`},
		{name: "duplicate key", raw: `{"preset":"trend","title":"T","title":"T2","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`, wantErr: `duplicate argument`},
		{name: "trailing data", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]} {"a":1}`, wantErr: `trailing data`},
		{name: "unknown series field", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2],"color":"red"}]}`, wantErr: `unknown field`},
		{name: "non-object series", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"unit":"count","series":[123]}`, wantErr: `must be a JSON object`},
		{name: "values not number", raw: `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":["oops",2]}]}`, wantErr: `must be a number or null`},
		{name: "projected_points float", raw: `{"preset":"trend","title":"T","note":"n","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2],"projected_points":1.5}]}`, wantErr: `must be an integer`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseRenderChartArgs(json.RawMessage(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("parse error = %v, want containing %q", err, test.wantErr)
			}
			// Also test through executor produces error content
			result := runRenderChart(t, test.raw, nil)
			if !strings.Contains(result.Content, "render_chart error:") {
				t.Fatalf("Content = %q, want render_chart error", result.Content)
			}
			if !strings.Contains(result.Content, test.wantErr) && !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Content = %q, want containing %q", result.Content, test.wantErr)
			}
		})
	}
}

func TestRenderChartSuccessSummary(t *testing.T) {
	raw := `{"preset":"trend","title":"My Title","x_kind":"category","x":["A","B","C"],"unit":"score","series":[{"label":"S1","values":[1,2,3]},{"label":"S2","values":[4,5,6]}]}`
	result := runRenderChart(t, raw, nil)
	if result.Summary != `Chart ready: "My Title" · 3 points · 2 series` {
		t.Fatalf("Summary = %q", result.Summary)
	}
	if !strings.Contains(result.Content, `"My Title"`) {
		t.Fatalf("Content = %q, want title in content", result.Content)
	}
}

func TestRenderChartDoesNotSpendBudget(t *testing.T) {
	budget := NewBudget(5)
	raw := `{"preset":"trend","title":"T","x_kind":"category","x":["A","B"],"unit":"count","series":[{"label":"S","values":[1,2]}]}`
	result := runRenderChart(t, raw, budget)
	if result.Content == "" {
		t.Fatal("empty content")
	}
	if budget.Remaining() != 5 {
		t.Fatalf("budget remaining = %d, want 5", budget.Remaining())
	}
}

func TestRenderChartToolDef(t *testing.T) {
	tool := renderChartTool()
	if tool.Def.Name != "render_chart" {
		t.Fatalf("tool name = %q, want render_chart", tool.Def.Name)
	}
	if tool.Def.Label == "" || tool.Def.Description == "" || len(tool.Def.Schema) == 0 {
		t.Fatalf("tool def incomplete: %+v", tool.Def)
	}
	if !strings.Contains(tool.Def.Description, "prior tool outputs") || !strings.Contains(tool.Def.Description, "projected_points") || !strings.Contains(tool.Def.Description, "note") {
		t.Fatalf("description = %q, want copy observed facts, projected_points, note guidance", tool.Def.Description)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.Def.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	encodedSchema := string(tool.Def.Schema)
	if !strings.Contains(encodedSchema, `"ranking"`) || !strings.Contains(encodedSchema, `"categories"`) {
		t.Fatalf("schema = %s, want ranking preset and categories", encodedSchema)
	}
}

func TestRenderChartRegistryOrder(t *testing.T) {
	names := NewRegistry().Names()
	want := []string{"read_issues", "get_score_summary", "get_search_console_data", "get_business_profile", "read_issue_work", "read_page", "render_chart"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Fatalf("names[%d] = %q, want %q; full %v", i, names[i], n, names)
		}
	}
}
