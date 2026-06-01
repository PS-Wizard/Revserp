package config

import (
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// Config holds runtime configuration.
type Config struct {
	AppEnv              string
	HTTPAddr            string
	DatabaseURL         string
	AuthProvider        string
	SupabaseJWTIssuer   string
	SupabaseJWKSURL     string
	SupabaseJWTAudience string
	WorkerConcurrency   int
	WorkerPollInterval  time.Duration
}

// Load reads configuration from the environment.
func Load() Config {
	_ = godotenv.Load()

	return Config{
		AppEnv:              getEnv("APP_ENV", "development"),
		HTTPAddr:            getEnv("HTTP_ADDR", ":8080"),
		DatabaseURL:         getEnv("DATABASE_URL", ""),
		AuthProvider:        getEnv("AUTH_PROVIDER", "supabase"),
		SupabaseJWTIssuer:   getEnv("SUPABASE_JWT_ISSUER", ""),
		SupabaseJWKSURL:     getEnv("SUPABASE_JWKS_URL", ""),
		SupabaseJWTAudience: getEnv("SUPABASE_JWT_AUDIENCE", "authenticated"),
		WorkerConcurrency:   getEnvInt("WORKER_CONCURRENCY", 2),
		WorkerPollInterval:  getEnvDuration("WORKER_POLL_INTERVAL", 2*time.Second),
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
