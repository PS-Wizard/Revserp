package app

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type createCrawlIssueRequest struct {
	CrawlPageID string `json:"crawl_page_id"`
	URL         string `json:"url"`
	Pillar      string `json:"pillar"`
	Bucket      string `json:"bucket"`
	IssueType   string `json:"issue_type"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
	Details     string `json:"details"`
}

type crawlIssueResponse struct {
	ID          string `json:"id"`
	CrawlID     string `json:"crawl_id"`
	CrawlPageID string `json:"crawl_page_id,omitempty"`
	URL         string `json:"url"`
	Pillar      string `json:"pillar"`
	Bucket      string `json:"bucket"`
	IssueType   string `json:"issue_type"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
	Details     string `json:"details"`
	CreatedAt   string `json:"created_at"`
}

// handleCreateCrawlIssue creates a derived issue row for a crawl the user can access.
func (a *App) handleCreateCrawlIssue(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	var requestBody createCrawlIssueRequest
	if err := readJSON(r, &requestBody); err != nil && !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	url := strings.TrimSpace(requestBody.URL)
	pillar, ok := normalizeIssuePillar(requestBody.Pillar)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "pillar must be seo, aeo, or pagespeed")
		return
	}
	bucket := strings.TrimSpace(requestBody.Bucket)
	issueType := strings.TrimSpace(requestBody.IssueType)
	severity, ok := normalizeIssueSeverity(requestBody.Severity)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "severity must be high, medium, or low")
		return
	}
	message := strings.TrimSpace(requestBody.Message)
	details := strings.TrimSpace(requestBody.Details)
	if url == "" || bucket == "" || issueType == "" || message == "" || details == "" {
		writeJSONError(w, http.StatusBadRequest, "url, pillar, bucket, issue_type, severity, message, and details are required")
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
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	crawlPageID, err := parseOptionalUUID(requestBody.CrawlPageID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl page id")
		return
	}
	if crawlPageID.Valid {
		page, err := queries.GetCrawlPageByIDForUser(r.Context(), sqlc.GetCrawlPageByIDForUserParams{ID: crawlPageID, UserID: user.ID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusBadRequest, "invalid crawl page id")
				return
			}

			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}

		if page.CrawlID != crawlID {
			writeJSONError(w, http.StatusBadRequest, "crawl page does not belong to crawl")
			return
		}
	}

	issue, err := queries.CreateCrawlIssue(r.Context(), sqlc.CreateCrawlIssueParams{
		CrawlID:     crawlID,
		CrawlPageID: crawlPageID,
		Url:         url,
		Pillar:      pillar,
		Bucket:      bucket,
		IssueType:   issueType,
		Severity:    severity,
		Message:     message,
		Details:     details,
	})
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			writeJSONError(w, http.StatusConflict, "crawl issue already exists")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, newCreatedCrawlIssueResponse(issue))
}

// handleListCrawlIssues lists issue rows for a crawl the user can access.
func (a *App) handleListCrawlIssues(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
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
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	total, err := queries.CountCrawlIssuesForCrawlByUser(r.Context(), sqlc.CountCrawlIssuesForCrawlByUserParams{CrawlID: crawlID, UserID: user.ID})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	issues, err := queries.ListCrawlIssuesForCrawlByUser(r.Context(), sqlc.ListCrawlIssuesForCrawlByUserParams{
		CrawlID: crawlID,
		UserID:  user.ID,
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	responses := make([]crawlIssueResponse, 0, len(issues))
	for _, issue := range issues {
		responses = append(responses, newListedCrawlIssueResponse(issue))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"issues": responses,
		"pagination": paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(responses)),
			Total:  total,
		},
	})
}

