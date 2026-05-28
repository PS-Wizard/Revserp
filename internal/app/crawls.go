package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type createCrawlRequest struct {
	ConfigSnapshot json.RawMessage `json:"config_snapshot"`
}

type crawlResponse struct {
	ID             string          `json:"id"`
	ProjectID      string          `json:"project_id"`
	Status         string          `json:"status"`
	ConfigSnapshot json.RawMessage `json:"config_snapshot,omitempty"`
	SEOScore       *int32          `json:"seo_score,omitempty"`
	AEOScore       *int32          `json:"aeo_score,omitempty"`
	PageSpeedScore *int32          `json:"pagespeed_score,omitempty"`
	OverallScore   *int32          `json:"overall_score,omitempty"`
	StartedAt      string          `json:"started_at,omitempty"`
	CompletedAt    string          `json:"completed_at,omitempty"`
	CreatedAt      string          `json:"created_at"`
}

// handleCreateCrawl creates a crawl record for a project the user can access.
func (a *App) handleCreateCrawl(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var requestBody createCrawlRequest
	if err := readJSON(r, &requestBody); err != nil && !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback(r.Context())

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	crawl, err := queries.CreateCrawl(r.Context(), sqlc.CreateCrawlParams{
		ProjectID:      projectID,
		Status:         "queued",
		ConfigSnapshot: requestBody.ConfigSnapshot,
		StartedAt:      pgtype.Timestamptz{},
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, newCrawlResponse(crawl))
}

// handleListCrawls lists crawls for a project the user can access.
func (a *App) handleListCrawls(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback(r.Context())

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	crawls, err := queries.ListCrawlsForProject(r.Context(), projectID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	responses := make([]crawlResponse, 0, len(crawls))
	for _, crawl := range crawls {
		responses = append(responses, newCrawlResponse(crawl))
	}

	writeJSON(w, http.StatusOK, map[string]any{"crawls": responses})
}

// handleGetCrawl returns a crawl only if the current user belongs to the owning organization.
func (a *App) handleGetCrawl(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback(r.Context())

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	crawl, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, newCrawlResponse(crawl))
}

// newCrawlResponse converts a DB crawl into an API response.
func newCrawlResponse(crawl sqlc.Crawl) crawlResponse {
	response := crawlResponse{
		ID:        crawl.ID.String(),
		ProjectID: crawl.ProjectID.String(),
		Status:    crawl.Status,
		CreatedAt: formatTimestamp(crawl.CreatedAt),
	}

	if len(crawl.ConfigSnapshot) > 0 {
		response.ConfigSnapshot = json.RawMessage(crawl.ConfigSnapshot)
	}
	if crawl.SeoScore.Valid {
		response.SEOScore = &crawl.SeoScore.Int32
	}
	if crawl.AeoScore.Valid {
		response.AEOScore = &crawl.AeoScore.Int32
	}
	if crawl.PagespeedScore.Valid {
		response.PageSpeedScore = &crawl.PagespeedScore.Int32
	}
	if crawl.OverallScore.Valid {
		response.OverallScore = &crawl.OverallScore.Int32
	}
	if crawl.StartedAt.Valid {
		response.StartedAt = formatTimestamp(crawl.StartedAt)
	}
	if crawl.CompletedAt.Valid {
		response.CompletedAt = formatTimestamp(crawl.CompletedAt)
	}

	return response
}

// formatTimestamp formats a database timestamp for API responses.
func formatTimestamp(value pgtype.Timestamptz) string {
	return value.Time.UTC().Format(time.RFC3339)
}
