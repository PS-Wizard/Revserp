package gsc

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FetchOverview loads one Search Console overview payload live from Google.
func (service *Service) FetchOverview(ctx context.Context, accessToken, siteURL string) (OverviewPayload, error) {
	historyDays := 360
	historyStart, historyEnd := getDateRange(historyDays, 0)
	requestContext, cancelRequests := context.WithCancel(ctx)
	defer cancelRequests()

	type analyticsResult struct {
		days int
		kind string
		rows []SearchAnalyticsRow
		err  error
	}

	requestCount := 1 + len(overviewWindowOptions)*4
	results := make(chan analyticsResult, requestCount)

	go func() {
		rows, err := service.querySearchAnalytics(requestContext, accessToken, siteURL, map[string]any{
			"startDate":  historyStart,
			"endDate":    historyEnd,
			"dimensions": []string{"date"},
			"dataState":  "final",
			"rowLimit":   historyDays + 14,
		})
		results <- analyticsResult{kind: "trend", rows: rows, err: err}
	}()

	for _, days := range overviewWindowOptions {
		windowStart, windowEnd := getDateRange(days, 0)

		go func(days int) {
			rows, err := service.querySearchAnalytics(requestContext, accessToken, siteURL, map[string]any{
				"startDate":  windowStart,
				"endDate":    windowEnd,
				"dimensions": []string{"query"},
				"dataState":  "final",
				"rowLimit":   50,
			})
			results <- analyticsResult{days: days, kind: "query", rows: rows, err: err}
		}(days)

		go func(days int) {
			rows, err := service.querySearchAnalytics(requestContext, accessToken, siteURL, map[string]any{
				"startDate":  windowStart,
				"endDate":    windowEnd,
				"dimensions": []string{"page"},
				"dataState":  "final",
				"rowLimit":   25,
			})
			results <- analyticsResult{days: days, kind: "page", rows: rows, err: err}
		}(days)

		go func(days int) {
			rows, err := service.querySearchAnalytics(requestContext, accessToken, siteURL, map[string]any{
				"startDate":  windowStart,
				"endDate":    windowEnd,
				"dimensions": []string{"country"},
				"dataState":  "final",
				"rowLimit":   25,
			})
			results <- analyticsResult{days: days, kind: "country", rows: rows, err: err}
		}(days)

		go func(days int) {
			rows, err := service.querySearchAnalytics(requestContext, accessToken, siteURL, map[string]any{
				"startDate":  windowStart,
				"endDate":    windowEnd,
				"dimensions": []string{"device"},
				"dataState":  "final",
				"rowLimit":   10,
			})
			results <- analyticsResult{days: days, kind: "device", rows: rows, err: err}
		}(days)
	}

	var trendRows []SearchAnalyticsRow
	queryRowsByWindow := make(map[int][]SearchAnalyticsRow, len(overviewWindowOptions))
	pageRowsByWindow := make(map[int][]SearchAnalyticsRow, len(overviewWindowOptions))
	countryRowsByWindow := make(map[int][]SearchAnalyticsRow, len(overviewWindowOptions))
	deviceRowsByWindow := make(map[int][]SearchAnalyticsRow, len(overviewWindowOptions))

	for range requestCount {
		result := <-results
		if result.err != nil {
			cancelRequests()
			return OverviewPayload{}, result.err
		}

		switch result.kind {
		case "trend":
			trendRows = result.rows
		case "query":
			queryRowsByWindow[result.days] = result.rows
		case "page":
			pageRowsByWindow[result.days] = result.rows
		case "country":
			countryRowsByWindow[result.days] = result.rows
		case "device":
			deviceRowsByWindow[result.days] = result.rows
		}
	}

	payload := OverviewPayload{
		HistoryDays: historyDays,
		Windows:     make(map[string]OverviewWindow, len(overviewWindowOptions)),
	}
	for _, days := range overviewWindowOptions {
		payload.Windows[formatWindowKey(days)] = buildOverviewWindow(days, trendRows, queryRowsByWindow[days], pageRowsByWindow[days], countryRowsByWindow[days], deviceRowsByWindow[days])
	}
	return payload, nil
}

