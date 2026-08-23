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
	if cfg.AutoCrawlSchedulerInterval != time.Minute {
		t.Errorf("AutoCrawlSchedulerInterval default: got %s, want 1m", cfg.AutoCrawlSchedulerInterval)
	}
}

func TestLoadAutoCrawlCustom(t *testing.T) {
	for _, k := range []string{
		"AUTO_CRAWL_CONCURRENCY",
		"AUTO_CRAWL_POLL_INTERVAL",
		"AUTO_CRAWL_SCHEDULER_INTERVAL",
		"DATABASE_URL",
	} {
		unsetEnv(t, k)
	}
	setEnv(t, "DATABASE_URL", "postgres://test:test@localhost/test")
	setEnv(t, "AUTO_CRAWL_CONCURRENCY", "3")
	setEnv(t, "AUTO_CRAWL_POLL_INTERVAL", "5s")
	setEnv(t, "AUTO_CRAWL_SCHEDULER_INTERVAL", "10m")

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
}

func TestLoadDBTimeoutDefaults(t *testing.T) {
	for _, k := range []string{"DB_STATEMENT_TIMEOUT", "DB_LOCK_TIMEOUT", "DATABASE_URL"} {
		unsetEnv(t, k)
	}
	setEnv(t, "DATABASE_URL", "postgres://test:test@localhost/test")

	cfg := Load()
	if cfg.DBStatementTimeout != 90*time.Second {
		t.Errorf("DBStatementTimeout default: got %s, want 90s", cfg.DBStatementTimeout)
	}
	if cfg.DBLockTimeout != 10*time.Second {
		t.Errorf("DBLockTimeout default: got %s, want 10s", cfg.DBLockTimeout)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate defaults: unexpected error %v", err)
	}
}

func TestLoadDBTimeoutCustom(t *testing.T) {
	for _, k := range []string{"DB_STATEMENT_TIMEOUT", "DB_LOCK_TIMEOUT", "DATABASE_URL"} {
		unsetEnv(t, k)
	}
	setEnv(t, "DATABASE_URL", "postgres://test:test@localhost/test")
	setEnv(t, "DB_STATEMENT_TIMEOUT", "45s")
	setEnv(t, "DB_LOCK_TIMEOUT", "7s")

	cfg := Load()
	if cfg.DBStatementTimeout != 45*time.Second {
		t.Errorf("DBStatementTimeout: got %s, want 45s", cfg.DBStatementTimeout)
	}
	if cfg.DBLockTimeout != 7*time.Second {
		t.Errorf("DBLockTimeout: got %s, want 7s", cfg.DBLockTimeout)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate custom: unexpected error %v", err)
	}
}

func TestValidateDBTimeoutInvalidDuration(t *testing.T) {
	for _, k := range []string{"DB_STATEMENT_TIMEOUT", "DB_LOCK_TIMEOUT", "DATABASE_URL"} {
		unsetEnv(t, k)
	}
	setEnv(t, "DATABASE_URL", "postgres://test:test@localhost/test")
	setEnv(t, "DB_STATEMENT_TIMEOUT", "bogus")

	cfg := Load()
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate should fail for invalid DB_STATEMENT_TIMEOUT")
	}
}

func TestValidateDBTimeoutZero(t *testing.T) {
	for _, k := range []string{"DB_STATEMENT_TIMEOUT", "DB_LOCK_TIMEOUT", "DATABASE_URL"} {
		unsetEnv(t, k)
	}
	setEnv(t, "DATABASE_URL", "postgres://test:test@localhost/test")
	setEnv(t, "DB_STATEMENT_TIMEOUT", "0s")

	cfg := Load()
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate should fail for zero DB_STATEMENT_TIMEOUT")
	}
}

func TestValidateDBTimeoutNegative(t *testing.T) {
	for _, k := range []string{"DB_STATEMENT_TIMEOUT", "DB_LOCK_TIMEOUT", "DATABASE_URL"} {
		unsetEnv(t, k)
	}
	setEnv(t, "DATABASE_URL", "postgres://test:test@localhost/test")
	setEnv(t, "DB_LOCK_TIMEOUT", "-5s")

	cfg := Load()
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate should fail for negative DB_LOCK_TIMEOUT")
	}
}

func TestValidateDBTimeoutDirectZeroField(t *testing.T) {
	// Direct field validation without env, ensures programmatic misuse is caught.
	for _, k := range []string{"DB_STATEMENT_TIMEOUT", "DB_LOCK_TIMEOUT"} {
		unsetEnv(t, k)
	}
	cfg := Config{
		DatabaseURL:        "postgres://test:test@localhost/test",
		DBStatementTimeout: 0,
		DBLockTimeout:      10 * time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate should fail for zero DBStatementTimeout field")
	}
	cfg = Config{
		DatabaseURL:        "postgres://test:test@localhost/test",
		DBStatementTimeout: 90 * time.Second,
		DBLockTimeout:      -1 * time.Second,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate should fail for negative DBLockTimeout field")
	}
}
