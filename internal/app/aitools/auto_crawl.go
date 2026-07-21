package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ps-wizard/revserp/internal/ai"
)

// AutoCrawlParams is the model-supplied auto-crawl configuration for the
// current project, already validated for shape by the tool. ConfigSnapshot is
// nil when the model set no crawl-config fields, which preserves the stored
// config; otherwise it is a JSON object the app layer normalizes.
type AutoCrawlParams struct {
	Enabled        bool
	FrequencyDays  *int
	RunAt          *string
	Timezone       *string
	ConfigSnapshot []byte
}

// AutoCrawlConfigurer is the application-owned, authorized auto-crawl update
// path. It resolves project ownership, validates the schedule, computes the
// next run, and persists the settings.
type AutoCrawlConfigurer func(context.Context, Scope, AutoCrawlParams) error

// autoCrawlConfigSnapshot is the crawl config the agent may set for scheduled
// runs. Field names match the persisted crawl config snapshot so the app layer
// can normalize it directly.
type autoCrawlConfigSnapshot struct {
	MaxDepth        *int `json:"max_depth,omitempty"`
	MaxPages        *int `json:"max_pages,omitempty"`
	RequestDelayMs  *int `json:"request_delay_ms,omitempty"`
	RequestJitterMs *int `json:"request_jitter_ms,omitempty"`
}

func configureAutoCrawlTool(configure AutoCrawlConfigurer) Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "configure_auto_crawl",
			Description: "Enable, disable, or configure the current project's scheduled auto-crawl. enabled is required. Optional frequency_days (1-30), run_at (\"HH:MM\" 24h), timezone (IANA name like \"America/New_York\"; defaults to the user's local timezone if omitted), and crawl config max_depth, max_pages, delay_ms, jitter_ms for the scheduled runs.",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "enabled": {"type": "boolean"},
    "frequency_days": {"type": "integer", "minimum": 1, "maximum": 30},
    "run_at": {"type": "string"},
    "timezone": {"type": "string"},
    "max_depth": {"type": "integer", "minimum": 0},
    "max_pages": {"type": "integer", "minimum": 1},
    "delay_ms": {"type": "integer", "minimum": 1},
    "jitter_ms": {"type": "integer", "minimum": 1}
  },
  "required": ["enabled"],
  "additionalProperties": false
}`),
		},
		Execute: func(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
			params, err := parseConfigureAutoCrawlArgs(args)
			if err != nil {
				return Result{}, err
			}
			// Default to the caller's timezone when the model didn't specify one,
			// so scheduled runs use the user's local time rather than UTC.
			if params.Timezone == nil && s.Timezone != "" {
				tz := s.Timezone
				params.Timezone = &tz
			}
			if configure == nil {
				return Result{}, errors.New("auto-crawl configuration is unavailable")
			}
			if err := configure(ctx, s, params); err != nil {
				return Result{}, err
			}
			summary := "auto-crawl updated"
			if !params.Enabled {
				summary = "auto-crawl disabled"
			}
			content, err := json.Marshal(map[string]bool{"enabled": params.Enabled})
			if err != nil {
				return Result{}, err
			}
			return Result{Content: string(content), Summary: summary}, nil
		},
	}
}

func parseConfigureAutoCrawlArgs(raw json.RawMessage) (AutoCrawlParams, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return AutoCrawlParams{}, errors.New("arguments must be an object with a boolean \"enabled\" field")
	}

	var params AutoCrawlParams
	var config autoCrawlConfigSnapshot
	hasConfig := false
	enabledSet := false

	for name, value := range fields {
		switch name {
		case "enabled":
			var enabled bool
			if err := json.Unmarshal(value, &enabled); err != nil {
				return AutoCrawlParams{}, errors.New("enabled must be a boolean")
			}
			params.Enabled = enabled
			enabledSet = true
		case "frequency_days":
			var days int
			if err := json.Unmarshal(value, &days); err != nil || days < 1 || days > 30 {
				return AutoCrawlParams{}, errors.New("frequency_days must be an integer between 1 and 30")
			}
			params.FrequencyDays = &days
		case "run_at":
			text, err := parseAutoCrawlString(value, name)
			if err != nil {
				return AutoCrawlParams{}, err
			}
			params.RunAt = &text
		case "timezone":
			text, err := parseAutoCrawlString(value, name)
			if err != nil {
				return AutoCrawlParams{}, err
			}
			params.Timezone = &text
		case "max_depth":
			var depth int
			if err := json.Unmarshal(value, &depth); err != nil || depth < 0 {
				return AutoCrawlParams{}, errors.New("max_depth must be a non-negative integer")
			}
			config.MaxDepth = &depth
			hasConfig = true
		case "max_pages":
			number, err := parseAutoCrawlPositiveInt(value, name)
			if err != nil {
				return AutoCrawlParams{}, err
			}
			config.MaxPages = &number
			hasConfig = true
		case "delay_ms":
			number, err := parseAutoCrawlPositiveInt(value, name)
			if err != nil {
				return AutoCrawlParams{}, err
			}
			config.RequestDelayMs = &number
			hasConfig = true
		case "jitter_ms":
			number, err := parseAutoCrawlPositiveInt(value, name)
			if err != nil {
				return AutoCrawlParams{}, err
			}
			config.RequestJitterMs = &number
			hasConfig = true
		default:
			return AutoCrawlParams{}, fmt.Errorf("unknown argument %q", name)
		}
	}

	if !enabledSet {
		return AutoCrawlParams{}, errors.New("enabled is required")
	}
	if hasConfig {
		encoded, err := json.Marshal(config)
		if err != nil {
			return AutoCrawlParams{}, err
		}
		params.ConfigSnapshot = encoded
	}
	return params, nil
}

func parseAutoCrawlString(value json.RawMessage, name string) (string, error) {
	var text string
	if err := json.Unmarshal(value, &text); err != nil || text == "" {
		return "", fmt.Errorf("%s must be a non-empty string", name)
	}
	return text, nil
}

func parseAutoCrawlPositiveInt(value json.RawMessage, name string) (int, error) {
	var number int
	if err := json.Unmarshal(value, &number); err != nil || number <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return number, nil
}
