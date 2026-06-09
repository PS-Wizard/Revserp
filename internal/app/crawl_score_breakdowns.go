package app

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/xuri/excelize/v2"

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
	Pillar         string
	Bucket         string
	IssueType      string
	IssueLabel     string
	Severity       string
	TotalAffected  int
	RecommendedFix string
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

// handleExportCrawlScoreBreakdownXLSX exports one crawl's detailed issue rows as a styled workbook.
func (a *App) handleExportCrawlScoreBreakdownXLSX(w http.ResponseWriter, r *http.Request) {
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

type crawlIssueWorkbookStyles struct {
	title               int
	subtitle            int
	header              int
	body                int
	bodyWrapped         int
	url                 int
	severityHigh        int
	severityMedium      int
	severityLow         int
	severityDefault     int
	issueLabelSEO       int
	issueLabelAEO       int
	issueLabelPageSpeed int
	issueLabelDefault   int
	recommendation      int
	summaryCount        int
	relatedURLs         int
	tableNote           int
}

func buildCrawlIssueExportWorkbook(crawlID string, exportRows []crawlIssueExportRow) ([]byte, error) {
	workbook := excelize.NewFile()
	overviewSheetName := "Overview"
	workbook.SetSheetName(workbook.GetSheetName(0), overviewSheetName)

	styles, err := newCrawlIssueWorkbookStyles(workbook)
	if err != nil {
		return nil, err
	}

	overviewRows := buildCrawlIssueOverviewRows(exportRows)
	duplicatePeerRows := buildDuplicatePeerExportRows(exportRows)

	if err := writeOverviewSheet(workbook, overviewSheetName, styles, crawlID, overviewRows); err != nil {
		return nil, err
	}
	if err := writeDetailedIssuesSheet(workbook, "All Issues", styles, crawlID, exportRows); err != nil {
		return nil, err
	}
	if err := workbook.SetSheetProps(overviewSheetName, &excelize.SheetPropsOptions{TabColorRGB: stringPointer("141414")}); err != nil {
		return nil, err
	}

	pillarSheets := []struct {
		name   string
		pillar string
		color  string
	}{
		{name: "SEO Issues", pillar: "SEO", color: "141414"},
		{name: "AEO Issues", pillar: "AEO", color: "2563EB"},
		{name: "PageSpeed Issues", pillar: "Pagespeed", color: "0F766E"},
	}
	for _, pillarSheet := range pillarSheets {
		pillarRows := filterExportRowsByPillar(exportRows, pillarSheet.pillar)
		if len(pillarRows) == 0 {
			continue
		}
		if _, err := workbook.NewSheet(pillarSheet.name); err != nil {
			return nil, err
		}
		if err := writeDetailedIssuesSheet(workbook, pillarSheet.name, styles, crawlID, pillarRows); err != nil {
			return nil, err
		}
		if err := workbook.SetSheetProps(pillarSheet.name, &excelize.SheetPropsOptions{TabColorRGB: stringPointer(pillarSheet.color)}); err != nil {
			return nil, err
		}
	}

	if len(duplicatePeerRows) > 0 {
		duplicateSheetName := "Duplicate Peers"
		if _, err := workbook.NewSheet(duplicateSheetName); err != nil {
			return nil, err
		}
		if err := writeDuplicatePeerSheet(workbook, duplicateSheetName, styles, crawlID, duplicatePeerRows); err != nil {
			return nil, err
		}
		if err := workbook.SetSheetProps(duplicateSheetName, &excelize.SheetPropsOptions{TabColorRGB: stringPointer("7C3AED")}); err != nil {
			return nil, err
		}
	}

	workbook.SetActiveSheet(0)
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func newCrawlIssueWorkbookStyles(workbook *excelize.File) (crawlIssueWorkbookStyles, error) {
	styles := crawlIssueWorkbookStyles{}
	var err error

	if styles.title, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 18, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"141414"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center"},
	}); err != nil {
		return styles, err
	}

	if styles.subtitle, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Italic: true, Size: 11, Color: "514C45"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"F4F2EC"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center"},
	}); err != nil {
		return styles, err
	}

	if styles.header, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"141414"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border:    []excelize.Border{{Type: "bottom", Color: "D6D3CE", Style: 1}},
	}); err != nil {
		return styles, err
	}

	if styles.body, err = workbook.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "top"},
	}); err != nil {
		return styles, err
	}

	if styles.bodyWrapped, err = workbook.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	}); err != nil {
		return styles, err
	}

	if styles.url, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Color: "2563EB", Underline: "single"},
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	}); err != nil {
		return styles, err
	}

	if styles.relatedURLs, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Color: "374151"},
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	}); err != nil {
		return styles, err
	}

	if styles.recommendation, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Color: "111827"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"F4F2EC"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	}); err != nil {
		return styles, err
	}

	if styles.summaryCount, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "111827"},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	}); err != nil {
		return styles, err
	}

	if styles.tableNote, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Italic: true, Color: "6B7280"},
		Alignment: &excelize.Alignment{Vertical: "center"},
	}); err != nil {
		return styles, err
	}

	if styles.severityHigh, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "7F1D1D"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"FEE2E2"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	}); err != nil {
		return styles, err
	}

	if styles.severityMedium, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "92400E"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"FEF3C7"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	}); err != nil {
		return styles, err
	}

	if styles.severityLow, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "155E75"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"CFFAFE"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	}); err != nil {
		return styles, err
	}

	if styles.severityDefault, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "374151"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"E5E7EB"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	}); err != nil {
		return styles, err
	}

	if styles.issueLabelSEO, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"141414"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
	}); err != nil {
		return styles, err
	}

	if styles.issueLabelAEO, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"2563EB"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
	}); err != nil {
		return styles, err
	}

	if styles.issueLabelPageSpeed, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"0F766E"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
	}); err != nil {
		return styles, err
	}

	if styles.issueLabelDefault, err = workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "111827"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"E5E7EB"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
	}); err != nil {
		return styles, err
	}

	return styles, nil
}

