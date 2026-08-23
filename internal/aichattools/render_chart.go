package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const renderChartToolName = "render_chart"

const renderChartSchema = `{
  "type": "object",
  "properties": {
    "preset": {"type": "string", "enum": ["trend"]},
    "title": {"type": "string", "minLength": 1, "maxLength": 120},
    "note": {"type": "string", "maxLength": 300},
    "x_kind": {"type": "string", "enum": ["date", "category"]},
    "x": {"type": "array", "items": {"type": "string", "minLength": 1, "maxLength": 80}, "minItems": 2, "maxItems": 60},
    "unit": {"type": "string", "enum": ["count", "percent", "score", "milliseconds"]},
    "series": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "label": {"type": "string", "minLength": 1, "maxLength": 60},
          "values": {"type": "array", "items": {"type": ["number", "null"]}, "minItems": 2, "maxItems": 60},
          "projected_points": {"type": "integer", "minimum": 1, "maximum": 59}
        },
        "required": ["label", "values"],
        "additionalProperties": false
      },
      "minItems": 1,
      "maxItems": 3
    }
  },
  "required": ["preset", "title", "x_kind", "x", "unit", "series"],
  "additionalProperties": false
}`

func renderChartTool() Tool {
	return Tool{
		Def: Def{
			Name:  renderChartToolName,
			Label: "Render chart",
			Description: "Render a trend chart when a trend or comparison is clearer visually. Use at most two charts per answer. " +
				"Copy observed x labels and values exactly from prior tool outputs. Forecast values may be modeled, but projected_points must mark the trailing forecast points and note must explain the assumptions.",
			Schema: json.RawMessage(renderChartSchema),
		},
		Execute: executeRenderChart,
	}
}

type renderChartArgs struct {
	Preset string
	Title  string
	Note   string
	XKind  string
	X      []string
	Unit   string
	Series []renderChartSeries
}

type renderChartSeries struct {
	Label           string
	Values          []*float64
	ProjectedPoints *int
}

func executeRenderChart(_ context.Context, raw json.RawMessage, _ Scope) (Result, error) {
	args, err := parseRenderChartArgs(raw)
	if err != nil {
		return Result{Content: renderChartToolName + " error: " + err.Error()}, nil
	}
	return Result{
		Content: fmt.Sprintf("Chart %q rendered with %d points across %d series.", args.Title, len(args.X), len(args.Series)),
		Summary: fmt.Sprintf("Chart ready: %q · %d points · %d series", args.Title, len(args.X), len(args.Series)),
	}, nil
}

