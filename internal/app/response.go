package app

import (
	"encoding/json"
	"net/http"
)

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeJSONError writes a simple JSON error response.
func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}

// setImmutableCache marks a response as immutable & cacheable for a year.
// Use only for responses keyed by an id whose content never changes (e.g. a
// completed crawl's score breakdown). private because responses are per-user.
func setImmutableCache(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
}

// setNoStore marks a response as never cacheable.
func setNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}
