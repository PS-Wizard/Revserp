package config

import (
	"os"
	"testing"
	"time"
)

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("setenv %s: %v", key, err)
	}
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetenv %s: %v", key, err)
	}
}

func TestLoadAutoCrawlDefaults(t *testing.T) {
	// Ensure we start with a clean slate for our keys.
	for _, k := range []string{
		"AUTO_CRAWL_CONCURRENCY",
		"AUTO_CRAWL_POLL_INTERVAL",
		"AUTO_CRAWL_SCHEDULER_INTERVAL",
		"AUTO_CRAWL_INTERVAL",
		"DATABASE_URL",
	} {
		unsetEnv(t, k)
	}
	setEnv(t, "DATABASE_URL", "postgres://test:test@localhost/test")

	cfg := Load()

	if cfg.AutoCrawlConcurrency != 1 {
		t.Errorf("AutoCrawlConcurrency default: got %d, want 1", cfg.AutoCrawlConcurrency)
	}
	if cfg.AutoCrawlPollInterval != 2*time.Second {
		t.Errorf("AutoCrawlPollInterval default: got %s, want 2s", cfg.AutoCrawlPollInterval)
	}
	if cfg.AutoCrawlSchedulerInterval != 5*time.Minute {
		t.Errorf("AutoCrawlSchedulerInterval default: got %s, want 5m", cfg.AutoCrawlSchedulerInterval)
	}
	if cfg.AutoCrawlInterval != 24*time.Hour {
		t.Errorf("AutoCrawlInterval default: got %s, want 24h", cfg.AutoCrawlInterval)
	}
}

func TestLoadAutoCrawlCustom(t *testing.T) {
	for _, k := range []string{
		"AUTO_CRAWL_CONCURRENCY",
		"AUTO_CRAWL_POLL_INTERVAL",
		"AUTO_CRAWL_SCHEDULER_INTERVAL",
		"AUTO_CRAWL_INTERVAL",
		"DATABASE_URL",
	} {
		unsetEnv(t, k)
	}
	setEnv(t, "DATABASE_URL", "postgres://test:test@localhost/test")
	setEnv(t, "AUTO_CRAWL_CONCURRENCY", "3")
	setEnv(t, "AUTO_CRAWL_POLL_INTERVAL", "5s")
	setEnv(t, "AUTO_CRAWL_SCHEDULER_INTERVAL", "10m")
	setEnv(t, "AUTO_CRAWL_INTERVAL", "12h")

	cfg := Load()

	if cfg.AutoCrawlConcurrency != 3 {
		t.Errorf("AutoCrawlConcurrency: got %d, want 3", cfg.AutoCrawlConcurrency)
	}
	if cfg.AutoCrawlPollInterval != 5*time.Second {
		t.Errorf("AutoCrawlPollInterval: got %s, want 5s", cfg.AutoCrawlPollInterval)
	}
	if cfg.AutoCrawlSchedulerInterval != 10*time.Minute {
		t.Errorf("AutoCrawlSchedulerInterval: got %s, want 10m", cfg.AutoCrawlSchedulerInterval)
	}
	if cfg.AutoCrawlInterval != 12*time.Hour {
		t.Errorf("AutoCrawlInterval: got %s, want 12h", cfg.AutoCrawlInterval)
	}
}
