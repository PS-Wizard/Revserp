package app

import (
	"fmt"

	"github.com/xuri/excelize/v2"
)

// workbookStyles holds pre-registered style IDs for the export workbook.
type workbookStyles struct {
	header         int
	evenRow        int
	oddRow         int
	severityHigh   int
	severityMedium int
	severityLow    int
}

func registerWorkbookStyles(f *excelize.File) (workbookStyles, error) {
	s := workbookStyles{}
	var err error

	// Header — dark slate fill, white bold text, centered, medium bottom border.
	s.header, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Size: 11, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"1E293B"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Border: []excelize.Border{
			{Type: "bottom", Color: "334155", Style: 2},
		},
	})
	if err != nil {
		return s, err
	}

	// Even data row — white background, subtle bottom border.
	s.evenRow, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Size: 10, Color: "1E293B"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"FFFFFF"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center", WrapText: true},
		Border: []excelize.Border{
			{Type: "bottom", Color: "E2E8F0", Style: 1},
		},
	})
	if err != nil {
		return s, err
	}

	// Odd data row — very light slate zebra stripe.
	s.oddRow, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Size: 10, Color: "1E293B"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"F8FAFC"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center", WrapText: true},
		Border: []excelize.Border{
			{Type: "bottom", Color: "E2E8F0", Style: 1},
		},
	})
	if err != nil {
		return s, err
	}

	// Severity High — red badge.
	s.severityHigh, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Size: 10, Color: "991B1B", Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"FEE2E2"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "bottom", Color: "E2E8F0", Style: 1},
		},
	})
	if err != nil {
		return s, err
	}

	// Severity Medium — amber badge.
	s.severityMedium, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Size: 10, Color: "92400E", Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"FEF3C7"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "bottom", Color: "E2E8F0", Style: 1},
		},
	})
	if err != nil {
		return s, err
	}

	// Severity Low — green badge.
	s.severityLow, err = f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Size: 10, Color: "065F46", Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"D1FAE5"}, Pattern: 1},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
		Border: []excelize.Border{
			{Type: "bottom", Color: "E2E8F0", Style: 1},
		},
	})
	if err != nil {
		return s, err
	}

	return s, nil
}

// severityStyle returns the style ID for a severity level string.
func severityStyle(severity string, styles workbookStyles) int {
	switch severity {
	case "High":
		return styles.severityHigh
	case "Medium":
		return styles.severityMedium
	case "Low":
		return styles.severityLow
	default:
		return styles.evenRow
	}
}

// applyDataRowStyling applies zebra stripe + severity badge styling to one data row.
// rowIndex is 0-based among data rows (header is row 1).
func applyDataRowStyling(f *excelize.File, sheet string, rowIndex int, colCount int, severity string, styles workbookStyles, severityCol int) {
	baseStyle := styles.evenRow
	if rowIndex%2 == 1 {
		baseStyle = styles.oddRow
	}

	startCell, _ := excelize.CoordinatesToCellName(1, rowIndex+2)
	endCell, _ := excelize.CoordinatesToCellName(colCount, rowIndex+2)
	_ = f.SetCellStyle(sheet, startCell, endCell, baseStyle)

	sevCell, _ := excelize.CoordinatesToCellName(severityCol, rowIndex+2)
	_ = f.SetCellStyle(sheet, sevCell, sevCell, severityStyle(severity, styles))
}

// applyHeaderRowStyling applies the header style to the first row of a sheet.
func applyHeaderRowStyling(f *excelize.File, sheet string, colCount int, styles workbookStyles) {
	startCell, _ := excelize.CoordinatesToCellName(1, 1)
	endCell, _ := excelize.CoordinatesToCellName(colCount, 1)
	_ = f.SetCellStyle(sheet, startCell, endCell, styles.header)
}

// setOverviewColumnWidths sets widths for the Overview sheet columns.
func setOverviewColumnWidths(f *excelize.File, sheet string) {
	widths := map[string]float64{
		"A": 12, // PILLAR
		"B": 18, // BUCKET
		"C": 26, // ISSUE_TYPE
		"D": 14, // SEVERITY
		"E": 17, // TOTAL_AFFECTED
	}
	for col, w := range widths {
		_ = f.SetColWidth(sheet, col, col, w)
	}
}

// setIssuesSheetColumnWidths sets widths for issue listing sheets.
func setIssuesSheetColumnWidths(f *excelize.File, sheet string) {
	widths := map[string]float64{
		"A": 11, // PILLAR
		"B": 16, // BUCKET
		"C": 22, // ISSUE_TYPE
		"D": 13, // SEVERITY
		"E": 16, // TOTAL_AFFECTED
		"F": 42, // URL
		"G": 38, // MESSAGE
		"H": 46, // DETAILS
	}
	for col, w := range widths {
		_ = f.SetColWidth(sheet, col, col, w)
	}
}

// setDuplicatesSheetColumnWidths sets widths for the Duplicate Peers sheet.
func setDuplicatesSheetColumnWidths(f *excelize.File, sheet string) {
	widths := map[string]float64{
		"A": 11, // PILLAR
		"B": 16, // BUCKET
		"C": 22, // ISSUE_TYPE
		"D": 42, // SOURCE_URL
		"E": 42, // RELATED_URL
		"F": 38, // MESSAGE
		"G": 46, // DETAILS
		"H": 13, // SEVERITY
	}
	for col, w := range widths {
		_ = f.SetColWidth(sheet, col, col, w)
	}
}

