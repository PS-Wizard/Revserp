package db

import (
	"testing"
	"time"
)

const testDatabaseURL = "postgres://revserp:revserp@localhost:5432/revserp_app?sslmode=disable"

func TestParsePoolConfigSetsRuntimeParams(t *testing.T) {
	cfg, err := parsePoolConfig(testDatabaseURL, 90*time.Second, 10*time.Second)
	if err != nil {
		t.Fatalf("parsePoolConfig: %v", err)
	}
	if got := cfg.ConnConfig.Config.RuntimeParams["statement_timeout"]; got != "90000" {
		t.Errorf("statement_timeout: got %q, want %q", got, "90000")
	}
	if got := cfg.ConnConfig.Config.RuntimeParams["lock_timeout"]; got != "10000" {
		t.Errorf("lock_timeout: got %q, want %q", got, "10000")
	}
}

func TestParsePoolConfigCustom(t *testing.T) {
	cfg, err := parsePoolConfig(testDatabaseURL, 45*time.Second, 7*time.Second)
	if err != nil {
		t.Fatalf("parsePoolConfig: %v", err)
	}
	if got := cfg.ConnConfig.Config.RuntimeParams["statement_timeout"]; got != "45000" {
		t.Errorf("statement_timeout: got %q, want %q", got, "45000")
	}
	if got := cfg.ConnConfig.Config.RuntimeParams["lock_timeout"]; got != "7000" {
		t.Errorf("lock_timeout: got %q, want %q", got, "7000")
	}
}

func TestParsePoolConfigRejectsZero(t *testing.T) {
	if _, err := parsePoolConfig(testDatabaseURL, 0, 10*time.Second); err == nil {
		t.Fatal("expected error for zero statement_timeout, got nil")
	}
	if _, err := parsePoolConfig(testDatabaseURL, 90*time.Second, 0); err == nil {
		t.Fatal("expected error for zero lock_timeout, got nil")
	}
}

func TestParsePoolConfigRejectsNegative(t *testing.T) {
	if _, err := parsePoolConfig(testDatabaseURL, -1*time.Second, 10*time.Second); err == nil {
		t.Fatal("expected error for negative statement_timeout")
	}
	if _, err := parsePoolConfig(testDatabaseURL, 90*time.Second, -5*time.Second); err == nil {
		t.Fatal("expected error for negative lock_timeout")
	}
}

func TestDurationToMsString(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{90 * time.Second, "90000"},
		{10 * time.Second, "10000"},
		{500 * time.Microsecond, "1"}, // sub-millisecond must not be 0
		{1 * time.Millisecond, "1"},
		{1500 * time.Microsecond, "2"}, // 1.5ms ceils to 2
		{0, "0"},
	}
	for _, c := range cases {
		if got := durationToMsString(c.d); got != c.want {
			t.Errorf("durationToMsString(%s): got %q, want %q", c.d, got, c.want)
		}
	}
}

func TestParsePoolConfigOverridesURLParams(t *testing.T) {
	// URL already contains timeouts; our code should enforce explicit values.
	urlWithTimeouts := testDatabaseURL + "&statement_timeout=1&lock_timeout=2"
	cfg, err := parsePoolConfig(urlWithTimeouts, 90*time.Second, 10*time.Second)
	if err != nil {
		t.Fatalf("parsePoolConfig: %v", err)
	}
	if got := cfg.ConnConfig.Config.RuntimeParams["statement_timeout"]; got != "90000" {
		t.Errorf("statement_timeout should override URL: got %q, want %q", got, "90000")
	}
	if got := cfg.ConnConfig.Config.RuntimeParams["lock_timeout"]; got != "10000" {
		t.Errorf("lock_timeout should override URL: got %q, want %q", got, "10000")
	}
}
