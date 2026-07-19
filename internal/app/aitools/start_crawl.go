package aitools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ps-wizard/revserp/internal/ai"
)

// CrawlStart is the typed metadata returned when a crawl is queued.
type CrawlStart struct {
	ID     string
	Status string
}

// CrawlCreator is the application-owned, authorized crawl creation path.
type CrawlCreator func(context.Context, Scope, []byte) (CrawlStart, error)

type startCrawlArgs struct {
	MaxPages *int
	DelayMs  *int
	JitterMs *int
}

type startCrawlConfig struct {
	MaxDepth            int  `json:"max_depth"`
	MaxPages            *int `json:"max_pages,omitempty"`
	FetchTimeoutSeconds int  `json:"fetch_timeout_seconds"`
	RequestDelayMs      *int `json:"request_delay_ms,omitempty"`
	RequestJitterMs     *int `json:"request_jitter_ms,omitempty"`
}

func startCrawlTool(create CrawlCreator) Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "start_crawl",
			Description: "Start a crawl for the current project. Optional positive integer max_pages, delay_ms, and jitter_ms arguments are in milliseconds where applicable.",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "max_pages": {"type": "integer", "minimum": 1},
    "delay_ms": {"type": "integer", "minimum": 1},
    "jitter_ms": {"type": "integer", "minimum": 1}
  },
  "additionalProperties": false
}`),
		},
		Execute: func(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
			parsed, err := parseStartCrawlArgs(args)
			if err != nil {
				return Result{}, err
			}
			if create == nil {
				return Result{}, errors.New("crawl creation is unavailable")
			}
			config, err := json.Marshal(startCrawlConfig{
				MaxDepth:            5,
				MaxPages:            parsed.MaxPages,
				FetchTimeoutSeconds: 10,
				RequestDelayMs:      parsed.DelayMs,
				RequestJitterMs:     parsed.JitterMs,
			})
			if err != nil {
				return Result{}, err
			}
			crawl, err := create(ctx, s, config)
			if err != nil {
				return Result{}, err
			}
			content, err := json.Marshal(map[string]string{"id": crawl.ID, "status": crawl.Status})
			if err != nil {
				return Result{}, err
			}
			return Result{Content: string(content), Summary: "crawl started", CrawlID: crawl.ID, CrawlProjectID: s.ProjectID.String()}, nil
		},
	}
}

func parseStartCrawlArgs(raw json.RawMessage) (startCrawlArgs, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return startCrawlArgs{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return startCrawlArgs{}, fmt.Errorf("arguments must be an object with positive integer fields")
	}
	var parsed startCrawlArgs
	for name, value := range fields {
		if name != "max_pages" && name != "delay_ms" && name != "jitter_ms" {
			return startCrawlArgs{}, fmt.Errorf("unknown argument %q", name)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return startCrawlArgs{}, fmt.Errorf("%s must be a positive integer", name)
		}
		var number int
		if err := json.Unmarshal(value, &number); err != nil || number <= 0 {
			return startCrawlArgs{}, fmt.Errorf("%s must be a positive integer", name)
		}
		switch name {
		case "max_pages":
			parsed.MaxPages = &number
		case "delay_ms":
			parsed.DelayMs = &number
		case "jitter_ms":
			parsed.JitterMs = &number
		}
	}
	return parsed, nil
}