func parseRenderChartArgs(raw json.RawMessage) (renderChartArgs, error) {
	var args renderChartArgs
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for _, key := range []string{"preset", "title", "x_kind", "x", "unit", "series"} {
		if _, ok := fields[key]; !ok {
			return args, fmt.Errorf("missing required argument %q", key)
		}
	}

	for key, value := range fields {
		switch key {
		case "preset":
			if err := json.Unmarshal(value, &args.Preset); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			if args.Preset != "trend" {
				return args, fmt.Errorf("invalid preset %q; valid presets: trend", args.Preset)
			}
		case "title":
			if err := json.Unmarshal(value, &args.Title); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.Title = strings.TrimSpace(args.Title)
			if n := utf8.RuneCountInString(args.Title); n < 1 || n > 120 {
				return args, fmt.Errorf("argument %q must be between 1 and 120 characters after trimming", key)
			}
		case "note":
			if err := json.Unmarshal(value, &args.Note); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.Note = strings.TrimSpace(args.Note)
			if utf8.RuneCountInString(args.Note) > 300 {
				return args, fmt.Errorf("argument %q must be at most 300 characters after trimming", key)
			}
		case "x_kind":
			if err := json.Unmarshal(value, &args.XKind); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			if args.XKind != "date" && args.XKind != "category" {
				return args, fmt.Errorf("invalid x_kind %q; valid x_kind: date, category", args.XKind)
			}
		case "x":
			if err := json.Unmarshal(value, &args.X); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
			if len(args.X) < 2 || len(args.X) > 60 {
				return args, fmt.Errorf("argument %q must have between 2 and 60 entries", key)
			}
			for i := range args.X {
				args.X[i] = strings.TrimSpace(args.X[i])
				if args.X[i] == "" {
					return args, fmt.Errorf("x[%d] must be nonempty after trimming", i)
				}
				if utf8.RuneCountInString(args.X[i]) > 80 {
					return args, fmt.Errorf("x[%d] must be at most 80 characters after trimming", i)
				}
			}
		case "unit":
			if err := json.Unmarshal(value, &args.Unit); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			if args.Unit != "count" && args.Unit != "percent" && args.Unit != "score" && args.Unit != "milliseconds" {
				return args, fmt.Errorf("invalid unit %q; valid units: count, percent, score, milliseconds", args.Unit)
			}
		case "series":
			var series []json.RawMessage
			if err := json.Unmarshal(value, &series); err != nil {
				return args, fmt.Errorf("argument %q must be an array", key)
			}
			if len(series) < 1 || len(series) > 3 {
				return args, fmt.Errorf("argument %q must have between 1 and 3 series", key)
			}
			args.Series = make([]renderChartSeries, len(series))
			for i := range series {
				parsed, err := parseRenderChartSeries(series[i], i)
				if err != nil {
					return args, err
				}
				args.Series[i] = parsed
			}
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}

	for i, series := range args.Series {
		if len(series.Values) != len(args.X) {
			return args, fmt.Errorf("series[%d].values length %d must equal x length %d", i, len(series.Values), len(args.X))
		}
		if series.ProjectedPoints != nil {
			if *series.ProjectedPoints < 1 || *series.ProjectedPoints > len(args.X)-1 {
				return args, fmt.Errorf("series[%d].projected_points must be between 1 and %d", i, len(args.X)-1)
			}
			if args.Note == "" {
				return args, errors.New("note is required when any series has projected_points")
			}
		}
	}
	if err := validateRenderChartX(args.XKind, args.X); err != nil {
		return args, err
	}
	return args, nil
}

func parseRenderChartSeries(raw json.RawMessage, index int) (renderChartSeries, error) {
	var series renderChartSeries
	fields, err := strictJSONFields(raw)
	if err != nil {
		if strings.Contains(err.Error(), "must be a JSON object") {
			return series, fmt.Errorf("series[%d] must be a JSON object", index)
		}
		return series, fmt.Errorf("series[%d]: %w", index, err)
	}
	for _, key := range []string{"label", "values"} {
		if _, ok := fields[key]; !ok {
			return series, fmt.Errorf("series[%d] missing required field %q", index, key)
		}
	}
	for key, value := range fields {
		switch key {
		case "label":
			if err := json.Unmarshal(value, &series.Label); err != nil {
				return series, fmt.Errorf("series[%d].label must be a string", index)
			}
			series.Label = strings.TrimSpace(series.Label)
			if n := utf8.RuneCountInString(series.Label); n < 1 || n > 60 {
				return series, fmt.Errorf("series[%d].label must be between 1 and 60 characters after trimming", index)
			}
		case "values":
			var values []json.RawMessage
			if err := json.Unmarshal(value, &values); err != nil {
				return series, fmt.Errorf("series[%d].values must be an array", index)
			}
			series.Values = make([]*float64, len(values))
			for i, value := range values {
				if err := json.Unmarshal(value, &series.Values[i]); err != nil {
					return series, fmt.Errorf("series[%d].values[%d] must be a number or null", index, i)
				}
				if series.Values[i] != nil && (math.IsNaN(*series.Values[i]) || math.IsInf(*series.Values[i], 0)) {
					return series, fmt.Errorf("series[%d].values[%d] must be a finite number", index, i)
				}
			}
		case "projected_points":
			var projected int
			if err := json.Unmarshal(value, &projected); err != nil {
				return series, fmt.Errorf("series[%d].projected_points must be an integer", index)
			}
			series.ProjectedPoints = &projected
		default:
			return series, fmt.Errorf("unknown field %q in series[%d]", key, index)
		}
	}
	return series, nil
}

func validateRenderChartX(kind string, values []string) error {
	if kind == "category" {
		seen := make(map[string]bool, len(values))
		for i, value := range values {
			if seen[value] {
				return fmt.Errorf("x[%d] %q is duplicated; category x labels must be unique", i, value)
			}
			seen[value] = true
		}
		return nil
	}

	var previous time.Time
	for i, value := range values {
		current, ok := parseChartDate(value)
		if !ok {
			return fmt.Errorf("x[%d] %q is not a valid date (expected YYYY-MM-DD or RFC3339)", i, value)
		}
		if i > 0 && !current.After(previous) {
			return fmt.Errorf("x must be strictly increasing dates; x[%d] %q is not after x[%d]", i, value, i-1)
		}
		previous = current
	}
	return nil
}

func parseChartDate(value string) (time.Time, bool) {
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed, true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil
}