func buildOverviewWindow(days int, trendRows, queryRows, pageRows, countryRows, deviceRows []SearchAnalyticsRow) OverviewWindow {
	rangeData := buildWindowRange(days)
	currentTrendRows := filterRowsByDate(trendRows, rangeData.CurrentStart, rangeData.CurrentEnd)
	previousTrendRows := filterRowsByDate(trendRows, rangeData.PreviousStart, rangeData.PreviousEnd)
	currentTotals := buildMetricTotalsFromRows(currentTrendRows)
	previousTotals := buildMetricTotalsFromRows(previousTrendRows)

	trimmedQueryRows := trimRows(queryRows, 25)

	return OverviewWindow{
		Range: rangeData,
		Summary: OverviewSummary{
			Clicks:      MetricSummary{Current: currentTotals.Clicks, Previous: previousTotals.Clicks},
			Impressions: MetricSummary{Current: currentTotals.Impressions, Previous: previousTotals.Impressions},
			CTR:         MetricSummary{Current: currentTotals.CTR, Previous: previousTotals.CTR},
			Position:    MetricSummary{Current: currentTotals.Position, Previous: previousTotals.Position},
		},
		Trend:            currentTrendRows,
		TopQueries:       trimmedQueryRows,
		TopPages:         trimRows(pageRows, 25),
		CountryBreakdown: countryRows,
		DeviceBreakdown:  deviceRows,
		Opportunities: OverviewOpportunities{
			LowCTRQueries:           buildLowCTROpportunities(trimmedQueryRows, currentTotals.CTR),
			StrikingDistanceQueries: buildStrikingDistanceOpportunities(trimmedQueryRows),
			QuestionQueries:         buildQuestionQueryOpportunities(trimmedQueryRows),
		},
	}
}

type metricTotals struct {
	Clicks      float64
	Impressions float64
	CTR         float64
	Position    float64
}

func buildMetricTotalsFromRows(rows []SearchAnalyticsRow) metricTotals {
	clicks := sumMetric(rows, func(row SearchAnalyticsRow) float64 { return row.Clicks })
	impressions := sumMetric(rows, func(row SearchAnalyticsRow) float64 { return row.Impressions })
	ctr := 0.0
	if impressions > 0 {
		ctr = clicks / impressions
	}
	return metricTotals{
		Clicks:      clicks,
		Impressions: impressions,
		CTR:         ctr,
		Position:    averagePosition(rows),
	}
}

func averagePosition(rows []SearchAnalyticsRow) float64 {
	totalImpressions := sumMetric(rows, func(row SearchAnalyticsRow) float64 { return row.Impressions })
	if totalImpressions <= 0 {
		return 0
	}

	weightedPositionSum := 0.0
	for _, row := range rows {
		weightedPositionSum += row.Position * row.Impressions
	}
	return weightedPositionSum / totalImpressions
}

func buildWindowRange(days int) OverviewRange {
	currentStart, currentEnd := getDateRange(days, 0)
	previousStart, previousEnd := getDateRange(days, days)
	return OverviewRange{
		CurrentStart:  currentStart,
		CurrentEnd:    currentEnd,
		PreviousStart: previousStart,
		PreviousEnd:   previousEnd,
	}
}

func filterRowsByDate(rows []SearchAnalyticsRow, startDate, endDate string) []SearchAnalyticsRow {
	filteredRows := make([]SearchAnalyticsRow, 0, len(rows))
	for _, row := range rows {
		if row.Date >= startDate && row.Date <= endDate {
			filteredRows = append(filteredRows, row)
		}
	}
	return filteredRows
}

func trimRows(rows []SearchAnalyticsRow, limit int) []SearchAnalyticsRow {
	if len(rows) <= limit {
		return rows
	}
	return rows[:limit]
}

func buildLowCTROpportunities(rows []SearchAnalyticsRow, siteCTR float64) []SearchAnalyticsRow {
	filteredRows := make([]SearchAnalyticsRow, 0, len(rows))
	for _, row := range rows {
		if row.Impressions >= 100 && row.Position <= 12 && row.CTR < maxFloat(siteCTR*0.8, 0.02) {
			filteredRows = append(filteredRows, row)
		}
	}
	sortRows(filteredRows, func(leftRow, rightRow SearchAnalyticsRow) bool {
		if leftRow.Impressions == rightRow.Impressions {
			return leftRow.CTR < rightRow.CTR
		}
		return leftRow.Impressions > rightRow.Impressions
	})
	return trimRows(filteredRows, 8)
}

