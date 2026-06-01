package crawler

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNormalizeConfigSnapshotAppliesDefaults(t *testing.T) {
	configSnapshot, normalizedConfigSnapshot, err := NormalizeConfigSnapshot(nil)
	if err != nil {
		t.Fatalf("normalize config snapshot: %v", err)
	}

	if configSnapshot.MaxDepth != 2 {
		t.Fatalf("got max depth %d", configSnapshot.MaxDepth)
	}
	if configSnapshot.MaxPages != nil {
		t.Fatalf("expected max pages to be nil")
	}
	if configSnapshot.FetchTimeoutSeconds != 10 {
		t.Fatalf("got fetch timeout seconds %d", configSnapshot.FetchTimeoutSeconds)
	}
	if configSnapshot.EnableJavascript {
		t.Fatalf("expected enable_javascript to default to false")
	}
	if len(normalizedConfigSnapshot) == 0 {
		t.Fatalf("expected normalized config snapshot bytes")
	}
}

func TestNormalizeConfigSnapshotUsesProvidedValues(t *testing.T) {
	configSnapshot, _, err := NormalizeConfigSnapshot([]byte(`{"max_depth":1,"max_pages":25,"fetch_timeout_seconds":7,"enable_javascript":true}`))
	if err != nil {
		t.Fatalf("normalize config snapshot: %v", err)
	}

	if configSnapshot.MaxDepth != 1 {
		t.Fatalf("got max depth %d", configSnapshot.MaxDepth)
	}
	if configSnapshot.MaxPages == nil || *configSnapshot.MaxPages != 25 {
		t.Fatalf("got max pages %#v", configSnapshot.MaxPages)
	}
	if configSnapshot.FetchTimeoutSeconds != 7 {
		t.Fatalf("got fetch timeout seconds %d", configSnapshot.FetchTimeoutSeconds)
	}
	if !configSnapshot.EnableJavascript {
		t.Fatalf("expected enable_javascript to be true")
	}
}

func TestConfigFromBaseURLAndSnapshotBuildsCrawlerConfig(t *testing.T) {
	crawlerConfig, err := ConfigFromBaseURLAndSnapshot("https://example.com", []byte(`{"max_depth":3,"fetch_timeout_seconds":4}`))
	if err != nil {
		t.Fatalf("build crawler config: %v", err)
	}

	if crawlerConfig.AllowedHost != "example.com" {
		t.Fatalf("got allowed host %q", crawlerConfig.AllowedHost)
	}
	if crawlerConfig.MaxDepth != 3 {
		t.Fatalf("got max depth %d", crawlerConfig.MaxDepth)
	}
	if crawlerConfig.MaxPages != 0 {
		t.Fatalf("got max pages %d", crawlerConfig.MaxPages)
	}
	if crawlerConfig.FetchTimeout != 4*time.Second {
		t.Fatalf("got fetch timeout %s", crawlerConfig.FetchTimeout)
	}
}

func TestNormalizeConfigSnapshotMarshalsExpectedJSON(t *testing.T) {
	_, normalizedConfigSnapshot, err := NormalizeConfigSnapshot([]byte(`{"max_pages":12}`))
	if err != nil {
		t.Fatalf("normalize config snapshot: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(normalizedConfigSnapshot, &decoded); err != nil {
		t.Fatalf("unmarshal normalized config snapshot: %v", err)
	}

	if decoded["max_depth"].(float64) != 2 {
		t.Fatalf("got max_depth %v", decoded["max_depth"])
	}
	if decoded["max_pages"].(float64) != 12 {
		t.Fatalf("got max_pages %v", decoded["max_pages"])
	}
	if decoded["fetch_timeout_seconds"].(float64) != 10 {
		t.Fatalf("got fetch_timeout_seconds %v", decoded["fetch_timeout_seconds"])
	}
}
