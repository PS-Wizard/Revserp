package auth

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// RequireAPIKey authenticates one bearer API key and stores its identity and metadata.
func RequireAPIKey(manager *APIKeyManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			headers := r.Header.Values("Authorization")
			if len(headers) != 1 {
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}

			raw, err := ParseBearer(headers[0])
			if err != nil {
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}

			identity, apiKey, err := manager.Authenticate(r.Context(), raw)
			if err != nil {
				if errors.Is(err, ErrInvalidCredential) || errors.Is(err, pgx.ErrNoRows) {
					http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
					return
				}
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}

			ctx := WithIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(WithAPIKey(ctx, apiKey)))
		})
	}
}
