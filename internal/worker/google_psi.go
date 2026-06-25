package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const googlePSIEndpoint = "https://www.googleapis.com/pagespeedonline/v5/runPagespeed"

var googlePSIHTTPClient = &http.Client{Timeout: 90 * time.Second}

type googlePSIStoredResult struct {
	URL          string                `json:"url"`
	Mobile       googlePSIDeviceResult `json:"mobile"`
	AnalysisDate string                `json:"analysis_date"`
}

type googlePSIDeviceResult struct {
	Success          bool             `json:"success"`
	PerformanceScore *int             `json:"performance_score,omitempty"`
	Metrics          googlePSIMetrics `json:"metrics,omitempty"`
	Strategy         string           `json:"strategy"`
	Error            string           `json:"error,omitempty"`
}

type googlePSIMetrics struct {
	FirstContentfulPaint   *float64 `json:"first_contentful_paint,omitempty"`
	LargestContentfulPaint *float64 `json:"largest_contentful_paint,omitempty"`
	CumulativeLayoutShift  *float64 `json:"cumulative_layout_shift,omitempty"`
	FirstInputDelay        *float64 `json:"first_input_delay,omitempty"`
	SpeedIndex             *float64 `json:"speed_index,omitempty"`
	TimeToInteractive      *float64 `json:"time_to_interactive,omitempty"`
}

