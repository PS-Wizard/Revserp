package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type scoreBreakdownIssueURLResponse struct {
	URL         string `json:"url"`
	CrawlPageID string `json:"crawl_page_id,omitempty"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
	Details     string `json:"details"`
}

// handleGetCrawlScoreBreakdown returns one persisted crawl score breakdown snapshot.
func (a *App) handleGetCrawlScoreBreakdown(w http.ResponseWriter, r *http.Request) {
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

	if _, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	breakdown, err := queries.GetCrawlScoreBreakdownByCrawlForUser(r.Context(), sqlc.GetCrawlScoreBreakdownByCrawlForUserParams{CrawlID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl score breakdown not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(breakdown.BreakdownJson)
}

// handleListScoreBreakdownIssueURLs returns paginated affected URLs for one grouped issue type.
func (a *App) handleListScoreBreakdownIssueURLs(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}
	pillar, ok := normalizeIssuePillar(chi.URLParam(r, "pillar"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "pillar must be seo, aeo, or pagespeed")
		return
	}
	bucket := strings.TrimSpace(chi.URLParam(r, "bucket"))
	issueType := strings.TrimSpace(chi.URLParam(r, "issueType"))
	if bucket == "" || issueType == "" {
		writeJSONError(w, http.StatusBadRequest, "bucket and issue type are required")
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
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

	if _, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	total, err := queries.CountDistinctCrawlIssueURLsByTypeForCrawlByUser(r.Context(), sqlc.CountDistinctCrawlIssueURLsByTypeForCrawlByUserParams{
		CrawlID:   crawlID,
		UserID:    user.ID,
		Pillar:    pillar,
		Bucket:    bucket,
		IssueType: issueType,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	issueURLs, err := queries.ListDistinctCrawlIssueURLsByTypeForCrawlByUser(r.Context(), sqlc.ListDistinctCrawlIssueURLsByTypeForCrawlByUserParams{
		CrawlID:   crawlID,
		UserID:    user.ID,
		Pillar:    pillar,
		Bucket:    bucket,
		IssueType: issueType,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	responses := make([]scoreBreakdownIssueURLResponse, 0, len(issueURLs))
	for _, issueURL := range issueURLs {
		response := scoreBreakdownIssueURLResponse{
			URL:      issueURL.Url,
			Severity: issueURL.Severity,
			Message:  issueURL.Message,
			Details:  issueURL.Details,
		}
		if issueURL.CrawlPageID.Valid {
			response.CrawlPageID = issueURL.CrawlPageID.String()
		}
		responses = append(responses, response)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"urls": responses,
		"pagination": paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(responses)),
			Total:  total,
		},
	})
}
