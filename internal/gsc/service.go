package gsc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const overviewCacheTTL = time.Hour

// overviewCacheMaxEntries bounds the overview cache so it can't grow
// unbounded across org+site keys. When full, the oldest entry is evicted.
const overviewCacheMaxEntries = 500

type overviewCacheEntry struct {
	payload   OverviewPayload
	fetchedAt time.Time
}

// Service owns Google OAuth and Search Console API operations.
type Service struct {
	clientID         string
	clientSecret     string
	redirectURL      string
	encryptionSecret string
	httpClient       *http.Client

	overviewCacheMu sync.Mutex
	overviewCache   map[string]overviewCacheEntry
	overviewGroup   singleflight.Group
}

// NewService builds one Google OAuth and Search Console service.
func NewService(clientID, clientSecret, redirectURL, encryptionSecret string) *Service {
	return &Service{
		clientID:         strings.TrimSpace(clientID),
		clientSecret:     strings.TrimSpace(clientSecret),
		redirectURL:      strings.TrimSpace(redirectURL),
		encryptionSecret: strings.TrimSpace(encryptionSecret),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		overviewCache: make(map[string]overviewCacheEntry),
	}
}

// FetchOverviewCached returns a cached overview for organizationID+siteURL if one
// exists and is younger than overviewCacheTTL; otherwise it fetches live and caches
// the result. The cache key is scoped by organizationID so two organizations sharing
// the same Search Console site never see each other's cached data.
func (service *Service) FetchOverviewCached(ctx context.Context, accessToken, organizationID, siteURL string) (OverviewPayload, error) {
	cacheKey := organizationID + "|" + siteURL

	service.overviewCacheMu.Lock()
	entry, ok := service.overviewCache[cacheKey]
	if ok && time.Since(entry.fetchedAt) >= overviewCacheTTL {
		delete(service.overviewCache, cacheKey)
		ok = false
	}
	service.overviewCacheMu.Unlock()

	if ok {
		return entry.payload, nil
	}

	// Coalesce concurrent misses for the same key into one upstream fetch.
	result, err, _ := service.overviewGroup.Do(cacheKey, func() (any, error) {
		payload, err := service.FetchOverview(ctx, accessToken, siteURL)
		if err != nil {
			return nil, err
		}

		service.overviewCacheMu.Lock()
		service.evictExpiredLocked()
		if _, exists := service.overviewCache[cacheKey]; !exists && len(service.overviewCache) >= overviewCacheMaxEntries {
			service.evictOldestLocked()
		}
		service.overviewCache[cacheKey] = overviewCacheEntry{payload: payload, fetchedAt: time.Now()}
		service.overviewCacheMu.Unlock()

		return payload, nil
	})
	if err != nil {
		return OverviewPayload{}, err
	}

	return result.(OverviewPayload), nil
}

// evictExpiredLocked removes all cache entries past overviewCacheTTL.
// Callers must hold overviewCacheMu.
func (service *Service) evictExpiredLocked() {
	now := time.Now()
	for key, entry := range service.overviewCache {
		if now.Sub(entry.fetchedAt) >= overviewCacheTTL {
			delete(service.overviewCache, key)
		}
	}
}

// evictOldestLocked removes the least-recently-fetched cache entry.
// Callers must hold overviewCacheMu.
func (service *Service) evictOldestLocked() {
	var oldestKey string
	var oldestAt time.Time
	for key, entry := range service.overviewCache {
		if oldestKey == "" || entry.fetchedAt.Before(oldestAt) {
			oldestKey = key
			oldestAt = entry.fetchedAt
		}
	}
	if oldestKey != "" {
		delete(service.overviewCache, oldestKey)
	}
}

// BuildAuthURL builds one Google consent URL.
func (service *Service) BuildAuthURL(state string) (string, error) {
	if err := service.validateOAuthConfig(); err != nil {
		return "", err
	}
	if strings.TrimSpace(state) == "" {
		return "", &Error{Message: "missing Google OAuth state"}
	}

	params := url.Values{}
	params.Set("client_id", service.clientID)
	params.Set("redirect_uri", service.redirectURL)
	params.Set("response_type", "code")
	params.Set("scope", googleWebmastersReadOnlyScope)
	params.Set("access_type", "offline")
	params.Set("include_granted_scopes", "true")
	params.Set("prompt", "consent")
	params.Set("state", state)

	return googleAuthBaseURL + "?" + params.Encode(), nil
}