type googlePSIAPIResponse struct {
	LighthouseResult struct {
		Categories map[string]struct {
			Score *float64 `json:"score"`
		} `json:"categories"`
		Audits map[string]struct {
			NumericValue *float64 `json:"numericValue"`
		} `json:"audits"`
	} `json:"lighthouseResult"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (worker *Worker) enrichCrawlWithGooglePSI(ctx context.Context, crawlID pgtype.UUID, pageURL string) (*googlePSIStoredResult, error) {
	apiKey := strings.TrimSpace(worker.cfg.PageSpeedAPIKey)
	if apiKey == "" {
		return nil, nil
	}

	result, err := runGooglePSIMobile(ctx, apiKey, pageURL)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal([]googlePSIStoredResult{result})
	if err != nil {
		return nil, fmt.Errorf("marshal google psi results: %w", err)
	}

	if err := worker.queries.UpdateCrawlGooglePSIResults(ctx, sqlc.UpdateCrawlGooglePSIResultsParams{ID: crawlID, GooglePsiResults: payload}); err != nil {
		return nil, fmt.Errorf("update google psi results: %w", err)
	}

	return &result, nil
}

func runGooglePSIMobile(ctx context.Context, apiKey string, pageURL string) (googlePSIStoredResult, error) {
	psiURL, err := normalizeGooglePSIPageURL(pageURL)
	if err != nil {
		return googlePSIStoredResult{}, err
	}

	mobileResult, err := callGooglePSI(ctx, apiKey, psiURL, "mobile")
	if err != nil {
		mobileResult = googlePSIDeviceResult{Success: false, Strategy: "mobile", Error: err.Error()}
	}

	return googlePSIStoredResult{
		URL:          psiURL,
		Mobile:       mobileResult,
		AnalysisDate: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func callGooglePSI(ctx context.Context, apiKey string, pageURL string, strategy string) (googlePSIDeviceResult, error) {
	requestURL, err := buildGooglePSIURL(apiKey, pageURL, strategy)
	if err != nil {
		return googlePSIDeviceResult{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return googlePSIDeviceResult{}, fmt.Errorf("build google psi request: %w", err)
	}

	response, err := googlePSIHTTPClient.Do(request)
	if err != nil {
		return googlePSIDeviceResult{}, fmt.Errorf("call google psi: %w", err)
	}
	defer response.Body.Close()

	var apiResponse googlePSIAPIResponse
	if err := json.NewDecoder(response.Body).Decode(&apiResponse); err != nil {
		return googlePSIDeviceResult{}, fmt.Errorf("decode google psi response: %w", err)
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if apiResponse.Error != nil && strings.TrimSpace(apiResponse.Error.Message) != "" {
			return googlePSIDeviceResult{}, fmt.Errorf("google psi returned %d: %s", response.StatusCode, apiResponse.Error.Message)
		}
		return googlePSIDeviceResult{}, fmt.Errorf("google psi returned %d", response.StatusCode)
	}

	return googlePSIDeviceResult{
		Success:          true,
		PerformanceScore: extractGooglePSIPerformanceScore(apiResponse),
		Metrics:          extractGooglePSIMetrics(apiResponse),
		Strategy:         strategy,
	}, nil
}

func buildGooglePSIURL(apiKey string, pageURL string, strategy string) (string, error) {
	if strings.TrimSpace(pageURL) == "" {
		return "", fmt.Errorf("missing page url")
	}

	endpointURL, err := url.Parse(googlePSIEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse google psi endpoint: %w", err)
	}

	query := endpointURL.Query()
	query.Set("url", pageURL)
	query.Set("strategy", strategy)
	query.Set("category", "performance")
	query.Set("key", apiKey)
	endpointURL.RawQuery = query.Encode()

	return endpointURL.String(), nil
}

func normalizeGooglePSIPageURL(pageURL string) (string, error) {
	parsedURL, err := url.Parse(strings.TrimSpace(pageURL))
	if err != nil {
		return "", fmt.Errorf("parse page url: %w", err)
	}
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		return "", fmt.Errorf("page url must include scheme and host")
	}

	return (&url.URL{Scheme: parsedURL.Scheme, Host: parsedURL.Host, Path: "/"}).String(), nil
}

func extractGooglePSIPerformanceScore(response googlePSIAPIResponse) *int {
	performanceCategory, exists := response.LighthouseResult.Categories["performance"]
	if !exists || performanceCategory.Score == nil {
		return nil
	}

	score := int(*performanceCategory.Score*100 + 0.5)
	return &score
}

func extractGooglePSIMetrics(response googlePSIAPIResponse) googlePSIMetrics {
	return googlePSIMetrics{
		FirstContentfulPaint:   extractAuditMillisecondsAsSeconds(response, "first-contentful-paint"),
		LargestContentfulPaint: extractAuditMillisecondsAsSeconds(response, "largest-contentful-paint"),
		CumulativeLayoutShift:  extractAuditValue(response, "cumulative-layout-shift", 3),
		FirstInputDelay:        extractAuditMillisecondsAsSeconds(response, "max-potential-fid"),
		SpeedIndex:             extractAuditMillisecondsAsSeconds(response, "speed-index"),
		TimeToInteractive:      extractAuditMillisecondsAsSeconds(response, "interactive"),
	}
}

func extractAuditMillisecondsAsSeconds(response googlePSIAPIResponse, auditKey string) *float64 {
	value := extractAuditValue(response, auditKey, 2)
	if value == nil {
		return nil
	}
	seconds := roundFloat(*value/1000, 2)
	return &seconds
}

func extractAuditValue(response googlePSIAPIResponse, auditKey string, decimalPlaces int) *float64 {
	audit, exists := response.LighthouseResult.Audits[auditKey]
	if !exists || audit.NumericValue == nil {
		return nil
	}

	value := roundFloat(*audit.NumericValue, decimalPlaces)
	return &value
}

func (worker *Worker) persistGooglePSIIssues(ctx context.Context, crawlID pgtype.UUID, result *googlePSIStoredResult) (int, error) {
	if result == nil || !result.Mobile.Success {
		return 0, nil
	}

	issues := buildGooglePSIIssues(crawlID, *result)
	for _, issue := range issues {
		if _, err := worker.queries.CreateCrawlIssue(ctx, issue); err != nil {
			return 0, fmt.Errorf("create google psi issue %q for %q: %w", issue.IssueType, issue.Url, err)
		}
	}

	return len(issues), nil
}

func buildGooglePSIIssues(crawlID pgtype.UUID, result googlePSIStoredResult) []sqlc.CreateCrawlIssueParams {
	issues := make([]sqlc.CreateCrawlIssueParams, 0, 7)
	if result.Mobile.PerformanceScore != nil {
		if severity := googlePSIPerformanceSeverity(*result.Mobile.PerformanceScore); severity != "" {
			issues = append(issues, newGooglePSIIssue(crawlID, result.URL, "google_psi_mobile_performance", severity, "Mobile PageSpeed score needs attention", fmt.Sprintf("Google PageSpeed mobile performance score is %d.", *result.Mobile.PerformanceScore)))
		}
	}
	if result.Mobile.Metrics.FirstContentfulPaint != nil {
		if severity := googlePSIFCPSeverity(*result.Mobile.Metrics.FirstContentfulPaint); severity != "" {
			issues = append(issues, newGooglePSIIssue(crawlID, result.URL, "google_psi_fcp", severity, "First Contentful Paint needs attention", fmt.Sprintf("Google PageSpeed mobile FCP is %.2fs.", *result.Mobile.Metrics.FirstContentfulPaint)))
		}
	}
	if result.Mobile.Metrics.LargestContentfulPaint != nil {
		if severity := googlePSILCPSeverity(*result.Mobile.Metrics.LargestContentfulPaint); severity != "" {
			issues = append(issues, newGooglePSIIssue(crawlID, result.URL, "google_psi_lcp", severity, "Largest Contentful Paint needs attention", fmt.Sprintf("Google PageSpeed mobile LCP is %.2fs.", *result.Mobile.Metrics.LargestContentfulPaint)))
		}
	}
	if result.Mobile.Metrics.CumulativeLayoutShift != nil {
		if severity := googlePSICLSSeverity(*result.Mobile.Metrics.CumulativeLayoutShift); severity != "" {
			issues = append(issues, newGooglePSIIssue(crawlID, result.URL, "google_psi_cls", severity, "Cumulative Layout Shift needs attention", fmt.Sprintf("Google PageSpeed mobile CLS is %.3f.", *result.Mobile.Metrics.CumulativeLayoutShift)))
		}
	}
	if result.Mobile.Metrics.FirstInputDelay != nil {
		if severity := googlePSIFIDSeverity(*result.Mobile.Metrics.FirstInputDelay); severity != "" {
			issues = append(issues, newGooglePSIIssue(crawlID, result.URL, "google_psi_fid", severity, "First Input Delay needs attention", fmt.Sprintf("Google PageSpeed mobile FID is %.0fms.", *result.Mobile.Metrics.FirstInputDelay*1000)))
		}
	}
	if result.Mobile.Metrics.SpeedIndex != nil {
		if severity := googlePSISpeedIndexSeverity(*result.Mobile.Metrics.SpeedIndex); severity != "" {
			issues = append(issues, newGooglePSIIssue(crawlID, result.URL, "google_psi_speed_index", severity, "Speed Index needs attention", fmt.Sprintf("Google PageSpeed mobile Speed Index is %.2fs.", *result.Mobile.Metrics.SpeedIndex)))
		}
	}
	if result.Mobile.Metrics.TimeToInteractive != nil {
		if severity := googlePSITTISeverity(*result.Mobile.Metrics.TimeToInteractive); severity != "" {
			issues = append(issues, newGooglePSIIssue(crawlID, result.URL, "google_psi_tti", severity, "Time to Interactive needs attention", fmt.Sprintf("Google PageSpeed mobile TTI is %.2fs.", *result.Mobile.Metrics.TimeToInteractive)))
		}
	}

	return issues
}

func newGooglePSIIssue(crawlID pgtype.UUID, pageURL string, issueType string, severity string, message string, details string) sqlc.CreateCrawlIssueParams {
	return sqlc.CreateCrawlIssueParams{
		CrawlID:   crawlID,
		Url:       pageURL,
		Pillar:    "pagespeed",
		Bucket:    "psi_cwv",
		IssueType: issueType,
		Severity:  severity,
		Message:   message,
		Details:   details,
	}
}

func googlePSIPerformanceSeverity(score int) string {
	switch {
	case score < 50:
		return "high"
	case score < 90:
		return "medium"
	default:
		return ""
	}
}

func googlePSILCPSeverity(valueSeconds float64) string {
	switch {
	case valueSeconds > 4.0:
		return "high"
	case valueSeconds > 2.5:
		return "medium"
	default:
		return ""
	}
}

func googlePSIFCPSeverity(valueSeconds float64) string {
	switch {
	case valueSeconds > 3.0:
		return "high"
	case valueSeconds > 1.8:
		return "medium"
	default:
		return ""
	}
}

func googlePSICLSSeverity(value float64) string {
	switch {
	case value > 0.25:
		return "high"
	case value > 0.1:
		return "medium"
	default:
		return ""
	}
}

func googlePSIFIDSeverity(valueSeconds float64) string {
	valueMs := valueSeconds * 1000
	switch {
	case valueMs > 300:
		return "high"
	case valueMs > 100:
		return "medium"
	default:
		return ""
	}
}

func googlePSISpeedIndexSeverity(valueSeconds float64) string {
	switch {
	case valueSeconds > 5.8:
		return "high"
	case valueSeconds > 3.4:
		return "medium"
	default:
		return ""
	}
}

func googlePSITTISeverity(valueSeconds float64) string {
	switch {
	case valueSeconds > 7.3:
		return "high"
	case valueSeconds > 3.8:
		return "medium"
	default:
		return ""
	}
}
func roundFloat(value float64, decimalPlaces int) float64 {
	multiplier := 1.0
	for range decimalPlaces {
		multiplier *= 10
	}
	return float64(int(value*multiplier+0.5)) / multiplier
}