func buildStrikingDistanceOpportunities(rows []SearchAnalyticsRow) []SearchAnalyticsRow {
	filteredRows := make([]SearchAnalyticsRow, 0, len(rows))
	for _, row := range rows {
		if row.Impressions >= 50 && row.Position >= 4 && row.Position <= 20 {
			filteredRows = append(filteredRows, row)
		}
	}
	sortRows(filteredRows, func(leftRow, rightRow SearchAnalyticsRow) bool {
		if leftRow.Position == rightRow.Position {
			return leftRow.Impressions > rightRow.Impressions
		}
		return leftRow.Position < rightRow.Position
	})
	return trimRows(filteredRows, 8)
}

func buildQuestionQueryOpportunities(rows []SearchAnalyticsRow) []SearchAnalyticsRow {
	prefixes := []string{"who ", "what ", "when ", "where ", "why ", "how ", "best ", "vs ", "can ", "should "}
	filteredRows := make([]SearchAnalyticsRow, 0, len(rows))
	for _, row := range rows {
		lowerQuery := strings.ToLower(strings.TrimSpace(row.Query))
		if lowerQuery == "" || row.Impressions < 20 {
			continue
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(lowerQuery, prefix) {
				filteredRows = append(filteredRows, row)
				break
			}
		}
	}
	sortRows(filteredRows, func(leftRow, rightRow SearchAnalyticsRow) bool {
		if leftRow.Impressions == rightRow.Impressions {
			return leftRow.Position < rightRow.Position
		}
		return leftRow.Impressions > rightRow.Impressions
	})
	return trimRows(filteredRows, 8)
}

func sortRows(rows []SearchAnalyticsRow, less func(leftRow, rightRow SearchAnalyticsRow) bool) {
	for leftIndex := 0; leftIndex < len(rows); leftIndex++ {
		for rightIndex := leftIndex + 1; rightIndex < len(rows); rightIndex++ {
			if less(rows[rightIndex], rows[leftIndex]) {
				rows[leftIndex], rows[rightIndex] = rows[rightIndex], rows[leftIndex]
			}
		}
	}
}

func sumMetric(rows []SearchAnalyticsRow, getter func(SearchAnalyticsRow) float64) float64 {
	total := 0.0
	for _, row := range rows {
		total += getter(row)
	}
	return total
}

func maxFloat(leftValue, rightValue float64) float64 {
	if leftValue > rightValue {
		return leftValue
	}
	return rightValue
}

func getDateRange(days, offsetDays int) (string, string) {
	endDate := time.Now().UTC().AddDate(0, 0, -(offsetDays + 3)).Format(time.DateOnly)
	startDate := time.Now().UTC().AddDate(0, 0, -(offsetDays + 3 + maxInt(days-1, 0))).Format(time.DateOnly)
	return startDate, endDate
}

func maxInt(leftValue, rightValue int) int {
	if leftValue > rightValue {
		return leftValue
	}
	return rightValue
}

func formatWindowKey(days int) string {
	return strconv.Itoa(days)
}

func normalizeDomain(value string) string {
	candidate := strings.TrimSpace(value)
	if strings.HasPrefix(candidate, "sc-domain:") {
		candidate = strings.TrimPrefix(candidate, "sc-domain:")
	}
	if !strings.Contains(candidate, "://") {
		candidate = "https://" + candidate
	}
	parsedURL, err := url.Parse(candidate)
	if err != nil {
		return ""
	}
	host := parsedURL.Host
	if host == "" {
		host = parsedURL.Path
	}
	return strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), "/"), "www.")
}

func rankSiteForProject(projectBaseURL, siteURL string) int {
	projectHost := normalizeDomain(projectBaseURL)
	if projectHost == "" || strings.TrimSpace(siteURL) == "" {
		return -1
	}

	if strings.HasPrefix(siteURL, "sc-domain:") {
		domain := normalizeDomain(siteURL)
		if domain == projectHost {
			return 90
		}
		if strings.HasSuffix(projectHost, "."+domain) {
			return 70
		}
		return -1
	}

	parsedURL, err := url.Parse(siteURL)
	if err != nil {
		return -1
	}
	siteHost := normalizeDomain(parsedURL.Host)
	if siteHost == projectHost {
		pathBonus := 0
		if parsedURL.Path == "" || parsedURL.Path == "/" {
			pathBonus = 10
		}
		secureBonus := 0
		if parsedURL.Scheme == "https" {
			secureBonus = 5
		}
		return 100 + pathBonus + secureBonus
	}
	return -1
}
