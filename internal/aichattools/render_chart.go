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
    "preset": {"type": "string", "enum": ["trend", "ranking"]},
    "title": {"type": "string", "minLength": 1, "maxLength": 120},
    "note": {"type": "string", "maxLength": 300},
    "x_kind": {"type": "string", "enum": ["date", "category"]},
    "x": {"type": "array", "items": {"type": "string", "minLength": 1, "maxLength": 80}, "minItems": 2, "maxItems": 60},
    "categories": {"type": "array", "items": {"type": "string", "minLength": 1, "maxLength": 80}, "minItems": 2, "maxItems": 12},
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
  "required": ["preset", "title", "unit", "series"],
  "additionalProperties": false
}`

func renderChartTool() Tool {
	return Tool{
		Def: Def{
			Name:  renderChartToolName,
			Label: "Render chart",
			Description: "Render a trend or vertical ranking bar chart when data is clearer visually. Use at most two charts per answer. " +
				"Use trend with x_kind and x. Use ranking with 2 to 12 ordered categories. Copy observed labels and values exactly from prior tool outputs. Only trend supports projected_points, and note must explain forecast assumptions.",
			Schema: json.RawMessage(renderChartSchema),
		},
		Execute: executeRenderChart,
	}
}

type renderChartArgs struct {
	Preset     string
	Title      string
	Note       string
	XKind      string
	X          []string
	Categories []string
	Unit       string
	Series     []renderChartSeries
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
	pointCount := len(args.X)
	if args.Preset == "ranking" {
		pointCount = len(args.Categories)
	}
	return Result{
		Content: fmt.Sprintf("Chart %q rendered with %d points across %d series.", args.Title, pointCount, len(args.Series)),
		Summary: fmt.Sprintf("Chart ready: %q · %d points · %d series", args.Title, pointCount, len(args.Series)),
	}, nil
}

func parseRenderChartArgs(raw json.RawMessage) (renderChartArgs, error) {
	var args renderChartArgs
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for _, key := range []string{"preset", "title", "unit", "series"} {
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
			if args.Preset != "trend" && args.Preset != "ranking" {
				return args, fmt.Errorf("invalid preset %q; valid presets: trend, ranking", args.Preset)
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
		case "categories":
			if err := json.Unmarshal(value, &args.Categories); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
			if len(args.Categories) < 2 || len(args.Categories) > 12 {
				return args, fmt.Errorf("argument %q must have between 2 and 12 entries", key)
			}
			for i := range args.Categories {
				args.Categories[i] = strings.TrimSpace(args.Categories[i])
				if args.Categories[i] == "" {
					return args, fmt.Errorf("categories[%d] must be nonempty after trimming", i)
				}
				if utf8.RuneCountInString(args.Categories[i]) > 80 {
					return args, fmt.Errorf("categories[%d] must be at most 80 characters after trimming", i)
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

	var labels []string
	switch args.Preset {
	case "trend":
		for _, key := range []string{"x_kind", "x"} {
			if _, ok := fields[key]; !ok {
				return args, fmt.Errorf("missing required argument %q for preset %q", key, args.Preset)
			}
		}
		if _, ok := fields["categories"]; ok {
			return args, errors.New("argument \"categories\" is not allowed for preset \"trend\"")
		}
		labels = args.X
		if err := validateRenderChartX(args.XKind, args.X); err != nil {
			return args, err
		}
	case "ranking":
		if _, ok := fields["categories"]; !ok {
			return args, fmt.Errorf("missing required argument %q for preset %q", "categories", args.Preset)
		}
		for _, key := range []string{"x_kind", "x"} {
			if _, ok := fields[key]; ok {
				return args, fmt.Errorf("argument %q is not allowed for preset %q", key, args.Preset)
			}
		}
		labels = args.Categories
		if err := validateUniqueChartLabels("categories", args.Categories); err != nil {
			return args, err
		}
	}

	for i, series := range args.Series {
		if len(series.Values) != len(labels) {
			return args, fmt.Errorf("series[%d].values length %d must equal %s length %d", i, len(series.Values), chartLabelField(args.Preset), len(labels))
		}
		if args.Preset == "ranking" {
			if series.ProjectedPoints != nil {
				return args, fmt.Errorf("series[%d].projected_points is not allowed for preset \"ranking\"", i)
			}
			for j, value := range series.Values {
				if value == nil {
					return args, fmt.Errorf("series[%d].values[%d] must be a number for preset \"ranking\"", i, j)
				}
			}
			continue
		}
		if series.ProjectedPoints != nil {
			if *series.ProjectedPoints < 1 || *series.ProjectedPoints > len(labels)-1 {
				return args, fmt.Errorf("series[%d].projected_points must be between 1 and %d", i, len(labels)-1)
			}
			if args.Note == "" {
				return args, errors.New("note is required when any series has projected_points")
			}
		}
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

func chartLabelField(preset string) string {
	if preset == "ranking" {
		return "categories"
	}
	return "x"
}

func validateUniqueChartLabels(field string, values []string) error {
	seen := make(map[string]bool, len(values))
	for i, value := range values {
		if seen[value] {
			return fmt.Errorf("%s[%d] %q is duplicated; labels must be unique", field, i, value)
		}
		seen[value] = true
	}
	return nil
}

func validateRenderChartX(kind string, values []string) error {
	if kind == "category" {
		return validateUniqueChartLabels("x", values)
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
