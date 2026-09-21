package ga

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const (
	defaultMaxResponseBytes = 10 << 20
	cacheTTL                = time.Hour
	cacheMaxEntries         = 500
	overviewTimeout         = 45 * time.Second
	adminBaseURL            = "https://analyticsadmin.googleapis.com/v1beta"
	dataBaseURL             = "https://analyticsdata.googleapis.com/v1beta"
)

type cacheEntry struct {
	overview  Overview
	fetchedAt time.Time
}

// Service owns Google Analytics Admin and Data API operations.
type Service struct {
	httpClient       *http.Client
	maxResponseBytes int64
	// These URLs are package-test overrides.
	adminBaseURL string
	dataBaseURL  string
	cacheMu      sync.Mutex
	cache        map[string]cacheEntry
	group        singleflight.Group
}

// NewService builds a Google Analytics service.
func NewService(maxResponseBytes int64) *Service {
	return &Service{
		httpClient:       &http.Client{Timeout: 30 * time.Second},
		maxResponseBytes: maxResponseBytes,
		adminBaseURL:     adminBaseURL,
		dataBaseURL:      dataBaseURL,
		cache:            make(map[string]cacheEntry),
	}
}

func (service *Service) responseLimit() int64 {
	if service.maxResponseBytes > 0 {
		return service.maxResponseBytes
	}
	return defaultMaxResponseBytes
}

func (service *Service) readBody(response *http.Response) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(response.Body, service.responseLimit()+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > service.responseLimit() {
		return nil, &Error{StatusCode: response.StatusCode, Message: fmt.Sprintf("response body exceeds %d byte limit", service.responseLimit())}
	}
	return body, nil
}

