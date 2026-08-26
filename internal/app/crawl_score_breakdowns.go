package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type scoreBreakdownIssueURLWorkResponse struct {
	AttemptID       string `json:"attempt_id"`
	Status          string `json:"status"`
	Locked          bool   `json:"locked"`
	ContributedByMe bool   `json:"contributed_by_me"`
}

type scoreBreakdownIssueURLResponse struct {
	URL         string                              `json:"url"`
	CrawlPageID string                              `json:"crawl_page_id,omitempty"`
	Severity    string                              `json:"severity"`
	Message     string                              `json:"message"`
	Details     string                              `json:"details"`
	IssueID     string                              `json:"issue_id"`
	Work        *scoreBreakdownIssueURLWorkResponse `json:"work,omitempty"`
}

// handleGetCrawlScoreBreakdown returns one persisted crawl score breakdown snapshot.
func (a *App) handleGetCrawlScoreBreakdown(w http.ResponseWriter, r *http.Request) {
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
		serverError(w, r, err)
		return
	}

	breakdown, err := a.Queries.GetCrawlScoreBreakdownByCrawlForUser(r.Context(), sqlc.GetCrawlScoreBreakdownByCrawlForUserParams{CrawlID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl score breakdown not found")
			return
		}
		serverError(w, r, err)
		return
	}

	if isCrawlStatusTerminal(crawl.Status) {
		setImmutableCache(w)
	} else {
		setNoStore(w)
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
	workStatus, ok := normalizeScoreBreakdownWorkStatus(r.URL.Query().Get("work_status"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "work_status must be all, needs_action, or marked_done")
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
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
		serverError(w, r, err)
		return
	}

	workActionsEnabled := false
	if latestID, err := a.Queries.GetLatestCompletedCrawlIDForScoreBreakdown(r.Context(), crawl.ProjectID); err == nil {
		workActionsEnabled = latestID == crawlID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		serverError(w, r, err)
		return
	}

	total, err := a.Queries.CountDistinctCrawlIssueURLsByTypeForCrawlByUser(r.Context(), sqlc.CountDistinctCrawlIssueURLsByTypeForCrawlByUserParams{
		CrawlID:    crawlID,
		UserID:     user.ID,
		Pillar:     pillar,
		Bucket:     bucket,
		IssueType:  issueType,
		WorkStatus: workStatus,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	issueURLs, err := a.Queries.ListDistinctCrawlIssueURLsByTypeForCrawlByUser(r.Context(), sqlc.ListDistinctCrawlIssueURLsByTypeForCrawlByUserParams{
		CrawlID:    crawlID,
		UserID:     user.ID,
		Pillar:     pillar,
		Bucket:     bucket,
		IssueType:  issueType,
		WorkStatus: workStatus,
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	responses := make([]scoreBreakdownIssueURLResponse, 0, len(issueURLs))
	for _, issueURL := range issueURLs {
		response := scoreBreakdownIssueURLResponse{
			URL:      issueURL.Url,
			Severity: issueURL.Severity,
			Message:  issueURL.Message,
			Details:  issueURL.Details,
			IssueID:  issueURL.IssueID.String(),
		}
		if issueURL.CrawlPageID.Valid {
			response.CrawlPageID = issueURL.CrawlPageID.String()
		}
		if issueURL.WorkAttemptID != "" {
			response.Work = &scoreBreakdownIssueURLWorkResponse{
				AttemptID:       issueURL.WorkAttemptID,
				Status:          issueURL.WorkStatus,
				Locked:          issueURL.WorkLocked,
				ContributedByMe: issueURL.ContributedByMe,
			}
		}
		responses = append(responses, response)
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"urls":                 responses,
		"work_actions_enabled": workActionsEnabled,
		"pagination": paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(responses)),
			Total:  total,
		},
	})
}

func normalizeScoreBreakdownWorkStatus(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "all", true
	}
	switch strings.ToLower(trimmed) {
	case "all":
		return "all", true
	case "needs_action":
		return "needs_action", true
	case "marked_done":
		return "marked_done", true
	default:
		return "", false
	}
}
