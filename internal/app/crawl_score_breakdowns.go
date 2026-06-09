package app

import (
	"encoding/csv"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

type scoreBreakdownIssueURLResponse struct {
	URL         string `json:"url"`
	CrawlPageID string `json:"crawl_page_id,omitempty"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
	Details     string `json:"details"`
}

type crawlIssueExportRow struct {
	Pillar         string
	Bucket         string
	IssueType      string
	IssueLabel     string
	Severity       string
	TotalAffected  int
	URL            string
	Message        string
	Details        string
	RecommendedFix string
	Suggestion     string
}

type crawlIssueExportGroupKey struct {
	Pillar    string
	Bucket    string
	IssueType string
}

type crawlIssueExportRowKey struct {
	Group crawlIssueExportGroupKey
	URL   string
}

// handleExportCrawlScoreBreakdownCSV exports one crawl's detailed issue rows as CSV.
func (a *App) handleExportCrawlScoreBreakdownCSV(w http.ResponseWriter, r *http.Request) {
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

	crawlIssues, err := queries.ListCrawlIssuesForCrawl(r.Context(), crawlID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	exportRows := buildCrawlIssueExportRows(crawlIssues)
	filename := "crawl-issues-" + crawlID.String() + ".csv"

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.WriteHeader(http.StatusOK)

	csvWriter := csv.NewWriter(w)
	defer csvWriter.Flush()

	if err := csvWriter.Write([]string{
		"PILLAR",
		"BUCKET",
		"ISSUE_TYPE",
		"ISSUE_LABEL",
		"SEVERITY",
		"TOTAL_AFFECTED",
		"URL",
		"MESSAGE",
		"DETAILS",
		"RECOMMENDED_FIX",
		"SUGGESTION",
	}); err != nil {
		return
	}

	for _, exportRow := range exportRows {
		if err := csvWriter.Write([]string{
			exportRow.Pillar,
			exportRow.Bucket,
			exportRow.IssueType,
			exportRow.IssueLabel,
			exportRow.Severity,
			strconv.Itoa(exportRow.TotalAffected),
			exportRow.URL,
			exportRow.Message,
			exportRow.Details,
			exportRow.RecommendedFix,
			exportRow.Suggestion,
		}); err != nil {
			return
		}
	}
}

// buildCrawlIssueExportRows flattens one crawl's issues into unique export rows per affected URL.
func buildCrawlIssueExportRows(crawlIssues []sqlc.ListCrawlIssuesForCrawlRow) []crawlIssueExportRow {
	rowsByKey := make(map[crawlIssueExportRowKey]crawlIssueExportRow)
	affectedURLsByGroup := make(map[crawlIssueExportGroupKey]map[string]struct{})

	for _, crawlIssue := range crawlIssues {
		url := strings.TrimSpace(crawlIssue.Url)
		if url == "" {
			continue
		}

		groupKey := crawlIssueExportGroupKey{
			Pillar:    crawlIssue.Pillar,
			Bucket:    crawlIssue.Bucket,
			IssueType: crawlIssue.IssueType,
		}
		if _, exists := affectedURLsByGroup[groupKey]; !exists {
			affectedURLsByGroup[groupKey] = make(map[string]struct{})
		}
		affectedURLsByGroup[groupKey][url] = struct{}{}

		rowKey := crawlIssueExportRowKey{Group: groupKey, URL: url}
		candidateRow := crawlIssueExportRow{
			Pillar:         issueshared.HumanizeIdentifier(crawlIssue.Pillar),
			Bucket:         issueshared.HumanizeIdentifier(crawlIssue.Bucket),
			IssueType:      crawlIssue.IssueType,
			IssueLabel:     issueshared.HumanizeIdentifier(crawlIssue.IssueType),
			Severity:       crawlIssue.Severity,
			URL:            url,
			Message:        crawlIssue.Message,
			Details:        crawlIssue.Details,
			RecommendedFix: issues.RecommendedFix(crawlIssue.Pillar, crawlIssue.Bucket, crawlIssue.IssueType, crawlIssue.Message, crawlIssue.Details),
		}

		existingRow, exists := rowsByKey[rowKey]
		if !exists || issueshared.SeverityRank(candidateRow.Severity) > issueshared.SeverityRank(existingRow.Severity) {
			rowsByKey[rowKey] = candidateRow
			continue
		}
		if existingRow.Message == "" && candidateRow.Message != "" {
			existingRow.Message = candidateRow.Message
		}
		if existingRow.Details == "" && candidateRow.Details != "" {
			existingRow.Details = candidateRow.Details
		}
		rowsByKey[rowKey] = existingRow
	}

	exportRows := make([]crawlIssueExportRow, 0, len(rowsByKey))
	for rowKey, exportRow := range rowsByKey {
		exportRow.TotalAffected = len(affectedURLsByGroup[rowKey.Group])
		exportRows = append(exportRows, exportRow)
	}

	sort.Slice(exportRows, func(leftIndex int, rightIndex int) bool {
		leftRow := exportRows[leftIndex]
		rightRow := exportRows[rightIndex]
		if pillarSortValue(leftRow.Pillar) != pillarSortValue(rightRow.Pillar) {
			return pillarSortValue(leftRow.Pillar) < pillarSortValue(rightRow.Pillar)
		}
		if leftRow.Bucket != rightRow.Bucket {
			return leftRow.Bucket < rightRow.Bucket
		}
		if leftRow.IssueLabel != rightRow.IssueLabel {
			return leftRow.IssueLabel < rightRow.IssueLabel
		}
		return leftRow.URL < rightRow.URL
	})

	return exportRows
}

// pillarSortValue keeps exported issue rows in the same pillar order as the Miller view.
func pillarSortValue(pillarLabel string) int {
	switch strings.TrimSpace(pillarLabel) {
	case "SEO":
		return 0
	case "AEO":
		return 1
	case "Pagespeed":
		return 2
	default:
		return 3
	}
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
