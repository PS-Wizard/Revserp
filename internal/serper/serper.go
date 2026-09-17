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
	"net/http"
	"time"
)

const requestTimeout = 30 * time.Second

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
	LL      string  `json:"ll"`
	Places  []Place `json:"places"`
	Credits int     `json:"credits"`
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

func (c *Client) Places(ctx context.Context, query string) (PlacesResponse, error) {
	var response PlacesResponse
	if err := c.post(ctx, c.placesEndpoint, map[string]string{"q": query}, &response); err != nil {
		return PlacesResponse{}, fmt.Errorf("serper places: %w", err)
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
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", response.StatusCode, truncate(raw, 300))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func truncate(raw []byte, max int) string {
	if len(raw) > max {
		return string(raw[:max])
	}
	return string(raw)
}