// handleGetCrawlIssue returns an issue row only if the current user belongs to the owning organization.
func (a *App) handleGetCrawlIssue(w http.ResponseWriter, r *http.Request) {
	issueID, err := parseUUIDParam(chi.URLParam(r, "issueID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid issue id")
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

	issue, err := queries.GetCrawlIssueByIDForUser(r.Context(), sqlc.GetCrawlIssueByIDForUserParams{ID: issueID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl issue not found")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, newFetchedCrawlIssueResponse(issue))
}

// newCrawlIssueResponse converts a crawl issue row into an API response.
func newCrawlIssueResponse(issue sqlc.CrawlIssue) crawlIssueResponse {
	return buildCrawlIssueResponse(
		issue.ID,
		issue.CrawlID,
		issue.CrawlPageID,
		issue.Url,
		issue.Pillar,
		issue.Bucket,
		issue.IssueType,
		issue.Severity,
		issue.Message,
		issue.Details,
		issue.CreatedAt,
	)
}

// newCreatedCrawlIssueResponse converts an inserted crawl issue row into an API response.
func newCreatedCrawlIssueResponse(issue sqlc.CreateCrawlIssueRow) crawlIssueResponse {
	return buildCrawlIssueResponse(
		issue.ID,
		issue.CrawlID,
		issue.CrawlPageID,
		issue.Url,
		issue.Pillar,
		issue.Bucket,
		issue.IssueType,
		issue.Severity,
		issue.Message,
		issue.Details,
		issue.CreatedAt,
	)
}

// newListedCrawlIssueResponse converts a listed crawl issue row into an API response.
func newListedCrawlIssueResponse(issue sqlc.ListCrawlIssuesForCrawlByUserRow) crawlIssueResponse {
	return buildCrawlIssueResponse(
		issue.ID,
		issue.CrawlID,
		issue.CrawlPageID,
		issue.Url,
		issue.Pillar,
		issue.Bucket,
		issue.IssueType,
		issue.Severity,
		issue.Message,
		issue.Details,
		issue.CreatedAt,
	)
}

// newFetchedCrawlIssueResponse converts a fetched crawl issue row into an API response.
func newFetchedCrawlIssueResponse(issue sqlc.GetCrawlIssueByIDForUserRow) crawlIssueResponse {
	return buildCrawlIssueResponse(
		issue.ID,
		issue.CrawlID,
		issue.CrawlPageID,
		issue.Url,
		issue.Pillar,
		issue.Bucket,
		issue.IssueType,
		issue.Severity,
		issue.Message,
		issue.Details,
		issue.CreatedAt,
	)
}

// buildCrawlIssueResponse builds one issue API response from normalized row values.
func buildCrawlIssueResponse(id pgtype.UUID, crawlID pgtype.UUID, crawlPageID pgtype.UUID, url string, pillar string, bucket string, issueType string, severity string, message string, details string, createdAt pgtype.Timestamptz) crawlIssueResponse {
	response := crawlIssueResponse{
		ID:        id.String(),
		CrawlID:   crawlID.String(),
		URL:       url,
		Pillar:    pillar,
		Bucket:    bucket,
		IssueType: issueType,
		Severity:  severity,
		Message:   message,
		Details:   details,
		CreatedAt: formatTimestamp(createdAt),
	}

	if crawlPageID.Valid {
		response.CrawlPageID = crawlPageID.String()
	}

	return response
}

// parseOptionalUUID parses an optional UUID string.
func parseOptionalUUID(value string) (pgtype.UUID, error) {
	trimmedValue := strings.TrimSpace(value)
	if trimmedValue == "" {
		return pgtype.UUID{}, nil
	}

	id, err := parseUUIDParam(trimmedValue)
	if err != nil {
		return pgtype.UUID{}, err
	}

	return id, nil
}

// normalizeIssuePillar validates and normalizes one issue pillar.
func normalizeIssuePillar(value string) (string, bool) {
	normalizedValue := strings.ToLower(strings.TrimSpace(value))
	switch normalizedValue {
	case "seo", "aeo", "pagespeed":
		return normalizedValue, true
	default:
		return "", false
	}
}

// normalizeIssueSeverity validates and normalizes one issue severity.
func normalizeIssueSeverity(value string) (string, bool) {
	normalizedValue := strings.ToLower(strings.TrimSpace(value))
	switch normalizedValue {
	case "high", "error":
		return "high", true
	case "medium", "warning":
		return "medium", true
	case "low", "info":
		return "low", true
	default:
		return "", false
	}
}
