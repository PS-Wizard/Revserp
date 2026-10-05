// Package geography implements the free geocoding calls of Layer 2 local SEO:
// forward address search and reverse coordinate lookup against a Nominatim
// endpoint. The client never caches; the lead server owns the permanent cache
// and singleton wiring, so this package only shapes requests and parses
// responses.
package geography

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// nominatimRequestTimeout bounds one HTTP round trip. The rate-limit wait
	// is separate and respects the caller's context.
	nominatimRequestTimeout = 30 * time.Second

	// nominatimMinRequestInterval keeps this client inside the public
	// Nominatim usage policy of at most one request per second. The wait is
	// per client and serialized, so concurrent callers never burst.
	nominatimMinRequestInterval = time.Second

	// nominatimSearchLimit is the fixed number of forward-search candidates.
	nominatimSearchLimit = 5

	// maxNominatimResponseBytes rejects an unexpectedly large body instead of
	// silently truncating it and decoding a partial document.
	maxNominatimResponseBytes = 2 << 20

	// defaultNominatimUserAgent identifies this product when no User-Agent is
	// configured. Public Nominatim requires an identifying agent.
	defaultNominatimUserAgent = "revserp-local-seo/1.0 (+https://revserp.com)"

	// nominatimAcceptLanguage asks the provider for Latin-script names.
	// Nominatim defaults to the local script, so a Nepal lookup returns the
	// name in Devanagari and it is substituted straight into a search query.
	// Nobody types that, so the generated query is useless.
	nominatimAcceptLanguage = "en"
)

