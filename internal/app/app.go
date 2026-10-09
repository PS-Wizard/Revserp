package app

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/aiskills"
	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/ga"
	"github.com/ps-wizard/revserp/internal/geography"
	"github.com/ps-wizard/revserp/internal/googleplaces"
	"github.com/ps-wizard/revserp/internal/gsc"
	"github.com/ps-wizard/revserp/internal/locationlandmarks"
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
	GAService      *ga.Service
	Nominatim      *geography.NominatimClient
	Landmarks      *locationlandmarks.Client
	PlacesListings *googleplaces.PlacesListingClient
	// Skills is the read-only filesystem catalog of Git-managed AI skill
	// folders. A nil catalog (tests that build App without New) serves an
	// empty skill list.
	Skills *aiskills.Catalog
	// MCPConnect dials one generic session for connection validation
	// (initialize plus tools/list discovery only, never tool execution).
	// Wired by default in New; tests may replace it with a fake. A nil value
	// answers 503 so handlers degrade safely.
	MCPConnect MCPConnectFunc
	OrgEvents  *organizationEventHub
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
		GSCService:     gsc.NewService(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL, cfg.GoogleTokenEncryptionSecret, cfg.MaxAPIResponseBytes),
		GAService:      ga.NewService(cfg.MaxAPIResponseBytes),
		OrgEvents:      newOrganizationEventHub(),
		MCPConnect:     DefaultMCPConnect,
		Nominatim:      geography.NewNominatimClient(cfg.NominatimEndpoint, cfg.NominatimUserAgent),
		Landmarks:      locationlandmarks.NewClient(locationlandmarks.Config{APIKey: cfg.GoogleMapsAPIKey}),
		PlacesListings: googleplaces.NewPlacesListingClient(cfg.GoogleMapsAPIKey, ""),
		Skills:         aiskills.New(cfg.AISkillsDir),
	}
}