// ListProperties lists every accessible Analytics property.
func (service *Service) ListProperties(ctx context.Context, accessToken string) ([]Property, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, &Error{Message: "missing Google access token"}
	}
	properties := []Property{}
	pageToken := ""
	for {
		endpoint, err := url.Parse(strings.TrimRight(service.adminBaseURL, "/") + "/accountSummaries")
		if err != nil {
			return nil, fmt.Errorf("build Analytics Admin URL: %w", err)
		}
		query := endpoint.Query()
		query.Set("pageSize", "200")
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		endpoint.RawQuery = query.Encode()
		var payload struct {
			AccountSummaries []struct {
				DisplayName       string `json:"displayName"`
				PropertySummaries []struct {
					Property    string `json:"property"`
					DisplayName string `json:"displayName"`
				} `json:"propertySummaries"`
			} `json:"accountSummaries"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := service.getJSON(ctx, endpoint.String(), accessToken, &payload); err != nil {
			return nil, err
		}
		for _, account := range payload.AccountSummaries {
			for _, property := range account.PropertySummaries {
				id := strings.TrimPrefix(strings.TrimSpace(property.Property), "properties/")
				if id == "" || strings.TrimSpace(property.DisplayName) == "" {
					continue
				}
				properties = append(properties, Property{PropertyID: id, DisplayName: property.DisplayName, AccountDisplayName: account.DisplayName})
			}
		}
		pageToken = strings.TrimSpace(payload.NextPageToken)
		if pageToken == "" {
			break
		}
	}
	return properties, nil
}

// FetchOverviewCached loads one property overview, caching by organization and property for one hour.
func (service *Service) FetchOverviewCached(ctx context.Context, accessToken, organizationID, propertyID string) (Overview, error) {
	key := organizationID + "|" + propertyID
	service.cacheMu.Lock()
	entry, ok := service.cache[key]
	if ok && time.Since(entry.fetchedAt) >= cacheTTL {
		delete(service.cache, key)
		ok = false
	}
	service.cacheMu.Unlock()
	if ok {
		return entry.overview, nil
	}

	value, err, _ := service.group.Do(key, func() (any, error) {
		service.cacheMu.Lock()
		entry, ok := service.cache[key]
		if ok && time.Since(entry.fetchedAt) >= cacheTTL {
			delete(service.cache, key)
			ok = false
		}
		service.cacheMu.Unlock()
		if ok {
			return entry.overview, nil
		}
		overview, err := service.FetchOverview(ctx, accessToken, propertyID)
		if err != nil {
			return nil, err
		}
		service.cacheMu.Lock()
		service.evictExpiredLocked()
		if _, exists := service.cache[key]; !exists && len(service.cache) >= cacheMaxEntries {
			service.evictOldestLocked()
		}
		service.cache[key] = cacheEntry{overview: overview, fetchedAt: time.Now()}
		service.cacheMu.Unlock()
		return overview, nil
	})
	if err != nil {
		return Overview{}, err
	}
	return value.(Overview), nil
}

func (service *Service) evictExpiredLocked() {
	for key, entry := range service.cache {
		if time.Since(entry.fetchedAt) >= cacheTTL {
			delete(service.cache, key)
		}
	}
}

func (service *Service) evictOldestLocked() {
	var oldest string
	var oldestAt time.Time
	for key, entry := range service.cache {
		if oldest == "" || entry.fetchedAt.Before(oldestAt) {
			oldest, oldestAt = key, entry.fetchedAt
		}
	}
	delete(service.cache, oldest)
}

// FetchOverview gets the fixed 360-day Analytics overview for one property.
func (service *Service) FetchOverview(ctx context.Context, accessToken, propertyID string) (Overview, error) {
	if strings.TrimSpace(propertyID) == "" {
		return Overview{}, &Error{Message: "missing Analytics property ID"}
	}
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	currentStart := yesterday.AddDate(0, 0, -179)
	previousEnd := currentStart.AddDate(0, 0, -1)
	previousStart := previousEnd.AddDate(0, 0, -179)
	rangeValue := Range{CurrentStart: currentStart.Format(time.DateOnly), CurrentEnd: yesterday.Format(time.DateOnly), PreviousStart: previousStart.Format(time.DateOnly), PreviousEnd: previousEnd.Format(time.DateOnly)}

	ctx, cancel := context.WithTimeout(ctx, overviewTimeout)
	defer cancel()

	// reportRequest is the single source of truth for every report shape; the
	// batch calls below just re-order those requests into two HTTP round trips.
	batchA := []map[string]any{
		reportRequest(rangeValue.CurrentStart, rangeValue.CurrentEnd, "", 0),
		reportRequest(rangeValue.PreviousStart, rangeValue.PreviousEnd, "", 0),
		reportRequest(rangeValue.PreviousStart, rangeValue.CurrentEnd, "date", 0),
		reportRequest(rangeValue.CurrentStart, rangeValue.CurrentEnd, "landingPagePlusQueryString", 50),
		reportRequest(rangeValue.CurrentStart, rangeValue.CurrentEnd, "sessionDefaultChannelGroup", 25),
	}
	batchB := []map[string]any{
		reportRequest(rangeValue.CurrentStart, rangeValue.CurrentEnd, "sessionSourceMedium", 50),
		reportRequest(rangeValue.CurrentStart, rangeValue.CurrentEnd, "country", 25),
		reportRequest(rangeValue.CurrentStart, rangeValue.CurrentEnd, "deviceCategory", 25),
	}

	var reportsA, reportsB []reportResponse
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		reportsA, err = service.batchRunReports(groupCtx, accessToken, propertyID, batchA)
		return err
	})
	group.Go(func() error {
		var err error
		reportsB, err = service.batchRunReports(groupCtx, accessToken, propertyID, batchB)
		return err
	})
	if err := group.Wait(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return Overview{}, fmt.Errorf("fetch Analytics overview timed out after %s: %w", overviewTimeout, err)
		}
		return Overview{}, err
	}

	current := reportsA[0]
	previous := reportsA[1]
	overview := Overview{
		HistoryDays:  360,
		WindowDays:   180,
		Range:        rangeValue,
		Trend:        trendRows(reportsA[2]),
		LandingPages: breakdownRows(reportsA[3]),
		Channels:     breakdownRows(reportsA[4]),
		Sources:      breakdownRows(reportsB[0]),
		Countries:    breakdownRows(reportsB[1]),
		Devices:      breakdownRows(reportsB[2]),
	}
	overview.Summary = Summary{ActiveUsers: pair(current, previous, 0), Sessions: pair(current, previous, 1), EngagementRate: pair(current, previous, 2), KeyEvents: pair(current, previous, 3)}
	return overview, nil
}

// FetchRealtime returns the currently active user count without caching it.
func (service *Service) FetchRealtime(ctx context.Context, accessToken, propertyID string) (float64, error) {
	if strings.TrimSpace(propertyID) == "" {
		return 0, &Error{Message: "missing Analytics property ID"}
	}
	var payload reportResponse
	if err := service.postJSON(ctx, strings.TrimRight(service.dataBaseURL, "/")+"/properties/"+url.PathEscape(propertyID)+":runRealtimeReport", accessToken, map[string]any{"metrics": []map[string]string{{"name": "activeUsers"}}}, &payload); err != nil {
		return 0, err
	}
	if len(payload.Rows) == 0 || len(payload.Rows[0].MetricValues) == 0 {
		return 0, nil
	}
	return metricValue(payload.Rows[0].MetricValues[0].Value), nil
}

type reportResponse struct {
	Rows []struct {
		DimensionValues []struct {
			Value string `json:"value"`
		} `json:"dimensionValues"`
		MetricValues []struct {
			Value string `json:"value"`
		} `json:"metricValues"`
	} `json:"rows"`
}

func reportRequest(start, end, dimension string, limit int) map[string]any {
	request := map[string]any{
		"dateRanges": []map[string]string{{"startDate": start, "endDate": end}},
		"metrics":    []map[string]string{{"name": "activeUsers"}, {"name": "sessions"}, {"name": "engagementRate"}, {"name": "keyEvents"}},
	}
	if dimension != "" {
		request["dimensions"] = []map[string]string{{"name": dimension}}
		if dimension == "date" {
			request["orderBys"] = []map[string]any{{"dimension": map[string]string{"dimensionName": "date"}}}
		} else {
			request["orderBys"] = []map[string]any{{"metric": map[string]string{"metricName": "sessions"}, "desc": true}}
			request["limit"] = strconv.Itoa(limit)
		}
	}
	return request
}

func pair(current, previous reportResponse, metric int) MetricPair {
	return MetricPair{Current: reportMetric(current, metric), Previous: reportMetric(previous, metric)}
}

func reportMetric(report reportResponse, metric int) float64 {
	if len(report.Rows) == 0 || len(report.Rows[0].MetricValues) <= metric {
		return 0
	}
	return metricValue(report.Rows[0].MetricValues[metric].Value)
}

func trendRows(report reportResponse) []TrendRow {
	rows := make([]TrendRow, 0, len(report.Rows))
	for _, row := range report.Rows {
		if len(row.DimensionValues) == 0 {
			continue
		}
		rows = append(rows, TrendRow{Date: normalizeDate(row.DimensionValues[0].Value), ActiveUsers: rowMetric(row, 0), Sessions: rowMetric(row, 1), EngagementRate: rowMetric(row, 2), KeyEvents: rowMetric(row, 3)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Date < rows[j].Date })
	return rows
}

func breakdownRows(report reportResponse) []BreakdownRow {
	rows := make([]BreakdownRow, 0, len(report.Rows))
	for _, row := range report.Rows {
		label := ""
		if len(row.DimensionValues) > 0 {
			label = row.DimensionValues[0].Value
		}
		rows = append(rows, BreakdownRow{Label: label, ActiveUsers: rowMetric(row, 0), Sessions: rowMetric(row, 1), EngagementRate: rowMetric(row, 2), KeyEvents: rowMetric(row, 3)})
	}
	return rows
}

func rowMetric(row struct {
	DimensionValues []struct {
		Value string `json:"value"`
	} `json:"dimensionValues"`
	MetricValues []struct {
		Value string `json:"value"`
	} `json:"metricValues"`
}, metric int) float64 {
	if len(row.MetricValues) <= metric {
		return 0
	}
	return metricValue(row.MetricValues[metric].Value)
}

func metricValue(value string) float64 {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func normalizeDate(value string) string {
	if len(value) == 8 {
		if parsed, err := time.Parse("20060102", value); err == nil {
			return parsed.Format(time.DateOnly)
		}
	}
	return value
}

// batchReport is one entry of a batchRunReports response: a runReport body
// plus an optional per-report error.
type batchReport struct {
	reportResponse
	Error struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// err reports a per-report failure returned inside a batch response.
func (report batchReport) err() error {
	if message := strings.TrimSpace(report.Error.Message); message != "" {
		return &Error{Message: message}
	}
	if status := strings.TrimSpace(report.Error.Status); status != "" {
		return &Error{Message: status}
	}
	return nil
}

// batchRunReports sends up to five runReport requests in one HTTP call and
// returns the responses in request order.
func (service *Service) batchRunReports(ctx context.Context, accessToken, propertyID string, requests []map[string]any) ([]reportResponse, error) {
	var payload struct {
		Reports []batchReport `json:"reports"`
	}
	endpoint := strings.TrimRight(service.dataBaseURL, "/") + "/properties/" + url.PathEscape(propertyID) + ":batchRunReports"
	if err := service.postJSON(ctx, endpoint, accessToken, map[string]any{"requests": requests}, &payload); err != nil {
		return nil, err
	}
	if len(payload.Reports) != len(requests) {
		return nil, &Error{Message: "Google returned an unexpected Analytics batch response"}
	}
	responses := make([]reportResponse, len(payload.Reports))
	for index, report := range payload.Reports {
		if err := report.err(); err != nil {
			return nil, err
		}
		responses[index] = report.reportResponse
	}
	return responses, nil
}

func (service *Service) getJSON(ctx context.Context, endpoint, accessToken string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build Analytics Admin request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	return service.doJSON(request, target)
}

func (service *Service) postJSON(ctx context.Context, endpoint, accessToken string, payload, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal Analytics request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build Analytics Data request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")
	return service.doJSON(request, target)
}

func (service *Service) doJSON(request *http.Request, target any) error {
	response, err := service.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send Analytics request: %w", err)
	}
	defer response.Body.Close()
	body, err := service.readBody(response)
	if err != nil {
		return fmt.Errorf("read Analytics response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeError(response.StatusCode, body)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return &Error{StatusCode: response.StatusCode, Message: "Google returned an invalid Analytics response"}
	}
	return nil
}

func decodeError(status int, body []byte) error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if message := strings.TrimSpace(payload.Error.Message); message != "" {
			return &Error{StatusCode: status, Message: message}
		}
		if statusText := strings.TrimSpace(payload.Error.Status); statusText != "" {
			return &Error{StatusCode: status, Message: statusText}
		}
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = "Google Analytics request failed"
	}
	return &Error{StatusCode: status, Message: message}
}
