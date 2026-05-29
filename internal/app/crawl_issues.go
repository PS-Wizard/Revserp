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
	Severity    string `json:"severity"`
	Category    string `json:"category"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	Details     string `json:"details"`
}

type crawlIssueResponse struct {
	ID          string `json:"id"`
	CrawlID     string `json:"crawl_id"`
	CrawlPageID string `json:"crawl_page_id,omitempty"`
	URL         string `json:"url"`
	Severity    string `json:"severity"`
	Category    string `json:"category"`
	Code        string `json:"code"`
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
	severity := strings.TrimSpace(requestBody.Severity)
	category := strings.TrimSpace(requestBody.Category)
	code := strings.TrimSpace(requestBody.Code)
	message := strings.TrimSpace(requestBody.Message)
	details := strings.TrimSpace(requestBody.Details)
	if url == "" || severity == "" || category == "" || code == "" || message == "" || details == "" {
		writeJSONError(w, http.StatusBadRequest, "url, severity, category, code, message, and details are required")
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
		Severity:    severity,
		Category:    category,
		Code:        code,
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

	writeJSON(w, http.StatusCreated, newCrawlIssueResponse(issue))
}

// handleListCrawlIssues lists issue rows for a crawl the user can access.
func (a *App) handleListCrawlIssues(w http.ResponseWriter, r *http.Request) {
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
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	issues, err := queries.ListCrawlIssuesForCrawlByUser(r.Context(), sqlc.ListCrawlIssuesForCrawlByUserParams{CrawlID: crawlID, UserID: user.ID})
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
		responses = append(responses, newCrawlIssueResponse(issue))
	}

	writeJSON(w, http.StatusOK, map[string]any{"issues": responses})
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

	writeJSON(w, http.StatusOK, newCrawlIssueResponse(issue))
}

// newCrawlIssueResponse converts a crawl issue row into an API response.
func newCrawlIssueResponse(issue sqlc.CrawlIssue) crawlIssueResponse {
	response := crawlIssueResponse{
		ID:        issue.ID.String(),
		CrawlID:   issue.CrawlID.String(),
		URL:       issue.Url,
		Severity:  issue.Severity,
		Category:  issue.Category,
		Code:      issue.Code,
		Message:   issue.Message,
		Details:   issue.Details,
		CreatedAt: formatTimestamp(issue.CreatedAt),
	}

	if issue.CrawlPageID.Valid {
		response.CrawlPageID = issue.CrawlPageID.String()
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
