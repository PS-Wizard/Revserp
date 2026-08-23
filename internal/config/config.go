package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const (
	DefaultDBStatementTimeout = 90 * time.Second
	DefaultDBLockTimeout      = 10 * time.Second
)

// Config holds runtime configuration.
type Config struct {
	AppEnv                      string
	HTTPAddr                    string
	DatabaseURL                 string
	DBStatementTimeout          time.Duration
	DBLockTimeout               time.Duration
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
	AutoCrawlConcurrency        int
	AutoCrawlPollInterval       time.Duration
	AutoCrawlSchedulerInterval  time.Duration
	AIAuditWorkerConcurrency    int
	AIAuditWorkerPollInterval   time.Duration
	AIChatWorkerConcurrency     int
	AIChatWorkerPollInterval    time.Duration
	OpenRouterAPIKey            string
	AIVisibilityModels          []string
	AIVisibilityRateDelay       time.Duration
	AIProvider                  string
	DeepSeekAPIKey              string
	DeepSeekModel               string
	DeepSeekBaseURL             string
	AITurnTimeout               time.Duration
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
	CrawlMaxRetries             int
	CrawlRetryBase              time.Duration
	CrawlRetryMax               time.Duration
	CrawlTimeout                time.Duration
	MaxAPIResponseBytes         int64
}

// Load reads configuration from the environment.
func Load() Config {
	_ = godotenv.Load()

	return Config{
		AppEnv:                      getEnv("APP_ENV", "development"),
		HTTPAddr:                    getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL:                 getEnv("DATABASE_URL", ""),
		DBStatementTimeout:          getEnvDuration("DB_STATEMENT_TIMEOUT", DefaultDBStatementTimeout),
		DBLockTimeout:               getEnvDuration("DB_LOCK_TIMEOUT", DefaultDBLockTimeout),
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
		AutoCrawlConcurrency:        getEnvInt("AUTO_CRAWL_CONCURRENCY", 1),
		AutoCrawlPollInterval:       getEnvDuration("AUTO_CRAWL_POLL_INTERVAL", 2*time.Second),
		AutoCrawlSchedulerInterval:  getEnvDuration("AUTO_CRAWL_SCHEDULER_INTERVAL", time.Minute),
		AIAuditWorkerConcurrency:    getEnvInt("AI_AUDIT_WORKER_CONCURRENCY", 2),
		AIAuditWorkerPollInterval:   getEnvDuration("AI_AUDIT_WORKER_POLL_INTERVAL", 2*time.Second),
		AIChatWorkerConcurrency:     getEnvInt("AI_CHAT_WORKER_CONCURRENCY", 20),
		AIChatWorkerPollInterval:    getEnvDuration("AI_CHAT_WORKER_POLL_INTERVAL", 2*time.Second),
		OpenRouterAPIKey:            getEnv("OPENROUTER_API_KEY", ""),
		AIVisibilityModels:          getEnvCSV("AI_VISIBILITY_MODELS", []string{"meta-llama/llama-3.3-70b-instruct:free", "nvidia/nemotron-3-super-120b-a12b:free", "nousresearch/hermes-3-llama-3.1-405b:free"}),
		AIVisibilityRateDelay:       getEnvDuration("AI_VISIBILITY_RATE_DELAY", 9*time.Second),
		AIProvider:                  strings.ToLower(getEnv("AI_PROVIDER", "deepseek")),
		DeepSeekAPIKey:              getEnv("DEEPSEEK_API_KEY", ""),
		DeepSeekModel:               getEnv("DEEPSEEK_MODEL", "deepseek-v4-flash"),
		DeepSeekBaseURL:             getEnv("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
		AITurnTimeout:               getEnvDurationInRange("AI_TURN_TIMEOUT", 5*time.Minute, 30*time.Second, 10*time.Minute),
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
		CrawlMaxRetries:             getEnvInt("CRAWL_MAX_RETRIES", 3),
		CrawlRetryBase:              time.Duration(getEnvInt("CRAWL_RETRY_BASE_MS", 1000)) * time.Millisecond,
		CrawlRetryMax:               time.Duration(getEnvInt("CRAWL_RETRY_MAX_MS", 15000)) * time.Millisecond,
		CrawlTimeout:                getEnvDuration("CRAWL_TIMEOUT", 30*time.Minute),
		MaxAPIResponseBytes:         getEnvInt64("MAX_API_RESPONSE_BYTES", 10<<20),
	}
}

// Validate checks that all required configuration values are set.
func (cfg *Config) Validate() error {
	if cfg.DatabaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	if raw, ok := os.LookupEnv("DB_STATEMENT_TIMEOUT"); ok && raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("DB_STATEMENT_TIMEOUT: invalid duration %q: %w", raw, err)
		}
		if d <= 0 {
			return fmt.Errorf("DB_STATEMENT_TIMEOUT must be > 0, got %s", d)
		}
	}
	if cfg.DBStatementTimeout <= 0 {
		return fmt.Errorf("DB_STATEMENT_TIMEOUT must be > 0, got %s", cfg.DBStatementTimeout)
	}
	if raw, ok := os.LookupEnv("DB_LOCK_TIMEOUT"); ok && raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("DB_LOCK_TIMEOUT: invalid duration %q: %w", raw, err)
		}
		if d <= 0 {
			return fmt.Errorf("DB_LOCK_TIMEOUT must be > 0, got %s", d)
		}
	}
	if cfg.DBLockTimeout <= 0 {
		return fmt.Errorf("DB_LOCK_TIMEOUT must be > 0, got %s", cfg.DBLockTimeout)
	}
	return nil
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

// getEnvInt64 returns an int64 environment variable or a default value.
func getEnvInt64(key string, defaultValue int64) int64 {
	value := getEnv(key, "")
	if value == "" {
		return defaultValue
	}

	parsedValue, err := strconv.ParseInt(value, 10, 64)
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

// getEnvDurationInRange returns a duration environment variable clamped to
// [min, max], falling back to defaultValue when unset or unparseable.
func getEnvDurationInRange(key string, defaultValue, min, max time.Duration) time.Duration {
	value := getEnvDuration(key, defaultValue)
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
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
