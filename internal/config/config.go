package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds runtime configuration.
type Config struct {
	AppEnv                      string
	HTTPAddr                    string
	DatabaseURL                 string
	AuthProvider                string
	SupabaseJWTIssuer           string
	SupabaseJWKSURL             string
	SupabaseJWTAudience         string
	SupabaseAnonKey             string
	SessionCookieName           string
	SessionCookieDomain         string
	SessionTTL                  time.Duration
	CORSAllowedOrigins          []string
	WorkerConcurrency           int
	WorkerPollInterval          time.Duration
	CrawlPageWorkerCount        int
	AIAuditWorkerConcurrency    int
	AIAuditWorkerPollInterval   time.Duration
	OpenRouterAPIKey            string
	GeminiAPIKey                string
	GeminiModel                 string
	ObscuraPath                 string
	RendererConcurrency         int
	ObscuraTimeout              time.Duration
	ObscuraKillTimeout          time.Duration
	FrontendURL                 string
	GoogleClientID              string
	GoogleClientSecret          string
	GoogleRedirectURL           string
	GoogleTokenEncryptionSecret string
	PageSpeedAPIKey             string
}

// Load reads configuration from the environment.
func Load() Config {
	_ = godotenv.Load()

	return Config{
		AppEnv:                      getEnv("APP_ENV", "development"),
		HTTPAddr:                    getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL:                 getEnv("DATABASE_URL", ""),
		AuthProvider:                getEnv("AUTH_PROVIDER", "supabase"),
		SupabaseJWTIssuer:           getEnv("SUPABASE_JWT_ISSUER", ""),
		SupabaseJWKSURL:             getEnv("SUPABASE_JWKS_URL", ""),
		SupabaseJWTAudience:         getEnv("SUPABASE_JWT_AUDIENCE", "authenticated"),
		SupabaseAnonKey:             getEnv("SUPABASE_ANON_KEY", ""),
		SessionCookieName:           getEnv("SESSION_COOKIE_NAME", "revserp_session"),
		SessionCookieDomain:         getEnv("SESSION_COOKIE_DOMAIN", ""),
		SessionTTL:                  getEnvDuration("SESSION_TTL", 30*24*time.Hour),
		CORSAllowedOrigins:          getEnvCSV("CORS_ALLOWED_ORIGINS", []string{"http://localhost:3000", "http://localhost:5173", "http://127.0.0.1:3000", "http://127.0.0.1:5173"}),
		WorkerConcurrency:           getEnvInt("WORKER_CONCURRENCY", 2),
		WorkerPollInterval:          getEnvDuration("WORKER_POLL_INTERVAL", 2*time.Second),
		CrawlPageWorkerCount:        getEnvInt("CRAWL_PAGE_WORKER_COUNT", 4),
		AIAuditWorkerConcurrency:    getEnvInt("AI_AUDIT_WORKER_CONCURRENCY", 2),
		AIAuditWorkerPollInterval:   getEnvDuration("AI_AUDIT_WORKER_POLL_INTERVAL", 2*time.Second),
		OpenRouterAPIKey:            getEnv("OPENROUTER_API_KEY", ""),
		GeminiAPIKey:                getEnv("GEMINI_API_KEY", ""),
		GeminiModel:                 getEnv("GEMINI_MODEL", "gemini-2.5-flash"),
		ObscuraPath:                 getEnv("OBSCURA_PATH", ""),
		RendererConcurrency:         getEnvInt("RENDERER_CONCURRENCY", 2),
		ObscuraTimeout:              time.Duration(getEnvInt("OBSCURA_TIMEOUT_SECONDS", 5)) * time.Second,
		ObscuraKillTimeout:          time.Duration(getEnvInt("OBSCURA_KILL_TIMEOUT_SECONDS", 7)) * time.Second,
		FrontendURL:                 getEnv("FRONTEND_URL", ""),
		GoogleClientID:              getEnv("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret:          getEnv("GOOGLE_CLIENT_SECRET", ""),
		GoogleRedirectURL:           getEnv("GOOGLE_REDIRECT_URL", ""),
		GoogleTokenEncryptionSecret: getEnv("GOOGLE_TOKEN_ENCRYPTION_SECRET", ""),
		PageSpeedAPIKey:             getEnv("PAGESPEED_API_KEY", ""),
	}
}

// getEnv returns an environment variable or a default value.
func getEnv(key string, defaultValue string) string {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return defaultValue
	}

	return value
}

// getEnvInt returns an integer environment variable or a default value.
func getEnvInt(key string, defaultValue int) int {
	value := getEnv(key, "")
	if value == "" {
		return defaultValue
	}

	parsedValue, err := strconv.Atoi(value)
	if err != nil {
		return defaultValue
	}

	return parsedValue
}

// getEnvDuration returns a duration environment variable or a default value.
func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	value := getEnv(key, "")
	if value == "" {
		return defaultValue
	}

	parsedValue, err := time.ParseDuration(value)
	if err != nil {
		return defaultValue
	}

	return parsedValue
}

// getEnvCSV returns a comma-separated environment variable or a default value.
func getEnvCSV(key string, defaultValue []string) []string {
	value := getEnv(key, "")
	if value == "" {
		return defaultValue
	}

	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmedValue := strings.TrimSpace(part)
		if trimmedValue == "" {
			continue
		}
		values = append(values, trimmedValue)
	}
	if len(values) == 0 {
		return defaultValue
	}

	return values
}
