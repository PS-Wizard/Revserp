// Package serper is a thin client for the Serper.dev Google search APIs used
// by maps visibility checks. Only the endpoints this product consumes are
// implemented: POST /maps (local pack around a query's implied viewport) and
// POST /places (a brand's listings in a city, used to resolve our listing).
package serper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	requestTimeout = 30 * time.Second

	// serperMapsZoom is the fixed grid viewport zoom.
	serperMapsZoom = 14

	// serperMapsLLFormat builds the "ll" viewport string. Keep the literal "z"
	// suffix and latitude-first order: serper silently samples a different
	// viewport when either is wrong instead of returning an error.
	serperMapsLLFormat = "@%.6f,%.6f,%dz"

	// Serper normalizes viewport coordinates; a live reference drifted 1.46 metres.
	MapsViewportToleranceM = 10.0
)

type MapsRequest struct {
	Q string `json:"q"`
}

type Place struct {
	Position     int               `json:"position"`
	Title        string            `json:"title"`
	Address      string            `json:"address"`
	Latitude     float64           `json:"latitude"`
	Longitude    float64           `json:"longitude"`
	Rating       float64           `json:"rating"`
	RatingCount  int               `json:"ratingCount"`
	Type         string            `json:"type"`
	Types        []string          `json:"types"`
	Website      string            `json:"website"`
	PhoneNumber  string            `json:"phoneNumber"`
	Description  string            `json:"description"`
	CID          string            `json:"cid"`
	PlaceID      string            `json:"placeId"`
	FID          string            `json:"fid"`
	OpeningHours map[string]string `json:"openingHours"`
	ThumbnailURL string            `json:"thumbnailUrl"`
}

type MapsResponse struct {
	LL             string   `json:"ll"`
	Places         []Place  `json:"places"`
	Credits        int      `json:"credits"`
	ViewportDriftM *float64 `json:"viewport_drift_m,omitempty"`
}

type PlacesResponse struct {
	Places  []Place `json:"places"`
	Credits int     `json:"credits"`
}

// Review is one public Google Maps review. No owner reply exists in this
// payload: Serper exposes the customer-facing review card only.
type Review struct {
	Rating  int           `json:"rating"`
	Date    string        `json:"date"`
	IsoDate string        `json:"isoDate"`
	Snippet string        `json:"snippet"`
	Likes   int           `json:"likes"`
	Link    string        `json:"link"`
	ID      string        `json:"id"`
	User    ReviewUser    `json:"user"`
	Media   []ReviewMedia `json:"media"`
}

type ReviewUser struct {
	Name      string `json:"name"`
	Thumbnail string `json:"thumbnail"`
	Link      string `json:"link"`
	Reviews   int    `json:"reviews"`
	Photos    int    `json:"photos"`
}

type ReviewMedia struct {
	Type     string `json:"type"`
	ImageURL string `json:"imageUrl"`
}

// ReviewsResponse is the /reviews payload: 20 reviews per call, 1 credit, and
// a cursor. num/page are echoed by the API but ignored, so pagination is
// cursor-only via NextPageToken sent back as "nextPageToken".
type ReviewsResponse struct {
	Reviews       []Review `json:"reviews"`
	NextPageToken string   `json:"nextPageToken"`
	Credits       int      `json:"credits"`
}

func (c *Client) Reviews(ctx context.Context, placeID string) (ReviewsResponse, error) {
	var response ReviewsResponse
	body := map[string]string{"placeId": placeID}
	if err := c.post(ctx, c.reviewsEndpoint, body, &response); err != nil {
		return ReviewsResponse{}, fmt.Errorf("serper reviews: %w", err)
	}
	return response, nil
}

type Client struct {
	apiKey          string
	mapsEndpoint    string
	placesEndpoint  string
	reviewsEndpoint string
	http            *http.Client
}

func NewClient(apiKey, mapsEndpoint, placesEndpoint, reviewsEndpoint string) *Client {
	return &Client{
		apiKey:          apiKey,
		mapsEndpoint:    mapsEndpoint,
		placesEndpoint:  placesEndpoint,
		reviewsEndpoint: reviewsEndpoint,
		http:            &http.Client{Timeout: requestTimeout},
	}
}

func (c *Client) Maps(ctx context.Context, query string) (MapsResponse, error) {
	var response MapsResponse
	if err := c.post(ctx, c.mapsEndpoint, map[string]string{"q": query}, &response); err != nil {
		return MapsResponse{}, fmt.Errorf("serper maps: %w", err)
	}
	return response, nil
}

// FormatMapsViewport validates latitude/longitude and formats the fixed
// zoom-14 "ll" viewport string, the single source of truth for stored ranks.
func FormatMapsViewport(latitude, longitude float64) (string, error) {
	if err := validateMapsCoordinates(latitude, longitude); err != nil {
		return "", err
	}
	return fmt.Sprintf(serperMapsLLFormat, latitude, longitude, serperMapsZoom), nil
}

