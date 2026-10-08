package googleplaces

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ps-wizard/revserp/internal/serper"
)

// placesTextSearchNameRequest searches listings by name only. It carries no
// locationBias: the bound-location flow has no draft coordinates, and sending
// a zero coordinate would fake a bias in the Gulf of Guinea.
type placesTextSearchNameRequest struct {
	TextQuery    string `json:"textQuery"`
	LanguageCode string `json:"languageCode"`
}

// SearchListingsByName runs one unbiased Places Text Search for a typed
// business name and maps it into the stored serper.MapsListingResponse
// evidence shape. Credits is always 0: Google bills the call itself, and this
// client never manufactures a Serper-style provider charge. It makes no
// second call and never retries: Text Search is billable per call.
func (c *PlacesListingClient) SearchListingsByName(ctx context.Context, query string) (serper.MapsListingResponse, error) {
	var response serper.MapsListingResponse
	query = strings.TrimSpace(query)
	if err := validatePlacesListingNameQuery(query); err != nil {
		return response, fmt.Errorf("google places listing: %w", err)
	}
	decoded, err := c.postPlacesTextSearch(ctx, placesTextSearchNameRequest{TextQuery: query, LanguageCode: "en"})
	if err != nil {
		return response, err
	}
	return mapPlacesTextSearchResponse(decoded), nil
}

func validatePlacesListingNameQuery(query string) error {
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("empty query")
	}
	if len(query) > 500 {
		return fmt.Errorf("query must fit within 500 bytes")
	}
	return nil
}

// postPlacesTextSearch sends one Text Search request shared by the biased and
// unbiased lookups. One call, no retry, credentials stay out of errors.
func (c *PlacesListingClient) postPlacesTextSearch(ctx context.Context, payload any) (placesTextSearchResponse, error) {
	var decoded placesTextSearchResponse
	body, err := json.Marshal(payload)
	if err != nil {
		return decoded, fmt.Errorf("google places listing: encode request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return decoded, fmt.Errorf("google places listing: build request: %w", err)
	}
	request.Header.Set("X-Goog-Api-Key", c.apiKey)
	request.Header.Set("X-Goog-FieldMask", PlacesListingFieldMask)
	request.Header.Set("Content-Type", "application/json")

	httpResponse, err := c.http.Do(request)
	if err != nil {
		return decoded, fmt.Errorf("google places listing: transport: %w", err)
	}
	defer func() { _ = httpResponse.Body.Close() }()

	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return decoded, fmt.Errorf("google places listing: status %d", httpResponse.StatusCode)
	}

	if err := json.NewDecoder(io.LimitReader(httpResponse.Body, 2<<20)).Decode(&decoded); err != nil {
		return decoded, fmt.Errorf("google places listing: decode response: %w", err)
	}
	return decoded, nil
}

// mapPlacesTextSearchResponse keeps a missing place coordinate distinct from a
// real zero coordinate in the returned listing.
func mapPlacesTextSearchResponse(decoded placesTextSearchResponse) serper.MapsListingResponse {
	response := serper.MapsListingResponse{Places: make([]serper.MapsListingPlace, 0, len(decoded.Places))}
	for _, place := range decoded.Places {
		mapped := serper.MapsListingPlace{
			PlaceID: place.ID,
			Address: place.FormattedAddress,
		}
		if place.DisplayName != nil {
			mapped.Title = place.DisplayName.Text
		}
		if place.Location != nil {
			mapped.Latitude = place.Location.Latitude
			mapped.Longitude = place.Location.Longitude
		}
		response.Places = append(response.Places, mapped)
	}
	return response
}
