package app

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const maxRequestBodySize = 1 << 20 // 1 MB

// noopWriter discards all writes; it satisfies http.ResponseWriter without
// sending any response, preventing http.MaxBytesReader from writing a 413
// before readJSON returns.
type noopWriter struct{}

func (noopWriter) Header() http.Header         { return http.Header{} }
func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }
func (noopWriter) WriteHeader(int)             {}

// readJSON decodes a JSON request body with a 1 MB size limit.
func readJSON(r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(noopWriter{}, r.Body, maxRequestBodySize)
	return json.NewDecoder(r.Body).Decode(target)
}

// serverError logs the error with a chi request ID when available and
// writes an opaque 500 response to the client.
func serverError(w http.ResponseWriter, r *http.Request, err error) {
	reqID := middleware.GetReqID(r.Context())
	if reqID != "" {
		log.Printf("server error [%s]: %v", reqID, err)
	} else {
		log.Printf("server error: %v", err)
	}
	writeJSONError(w, http.StatusInternalServerError, "internal server error")
}

// withTx manages begin / rollback / commit boilerplate for a database
// transaction. The callback fn receives queries scoped to the transaction
// and must write its own error response on failure. withTx writes an error
// response only if Begin, Rollback, or Commit itself fails.
// Returns true if the transaction committed successfully.
func (a *App) withTx(w http.ResponseWriter, r *http.Request, fn func(*sqlc.Queries) error) bool {
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return false
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(r.Context()); rollbackErr != nil {
			log.Printf("transaction rollback error: %v", rollbackErr)
		}
	}()

	if err := fn(a.Queries.WithTx(tx)); err != nil {
		return false
	}

	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return false
	}
	committed = true
	return true
}
