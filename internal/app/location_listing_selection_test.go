package app

import (
	"math"
	"testing"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestListingResolutionDirectSearchAndViewport(t *testing.T) {
	latitude, longitude := 27.7096675, 85.3222025
	location := sqlc.GetProjectLocationForUserRow{Name: "draft", Latitude: 1, Longitude: 2}
	input := createListingLookupRequest{SearchQuery: "Sun Nepal Life Insurance", Latitude: &latitude, Longitude: &longitude}
	selection, err := listingResolutionSelection(location, input)
	if err != nil || selection.Query != input.SearchQuery || selection.Latitude != latitude || selection.Longitude != longitude {
		t.Fatalf("direct search must use requested viewport without geography lookup: %+v, %v", selection, err)
	}
	input.SearchQuery = " sun  nepal life insurance "
	same, err := listingResolutionSelection(location, input)
	if err != nil || same.CandidateKey != selection.CandidateKey {
		t.Fatalf("same normalized search must retain attempt key: %+v, %v", same, err)
	}
	input.SearchQuery = "Different insurance company"
	different, err := listingResolutionSelection(location, input)
	if err != nil || different.CandidateKey == selection.CandidateKey {
		t.Fatal("different explicit search must have a different attempt key")
	}
	input.Latitude = nil
	if _, err := listingResolutionSelection(location, input); err == nil {
		t.Fatal("partial viewport must fail before reservation")
	}
	bad := math.NaN()
	input.Latitude = &bad
	if _, err := listingResolutionSelection(location, input); err == nil {
		t.Fatal("non-finite viewport must fail before reservation")
	}
}
