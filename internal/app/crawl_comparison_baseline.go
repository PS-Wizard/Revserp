package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Comparison baseline metadata for one completed crawl: the immediate prior
// completed comparable project crawl, never the current crawl itself.

// comparisonBaselineCrawlResponse describes the baseline crawl. Timestamps are
// pointers so absent values encode as null per the API contract.
type comparisonBaselineCrawlResponse struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"project_id"`
	Status      string  `json:"status"`
	StartedAt   *string `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
}

// comparisonBaselineResponse is GET /crawls/{crawlID}/comparison-baseline.
// BaselineBreakdown is the full persisted score breakdown snapshot, the same
// JSON as GET /crawls/{crawlID}/score-breakdown, or null when unavailable.
type comparisonBaselineResponse struct {
	CurrentCrawlID    string                           `json:"current_crawl_id"`
	BaselineCrawl     *comparisonBaselineCrawlResponse `json:"baseline_crawl"`
	BaselineBreakdown json.RawMessage                  `json:"baseline_breakdown"`
	// ExcludedIssueTypes lists previous issue types the current crawl never
	// checked because its exclusion patterns skip those URLs. Omitted when
	// the current crawl excludes nothing, so older clients see no change.
	ExcludedIssueTypes []comparisonExcludedIssueType `json:"excluded_issue_types,omitempty"`
}

// handleGetCrawlComparisonBaseline answers which crawl is the diff baseline
// for one crawl: its immediate prior completed comparable project crawl.
func (a *App) handleGetCrawlComparisonBaseline(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}
	// The server always selects the true predecessor; callers cannot override it.
	if comparisonBaselineOverrideRequested(r) {
		writeJSONError(w, http.StatusBadRequest, "baseline_crawl_id override is not supported")
		return
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User

	current, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}
		serverError(w, r, err)
		return
	}

	// Predecessor ordering needs completed_at and a root comparable source, so
	// anything else honestly reports no baseline instead of a wrong one.
	if current.Status != string(CrawlStatusCompleted) || !isComparableComparisonSource(current.Source) {
		setNoStore(w)
		writeJSON(w, http.StatusOK, buildComparisonBaselineResponse(crawlID, nil, nil))
		return
	}

	baselineID, err := a.Queries.GetPreviousCompletedCrawlID(r.Context(), crawlID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// First crawl: no baseline, never the current crawl as its own baseline.
			setNoStore(w)
			writeJSON(w, http.StatusOK, buildComparisonBaselineResponse(crawlID, nil, nil))
			return
		}
		serverError(w, r, err)
		return
	}

	baseline, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: baselineID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "baseline crawl not found")
			return
		}
		serverError(w, r, err)
		return
	}
	if baseline.ProjectID != current.ProjectID {
		writeJSONError(w, http.StatusBadRequest, "crawls must belong to the same project")
		return
	}

	var breakdown json.RawMessage
	if row, err := a.Queries.GetCrawlScoreBreakdownByCrawlForUser(r.Context(), sqlc.GetCrawlScoreBreakdownByCrawlForUserParams{CrawlID: baselineID, UserID: user.ID}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			serverError(w, r, err)
			return
		}
		// Snapshot unavailable: baseline metadata still exists, breakdown stays null.
	} else if validated, err := validateComparisonBaselineBreakdown(row.BreakdownJson); err != nil {
		serverError(w, r, err)
		return
	} else {
		breakdown = validated
	}

	// Baseline selection changes when crawls are deleted, so never cache it.
	setNoStore(w)
	// Excluded URLs were never checked, so their missing issues explain
	// unverified disappearances without claiming confirmed fixes.
	excluded, err := loadComparisonExcludedIssueTypes(r.Context(), a.Queries, user.ID, baselineID, crawlID, current.ConfigSnapshot)
	if err != nil {
		serverError(w, r, err)
		return
	}
	response := buildComparisonBaselineResponse(crawlID, &baseline, breakdown)
	response.ExcludedIssueTypes = excluded
	writeJSON(w, http.StatusOK, response)
}

// comparisonBaselineOverrideRequested reports whether the caller tried to pick
// its own baseline instead of letting the server select the true predecessor.
func comparisonBaselineOverrideRequested(r *http.Request) bool {
	return strings.TrimSpace(r.URL.Query().Get("baseline_crawl_id")) != ""
}

// isComparableComparisonSource reports whether a crawl source participates in
// root project diffs. It mirrors the comparable sources in
// GetPreviousCompletedCrawlID so a non-root crawl never gets a wrong baseline.
func isComparableComparisonSource(source string) bool {
	switch source {
	case "manual", "auto", "mcp":
		return true
	default:
		return false
	}
}

// buildComparisonBaselineResponse maps one baseline crawl row plus its
// snapshot into the comparison-baseline API shape. A nil baseline encodes as
// null baseline_crawl and null baseline_breakdown.
func buildComparisonBaselineResponse(currentID pgtype.UUID, baseline *sqlc.GetCrawlByIDForUserRow, breakdown json.RawMessage) comparisonBaselineResponse {
	response := comparisonBaselineResponse{CurrentCrawlID: currentID.String()}
	if baseline == nil {
		return response
	}
	response.BaselineCrawl = &comparisonBaselineCrawlResponse{
		ID:          baseline.ID.String(),
		ProjectID:   baseline.ProjectID.String(),
		Status:      baseline.Status,
		StartedAt:   comparisonBaselineTimestamp(baseline.StartedAt),
		CompletedAt: comparisonBaselineTimestamp(baseline.CompletedAt),
	}
	response.BaselineBreakdown = breakdown
	return response
}

// comparisonBaselineTimestamp formats one timestamp or returns nil for null.
func comparisonBaselineTimestamp(value pgtype.Timestamptz) *string {
	if !value.Valid {
		return nil
	}
	formatted := value.Time.UTC().Format(time.RFC3339)
	return &formatted
}

// validateComparisonBaselineBreakdown keeps the stored snapshot bytes only
// when they are valid JSON, so malformed snapshots error honestly downstream.
func validateComparisonBaselineBreakdown(raw []byte) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty score breakdown snapshot")
	}
	var decoded json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}
