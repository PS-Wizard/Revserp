package locationlandmarks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Nearby Pro choice: explicit refresh sends exactly one searchNearby call with
// an independent monthly allowance; usage beyond the allowance is billable to
// the key, and zero app credits must never be read as guarantee-free. This
// client never retries: searchNearby is billable per call.

const (
	// placesFieldMask is pinned: only the stored landmark fields are requested.
	placesFieldMask = "places.id,places.displayName,places.location,places.primaryType"

	placesSearchRadiusM   = 5000.0
	placesMaxResultCount  = 20
	placesRankPreference  = "POPULARITY"
	placesLanguageCode    = "en"
	placesProvider        = "google_places"
	placesProviderRefPref = "places/"
)

// placesIncludedPrimaryTypes is the flat Google Table A allowlist. Filtering
// happens server-side via includedPrimaryTypes, so bank, lab, and academic
// subunit results are excluded by primary type with no string heuristics.
var placesIncludedPrimaryTypes = []string{
	"tourist_attraction",
	"museum",
	"art_gallery",
	"park",
	"national_park",
	"zoo",
	"aquarium",
	"botanical_garden",
	"plaza",
	"hindu_temple",
	"buddhist_temple",
	"church",
	"mosque",
	"synagogue",
	"historical_landmark",
	"shopping_mall",
	"university",
	"stadium",
	"train_station",
	"performing_arts_theater",
	"campground",
}

var placesAllowedPrimaryTypes = func() map[string]bool {
	allowed := make(map[string]bool, len(placesIncludedPrimaryTypes))
	for _, t := range placesIncludedPrimaryTypes {
		allowed[t] = true
	}
	return allowed
}()

type placesSearchNearbyRequest struct {
	IncludedPrimaryTypes []string                  `json:"includedPrimaryTypes"`
	MaxResultCount       int                       `json:"maxResultCount"`
	RankPreference       string                    `json:"rankPreference"`
	LanguageCode         string                    `json:"languageCode"`
	LocationRestriction  placesLocationRestriction `json:"locationRestriction"`
}

type placesLocationRestriction struct {
	Circle placesCircle `json:"circle"`
}

type placesCircle struct {
	Center placesCenter `json:"center"`
	Radius float64      `json:"radius"`
}

type placesCenter struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type placesSearchNearbyResponse struct {
	Places []placesEntry `json:"places"`
}

type placesEntry struct {
	ID          string               `json:"id"`
	DisplayName *placesDisplayName   `json:"displayName"`
	Location    *placesEntryLocation `json:"location"`
	PrimaryType string               `json:"primaryType"`
}

type placesDisplayName struct {
	Text string `json:"text"`
}

type placesEntryLocation struct {
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

func (c *Client) fetchPlacesCandidates(ctx context.Context, latitude, longitude float64) ([]Candidate, error) {
	body, err := json.Marshal(placesSearchNearbyRequest{
		IncludedPrimaryTypes: placesIncludedPrimaryTypes,
		MaxResultCount:       placesMaxResultCount,
		RankPreference:       placesRankPreference,
		LanguageCode:         placesLanguageCode,
		LocationRestriction: placesLocationRestriction{
			Circle: placesCircle{
				Center: placesCenter{Latitude: latitude, Longitude: longitude},
				Radius: placesSearchRadiusM,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("places candidates: encode request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("places candidates: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Goog-Api-Key", c.apiKey)
	request.Header.Set("X-Goog-FieldMask", placesFieldMask)

	raw, err := c.do(request)
	if err != nil {
		return nil, fmt.Errorf("places candidates: %w", err)
	}
	var payload placesSearchNearbyResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("places candidates: decode response: %w", err)
	}
	if len(payload.Places) > placesMaxResultCount {
		return nil, fmt.Errorf("places candidates: %d entries exceed the %d entry limit", len(payload.Places), placesMaxResultCount)
	}

	candidates := make([]Candidate, 0, len(payload.Places))
	seen := make(map[string]bool, len(payload.Places))
	for _, entry := range payload.Places {
		candidate, ok := entry.candidate()
		if !ok || seen[candidate.ProviderRef] {
			continue
		}
		seen[candidate.ProviderRef] = true
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

func (e placesEntry) candidate() (Candidate, bool) {
	id := strings.TrimSpace(e.ID)
	if id == "" {
		return Candidate{}, false
	}
	name := ""
	if e.DisplayName != nil {
		name = strings.TrimSpace(e.DisplayName.Text)
	}
	if name == "" {
		return Candidate{}, false
	}
	if e.Location == nil || e.Location.Latitude == nil || e.Location.Longitude == nil {
		return Candidate{}, false
	}
	latitude, longitude := *e.Location.Latitude, *e.Location.Longitude
	if validateLandmarkCoordinates(latitude, longitude) != nil {
		return Candidate{}, false
	}
	primaryType := strings.TrimSpace(e.PrimaryType)
	if primaryType == "" || !placesAllowedPrimaryTypes[primaryType] {
		return Candidate{}, false
	}
	return Candidate{
		Name:        name,
		Latitude:    latitude,
		Longitude:   longitude,
		Provider:    placesProvider,
		ProviderRef: placesProviderRefPref + id,
		Categories:  []string{primaryType},
	}, true
}
