package app

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type createCrawlRequest struct {
	ConfigSnapshot json.RawMessage `json:"config_snapshot"`
}

const defaultCrawlWorkerCount = 4

type crawlResponse struct {
	ID               string          `json:"id"`
	ProjectID        string          `json:"project_id"`
	Status           string          `json:"status"`
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

	writeJSON(w, http.StatusCreated, newCrawlResponseFromCreateRow(crawl))
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
		responses = append(responses, newCrawlResponseFromListRow(crawl))
	}

	writeJSON(w, http.StatusOK, map[string]any{"crawls": responses})
}

// handleRunCrawl runs one queued crawl synchronously through the HTTP crawler pipeline.
func (a *App) handleRunCrawl(w http.ResponseWriter, r *http.Request) {
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

	if crawl.Status != "queued" {
		writeJSONError(w, http.StatusConflict, "crawl is not runnable")
		return
	}

	project, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: crawl.ProjectID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	crawlConfig, err := newDefaultCrawlerConfig(project.BaseUrl)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "project base_url is invalid")
		return
	}

	fetcher := crawler.NewFetcher(crawlConfig.FetchTimeout, crawlConfig.UserAgent)
	parser := crawler.NewParser()
	store := crawler.NewStore(a.DB)
	runner := crawler.NewRunner(crawlConfig, defaultCrawlWorkerCount, fetcher, parser).WithStore(store)

	if _, err := runner.RunAndPersist(r.Context(), crawlID, project.BaseUrl); err != nil {
		log.Printf("crawl run failed: crawl_id=%s project_id=%s base_url=%q error=%v", crawlID.String(), project.ID.String(), project.BaseUrl, err)
		writeJSONError(w, http.StatusInternalServerError, "crawl run failed")
		return
	}

	updatedCrawl, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, newCrawlResponseFromGetRow(updatedCrawl))
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

	writeJSON(w, http.StatusOK, newCrawlResponseFromGetRow(crawl))
}

// newDefaultCrawlerConfig builds the current hardcoded crawler settings for one project root URL.
// TODO: expose crawl settings via crawl config / user settings.
func newDefaultCrawlerConfig(baseURL string) (crawler.CrawlerConfig, error) {
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil {
		return crawler.CrawlerConfig{}, err
	}

	if parsedBaseURL.Host == "" {
		return crawler.CrawlerConfig{}, errors.New("missing host")
	}

	return crawler.CrawlerConfig{
		AllowedHost:  parsedBaseURL.Host,
		MaxDepth:     2,
		MaxPages:     100,
		FetchTimeout: 10 * time.Second,
		UserAgent:    "revserp-bot/0.1",
	}, nil
}

// newCrawlResponseFromCreateRow converts a created crawl row into an API response.
func newCrawlResponseFromCreateRow(crawl sqlc.CreateCrawlRow) crawlResponse {
	return buildCrawlResponse(
		crawl.ID,
		crawl.ProjectID,
		crawl.Status,
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

// formatTimestamp formats a database timestamp for API responses.
func formatTimestamp(value pgtype.Timestamptz) string {
	return value.Time.UTC().Format(time.RFC3339)
}
