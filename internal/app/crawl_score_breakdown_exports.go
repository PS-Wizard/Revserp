package app

import (
	"encoding/csv"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

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
	RelatedURLs    []string
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

type crawlIssueOverviewRow struct {
	Pillar        string
	Bucket        string
	IssueType     string
	IssueLabel    string
	Severity      string
	TotalAffected int
}

type duplicatePeerExportRow struct {
	Pillar     string
	Bucket     string
	IssueType  string
	IssueLabel string
	SourceURL  string
	RelatedURL string
	Message    string
	Details    string
	Severity   string
}

var issueDetailURLPattern = regexp.MustCompile(`https?://[^\s,]+`)

// handleExportCrawlScoreBreakdownCSV exports one crawl's detailed issue rows as CSV.
func (a *App) handleExportCrawlScoreBreakdownCSV(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	exportRows, ok := a.loadCrawlIssueExportRows(w, r, crawlID)
	if !ok {
		return
	}

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

// handleExportCrawlScoreBreakdownXLSX exports one crawl's detailed issue rows as a styled workbook.
func (a *App) handleExportCrawlScoreBreakdownXLSX(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	exportRows, ok := a.loadCrawlIssueExportRows(w, r, crawlID)
	if !ok {
		return
	}

	workbookBytes, err := buildCrawlIssueExportWorkbook(crawlID.String(), exportRows)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "unable to build workbook")
		return
	}

	filename := "crawl-issues-" + crawlID.String() + ".xlsx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(workbookBytes)
}

func (a *App) loadCrawlIssueExportRows(w http.ResponseWriter, r *http.Request, crawlID pgtype.UUID) ([]crawlIssueExportRow, bool) {
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

		crawlIssues, err := queries.ListCrawlIssuesForCrawl(r.Context(), crawlID)
		if err != nil {
			serverError(w, r, err)
			return err
		}

		exportRows = buildCrawlIssueExportRows(crawlIssues)
		return nil
	}) {
		return nil, false
	}

	return exportRows, true
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
			RelatedURLs:    extractURLsFromText(crawlIssue.Details),
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
		if len(existingRow.RelatedURLs) == 0 && len(candidateRow.RelatedURLs) > 0 {
			existingRow.RelatedURLs = candidateRow.RelatedURLs
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

func buildCrawlIssueOverviewRows(exportRows []crawlIssueExportRow) []crawlIssueOverviewRow {
	overviewRowsByGroup := make(map[crawlIssueExportGroupKey]crawlIssueOverviewRow)
	for _, exportRow := range exportRows {
		groupKey := crawlIssueExportGroupKey{Pillar: exportRow.Pillar, Bucket: exportRow.Bucket, IssueType: exportRow.IssueType}
		overviewRow, exists := overviewRowsByGroup[groupKey]
		if !exists || issueshared.SeverityRank(exportRow.Severity) > issueshared.SeverityRank(overviewRow.Severity) {
			overviewRowsByGroup[groupKey] = crawlIssueOverviewRow{
				Pillar: exportRow.Pillar, Bucket: exportRow.Bucket, IssueType: exportRow.IssueType, IssueLabel: exportRow.IssueLabel, Severity: exportRow.Severity, TotalAffected: exportRow.TotalAffected,
			}
			continue
		}
		if overviewRow.TotalAffected < exportRow.TotalAffected {
			overviewRow.TotalAffected = exportRow.TotalAffected
			overviewRowsByGroup[groupKey] = overviewRow
		}
	}
	overviewRows := make([]crawlIssueOverviewRow, 0, len(overviewRowsByGroup))
	for _, overviewRow := range overviewRowsByGroup {
		overviewRows = append(overviewRows, overviewRow)
	}
	sort.Slice(overviewRows, func(leftIndex int, rightIndex int) bool {
		leftRow := overviewRows[leftIndex]
		rightRow := overviewRows[rightIndex]
		if pillarSortValue(leftRow.Pillar) != pillarSortValue(rightRow.Pillar) {
			return pillarSortValue(leftRow.Pillar) < pillarSortValue(rightRow.Pillar)
		}
		if leftRow.Bucket != rightRow.Bucket {
			return leftRow.Bucket < rightRow.Bucket
		}
		if leftRow.TotalAffected != rightRow.TotalAffected {
			return leftRow.TotalAffected > rightRow.TotalAffected
		}
		return leftRow.IssueLabel < rightRow.IssueLabel
	})
	return overviewRows
}

func buildDuplicatePeerExportRows(exportRows []crawlIssueExportRow) []duplicatePeerExportRow {
	duplicatePeerRows := make([]duplicatePeerExportRow, 0)
	for _, exportRow := range exportRows {
		if len(exportRow.RelatedURLs) == 0 {
			continue
		}
		for _, relatedURL := range exportRow.RelatedURLs {
			duplicatePeerRows = append(duplicatePeerRows, duplicatePeerExportRow{
				Pillar: exportRow.Pillar, Bucket: exportRow.Bucket, IssueType: exportRow.IssueType, IssueLabel: exportRow.IssueLabel, Severity: exportRow.Severity, SourceURL: exportRow.URL, RelatedURL: relatedURL, Message: exportRow.Message, Details: exportRow.Details,
			})
		}
	}
	sort.Slice(duplicatePeerRows, func(leftIndex int, rightIndex int) bool {
		leftRow := duplicatePeerRows[leftIndex]
		rightRow := duplicatePeerRows[rightIndex]
		if pillarSortValue(leftRow.Pillar) != pillarSortValue(rightRow.Pillar) {
			return pillarSortValue(leftRow.Pillar) < pillarSortValue(rightRow.Pillar)
		}
		if leftRow.IssueLabel != rightRow.IssueLabel {
			return leftRow.IssueLabel < rightRow.IssueLabel
		}
		if leftRow.SourceURL != rightRow.SourceURL {
			return leftRow.SourceURL < rightRow.SourceURL
		}
		return leftRow.RelatedURL < rightRow.RelatedURL
	})
	return duplicatePeerRows
}

func filterExportRowsByPillar(exportRows []crawlIssueExportRow, pillar string) []crawlIssueExportRow {
	filteredRows := make([]crawlIssueExportRow, 0)
	for _, exportRow := range exportRows {
		if exportRow.Pillar != pillar {
			continue
		}
		filteredRows = append(filteredRows, exportRow)
	}
	return filteredRows
}

func formatDetailTextForWorkbook(details string) string {
	formattedDetails := strings.TrimSpace(details)
	for _, relatedURL := range extractURLsFromText(formattedDetails) {
		formattedDetails = strings.ReplaceAll(formattedDetails, relatedURL, "\n"+relatedURL)
	}
	return strings.TrimSpace(formattedDetails)
}

func extractURLsFromText(value string) []string {
	matches := issueDetailURLPattern.FindAllString(value, -1)
	if len(matches) == 0 {
		return nil
	}

	urls := make([]string, 0, len(matches))
	seenURLs := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		normalizedURL := strings.TrimRight(strings.TrimSpace(match), ".,;)")
		if normalizedURL == "" {
			continue
		}
		if _, exists := seenURLs[normalizedURL]; exists {
			continue
		}
		seenURLs[normalizedURL] = struct{}{}
		urls = append(urls, normalizedURL)
	}
	return urls
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
