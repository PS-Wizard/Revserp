package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestConfigureAutoCrawlToolParsesAndForwards(t *testing.T) {
	var gotScope Scope
	var gotParams AutoCrawlParams
	tool := configureAutoCrawlTool(func(_ context.Context, scope Scope, params AutoCrawlParams) error {
		gotScope = scope
		gotParams = params
		return nil
	})
	scope := Scope{ProjectID: pgtype.UUID{Valid: true}, UserID: pgtype.UUID{Valid: true}}

	args := `{"enabled":true,"frequency_days":7,"run_at":"04:30","timezone":"America/New_York","max_depth":3,"max_pages":50,"delay_ms":250,"jitter_ms":75}`
	result, err := tool.Execute(context.Background(), json.RawMessage(args), scope)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotScope.ProjectID != scope.ProjectID || gotScope.UserID != scope.UserID {
		t.Fatal("configurer did not receive scoped project and user")
	}
	if !gotParams.Enabled || gotParams.FrequencyDays == nil || *gotParams.FrequencyDays != 7 {
		t.Fatalf("unexpected params: %+v", gotParams)
	}
	if gotParams.RunAt == nil || *gotParams.RunAt != "04:30" || gotParams.Timezone == nil || *gotParams.Timezone != "America/New_York" {
		t.Fatalf("unexpected schedule params: %+v", gotParams)
	}
	var config map[string]any
	if err := json.Unmarshal(gotParams.ConfigSnapshot, &config); err != nil {
		t.Fatalf("decode config snapshot: %v", err)
	}
	if config["max_depth"] != float64(3) || config["max_pages"] != float64(50) ||
		config["request_delay_ms"] != float64(250) || config["request_jitter_ms"] != float64(75) {
		t.Fatalf("unexpected config snapshot: %v", config)
	}
	if result.Summary != "auto-crawl updated" || result.Content != `{"enabled":true}` {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestConfigureAutoCrawlToolDisabledSummaryAndNoConfig(t *testing.T) {
	var gotParams AutoCrawlParams
	tool := configureAutoCrawlTool(func(_ context.Context, _ Scope, params AutoCrawlParams) error {
		gotParams = params
		return nil
	})
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"enabled":false}`), Scope{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotParams.Enabled {
		t.Fatal("expected enabled=false")
	}
	if gotParams.ConfigSnapshot != nil {
		t.Fatalf("expected nil config snapshot when no crawl-config fields set, got %s", gotParams.ConfigSnapshot)
	}
	if result.Summary != "auto-crawl disabled" {
		t.Fatalf("unexpected summary: %q", result.Summary)
	}
}

func TestConfigureAutoCrawlToolRejectsInvalidArguments(t *testing.T) {
	tool := configureAutoCrawlTool(func(context.Context, Scope, AutoCrawlParams) error {
		t.Fatal("configurer called for invalid arguments")
		return nil
	})
	for _, args := range []string{
		`{}`,
		`{"frequency_days":7}`,
		`{"enabled":"yes"}`,
		`{"enabled":true,"frequency_days":0}`,
		`{"enabled":true,"frequency_days":31}`,
		`{"enabled":true,"run_at":""}`,
		`{"enabled":true,"timezone":""}`,
		`{"enabled":true,"max_depth":-1}`,
		`{"enabled":true,"max_pages":0}`,
		`{"enabled":true,"delay_ms":-5}`,
		`{"enabled":true,"jitter_ms":1.5}`,
		`{"enabled":true,"unknown":1}`,
		`{"enabled":true,"project_id":"x"}`,
		`[]`,
	} {
		t.Run(args, func(t *testing.T) {
			if _, err := tool.Execute(context.Background(), []byte(args), Scope{}); err == nil {
				t.Fatal("expected invalid argument error")
			}
		})
	}
}

func TestConfigureAutoCrawlToolUnavailableWhenNil(t *testing.T) {
	tool := configureAutoCrawlTool(nil)
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"enabled":true}`), Scope{}); err == nil {
		t.Fatal("expected unavailable error when configurer is nil")
	}
}

func TestConfigureAutoCrawlToolPropagatesFailure(t *testing.T) {
	want := errors.New("forbidden")
	tool := configureAutoCrawlTool(func(context.Context, Scope, AutoCrawlParams) error {
		return want
	})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"enabled":true}`), Scope{}); !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}
