package app

import (
	"net/http"
	"strings"

	"github.com/ps-wizard/revserp/internal/config"
)

// ResolveEmailOrigin picks the base URL for links inside outgoing email. The
// browser's Origin wins when it is an explicitly allowed CORS origin (local,
// beta, and prod all call the API from their own origin); otherwise the
// configured frontend URL is the safe fallback.
func ResolveEmailOrigin(r *http.Request, cfg config.Config) string {
	fallback := strings.TrimSuffix(strings.TrimSpace(cfg.FrontendURL), "/")
	origin := strings.TrimSuffix(strings.TrimSpace(r.Header.Get("Origin")), "/")
	if origin == "" {
		return fallback
	}
	for _, allowed := range cfg.CORSAllowedOrigins {
		if origin == strings.TrimSuffix(strings.TrimSpace(allowed), "/") {
			return origin
		}
	}
	return fallback
}