// GeocodedAddress is one resolved place: finite coordinates plus the display
// name, chosen locality and lower-case ISO country code from the provider.
// Locality prefers city/town/village over suburb/neighbourhood/municipality.
type GeocodedAddress struct {
	DisplayName string  `json:"display_name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Locality    string  `json:"locality"`
	CountryCode string  `json:"country_code"`
}

// NominatimClient is an HTTP client bound to one Nominatim base URL and one
// identifying User-Agent. Every call is serialized and spaced by
// nominatimMinRequestInterval to respect the public provider rate limit.
//
// The client deliberately holds no cache and issues no paid calls; the lead
// server owns permanent caching and explicit refresh.
type NominatimClient struct {
	baseURL   string
	userAgent string
	http      *http.Client

	// rateToken is a one-slot semaphore that serializes requests. Holding it
	// across the wait makes the spacing correct for concurrent callers.
	rateToken   chan struct{}
	lastRequest time.Time
	minInterval time.Duration
}

// NewNominatimClient returns a client for baseURL (trailing slash optional)
// that sends userAgent on every request. An empty userAgent falls back to an
// identifying default because public Nominatim rejects anonymous agents.
func NewNominatimClient(baseURL, userAgent string) *NominatimClient {
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		userAgent = defaultNominatimUserAgent
	}
	return &NominatimClient{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		userAgent:   userAgent,
		http:        &http.Client{Timeout: nominatimRequestTimeout},
		rateToken:   make(chan struct{}, 1),
		minInterval: nominatimMinRequestInterval,
	}
}

// SearchAddresses resolves an address string to at most nominatimSearchLimit
// candidates using format=jsonv2 and addressdetails=1. A blank address returns
// a non-nil empty slice without contacting the provider, and the same holds
// when the provider has no match. Provider status failures and malformed
// responses return an error, never an empty result that looks like no-match.
func (c *NominatimClient) SearchAddresses(ctx context.Context, address string) ([]GeocodedAddress, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return []GeocodedAddress{}, nil
	}
	query := url.Values{
		"q":              {address},
		"format":         {"jsonv2"},
		"addressdetails": {"1"},
		"limit":          {strconv.Itoa(nominatimSearchLimit)},
	}
	raw, err := c.get(ctx, "/search", query)
	if err != nil {
		return nil, fmt.Errorf("nominatim search: %w", err)
	}

	var places []nominatimPlace
	if err := json.Unmarshal(raw, &places); err != nil {
		return nil, fmt.Errorf("nominatim search: decode response: %w", err)
	}
	results := make([]GeocodedAddress, 0, len(places))
	for _, place := range places {
		result, err := place.geocoded()
		if err != nil {
			return nil, fmt.Errorf("nominatim search: %w", err)
		}
		results = append(results, result)
	}
	return results, nil
}

// ReverseAddress resolves finite latitude/longitude to the nearest place using
// format=jsonv2 and addressdetails=1. Invalid coordinates are rejected before
// any request is sent.
func (c *NominatimClient) ReverseAddress(ctx context.Context, latitude, longitude float64) (GeocodedAddress, error) {
	if err := validateCoordinates(latitude, longitude); err != nil {
		return GeocodedAddress{}, fmt.Errorf("nominatim reverse: %w", err)
	}
	query := url.Values{
		"lat":            {strconv.FormatFloat(latitude, 'f', -1, 64)},
		"lon":            {strconv.FormatFloat(longitude, 'f', -1, 64)},
		"format":         {"jsonv2"},
		"addressdetails": {"1"},
	}
	raw, err := c.get(ctx, "/reverse", query)
	if err != nil {
		return GeocodedAddress{}, fmt.Errorf("nominatim reverse: %w", err)
	}

	var place nominatimPlace
	if err := json.Unmarshal(raw, &place); err != nil {
		return GeocodedAddress{}, fmt.Errorf("nominatim reverse: decode response: %w", err)
	}
	result, err := place.geocoded()
	if err != nil {
		return GeocodedAddress{}, fmt.Errorf("nominatim reverse: %w", err)
	}
	return result, nil
}

func (c *NominatimClient) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := c.acquireNominatimRequest(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", c.userAgent)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Language", nominatimAcceptLanguage)

	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxNominatimResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(raw) > maxNominatimResponseBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxNominatimResponseBytes)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", response.StatusCode, truncateBody(raw, 300))
	}
	return raw, nil
}

// Public Nominatim requests must stay single-threaded as well as rate limited.
func (c *NominatimClient) acquireNominatimRequest(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case c.rateToken <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-c.rateToken }
	if !c.lastRequest.IsZero() {
		if wait := c.minInterval - time.Since(c.lastRequest); wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				release()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	c.lastRequest = time.Now()
	return release, nil
}

// nominatimPlace is the shared jsonv2 shape of a search element and a reverse
// result. lat/lon arrive as strings and are parsed strictly.
type nominatimPlace struct {
	DisplayName string           `json:"display_name"`
	Lat         string           `json:"lat"`
	Lon         string           `json:"lon"`
	Address     nominatimAddress `json:"address"`
}

type nominatimAddress struct {
	City          string `json:"city"`
	Town          string `json:"town"`
	Village       string `json:"village"`
	CityDistrict  string `json:"city_district"`
	Suburb        string `json:"suburb"`
	Neighbourhood string `json:"neighbourhood"`
	Municipality  string `json:"municipality"`
	CountryCode   string `json:"country_code"`
}

func (p nominatimPlace) geocoded() (GeocodedAddress, error) {
	latitude, err := parseCoordinate("latitude", p.Lat)
	if err != nil {
		return GeocodedAddress{}, err
	}
	longitude, err := parseCoordinate("longitude", p.Lon)
	if err != nil {
		return GeocodedAddress{}, err
	}
	return GeocodedAddress{
		DisplayName: p.DisplayName,
		Latitude:    latitude,
		Longitude:   longitude,
		Locality:    p.Address.locality(),
		CountryCode: p.Address.CountryCode,
	}, nil
}

// locality returns the smallest name a person would use for this place, not
// the widest boundary the provider carries. A Nepal lookup offers both
// "Kathmandu-05" and "Kathmandu Metropolitan City"; the first is a place, the
// second is a government boundary spanning roughly 50 square kilometres.
// Substituting a boundary into a search query burns a slot on words no
// customer types. Levels are tried smallest to widest, and any level the
// provider omits is skipped.
func (a nominatimAddress) locality() string {
	for _, candidate := range a.localityLadder() {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return ""
}

// localityLadder lists address levels smallest to widest. Neighbourhood,
// suburb, village and town are the names a person gives when describing where
// they are; city_district is the ward. The widest levels trail last so they
// are only reached when nothing narrower exists.
func (a nominatimAddress) localityLadder() []string {
	return []string{
		a.Neighbourhood,
		a.Suburb,
		a.Village,
		a.Town,
		a.CityDistrict,
		a.Municipality,
		a.City,
	}
}

// parseCoordinate parses a Nominatim lat/lon string strictly and rejects
// missing, malformed, non-finite and out-of-range values.
func parseCoordinate(field, raw string) (float64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, fmt.Errorf("missing %s", field)
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s %q: %w", field, raw, err)
	}
	limit := 180.0
	if field == "latitude" {
		limit = 90
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < -limit || value > limit {
		return 0, fmt.Errorf("%s %v outside [%v,%v]", field, value, -limit, limit)
	}
	return value, nil
}

// validateCoordinates rejects a non-finite or out-of-range latitude/longitude
// pair before any provider request is made.
func validateCoordinates(latitude, longitude float64) error {
	if math.IsNaN(latitude) || math.IsInf(latitude, 0) || latitude < -90 || latitude > 90 {
		return fmt.Errorf("latitude %v outside [-90,90]", latitude)
	}
	if math.IsNaN(longitude) || math.IsInf(longitude, 0) || longitude < -180 || longitude > 180 {
		return fmt.Errorf("longitude %v outside [-180,180]", longitude)
	}
	return nil
}

func truncateBody(raw []byte, max int) string {
	if len(raw) > max {
		return string(raw[:max])
	}
	return string(raw)
}