// MapsAt runs one Serper maps search at the real latitude/longitude viewport,
// always at the fixed zoom-14 grid scale, sending "q", "ll", and English "hl".
//
// It returns the decoded MapsResponse alongside the error so a charged call's
// credits are not lost when the echoed viewport is rejected; callers must not
// rank places from a response returned with a non-nil error.
func (c *Client) MapsAt(ctx context.Context, query string, latitude, longitude float64) (MapsResponse, error) {
	viewport, err := FormatMapsViewport(latitude, longitude)
	if err != nil {
		return MapsResponse{}, fmt.Errorf("serper maps at: %w", err)
	}
	body := map[string]string{"q": query, "ll": viewport, "hl": "en"}

	var response MapsResponse
	if err := c.post(ctx, c.mapsEndpoint, body, &response); err != nil {
		return response, fmt.Errorf("serper maps at: %w", err)
	}
	drift, err := mapsViewportDrift(response.LL, latitude, longitude)
	if err != nil {
		return response, fmt.Errorf("serper maps viewport format: %w", err)
	}
	response.ViewportDriftM = &drift
	if drift > MapsViewportToleranceM {
		return response, fmt.Errorf("serper maps viewport drift: %.3f metres exceeds %.0f metres", drift, MapsViewportToleranceM)
	}
	return response, nil
}

func (c *Client) Places(ctx context.Context, query string) (PlacesResponse, error) {
	var response PlacesResponse
	if err := c.post(ctx, c.placesEndpoint, map[string]string{"q": query}, &response); err != nil {
		return response, fmt.Errorf("serper places: %w", err)
	}
	if response.Places == nil {
		return response, fmt.Errorf("serper places: missing places array")
	}
	return response, nil
}

func (c *Client) post(ctx context.Context, endpoint string, body any, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("X-API-KEY", c.apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	// A failed HTTP response can still contain billable provider credits.
	decodeErr := json.Unmarshal(raw, out)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", response.StatusCode, truncate(raw, 300))
	}
	if decodeErr != nil {
		return fmt.Errorf("decode response: %w", decodeErr)
	}
	return nil
}

func truncate(raw []byte, max int) string {
	if len(raw) > max {
		return string(raw[:max])
	}
	return string(raw)
}

// validateMapsCoordinates rejects a latitude/longitude pair that serper would
// sample incorrectly: NaN or infinite values and out-of-range coordinates.
func validateMapsCoordinates(latitude, longitude float64) error {
	if math.IsNaN(latitude) || math.IsInf(latitude, 0) || latitude < -90 || latitude > 90 {
		return fmt.Errorf("latitude %v outside [-90,90]", latitude)
	}
	if math.IsNaN(longitude) || math.IsInf(longitude, 0) || longitude < -180 || longitude > 180 {
		return fmt.Errorf("longitude %v outside [-180,180]", longitude)
	}
	return nil
}

func validateMapsLLViewport(raw string, latitude, longitude float64) error {
	drift, err := mapsViewportDrift(raw, latitude, longitude)
	if err != nil {
		return fmt.Errorf("viewport format: %w", err)
	}
	if drift > MapsViewportToleranceM {
		return fmt.Errorf("viewport drift: %.3f metres exceeds %.0f metres", drift, MapsViewportToleranceM)
	}
	return nil
}

func mapsViewportDrift(raw string, latitude, longitude float64) (float64, error) {
	if err := validateMapsCoordinates(latitude, longitude); err != nil {
		return 0, err
	}
	echoLatitude, echoLongitude, zoom, err := parseMapsLLViewport(raw)
	if err != nil {
		return 0, err
	}
	if err := validateMapsCoordinates(echoLatitude, echoLongitude); err != nil {
		return 0, fmt.Errorf("viewport %q echoed invalid coordinates: %w", raw, err)
	}
	if zoom != serperMapsZoom {
		return 0, fmt.Errorf("viewport %q zoom = %d, want %d", raw, zoom, serperMapsZoom)
	}
	toRadians := math.Pi / 180
	deltaLatitude := (echoLatitude - latitude) * toRadians
	deltaLongitude := (echoLongitude - longitude) * toRadians
	a := math.Pow(math.Sin(deltaLatitude/2), 2) + math.Cos(latitude*toRadians)*math.Cos(echoLatitude*toRadians)*math.Pow(math.Sin(deltaLongitude/2), 2)
	a = math.Max(0, math.Min(1, a))
	return 6371008.8 * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a)), nil
}

// parseMapsLLViewport parses serper's "@lat,lon,zoomz" viewport string. The
// zoom must carry its trailing "z"; a missing suffix is a malformed viewport,
// not a zero zoom.
func parseMapsLLViewport(raw string) (latitude, longitude float64, zoom int, err error) {
	body, found := strings.CutPrefix(raw, "@")
	if !found {
		return 0, 0, 0, fmt.Errorf("viewport %q missing @ prefix", raw)
	}
	fields := strings.Split(body, ",")
	if len(fields) != 3 {
		return 0, 0, 0, fmt.Errorf("viewport %q has %d fields, want 3", raw, len(fields))
	}
	latitude, err = strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("viewport %q latitude %q: %w", raw, fields[0], err)
	}
	longitude, err = strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("viewport %q longitude %q: %w", raw, fields[1], err)
	}
	zoomText, found := strings.CutSuffix(fields[2], "z")
	if !found {
		return 0, 0, 0, fmt.Errorf("viewport %q zoom %q missing z suffix", raw, fields[2])
	}
	zoom, err = strconv.Atoi(zoomText)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("viewport %q zoom %q: %w", raw, fields[2], err)
	}
	return latitude, longitude, zoom, nil
}