func writeOverviewSheet(workbook *excelize.File, sheetName string, styles crawlIssueWorkbookStyles, crawlID string, overviewRows []crawlIssueOverviewRow) error {
	headers := []string{"PILLAR", "BUCKET", "ISSUE_TYPE", "ISSUE_LABEL", "SEVERITY", "TOTAL_AFFECTED", "RECOMMENDED_FIX"}
	if err := writeSheetScaffold(workbook, sheetName, styles, "revserp issue export overview", fmt.Sprintf("crawl %s · grouped summary with deterministic fix guidance", crawlID), len(headers)); err != nil {
		return err
	}
	if err := setHeaderRow(workbook, sheetName, headers, styles.header); err != nil {
		return err
	}
	if len(overviewRows) == 0 {
		if err := workbook.SetCellValue(sheetName, "A4", "No crawl issues are available for export yet."); err != nil {
			return err
		}
		if err := workbook.SetCellStyle(sheetName, "A4", "A4", styles.tableNote); err != nil {
			return err
		}
		return setOverviewSheetLayout(workbook, sheetName)
	}
	for rowIndex, overviewRow := range overviewRows {
		excelRow := rowIndex + 4
		if err := workbook.SetCellValue(sheetName, cellName(1, excelRow), overviewRow.Pillar); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(2, excelRow), overviewRow.Bucket); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(3, excelRow), overviewRow.IssueType); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(4, excelRow), overviewRow.IssueLabel); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(5, excelRow), strings.ToUpper(overviewRow.Severity)); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(6, excelRow), overviewRow.TotalAffected); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(7, excelRow), overviewRow.RecommendedFix); err != nil {
			return err
		}
		if err := applyOverviewRowStyles(workbook, sheetName, styles, overviewRow, excelRow); err != nil {
			return err
		}
	}
	if err := addStyledTable(workbook, sheetName, 7, len(overviewRows)+3, "overview"); err != nil {
		return err
	}
	return setOverviewSheetLayout(workbook, sheetName)
}

