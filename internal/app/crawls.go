package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// CrawlStatus is a typed crawl lifecycle status, used within this file for
// status filtering/terminal checks (scoped here rather than a repo-wide sweep).
type CrawlStatus string

const (
	CrawlStatusQueued    CrawlStatus = "queued"
	CrawlStatusRunning   CrawlStatus = "running"
	CrawlStatusCompleted CrawlStatus = "completed"
	CrawlStatusFailed    CrawlStatus = "failed"
	CrawlStatusCancelled CrawlStatus = "cancelled"
)

type createCrawlRequest struct {
	ConfigSnapshot json.RawMessage `json:"config_snapshot"`
}

type crawlResponse struct {
	ID               string          `json:"id"`
	ProjectID        string          `json:"project_id"`
	Status           string          `json:"status"`
	Phase            string          `json:"phase,omitempty"`
	ConfigSnapshot   json.RawMessage `json:"config_snapshot,omitempty"`
	URLsDiscovered   int32           `json:"urls_discovered"`
	URLsCrawled      int32           `json:"urls_crawled"`
	MaxDepthReached  int32           `json:"max_depth_reached"`
	GooglePSIResults json.RawMessage `json:"google_psi_results,omitempty"`
	HasLLMsTxt       *bool           `json:"has_llms_txt,omitempty"`
	SEOScore         *int32          `json:"seo_score,omitempty"`
	AEOScore         *int32          `json:"aeo_score,omitempty"`
	PageSpeedScore   *int32          `json:"pagespeed_score,omitempty"`
	OverallScore     *int32          `json:"overall_score,omitempty"`
	StartedAt        string          `json:"started_at,omitempty"`
	CompletedAt      string          `json:"completed_at,omitempty"`
	CreatedAt        string          `json:"created_at"`
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

	normalizedConfigSnapshot, err := normalizeCreateCrawlConfigSnapshot(requestBody.ConfigSnapshot)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
	if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	crawl, err := queries.CreateCrawl(r.Context(), sqlc.CreateCrawlParams{
		ProjectID:         projectID,
		RequestedByUserID: user.ID,
		Source:            "manual",
		Status:            "queued",
		ConfigSnapshot:    normalizedConfigSnapshot,
		StartedAt:         pgtype.Timestamptz{},
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, newCrawlResponseFromCreateRow(crawl))
}

// handleListCrawls lists crawls for a project the user can access.
func (a *App) handleListCrawls(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	statusFilter, err := parseCrawlStatusFilter(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}

	user := principal.User
	if _, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	total, err := a.Queries.CountCrawlsForProject(r.Context(), sqlc.CountCrawlsForProjectParams{ProjectID: projectID, Column2: statusFilter})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	crawls, err := a.Queries.ListCrawlsForProject(r.Context(), sqlc.ListCrawlsForProjectParams{
		ProjectID: projectID,
		Column2:   statusFilter,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	responses := make([]crawlResponse, 0, len(crawls))
	for _, crawl := range crawls {
		responses = append(responses, newCrawlResponseFromListRow(crawl))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"crawls": responses,
		"pagination": paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(responses)),
			Total:  total,
		},
	})
}

// handleGetCrawl returns a crawl only if the current user belongs to the owning organization.
func (a *App) handleGetCrawl(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}

	user := principal.User
	crawl, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if isCrawlStatusTerminal(crawl.Status) {
		setImmutableCache(w)
	} else {
		setNoStore(w)
	}
	writeJSON(w, http.StatusOK, newCrawlResponseFromGetRow(crawl))
}

