package app

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
	"github.com/ps-wizard/revserp/internal/serper"
)

var errInvalidListingSelection = errors.New("listing resolution: provide a search term and valid viewport coordinates")

type createListingLookupRequest struct {
	SearchQuery     string   `json:"search_query"`
	Latitude        *float64 `json:"latitude"`
	Longitude       *float64 `json:"longitude"`
	ExpectedCredits *int     `json:"expected_credits"`
}

func listingResolutionSelection(location sqlc.GetProjectLocationForUserRow, request createListingLookupRequest) (localvisibility.ListingLookupSelection, error) {
	query := strings.TrimSpace(request.SearchQuery)
	if query == "" {
		query = strings.TrimSpace(location.Name + " " + location.Address)
	}
	if query == "" || len(query) > 1000 || (request.Latitude == nil) != (request.Longitude == nil) {
		return localvisibility.ListingLookupSelection{}, errInvalidListingSelection
	}
	latitude, longitude := location.Latitude, location.Longitude
	if request.Latitude != nil {
		latitude, longitude = *request.Latitude, *request.Longitude
	}
	if _, err := serper.FormatMapsViewport(latitude, longitude); err != nil {
		return localvisibility.ListingLookupSelection{}, errInvalidListingSelection
	}
	canonical := struct {
		Name                string
		Latitude, Longitude float64
	}{strings.ToLower(strings.Join(strings.Fields(query), " ")), latitude, longitude}
	if canonical.Latitude == 0 {
		canonical.Latitude = 0
	}
	if canonical.Longitude == 0 {
		canonical.Longitude = 0
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return localvisibility.ListingLookupSelection{}, err
	}
	key := sha256.Sum256(raw)
	return localvisibility.ListingLookupSelection{Query: query, CandidateKey: fmt.Sprintf("%x", key), Latitude: latitude, Longitude: longitude}, nil
}