func writeDetailedIssuesSheet(workbook *excelize.File, sheetName string, styles crawlIssueWorkbookStyles, crawlID string, exportRows []crawlIssueExportRow) error {
	headers := []string{"PILLAR", "BUCKET", "ISSUE_TYPE", "ISSUE_LABEL", "SEVERITY", "TOTAL_AFFECTED", "URL", "MESSAGE", "DETAILS", "RELATED_URLS", "RECOMMENDED_FIX", "SUGGESTION"}
	if err := writeSheetScaffold(workbook, sheetName, styles, sheetName, fmt.Sprintf("crawl %s · one row per affected URL", crawlID), len(headers)); err != nil {
		return err
	}
	if err := setHeaderRow(workbook, sheetName, headers, styles.header); err != nil {
		return err
	}
	if len(exportRows) == 0 {
		if err := workbook.SetCellValue(sheetName, "A4", "No crawl issues are available for this sheet."); err != nil {
			return err
		}
		if err := workbook.SetCellStyle(sheetName, "A4", "A4", styles.tableNote); err != nil {
			return err
		}
		return setDetailedSheetLayout(workbook, sheetName)
	}
	for rowIndex, exportRow := range exportRows {
		excelRow := rowIndex + 4
		if err := workbook.SetCellValue(sheetName, cellName(1, excelRow), exportRow.Pillar); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(2, excelRow), exportRow.Bucket); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(3, excelRow), exportRow.IssueType); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(4, excelRow), exportRow.IssueLabel); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(5, excelRow), strings.ToUpper(exportRow.Severity)); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(6, excelRow), exportRow.TotalAffected); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(7, excelRow), exportRow.URL); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(8, excelRow), exportRow.Message); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(9, excelRow), formatDetailTextForWorkbook(exportRow.Details)); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(10, excelRow), strings.Join(exportRow.RelatedURLs, "\n")); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(11, excelRow), exportRow.RecommendedFix); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(12, excelRow), exportRow.Suggestion); err != nil {
			return err
		}
		if err := applyDetailedRowStyles(workbook, sheetName, styles, exportRow, excelRow); err != nil {
			return err
		}
	}
	if err := addStyledTable(workbook, sheetName, 12, len(exportRows)+3, sanitizeTableName(sheetName)); err != nil {
		return err
	}
	return setDetailedSheetLayout(workbook, sheetName)
}

func writeDuplicatePeerSheet(workbook *excelize.File, sheetName string, styles crawlIssueWorkbookStyles, crawlID string, duplicatePeerRows []duplicatePeerExportRow) error {
	headers := []string{"PILLAR", "BUCKET", "ISSUE_TYPE", "ISSUE_LABEL", "SEVERITY", "SOURCE_URL", "RELATED_URL", "MESSAGE", "DETAILS"}
	if err := writeSheetScaffold(workbook, sheetName, styles, "Duplicate content references", fmt.Sprintf("crawl %s · extracted related URLs for duplicate-content-style issues", crawlID), len(headers)); err != nil {
		return err
	}
	if err := setHeaderRow(workbook, sheetName, headers, styles.header); err != nil {
		return err
	}
	for rowIndex, duplicatePeerRow := range duplicatePeerRows {
		excelRow := rowIndex + 4
		if err := workbook.SetCellValue(sheetName, cellName(1, excelRow), duplicatePeerRow.Pillar); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(2, excelRow), duplicatePeerRow.Bucket); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(3, excelRow), duplicatePeerRow.IssueType); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(4, excelRow), duplicatePeerRow.IssueLabel); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(5, excelRow), strings.ToUpper(duplicatePeerRow.Severity)); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(6, excelRow), duplicatePeerRow.SourceURL); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(7, excelRow), duplicatePeerRow.RelatedURL); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(8, excelRow), duplicatePeerRow.Message); err != nil {
			return err
		}
		if err := workbook.SetCellValue(sheetName, cellName(9, excelRow), formatDetailTextForWorkbook(duplicatePeerRow.Details)); err != nil {
			return err
		}
		if err := applyDuplicatePeerRowStyles(workbook, sheetName, styles, duplicatePeerRow, excelRow); err != nil {
			return err
		}
	}
	if len(duplicatePeerRows) > 0 {
		if err := addStyledTable(workbook, sheetName, 9, len(duplicatePeerRows)+3, "duplicatePeers"); err != nil {
			return err
		}
	}
	return setDuplicatePeerSheetLayout(workbook, sheetName)
}

func writeSheetScaffold(workbook *excelize.File, sheetName string, styles crawlIssueWorkbookStyles, title string, subtitle string, columnCount int) error {
	lastColumnName, err := excelize.ColumnNumberToName(columnCount)
	if err != nil {
		return err
	}
	if err := workbook.MergeCell(sheetName, "A1", lastColumnName+"1"); err != nil {
		return err
	}
	if err := workbook.MergeCell(sheetName, "A2", lastColumnName+"2"); err != nil {
		return err
	}
	if err := workbook.SetCellValue(sheetName, "A1", title); err != nil {
		return err
	}
	if err := workbook.SetCellValue(sheetName, "A2", subtitle); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, "A1", lastColumnName+"1", styles.title); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, "A2", lastColumnName+"2", styles.subtitle); err != nil {
		return err
	}
	if err := workbook.SetRowHeight(sheetName, 1, 28); err != nil {
		return err
	}
	if err := workbook.SetRowHeight(sheetName, 2, 22); err != nil {
		return err
	}
	return workbook.SetPanes(sheetName, &excelize.Panes{Freeze: true, YSplit: 3, TopLeftCell: "A4", ActivePane: "bottomLeft"})
}

