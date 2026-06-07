package app

import (
	"github.com/jackc/pgx/v5/pgxpool"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/gsc"
)

// App holds shared application dependencies.
type App struct {
	Config         config.Config
	DB             *pgxpool.Pool
	Queries        *sqlc.Queries
	AuthVerifier   *internalauth.Verifier
	SupabaseClient *internalauth.SupabaseClient
	SessionManager *internalauth.SessionManager
	GSCService     *gsc.Service
}

// New builds an application with shared dependencies.
func New(cfg config.Config, dbPool *pgxpool.Pool, authVerifier *internalauth.Verifier) *App {
	supabaseClient := internalauth.NewSupabaseClient(cfg.SupabaseJWTIssuer, cfg.SupabaseAnonKey)
	sessionManager := internalauth.NewSessionManager(dbPool, authVerifier, supabaseClient, cfg.SessionCookieName, cfg.SessionTTL, cfg.AppEnv == "production")
	gscService := gsc.NewService(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL, cfg.GoogleTokenEncryptionSecret)

	return &App{
		Config:         cfg,
		DB:             dbPool,
		Queries:        sqlc.New(dbPool),
		AuthVerifier:   authVerifier,
		SupabaseClient: supabaseClient,
		SessionManager: sessionManager,
		GSCService:     gscService,
	}
}
