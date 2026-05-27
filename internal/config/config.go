package config

import (
	"os"

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