func setHeaderRow(workbook *excelize.File, sheetName string, headers []string, headerStyle int) error {
	for columnIndex, header := range headers {
		cellReference := cellName(columnIndex+1, 3)
		if err := workbook.SetCellValue(sheetName, cellReference, header); err != nil {
			return err
		}
	}
	lastHeaderCell := cellName(len(headers), 3)
	if err := workbook.SetCellStyle(sheetName, "A3", lastHeaderCell, headerStyle); err != nil {
		return err
	}
	return workbook.SetRowHeight(sheetName, 3, 24)
}

func addStyledTable(workbook *excelize.File, sheetName string, columnCount int, endRow int, tableName string) error {
	lastColumnName, err := excelize.ColumnNumberToName(columnCount)
	if err != nil {
		return err
	}
	return workbook.AddTable(sheetName, &excelize.Table{
		Range:          fmt.Sprintf("A3:%s%d", lastColumnName, endRow),
		Name:           sanitizeTableName(tableName),
		StyleName:      "TableStyleMedium2",
		ShowRowStripes: boolPointer(true),
	})
}

func setOverviewSheetLayout(workbook *excelize.File, sheetName string) error {
	widths := map[string]float64{"A": 14, "B": 22, "C": 28, "D": 28, "E": 12, "F": 14, "G": 58}
	for column, width := range widths {
		if err := workbook.SetColWidth(sheetName, column, column, width); err != nil {
			return err
		}
	}
	return nil
}

func setDetailedSheetLayout(workbook *excelize.File, sheetName string) error {
	widths := map[string]float64{"A": 14, "B": 22, "C": 28, "D": 28, "E": 12, "F": 14, "G": 42, "H": 34, "I": 52, "J": 42, "K": 58, "L": 24}
	for column, width := range widths {
		if err := workbook.SetColWidth(sheetName, column, column, width); err != nil {
			return err
		}
	}
	return nil
}

func setDuplicatePeerSheetLayout(workbook *excelize.File, sheetName string) error {
	widths := map[string]float64{"A": 14, "B": 22, "C": 28, "D": 28, "E": 12, "F": 40, "G": 40, "H": 32, "I": 52}
	for column, width := range widths {
		if err := workbook.SetColWidth(sheetName, column, column, width); err != nil {
			return err
		}
	}
	return nil
}

func applyOverviewRowStyles(workbook *excelize.File, sheetName string, styles crawlIssueWorkbookStyles, overviewRow crawlIssueOverviewRow, rowIndex int) error {
	if err := workbook.SetCellStyle(sheetName, cellName(1, rowIndex), cellName(3, rowIndex), styles.body); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(4, rowIndex), cellName(4, rowIndex), issueLabelStyle(styles, overviewRow.Pillar)); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(5, rowIndex), cellName(5, rowIndex), severityStyle(styles, overviewRow.Severity)); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(6, rowIndex), cellName(6, rowIndex), styles.summaryCount); err != nil {
		return err
	}
	return workbook.SetCellStyle(sheetName, cellName(7, rowIndex), cellName(7, rowIndex), styles.recommendation)
}

func applyDetailedRowStyles(workbook *excelize.File, sheetName string, styles crawlIssueWorkbookStyles, exportRow crawlIssueExportRow, rowIndex int) error {
	if err := workbook.SetCellStyle(sheetName, cellName(1, rowIndex), cellName(3, rowIndex), styles.body); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(4, rowIndex), cellName(4, rowIndex), issueLabelStyle(styles, exportRow.Pillar)); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(5, rowIndex), cellName(5, rowIndex), severityStyle(styles, exportRow.Severity)); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(6, rowIndex), cellName(6, rowIndex), styles.summaryCount); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(7, rowIndex), cellName(7, rowIndex), styles.url); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(8, rowIndex), cellName(9, rowIndex), styles.bodyWrapped); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(10, rowIndex), cellName(10, rowIndex), styles.relatedURLs); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(11, rowIndex), cellName(11, rowIndex), styles.recommendation); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(12, rowIndex), cellName(12, rowIndex), styles.bodyWrapped); err != nil {
		return err
	}
	if err := workbook.SetCellHyperLink(sheetName, cellName(7, rowIndex), exportRow.URL, "External"); err != nil {
		return err
	}
	if len(exportRow.RelatedURLs) == 1 {
		if err := workbook.SetCellHyperLink(sheetName, cellName(10, rowIndex), exportRow.RelatedURLs[0], "External"); err != nil {
			return err
		}
		if err := workbook.SetCellStyle(sheetName, cellName(10, rowIndex), cellName(10, rowIndex), styles.url); err != nil {
			return err
		}
	}
	return workbook.SetRowHeight(sheetName, rowIndex, 48)
}

