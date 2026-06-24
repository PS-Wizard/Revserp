package app

import (
	"encoding/csv"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const maxExportRows = 50000

// handleExportCrawlScoreBreakdownCSV exports one crawl's detailed issue rows as CSV.
func (a *App) handleExportCrawlScoreBreakdownCSV(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}
	filters := parseExportFilterParams(r)
	exportRows, ok := a.loadCrawlIssueExportRows(w, r, crawlID, filters)
	if !ok {
		return
	}

	filename := "crawl-issues-" + crawlID.String() + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"; filename*=UTF-8''"+url.PathEscape(filename))
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
			sanitizeSpreadsheetCell(exportRow.URL),
			sanitizeSpreadsheetCell(exportRow.Message),
			sanitizeSpreadsheetCell(exportRow.Details),
			sanitizeSpreadsheetCell(exportRow.RecommendedFix),
			sanitizeSpreadsheetCell(exportRow.Suggestion),
		}); err != nil {
			return
		}
	}
}

// handleExportCrawlScoreBreakdownXLSX exports one crawl's detailed issue rows as a styled workbook.
func (a *App) handleExportCrawlScoreBreakdownXLSX(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}
	filters := parseExportFilterParams(r)
	exportRows, ok := a.loadCrawlIssueExportRows(w, r, crawlID, filters)
	if !ok {
		return
	}

	workbookBuf, err := buildCrawlIssueExportWorkbook(exportRows)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "unable to build workbook")
		return
	}

	filename := "crawl-issues-" + crawlID.String() + ".xlsx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"; filename*=UTF-8''"+url.PathEscape(filename))
	w.WriteHeader(http.StatusOK)
	_, _ = workbookBuf.WriteTo(w)
}

func (a *App) loadCrawlIssueExportRows(w http.ResponseWriter, r *http.Request, crawlID pgtype.UUID, filters exportFilters) ([]crawlIssueExportRow, bool) {
	var exportRows []crawlIssueExportRow
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		user, _, err := a.ensureCurrentUser(r, queries)
		if err != nil {
			serverError(w, r, err)
			return err
		}

		if _, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "crawl not found")
				return err
			}
			serverError(w, r, err)
			return err
		}

		crawlIssues, err := queries.ListCrawlIssuesForCrawl(r.Context(), sqlc.ListCrawlIssuesForCrawlParams{CrawlID: crawlID, Limit: maxExportRows})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		crawlIssues = filterCrawlIssues(crawlIssues, filters)

		exportRows = buildCrawlIssueExportRows(crawlIssues)
		return nil
	}) {
		return nil, false
	}

	return exportRows, true
}

// exportFilters holds optional query filters for exporting only selected issue scopes.
type exportFilters struct {
	pillarIDs     []string // raw pillar identifiers, e.g. seo, aeo, pagespeed
	bucketKeys    []string // composite "pillarId::bucketId", e.g. seo::meta_tags
	issueTypeKeys []string // composite "pillarId::bucketId::issueTypeId"
}

// parseExportFilterParams reads optional filter query parameters from the export request.
func parseExportFilterParams(r *http.Request) exportFilters {
	var f exportFilters
	if s := r.URL.Query().Get("pillar_ids"); s != "" {
		for id := range strings.SplitSeq(s, ",") {
			if id = strings.TrimSpace(id); id != "" {
				f.pillarIDs = append(f.pillarIDs, id)
			}
		}
	}
	if s := r.URL.Query().Get("bucket_keys"); s != "" {
		for key := range strings.SplitSeq(s, ",") {
			if key = strings.TrimSpace(key); key != "" {
				f.bucketKeys = append(f.bucketKeys, key)
			}
		}
	}
	if s := r.URL.Query().Get("issue_type_keys"); s != "" {
		for key := range strings.SplitSeq(s, ",") {
			if key = strings.TrimSpace(key); key != "" {
				f.issueTypeKeys = append(f.issueTypeKeys, key)
			}
		}
	}
	return f
}

// hasAny returns true when at least one filter category is populated.
func (f exportFilters) hasAny() bool {
	return len(f.pillarIDs) > 0 || len(f.bucketKeys) > 0 || len(f.issueTypeKeys) > 0
}

// filterCrawlIssues applies export filters to raw crawl issue rows.
// Filter hierarchy: issue_type_keys > bucket_keys > pillar_ids.
// When multiple categories are provided, the most specific takes precedence.
func filterCrawlIssues(rows []sqlc.ListCrawlIssuesForCrawlRow, filters exportFilters) []sqlc.ListCrawlIssuesForCrawlRow {
	if !filters.hasAny() {
		return rows
	}

	issueTypeSet := make(map[string]struct{}, len(filters.issueTypeKeys))
	for _, k := range filters.issueTypeKeys {
		issueTypeSet[k] = struct{}{}
	}
	bucketSet := make(map[string]struct{}, len(filters.bucketKeys))
	for _, k := range filters.bucketKeys {
		bucketSet[k] = struct{}{}
	}
	pillarSet := make(map[string]struct{}, len(filters.pillarIDs))
	for _, id := range filters.pillarIDs {
		pillarSet[id] = struct{}{}
	}

	filtered := make([]sqlc.ListCrawlIssuesForCrawlRow, 0, len(rows))
	for _, row := range rows {
		compositeKey := row.Pillar + "::" + row.Bucket + "::" + row.IssueType
		bucketKey := row.Pillar + "::" + row.Bucket

		switch {
		case len(issueTypeSet) > 0:
			if _, ok := issueTypeSet[compositeKey]; ok {
				filtered = append(filtered, row)
			}
		case len(bucketSet) > 0:
			if _, ok := bucketSet[bucketKey]; ok {
				filtered = append(filtered, row)
			}
		default:
			if _, ok := pillarSet[row.Pillar]; ok {
				filtered = append(filtered, row)
			}
		}
	}
	return filtered
}
