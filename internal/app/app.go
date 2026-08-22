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
	APIKeyManager  *internalauth.APIKeyManager
	GSCService     *gsc.Service
}

// New builds an application with shared dependencies.
func New(cfg config.Config, dbPool *pgxpool.Pool, authVerifier *internalauth.Verifier) *App {
	queries := sqlc.New(dbPool)
	supabaseClient := internalauth.NewSupabaseClient(cfg.SupabaseJWTIssuer, cfg.SupabaseAnonKey)
	sessionManager := internalauth.NewSessionManager(
		dbPool,
		authVerifier,
		supabaseClient,
		cfg.SessionCookieName,
		cfg.SessionCookieDomain,
		cfg.SessionTTL,
		cfg.AppEnv == "production",
	)

	return &App{
		Config:         cfg,
		DB:             dbPool,
		Queries:        queries,
		AuthVerifier:   authVerifier,
		SupabaseClient: supabaseClient,
		SessionManager: sessionManager,
		APIKeyManager:  internalauth.NewAPIKeyManager(queries),
		GSCService:     gsc.NewService(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL, cfg.GoogleTokenEncryptionSecret, cfg.MaxAPIResponseBytes),
	}
}
