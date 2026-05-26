package app

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// App holds shared application dependencies.
type App struct {
	Config  config.Config
	DB      *pgxpool.Pool
	Queries *sqlc.Queries
}

// New builds an application with shared dependencies.
func New(cfg config.Config, dbPool *pgxpool.Pool) *App {
	return &App{
		Config:  cfg,
		DB:      dbPool,
		Queries: sqlc.New(dbPool),
	}
}