// handleDeleteCrawl deletes a completed or failed crawl the user can access.
func (a *App) handleDeleteCrawl(w http.ResponseWriter, r *http.Request) {
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
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
	if _, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := queries.DeleteCrawlByIDForUser(r.Context(), sqlc.DeleteCrawlByIDForUserParams{ID: crawlID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "cannot delete crawl while it is queued or running")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleCancelCrawl cancels a queued or running crawl the user can access,
// marking it 'cancelled' so it stops blocking new crawls and becomes deletable.
func (a *App) handleCancelCrawl(w http.ResponseWriter, r *http.Request) {
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
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
	if _, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := queries.CancelCrawlByIDForUser(r.Context(), sqlc.CancelCrawlByIDForUserParams{ID: crawlID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "crawl is not queued or running")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// newCrawlResponseFromCreateRow converts a created crawl row into an API response.
func newCrawlResponseFromCreateRow(crawl sqlc.CreateCrawlRow) crawlResponse {
	return buildCrawlResponse(
		crawl.ID,
		crawl.ProjectID,
		crawl.Status,
		crawl.Phase,
		crawl.ConfigSnapshot,
		crawl.UrlsDiscovered,
		crawl.UrlsCrawled,
		crawl.MaxDepthReached,
		crawl.GooglePsiResults,
		crawl.HasLlmsTxt,
		crawl.SeoScore,
		crawl.AeoScore,
		crawl.PagespeedScore,
		crawl.OverallScore,
		crawl.StartedAt,
		crawl.CompletedAt,
		crawl.CreatedAt,
	)
}

// newCrawlResponseFromGetRow converts a fetched crawl row into an API response.
func newCrawlResponseFromGetRow(crawl sqlc.GetCrawlByIDForUserRow) crawlResponse {
	return buildCrawlResponse(
		crawl.ID,
		crawl.ProjectID,
		crawl.Status,
		crawl.Phase,
		crawl.ConfigSnapshot,
		crawl.UrlsDiscovered,
		crawl.UrlsCrawled,
		crawl.MaxDepthReached,
		crawl.GooglePsiResults,
		crawl.HasLlmsTxt,
		crawl.SeoScore,
		crawl.AeoScore,
		crawl.PagespeedScore,
		crawl.OverallScore,
		crawl.StartedAt,
		crawl.CompletedAt,
		crawl.CreatedAt,
	)
}

// newCrawlResponseFromListRow converts a listed crawl row into an API response.
func newCrawlResponseFromListRow(crawl sqlc.ListCrawlsForProjectRow) crawlResponse {
	return buildCrawlResponse(
		crawl.ID,
		crawl.ProjectID,
		crawl.Status,
		crawl.Phase,
		crawl.ConfigSnapshot,
		crawl.UrlsDiscovered,
		crawl.UrlsCrawled,
		crawl.MaxDepthReached,
		crawl.GooglePsiResults,
		crawl.HasLlmsTxt,
		crawl.SeoScore,
		crawl.AeoScore,
		crawl.PagespeedScore,
		crawl.OverallScore,
		crawl.StartedAt,
		crawl.CompletedAt,
		crawl.CreatedAt,
	)
}

// buildCrawlResponse converts crawl fields into an API response.
func buildCrawlResponse(
	id pgtype.UUID,
	projectID pgtype.UUID,
	status string,
	phase pgtype.Text,
	configSnapshot []byte,
	urlsDiscovered int32,
	urlsCrawled int32,
	maxDepthReached int32,
	googlePSIResults []byte,
	hasLLMsTxt pgtype.Bool,
	seoScore pgtype.Int4,
	aeoScore pgtype.Int4,
	pageSpeedScore pgtype.Int4,
	overallScore pgtype.Int4,
	startedAt pgtype.Timestamptz,
	completedAt pgtype.Timestamptz,
	createdAt pgtype.Timestamptz,
) crawlResponse {
	response := crawlResponse{
		ID:              id.String(),
		ProjectID:       projectID.String(),
		Status:          status,
		URLsDiscovered:  urlsDiscovered,
		URLsCrawled:     urlsCrawled,
		MaxDepthReached: maxDepthReached,
		CreatedAt:       formatTimestamp(createdAt),
	}

	if phase.Valid {
		response.Phase = phase.String
	}
	if len(configSnapshot) > 0 {
		response.ConfigSnapshot = json.RawMessage(configSnapshot)
	}
	if len(googlePSIResults) > 0 {
		response.GooglePSIResults = json.RawMessage(googlePSIResults)
	}
	if hasLLMsTxt.Valid {
		response.HasLLMsTxt = &hasLLMsTxt.Bool
	}
	if seoScore.Valid {
		response.SEOScore = &seoScore.Int32
	}
	if aeoScore.Valid {
		response.AEOScore = &aeoScore.Int32
	}
	if pageSpeedScore.Valid {
		response.PageSpeedScore = &pageSpeedScore.Int32
	}
	if overallScore.Valid {
		response.OverallScore = &overallScore.Int32
	}
	if startedAt.Valid {
		response.StartedAt = formatTimestamp(startedAt)
	}
	if completedAt.Valid {
		response.CompletedAt = formatTimestamp(completedAt)
	}

	return response
}

type activeCrawlResponse struct {
	ID             string `json:"id"`
	ProjectID      string `json:"project_id"`
	Status         string `json:"status"`
	Phase          string `json:"phase,omitempty"`
	URLsDiscovered int32  `json:"urls_discovered"`
	URLsCrawled    int32  `json:"urls_crawled"`
	CreatedAt      string `json:"created_at"`
}

// handleListActiveOrganizationCrawls returns all queued/running crawls across all projects in an organization.
func (a *App) handleListActiveOrganizationCrawls(w http.ResponseWriter, r *http.Request) {
	organizationID, err := parseUUIDParam(chi.URLParam(r, "organizationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}

	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}

	user := principal.User
	if _, err := a.Queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{OrgID: organizationID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	crawls, err := a.Queries.ListActiveCrawlsForOrganization(r.Context(), organizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	responses := make([]activeCrawlResponse, 0, len(crawls))
	for _, c := range crawls {
		response := activeCrawlResponse{
			ID:             c.ID.String(),
			ProjectID:      c.ProjectID.String(),
			Status:         c.Status,
			URLsDiscovered: c.UrlsDiscovered,
			URLsCrawled:    c.UrlsCrawled,
			CreatedAt:      formatTimestamp(c.CreatedAt),
		}
		if c.Phase.Valid {
			response.Phase = c.Phase.String
		}
		responses = append(responses, response)
	}

	writeJSON(w, http.StatusOK, map[string]any{"crawls": responses})
}

// formatTimestamp formats a database timestamp for API responses.
func formatTimestamp(value pgtype.Timestamptz) string {
	return value.Time.UTC().Format(time.RFC3339)
}

// isCrawlStatusTerminal reports whether a crawl status is terminal (content never changes).
func isCrawlStatusTerminal(status string) bool {
	s := CrawlStatus(status)
	return s == CrawlStatusCompleted || s == CrawlStatusFailed || s == CrawlStatusCancelled
}

// parseCrawlStatusFilter validates the optional crawl list status filter.
func parseCrawlStatusFilter(r *http.Request) (string, error) {
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	if statusFilter == "" {
		return "", nil
	}

	switch CrawlStatus(statusFilter) {
	case CrawlStatusQueued, CrawlStatusRunning, CrawlStatusCompleted, CrawlStatusFailed, CrawlStatusCancelled:
		return statusFilter, nil
	default:
		return "", errors.New("invalid status")
	}
}