// ExchangeCode exchanges one callback code for Google OAuth tokens.
func (service *Service) ExchangeCode(ctx context.Context, code string) (TokenResponse, error) {
	if err := service.validateOAuthConfig(); err != nil {
		return TokenResponse{}, err
	}
	if strings.TrimSpace(code) == "" {
		return TokenResponse{}, &Error{Message: "missing Google OAuth code"}
	}

	payload := url.Values{}
	payload.Set("code", code)
	payload.Set("client_id", service.clientID)
	payload.Set("client_secret", service.clientSecret)
	payload.Set("redirect_uri", service.redirectURL)
	payload.Set("grant_type", "authorization_code")

	return service.postTokenRequest(ctx, payload)
}

// RefreshAccessToken refreshes one Google OAuth access token.
func (service *Service) RefreshAccessToken(ctx context.Context, refreshToken string) (TokenResponse, error) {
	if err := service.validateOAuthConfig(); err != nil {
		return TokenResponse{}, err
	}
	if strings.TrimSpace(refreshToken) == "" {
		return TokenResponse{}, &Error{Message: "missing Google refresh token"}
	}

	payload := url.Values{}
	payload.Set("refresh_token", refreshToken)
	payload.Set("client_id", service.clientID)
	payload.Set("client_secret", service.clientSecret)
	payload.Set("grant_type", "refresh_token")

	return service.postTokenRequest(ctx, payload)
}

// FetchSites loads all accessible Search Console properties for one access token.
func (service *Service) FetchSites(ctx context.Context, accessToken string) ([]SiteEntry, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, &Error{Message: "missing Google access token"}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, googleSitesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build Google sites request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)

	response, err := service.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send Google sites request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read Google sites response: %w", err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeGoogleAPIError(responseBody, "Failed to fetch Search Console sites")
	}

	var payload struct {
		SiteEntry []struct {
			SiteURL         string `json:"siteUrl"`
			PermissionLevel string `json:"permissionLevel"`
		} `json:"siteEntry"`
	}
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil, &Error{Message: "Google returned an invalid Search Console response"}
	}
	sites := make([]SiteEntry, 0, len(payload.SiteEntry))
	for _, entry := range payload.SiteEntry {
		sites = append(sites, SiteEntry{
			SiteURL:         entry.SiteURL,
			PermissionLevel: entry.PermissionLevel,
		})
	}
	return sites, nil
}

// RankSitesForProject sorts accessible properties by how well they match one project URL.
func (service *Service) RankSitesForProject(projectBaseURL string, sites []SiteEntry) []SiteEntry {
	rankedSites := make([]SiteEntry, 0, len(sites))
	for _, site := range sites {
		if strings.TrimSpace(site.SiteURL) == "" {
			continue
		}

		site.MatchScore = rankSiteForProject(projectBaseURL, site.SiteURL)
		if site.MatchScore < 0 {
			site.MatchScore = 0
		}
		rankedSites = append(rankedSites, site)
	}

	sort.Slice(rankedSites, func(leftIndex, rightIndex int) bool {
		leftSite := rankedSites[leftIndex]
		rightSite := rankedSites[rightIndex]
		if leftSite.MatchScore == rightSite.MatchScore {
			return leftSite.SiteURL < rightSite.SiteURL
		}
		return leftSite.MatchScore > rightSite.MatchScore
	})
	return rankedSites
}

func (service *Service) validateOAuthConfig() error {
	if service.clientID == "" || service.clientSecret == "" || service.redirectURL == "" || service.encryptionSecret == "" {
		return &Error{Message: "Google OAuth is not configured on the server"}
	}
	return nil
}