// buildCrawlIssueExportWorkbook builds a multi-sheet XLSX workbook for one crawl's issue export.
func buildCrawlIssueExportWorkbook(crawlID string, exportRows []crawlIssueExportRow) ([]byte, error) {
	f := excelize.NewFile()

	styles, err := registerWorkbookStyles(f)
	if err != nil {
		return nil, fmt.Errorf("register styles: %w", err)
	}

	overviewRows := buildCrawlIssueOverviewRows(exportRows)
	duplicatePeerRows := buildDuplicatePeerExportRows(exportRows)

	// --- Overview sheet ---
	overviewSheet := "Overview"
	overviewIdx, err := f.NewSheet(overviewSheet)
	if err != nil {
		return nil, fmt.Errorf("create overview sheet: %w", err)
	}
	f.SetActiveSheet(overviewIdx)

	overviewHeaders := []any{"PILLAR", "BUCKET", "ISSUE_TYPE", "SEVERITY", "TOTAL_AFFECTED"}
	if err := f.SetSheetRow(overviewSheet, "A1", &overviewHeaders); err != nil {
		return nil, fmt.Errorf("write overview headers: %w", err)
	}
	for i, row := range overviewRows {
		cell := fmt.Sprintf("A%d", i+2)
		rowData := []any{row.Pillar, row.Bucket, row.IssueType, row.Severity, row.TotalAffected}
		if err := f.SetSheetRow(overviewSheet, cell, &rowData); err != nil {
			return nil, fmt.Errorf("write overview row %d: %w", i, err)
		}
	}

	const severityColOverview = 4
	applyHeaderRowStyling(f, overviewSheet, 5, styles)
	setOverviewColumnWidths(f, overviewSheet)
	for i, row := range overviewRows {
		applyDataRowStyling(f, overviewSheet, i, 5, row.Severity, styles, severityColOverview)
	}

	// --- All Issues sheet ---
	if err := writeIssuesSheet(f, "All Issues", exportRows, styles); err != nil {
		return nil, err
	}

	// --- Pillar-specific issue sheets ---
	for _, pillar := range []string{"SEO", "AEO", "Pagespeed"} {
		pillarRows := filterExportRowsByPillar(exportRows, pillar)
		if len(pillarRows) == 0 {
			continue
		}
		sheetName := pillar + " Issues"
		if err := writeIssuesSheet(f, sheetName, pillarRows, styles); err != nil {
			return nil, err
		}
	}

	// --- Duplicate Peers sheet ---
	if len(duplicatePeerRows) > 0 {
		duplicatesSheet := "Duplicate Peers"
		_, err = f.NewSheet(duplicatesSheet)
		if err != nil {
			return nil, fmt.Errorf("create duplicates sheet: %w", err)
		}
		dupHeaders := []any{"PILLAR", "BUCKET", "ISSUE_TYPE", "SOURCE_URL", "RELATED_URL", "MESSAGE", "DETAILS", "SEVERITY"}
		if err := f.SetSheetRow(duplicatesSheet, "A1", &dupHeaders); err != nil {
			return nil, fmt.Errorf("write duplicates headers: %w", err)
		}
		for i, row := range duplicatePeerRows {
			cell := fmt.Sprintf("A%d", i+2)
			rowData := []any{row.Pillar, row.Bucket, row.IssueType, row.SourceURL, row.RelatedURL, row.Message, row.Details, row.Severity}
			if err := f.SetSheetRow(duplicatesSheet, cell, &rowData); err != nil {
				return nil, fmt.Errorf("write duplicate row %d: %w", i, err)
			}
		}

		const severityColDup = 8
		applyHeaderRowStyling(f, duplicatesSheet, 8, styles)
		setDuplicatesSheetColumnWidths(f, duplicatesSheet)
		for i, row := range duplicatePeerRows {
			applyDataRowStyling(f, duplicatesSheet, i, 8, row.Severity, styles, severityColDup)
		}
	}

	// Remove the default "Sheet1" that excelize creates automatically.
	if err := f.DeleteSheet("Sheet1"); err != nil {
		return nil, fmt.Errorf("delete default sheet: %w", err)
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, fmt.Errorf("write workbook buffer: %w", err)
	}

	return buf.Bytes(), nil
}

func writeIssuesSheet(f *excelize.File, sheetName string, rows []crawlIssueExportRow, styles workbookStyles) error {
	_, err := f.NewSheet(sheetName)
	if err != nil {
		return fmt.Errorf("create %q sheet: %w", sheetName, err)
	}
	headers := []any{"PILLAR", "BUCKET", "ISSUE_TYPE", "SEVERITY", "TOTAL_AFFECTED", "URL", "MESSAGE", "DETAILS"}
	if err := f.SetSheetRow(sheetName, "A1", &headers); err != nil {
		return fmt.Errorf("write %q headers: %w", sheetName, err)
	}
	for i, row := range rows {
		cell := fmt.Sprintf("A%d", i+2)
		details := formatDetailTextForWorkbook(row.Details)
		rowData := []any{row.Pillar, row.Bucket, row.IssueType, row.Severity, row.TotalAffected, row.URL, row.Message, details}
		if err := f.SetSheetRow(sheetName, cell, &rowData); err != nil {
			return fmt.Errorf("write %q row %d: %w", sheetName, i, err)
		}
	}

	const severityCol = 4
	applyHeaderRowStyling(f, sheetName, 8, styles)
	setIssuesSheetColumnWidths(f, sheetName)
	for i, row := range rows {
		applyDataRowStyling(f, sheetName, i, 8, row.Severity, styles, severityCol)
	}

	return nil
}