func applyDuplicatePeerRowStyles(workbook *excelize.File, sheetName string, styles crawlIssueWorkbookStyles, duplicatePeerRow duplicatePeerExportRow, rowIndex int) error {
	if err := workbook.SetCellStyle(sheetName, cellName(1, rowIndex), cellName(3, rowIndex), styles.body); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(4, rowIndex), cellName(4, rowIndex), issueLabelStyle(styles, duplicatePeerRow.Pillar)); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(5, rowIndex), cellName(5, rowIndex), severityStyle(styles, duplicatePeerRow.Severity)); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(6, rowIndex), cellName(7, rowIndex), styles.url); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheetName, cellName(8, rowIndex), cellName(9, rowIndex), styles.bodyWrapped); err != nil {
		return err
	}
	if err := workbook.SetCellHyperLink(sheetName, cellName(6, rowIndex), duplicatePeerRow.SourceURL, "External"); err != nil {
		return err
	}
	if err := workbook.SetCellHyperLink(sheetName, cellName(7, rowIndex), duplicatePeerRow.RelatedURL, "External"); err != nil {
		return err
	}
	return workbook.SetRowHeight(sheetName, rowIndex, 42)
}

func buildCrawlIssueOverviewRows(exportRows []crawlIssueExportRow) []crawlIssueOverviewRow {
	overviewRowsByGroup := make(map[crawlIssueExportGroupKey]crawlIssueOverviewRow)
	for _, exportRow := range exportRows {
		groupKey := crawlIssueExportGroupKey{Pillar: exportRow.Pillar, Bucket: exportRow.Bucket, IssueType: exportRow.IssueType}
		overviewRow, exists := overviewRowsByGroup[groupKey]
		if !exists || issueshared.SeverityRank(exportRow.Severity) > issueshared.SeverityRank(overviewRow.Severity) {
			overviewRowsByGroup[groupKey] = crawlIssueOverviewRow{
				Pillar: exportRow.Pillar, Bucket: exportRow.Bucket, IssueType: exportRow.IssueType, IssueLabel: exportRow.IssueLabel, Severity: exportRow.Severity, TotalAffected: exportRow.TotalAffected, RecommendedFix: exportRow.RecommendedFix,
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

func issueLabelStyle(styles crawlIssueWorkbookStyles, pillar string) int {
	switch strings.TrimSpace(pillar) {
	case "SEO":
		return styles.issueLabelSEO
	case "AEO":
		return styles.issueLabelAEO
	case "Pagespeed":
		return styles.issueLabelPageSpeed
	default:
		return styles.issueLabelDefault
	}
}

func severityStyle(styles crawlIssueWorkbookStyles, severity string) int {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "high":
		return styles.severityHigh
	case "medium":
		return styles.severityMedium
	case "low":
		return styles.severityLow
	default:
		return styles.severityDefault
	}
}

func formatDetailTextForWorkbook(details string) string {
	formattedDetails := strings.TrimSpace(details)
	for _, relatedURL := range extractURLsFromText(formattedDetails) {
		formattedDetails = strings.ReplaceAll(formattedDetails, relatedURL, "\n"+relatedURL)
	}
	return strings.TrimSpace(formattedDetails)
}

func sanitizeTableName(value string) string {
	var sanitizedBuilder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			sanitizedBuilder.WriteRune(character)
		}
	}
	sanitizedValue := sanitizedBuilder.String()
	if sanitizedValue == "" {
		return "IssueTable"
	}
	if sanitizedValue[0] >= '0' && sanitizedValue[0] <= '9' {
		return "T" + sanitizedValue
	}
	return sanitizedValue
}

func stringPointer(value string) *string {
	return &value
}

func boolPointer(value bool) *bool {
	return &value
}

func cellName(column int, row int) string {
	columnName, _ := excelize.ColumnNumberToName(column)
	return fmt.Sprintf("%s%d", columnName, row)
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
