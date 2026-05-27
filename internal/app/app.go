package app

import (
	"github.com/jackc/pgx/v5/pgxpool"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// App holds shared application dependencies.
type App struct {
	Config       config.Config
	DB           *pgxpool.Pool
	Queries      *sqlc.Queries
	AuthVerifier *internalauth.Verifier
}

// New builds an application with shared dependencies.
func New(cfg config.Config, dbPool *pgxpool.Pool, authVerifier *internalauth.Verifier) *App {
	return &App{
		Config:       cfg,
		DB:           dbPool,
		Queries:      sqlc.New(dbPool),
		AuthVerifier: authVerifier,
	}
}
