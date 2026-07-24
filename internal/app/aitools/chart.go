package aitools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ps-wizard/revserp/internal/ai"
)

// Chart rendering bounds. Kept tight: a chat chart is a quick visual, not a
// full dashboard, and the whole spec is echoed back to the model as the tool
// result, so it must stay small.
const (
	maxChartTitleLength = 120
	maxChartSeries      = 6
	maxChartDataPoints  = 400
	maxChartKeyLength   = 60
)

var chartTypes = map[string]bool{"line": true, "bar": true, "area": true, "pie": true}

// ChartSeries is one plotted measure: Key names the numeric field in each data
// row, Label is its human-facing name in the legend.
type ChartSeries struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// ChartSpec is a self-contained, render-ready chart the agent asks the client
// to draw. Data is inline: every value is expected to come from a prior tool
// result rather than the model's imagination (enforced by prompt, not code).
type ChartSpec struct {
	Type   string           `json:"type"`
	Title  string           `json:"title"`
	XKey   string           `json:"x_key"`
	Series []ChartSeries    `json:"series"`
	Data   []map[string]any `json:"data"`
}

func renderChartTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "render_chart",
			Description: "Render a chart in the chat to visualize data you have ALREADY obtained from other tools (e.g. score breakdowns from get_score_summary, issue counts from list_issues, Search Console metrics). Every data value must come from a real tool result — never invent, estimate, or extrapolate numbers. Prefer a chart when a trend, comparison, or breakdown is easier to see visually than in prose. x_key is the field name in each data row used for the x-axis (or slice label for pie); each series key names a numeric field in the same rows.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"type":{"type":"string","enum":["line","bar","area","pie"]},"title":{"type":"string","description":"Short chart title"},"x_key":{"type":"string","description":"Field name in each data row for the x-axis category or pie slice label"},"series":{"type":"array","minItems":1,"maxItems":6,"items":{"type":"object","properties":{"key":{"type":"string","description":"Field name of a numeric value in each data row"},"label":{"type":"string","description":"Legend label for this series"}},"required":["key","label"],"additionalProperties":false}},"data":{"type":"array","minItems":1,"description":"Array of row objects, each with the x_key field plus one numeric field per series key","items":{"type":"object"}}},"required":["type","title","x_key","series","data"],"additionalProperties":false}`),
		},
		Execute: func(_ context.Context, args json.RawMessage, _ Scope) (Result, error) {
			// Strict at the struct level (rejects unexpected top-level/series
			// fields); data row keys stay flexible since they land in maps.
			decoder := json.NewDecoder(bytes.NewReader(args))
			decoder.DisallowUnknownFields()
			var spec ChartSpec
			if err := decoder.Decode(&spec); err != nil {
				return Result{}, errors.New("chart arguments must be a valid chart object")
			}
			if err := validateChartSpec(&spec); err != nil {
				return Result{}, err
			}
			encoded, err := json.Marshal(spec)
			if err != nil {
				return Result{}, err
			}
			return Result{Content: string(encoded), Summary: "Chart: " + spec.Title, Chart: &spec}, nil
		},
	}
}

func validateChartSpec(spec *ChartSpec) error {
	spec.Title = strings.TrimSpace(spec.Title)
	spec.XKey = strings.TrimSpace(spec.XKey)
	if !chartTypes[spec.Type] {
		return errors.New("chart type must be one of line, bar, area, pie")
	}
	if spec.Title == "" || len(spec.Title) > maxChartTitleLength {
		return fmt.Errorf("chart title must be nonempty and at most %d characters", maxChartTitleLength)
	}
	if spec.XKey == "" || len(spec.XKey) > maxChartKeyLength {
		return errors.New("chart x_key must be a nonempty field name")
	}
	if len(spec.Series) == 0 || len(spec.Series) > maxChartSeries {
		return fmt.Errorf("chart must have between 1 and %d series", maxChartSeries)
	}
	if spec.Type == "pie" && len(spec.Series) != 1 {
		return errors.New("a pie chart must have exactly one series")
	}
	seenSeries := make(map[string]struct{}, len(spec.Series))
	for i := range spec.Series {
		spec.Series[i].Key = strings.TrimSpace(spec.Series[i].Key)
		spec.Series[i].Label = strings.TrimSpace(spec.Series[i].Label)
		key := spec.Series[i].Key
		if key == "" || len(key) > maxChartKeyLength || spec.Series[i].Label == "" {
			return errors.New("each chart series needs a nonempty key and label")
		}
		if key == spec.XKey {
			return errors.New("a series key must differ from x_key")
		}
		if _, dup := seenSeries[key]; dup {
			return errors.New("chart series keys must be unique")
		}
		seenSeries[key] = struct{}{}
	}
	if len(spec.Data) == 0 || len(spec.Data) > maxChartDataPoints {
		return fmt.Errorf("chart data must have between 1 and %d points", maxChartDataPoints)
	}
	for _, row := range spec.Data {
		if _, ok := row[spec.XKey]; !ok {
			return fmt.Errorf("every data row must include the x_key field %q", spec.XKey)
		}
		for _, series := range spec.Series {
			value, ok := row[series.Key]
			if !ok {
				return fmt.Errorf("every data row must include the series field %q", series.Key)
			}
			if _, isNumber := value.(float64); !isNumber {
				return fmt.Errorf("series field %q must be numeric in every data row", series.Key)
			}
		}
	}
	return nil
}
