package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool size and lifetime constants. These values are deliberately conservative:
// they prevent unbounded connection growth while keeping enough headroom for
// typical API + worker workloads on a single Postgres instance.
const (
	poolMaxConns          = 20
	poolMinConns          = 2
	poolMaxConnLifetime   = 1 * time.Hour
	poolMaxConnIdleTime   = 30 * time.Minute
	poolHealthCheckPeriod = 1 * time.Minute
)

// Connect opens a Postgres connection pool and verifies it.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}

	cfg.MaxConns = poolMaxConns
	cfg.MinConns = poolMinConns
	cfg.MaxConnLifetime = poolMaxConnLifetime
	cfg.MaxConnIdleTime = poolMaxConnIdleTime
	cfg.HealthCheckPeriod = poolHealthCheckPeriod

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
