package gsc

import (
	"context"
	"strconv"
)

// SummaryPayload holds one headline current/previous comparison for an
// arbitrary 7..480 day window. It is the days-aware replacement for indexing
// the fixed 180-day overview window: same 3-day lag, same current+previous
// semantics, one cached date-series fetch.
type SummaryPayload struct {
	Days    int             `json:"days"`
	Range   OverviewRange   `json:"range"`
	Summary OverviewSummary `json:"summary"`
}

// FetchSummary loads one headline summary live from Google with a single
// date-dimension request covering both the current window and the previous
// window, so current+previous never costs two upstream calls.
func (service *Service) FetchSummary(ctx context.Context, accessToken, siteURL string, days int) (SummaryPayload, error) {
	days = clampInt(days, queryPageDefaultDays, queryPageMinDays, queryPageMaxDays)
	windowRange := buildWindowRange(days)
	rows, err := service.querySearchAnalytics(ctx, accessToken, siteURL, map[string]any{
		"startDate":  windowRange.PreviousStart,
		"endDate":    windowRange.CurrentEnd,
		"dimensions": []string{"date"},
		"dataState":  "final",
		"rowLimit":   2*days + 14,
	})
	if err != nil {
		return SummaryPayload{}, err
	}
	currentRows := filterRowsByDate(rows, windowRange.CurrentStart, windowRange.CurrentEnd)
	previousRows := filterRowsByDate(rows, windowRange.PreviousStart, windowRange.PreviousEnd)
	currentTotals := buildMetricTotalsFromRows(currentRows)
	previousTotals := buildMetricTotalsFromRows(previousRows)
	return SummaryPayload{
		Days:  days,
		Range: windowRange,
		Summary: OverviewSummary{
			Clicks:      MetricSummary{Current: currentTotals.Clicks, Previous: previousTotals.Clicks},
			Impressions: MetricSummary{Current: currentTotals.Impressions, Previous: previousTotals.Impressions},
			CTR:         MetricSummary{Current: currentTotals.CTR, Previous: previousTotals.CTR},
			Position:    MetricSummary{Current: currentTotals.Position, Previous: previousTotals.Position},
		},
	}, nil
}

// FetchSummaryCached returns a cached summary for organizationID+siteURL+days
// if one exists and is younger than responseCacheTTL; otherwise it fetches
// live and caches the result. The cache key includes days so a days:28
// summary never reads a days:180 entry, and it is scoped by organizationID
// so two organizations sharing the same site never see each other's data.
func (service *Service) FetchSummaryCached(ctx context.Context, accessToken, organizationID, siteURL string, days int) (SummaryPayload, error) {
	days = clampInt(days, queryPageDefaultDays, queryPageMinDays, queryPageMaxDays)
	result, err := service.fetchCached("summary|"+organizationID+"|"+siteURL+"|"+strconv.Itoa(days), func() (any, error) {
		return service.FetchSummary(ctx, accessToken, siteURL, days)
	})
	if err != nil {
		return SummaryPayload{}, err
	}
	return result.(SummaryPayload), nil
}
