// Package locationlandmarks discovers nearby Google Places landmarks with one
// Places API (New) Nearby Search (searchNearby) call per refresh; it holds no
// cache and writes no rows, so the caller owns storage and refresh.
//
// Nearby Pro choice: explicit refresh sends exactly one searchNearby call with
// an independent monthly allowance; usage beyond the allowance is billable to
// the key, and zero app credits must never be read as guarantee-free.
package locationlandmarks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// ErrProviderBusy marks a provider that refused work because it is overloaded:
// HTTP 429, HTTP 504, or a Google RESOURCE_EXHAUSTED error body. It is
// deliberately separate from malformed-response errors, which a retry cannot
// fix. Busy responses fail immediately; the caller decides when to try again.
var ErrProviderBusy = errors.New("landmark provider busy")

const (
	defaultSearchNearbyEndpoint = "https://places.googleapis.com/v1/places:searchNearby"
	defaultTimeout              = 30 * time.Second

	maxProviderResponseBytes = 8 << 20
)

// Config injects the Google API key and endpoint override; empty endpoint falls
// back to the live searchNearby endpoint. Tests pass an httptest URL here.
type Config struct {
	APIKey  string
	BaseURL string
	Timeout time.Duration
}

// Client discovers Google Places landmark candidates with one searchNearby call.
type Client struct {
	apiKey   string
	endpoint string
	http     *http.Client
}

// Candidate is one valid, named Places entry; ProviderRef is "places/<placeID>".
type Candidate struct {
	Name        string   `json:"name"`
	Latitude    float64  `json:"latitude"`
	Longitude   float64  `json:"longitude"`
	Provider    string   `json:"provider"`
	ProviderRef string   `json:"provider_ref"`
	Categories  []string `json:"categories"`
}

// Landmark is a discovered candidate; metres are whole and coordinates are
// provider-returned, never re-derived. There is no road distance: straight_line_m
// is computed locally and every valid entry is kept regardless of distance.
type Landmark struct {
	Name          string    `json:"name"`
	Latitude      float64   `json:"latitude"`
	Longitude     float64   `json:"longitude"`
	StraightLineM int       `json:"straight_line_m"`
	Provider      string    `json:"provider"`
	ProviderRef   string    `json:"provider_ref"`
	Categories    []string  `json:"categories"`
	FetchedAt     time.Time `json:"fetched_at"`
}

// NewClient builds a discovery client, defaulting unset endpoint and timeout.
// The API key is stored but never logged or returned in errors.
func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if endpoint == "" {
		endpoint = defaultSearchNearbyEndpoint
	}
	return &Client{
		apiKey:   strings.TrimSpace(cfg.APIKey),
		endpoint: endpoint,
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// DiscoverCandidates returns valid named candidates within the pinned circle,
// rejecting missing credentials or invalid coordinates before any request.
func (c *Client) DiscoverCandidates(ctx context.Context, latitude, longitude float64) ([]Candidate, error) {
	if err := validateLandmarkCoordinates(latitude, longitude); err != nil {
		return nil, fmt.Errorf("landmark discovery: %w", err)
	}
	if strings.TrimSpace(c.apiKey) == "" {
		return nil, fmt.Errorf("landmark discovery: missing google api key")
	}
	return c.fetchPlacesCandidates(ctx, latitude, longitude)
}

// Discover is the complete refresh path: one searchNearby call. It returns
// landmarks only on full success, so a caller never replaces a good cache on a
// failed fetch. Errors and empty results never prevent grid runs: the caller
// keeps its existing cache and queries.
func (c *Client) Discover(ctx context.Context, latitude, longitude float64) ([]Landmark, error) {
	candidates, err := c.DiscoverCandidates(ctx, latitude, longitude)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return []Landmark{}, nil
	}
	fetchedAt := time.Now().UTC()
	landmarks := make([]Landmark, 0, len(candidates))
	for _, candidate := range candidates {
		landmarks = append(landmarks, Landmark{
			Name:          candidate.Name,
			Latitude:      candidate.Latitude,
			Longitude:     candidate.Longitude,
			StraightLineM: int(math.Round(straightLineMeters(latitude, longitude, candidate.Latitude, candidate.Longitude))),
			Provider:      candidate.Provider,
			ProviderRef:   candidate.ProviderRef,
			Categories:    candidate.Categories,
			FetchedAt:     fetchedAt,
		})
	}
	return landmarks, nil
}

func (c *Client) do(request *http.Request) ([]byte, error) {
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := readBoundedBody(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		snippet := truncateBody([]byte(strings.ReplaceAll(string(raw), c.apiKey, "[redacted]")), 300)
		if response.StatusCode == http.StatusTooManyRequests ||
			response.StatusCode == http.StatusGatewayTimeout ||
			strings.Contains(snippet, "RESOURCE_EXHAUSTED") {
			return nil, fmt.Errorf("%w: status %d: %s", ErrProviderBusy, response.StatusCode, snippet)
		}
		return nil, fmt.Errorf("status %d: %s", response.StatusCode, snippet)
	}
	return raw, nil
}

func readBoundedBody(body io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxProviderResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(raw) > maxProviderResponseBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxProviderResponseBytes)
	}
	return raw, nil
}

func truncateBody(raw []byte, max int) string {
	if len(raw) > max {
		return string(raw[:max])
	}
	return string(raw)
}
