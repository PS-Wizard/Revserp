package db

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a Postgres connection pool and verifies it.
//
// Session limits are enforced centrally via pgx RuntimeParams
// (statement_timeout / lock_timeout) on every pooled connection. The caller
// (internal/config) owns env parsing, defaults (90s/10s), and validation;
// db is pure and expects validated positive durations.
func Connect(ctx context.Context, databaseURL string, statementTimeout, lockTimeout time.Duration) (*pgxpool.Pool, error) {
	cfg, err := parsePoolConfig(databaseURL, statementTimeout, lockTimeout)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	return pool, nil
}

func parsePoolConfig(databaseURL string, statementTimeout, lockTimeout time.Duration) (*pgxpool.Config, error) {
	if statementTimeout <= 0 {
		return nil, fmt.Errorf("DB_STATEMENT_TIMEOUT must be > 0, got %s", statementTimeout)
	}
	if lockTimeout <= 0 {
		return nil, fmt.Errorf("DB_LOCK_TIMEOUT must be > 0, got %s", lockTimeout)
	}

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}

	if cfg.ConnConfig.Config.RuntimeParams == nil {
		cfg.ConnConfig.Config.RuntimeParams = make(map[string]string)
	}
	cfg.ConnConfig.Config.RuntimeParams["statement_timeout"] = durationToMsString(statementTimeout)
	cfg.ConnConfig.Config.RuntimeParams["lock_timeout"] = durationToMsString(lockTimeout)

	cfg.MaxConns = 25
	cfg.MinConns = 5
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	return cfg, nil
}

func durationToMsString(d time.Duration) string {
	// Ceil to milliseconds so a positive sub-millisecond duration does not
	// truncate to 0 (which disables the timeout in Postgres).
	ms := int64((d + time.Millisecond - 1) / time.Millisecond)
	if ms < 1 && d > 0 {
		ms = 1
	}
	return strconv.FormatInt(ms, 10)
}
