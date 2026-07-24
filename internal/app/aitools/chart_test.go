package aitools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRenderChartTool_ValidSpec(t *testing.T) {
	tool := renderChartTool()
	args := json.RawMessage(`{"type":"line","title":"Pillar scores","x_key":"pillar","series":[{"key":"score","label":"Score"}],"data":[{"pillar":"SEO","score":82},{"pillar":"AEO","score":74}]}`)

	result, err := tool.Execute(context.Background(), args, Scope{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Chart == nil {
		t.Fatal("expected Chart to be set on the result")
	}
	if result.Chart.Type != "line" || len(result.Chart.Data) != 2 {
		t.Fatalf("unexpected chart: %+v", result.Chart)
	}
	if result.Summary != "Chart: Pillar scores" {
		t.Fatalf("unexpected summary: %q", result.Summary)
	}
	// Content must be the round-trippable spec, since reload rebuilds from it.
	var roundTrip ChartSpec
	if err := json.Unmarshal([]byte(result.Content), &roundTrip); err != nil {
		t.Fatalf("content is not a valid chart spec: %v", err)
	}
}

func TestRenderChartTool_Rejects(t *testing.T) {
	tool := renderChartTool()
	cases := map[string]string{
		"bad type":            `{"type":"scatter","title":"x","x_key":"d","series":[{"key":"v","label":"V"}],"data":[{"d":"a","v":1}]}`,
		"non-numeric series":  `{"type":"bar","title":"x","x_key":"d","series":[{"key":"v","label":"V"}],"data":[{"d":"a","v":"lots"}]}`,
		"missing series field": `{"type":"bar","title":"x","x_key":"d","series":[{"key":"v","label":"V"}],"data":[{"d":"a"}]}`,
		"pie multi-series":     `{"type":"pie","title":"x","x_key":"d","series":[{"key":"a","label":"A"},{"key":"b","label":"B"}],"data":[{"d":"a","a":1,"b":2}]}`,
		"empty data":           `{"type":"line","title":"x","x_key":"d","series":[{"key":"v","label":"V"}],"data":[]}`,
		"series key equals x":  `{"type":"line","title":"x","x_key":"d","series":[{"key":"d","label":"D"}],"data":[{"d":1}]}`,
		"unknown field":        `{"type":"line","title":"x","x_key":"d","series":[{"key":"v","label":"V"}],"data":[{"d":"a","v":1}],"color":"red"}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := tool.Execute(context.Background(), json.RawMessage(args), Scope{}); err == nil {
				t.Fatalf("expected rejection for %s", name)
			}
		})
	}
}
