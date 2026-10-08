package serper

import (
	"context"
	"fmt"
)

// MapsListingPlace keeps missing Google coordinates distinct from real zero coordinates.
type MapsListingPlace struct {
	PlaceID   string   `json:"placeId"`
	CID       string   `json:"cid"`
	Title     string   `json:"title"`
	Address   string   `json:"address"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

// HasMapCoordinates requires both coordinates in degrees and within geographic bounds.
func (p MapsListingPlace) HasMapCoordinates() bool {
	return p.Latitude != nil && p.Longitude != nil && validateMapsCoordinates(*p.Latitude, *p.Longitude) == nil
}

// MapsListingResponse is paid Maps identity evidence, not the CID-only Places response.
type MapsListingResponse struct {
	LL      string             `json:"ll"`
	Places  []MapsListingPlace `json:"places"`
	Credits int                `json:"credits"`
}

// LookupMapsListing preserves decoded charges on errors and never retries a paid request.
// The display viewport may sit far from the request (see MapsAt), so only a
// malformed echoed viewport or a missing/null places array is rejected.
func (c *Client) LookupMapsListing(ctx context.Context, query string, latitude, longitude float64) (MapsListingResponse, error) {
	var response MapsListingResponse
	viewport, err := FormatMapsViewport(latitude, longitude)
	if err != nil {
		return response, fmt.Errorf("serper listing viewport: %w", err)
	}
	if err := c.post(ctx, c.mapsEndpoint, map[string]string{"q": query, "ll": viewport, "hl": "en"}, &response); err != nil {
		return response, fmt.Errorf("serper listing lookup: %w", err)
	}
	if response.Places == nil {
		return response, fmt.Errorf("serper listing lookup: missing places array")
	}
	if _, err := mapsViewportDrift(response.LL, latitude, longitude); err != nil {
		return response, fmt.Errorf("serper listing viewport: %w", err)
	}
	return response, nil
}
