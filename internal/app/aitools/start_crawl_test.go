package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestStartCrawlToolDefaultsAndOverrides(t *testing.T) {
	var gotScope Scope
	var gotConfig map[string]any
	tool := startCrawlTool(func(_ context.Context, scope Scope, raw []byte) (CrawlStart, error) {
		gotScope = scope
		if err := json.Unmarshal(raw, &gotConfig); err != nil {
			t.Fatalf("decode config: %v", err)
		}
		return CrawlStart{ID: "crawl-1", Status: "queued"}, nil
	})
	scope := Scope{ProjectID: pgtype.UUID{Valid: true}, UserID: pgtype.UUID{Valid: true}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{}`), scope)
	if err != nil {
		t.Fatalf("execute defaults: %v", err)
	}
	if gotScope.ProjectID != scope.ProjectID || gotScope.UserID != scope.UserID {
		t.Fatal("creator did not receive scoped project and user")
	}
	if gotConfig["max_depth"] != float64(5) || gotConfig["fetch_timeout_seconds"] != float64(10) {
		t.Fatalf("unexpected defaults: %v", gotConfig)
	}
	for _, name := range []string{"max_pages", "request_delay_ms", "request_jitter_ms"} {
		if _, ok := gotConfig[name]; ok {
			t.Errorf("default config unexpectedly contains %s", name)
		}
	}
	if result.CrawlID != "crawl-1" || result.CrawlProjectID != scope.ProjectID.String() || result.Content != `{"id":"crawl-1","status":"queued"}` {
		t.Fatalf("unexpected success result: %+v", result)
	}

	for _, test := range []struct {
		name string
		args string
		key  string
		want float64
	}{
		{"max pages", `{"max_pages":12}`, "max_pages", 12},
		{"delay", `{"delay_ms":250}`, "request_delay_ms", 250},
		{"jitter", `{"jitter_ms":75}`, "request_jitter_ms", 75},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotConfig = nil
			if _, err := tool.Execute(context.Background(), json.RawMessage(test.args), scope); err != nil {
				t.Fatalf("execute override: %v", err)
			}
			if gotConfig[test.key] != test.want {
				t.Fatalf("%s: got %v, want %v", test.key, gotConfig[test.key], test.want)
			}
		})
	}
}

func TestStartCrawlToolRejectsInvalidArguments(t *testing.T) {
	tool := startCrawlTool(func(context.Context, Scope, []byte) (CrawlStart, error) {
		t.Fatal("creator called for invalid arguments")
		return CrawlStart{}, nil
	})
	for _, args := range []string{
		`{"max_pages":0}`,
		`{"delay_ms":-1}`,
		`{"jitter_ms":1.5}`,
		`{"max_pages":"12"}`,
		`{"source":"manual"}`,
		`{"unknown":1}`,
		`[]`,
	} {
		t.Run(args, func(t *testing.T) {
			if _, err := tool.Execute(context.Background(), []byte(args), Scope{}); err == nil {
				t.Fatal("expected invalid argument error")
			}
		})
	}
}

func TestStartCrawlToolPropagatesAuthorizationFailure(t *testing.T) {
	want := errors.New("forbidden")
	tool := startCrawlTool(func(context.Context, Scope, []byte) (CrawlStart, error) {
		return CrawlStart{}, want
	})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`), Scope{}); !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}