func (service *Service) postTokenRequest(ctx context.Context, payload url.Values) (TokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(payload.Encode()))
	if err != nil {
		return TokenResponse{}, fmt.Errorf("build Google token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := service.httpClient.Do(request)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("send Google token request: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("read Google token response: %w", err)
	}

	var tokenResponse TokenResponse
	if err := json.Unmarshal(responseBody, &tokenResponse); err != nil {
		return TokenResponse{}, &Error{Message: "Google returned an invalid token response"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return TokenResponse{}, decodeGoogleAPIError(responseBody, "Google token exchange failed")
	}
	return tokenResponse, nil
}

func (service *Service) querySearchAnalytics(ctx context.Context, accessToken, siteURL string, payload map[string]any) ([]SearchAnalyticsRow, error) {
	if strings.TrimSpace(siteURL) == "" {
		return nil, &Error{Message: "missing Search Console site URL"}
	}

	encodedSiteURL := url.PathEscape(siteURL)
	requestBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal Search Analytics payload: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, googleSearchAnalyticsURLBase+"/"+encodedSiteURL+"/searchAnalytics/query", bytes.NewReader(requestBody))
	if err != nil {
		return nil, fmt.Errorf("build Search Analytics request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")

	response, err := service.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send Search Analytics request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read Search Analytics response: %w", err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, decodeGoogleAPIError(responseBody, "Failed to fetch Search Analytics")
	}

	var responsePayload struct {
		Rows []struct {
			Keys        []string `json:"keys"`
			Clicks      float64  `json:"clicks"`
			Impressions float64  `json:"impressions"`
			CTR         float64  `json:"ctr"`
			Position    float64  `json:"position"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(responseBody, &responsePayload); err != nil {
		return nil, &Error{Message: "Google returned an invalid Search Analytics response"}
	}
	dimensions := extractDimensions(payload)
	rows := make([]SearchAnalyticsRow, 0, len(responsePayload.Rows))
	for _, row := range responsePayload.Rows {
		normalizedRow := SearchAnalyticsRow{
			Clicks:      row.Clicks,
			Impressions: row.Impressions,
			CTR:         row.CTR,
			Position:    row.Position,
		}
		for index, dimension := range dimensions {
			if index >= len(row.Keys) {
				continue
			}
			switch dimension {
			case "date":
				normalizedRow.Date = row.Keys[index]
			case "query":
				normalizedRow.Query = row.Keys[index]
			case "page":
				normalizedRow.Page = row.Keys[index]
			case "country":
				normalizedRow.Country = row.Keys[index]
			case "device":
				normalizedRow.Device = row.Keys[index]
			}
		}
		rows = append(rows, normalizedRow)
	}
	return rows, nil
}

func extractDimensions(payload map[string]any) []string {
	rawDimensions, ok := payload["dimensions"]
	if !ok {
		return nil
	}
	typedDimensions, ok := rawDimensions.([]string)
	if ok {
		return typedDimensions
	}
	rawDimensionList, ok := rawDimensions.([]any)
	if !ok {
		return nil
	}
	dimensions := make([]string, 0, len(rawDimensionList))
	for _, rawDimension := range rawDimensionList {
		dimension, ok := rawDimension.(string)
		if ok {
			dimensions = append(dimensions, dimension)
		}
	}
	return dimensions
}

func decodeGoogleAPIError(responseBody []byte, fallbackMessage string) error {
	var responsePayload map[string]any
	if err := json.Unmarshal(responseBody, &responsePayload); err != nil {
		rawResponse := strings.TrimSpace(string(responseBody))
		if rawResponse == "" {
			return &Error{Message: fallbackMessage}
		}
		return &Error{Message: fallbackMessage + ": " + rawResponse}
	}

	if errorValue, ok := responsePayload["error"]; ok {
		switch typedError := errorValue.(type) {
		case map[string]any:
			if message, ok := typedError["message"].(string); ok && strings.TrimSpace(message) != "" {
				return &Error{Message: strings.TrimSpace(message)}
			}
			if status, ok := typedError["status"].(string); ok && strings.TrimSpace(status) != "" {
				return &Error{Message: strings.TrimSpace(status)}
			}
		case string:
			if strings.TrimSpace(typedError) != "" {
				return &Error{Message: strings.TrimSpace(typedError)}
			}
		}
	}

	if errorDescription, ok := responsePayload["error_description"].(string); ok && strings.TrimSpace(errorDescription) != "" {
		return &Error{Message: strings.TrimSpace(errorDescription)}
	}

	if rawResponse := strings.TrimSpace(string(responseBody)); rawResponse != "" {
		return &Error{Message: fallbackMessage + ": " + rawResponse}
	}

	return &Error{Message: fallbackMessage}
}
