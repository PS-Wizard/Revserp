// Package googleplaces is a thin client for the Google Places API (New) Text
// Search endpoint. It resolves a brand's listing identity from a text query
// and a geographic bias, independent of the Serper ranking path.
package googleplaces

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/ps-wizard/revserp/internal/serper"
)

// PlacesListingFieldMask requests the required identity fields at the Pro tier.
// Google bills the whole request at its highest requested SKU, not per field.
const PlacesListingFieldMask = "places.id,places.displayName,places.location,places.formattedAddress"

const (
	placesTextSearchDefaultEndpoint = "https://places.googleapis.com/v1/places:searchText"

	// placesTextSearchTimeout bounds the single request. This client never
	// retries: Text Search is billable per call.
	placesTextSearchTimeout = 30 * time.Second

	// placesLocationBiasRadiusM is a bias, not a restriction. Results outside
	// the circle are still returned, which is intended: identity is accepted
	// from anywhere, unlike a Serper viewport.
	placesLocationBiasRadiusM = 50000
)

// PlacesListingClient resolves listing identity through one Places Text Search call.
type PlacesListingClient struct {
	apiKey   string
	endpoint string
	http     *http.Client
}

// NewPlacesListingClient builds a Places Text Search client. An empty endpoint
// uses the live Google endpoint.
func NewPlacesListingClient(apiKey, endpoint string) *PlacesListingClient {
	if endpoint == "" {
		endpoint = placesTextSearchDefaultEndpoint
	}
	return &PlacesListingClient{
		apiKey:   apiKey,
		endpoint: endpoint,
		http: &http.Client{
			Timeout: placesTextSearchTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// LookupMapsListing runs one Places Text Search and maps the result into the
// stored serper.MapsListingResponse evidence shape. It always returns
// Credits=0: Google charges this call itself, and this client never
// manufactures a Serper-style provider charge. It makes no second call.
func (c *PlacesListingClient) LookupMapsListing(ctx context.Context, query string, latitude, longitude float64) (serper.MapsListingResponse, error) {
	var response serper.MapsListingResponse
	if err := validatePlacesListingInput(query, latitude, longitude); err != nil {
		return response, fmt.Errorf("google places listing: %w", err)
	}
	decoded, err := c.postPlacesTextSearch(ctx, placesTextSearchRequest{
		TextQuery:    query,
		LanguageCode: "en",
		LocationBias: placesLocationBias{
			Circle: placesBiasCircle{
				Center: placesLatLng{Latitude: latitude, Longitude: longitude},
				Radius: placesLocationBiasRadiusM,
			},
		},
	})
	if err != nil {
		return response, err
	}
	return mapPlacesTextSearchResponse(decoded), nil
}

// validatePlacesListingInput rejects a blank query or out-of-range coordinates
// before any billable request is sent.
func validatePlacesListingInput(query string, latitude, longitude float64) error {
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("empty query")
	}
	if math.IsNaN(latitude) || math.IsInf(latitude, 0) || latitude < -90 || latitude > 90 {
		return fmt.Errorf("latitude %v outside [-90,90]", latitude)
	}
	if math.IsNaN(longitude) || math.IsInf(longitude, 0) || longitude < -180 || longitude > 180 {
		return fmt.Errorf("longitude %v outside [-180,180]", longitude)
	}
	return nil
}

type placesTextSearchRequest struct {
	TextQuery    string             `json:"textQuery"`
	LanguageCode string             `json:"languageCode"`
	LocationBias placesLocationBias `json:"locationBias"`
}

type placesLocationBias struct {
	Circle placesBiasCircle `json:"circle"`
}

type placesBiasCircle struct {
	Center placesLatLng `json:"center"`
	Radius float64      `json:"radius"`
}

type placesLatLng struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type placesTextSearchResponse struct {
	Places []placesTextSearchPlace `json:"places"`
}

type placesTextSearchPlace struct {
	ID               string                `json:"id"`
	DisplayName      *placesLocalizedText  `json:"displayName"`
	FormattedAddress string                `json:"formattedAddress"`
	Location         *placesLatLngPointers `json:"location"`
}

type placesLocalizedText struct {
	Text string `json:"text"`
}

// placesLatLngPointers keeps a missing latitude/longitude distinct from a real
// zero coordinate in the returned place.
type placesLatLngPointers struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}
