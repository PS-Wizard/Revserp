package app

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestListingLookupResponseKeepsFailedSourceCandidate(t *testing.T) {
	row := sqlc.LocalListingLookup{
		Status: "failed", Query: "Nepal Life Insurance Company Ltd.", ExpectedCredits: 3,
		SourceLatitude:  pgtype.Float8{Float64: 27.7, Valid: true},
		SourceLongitude: pgtype.Float8{Float64: 85.3, Valid: true},
	}
	response, err := listingLookupResponse(row)
	if err != nil {
		t.Fatal(err)
	}
	if response.SourceCandidate == nil || response.SourceCandidate.DisplayName != row.Query || response.SourceCandidate.Latitude != 27.7 || response.SourceCandidate.Longitude != 85.3 {
		t.Fatalf("failed attempt lost its source identity: %+v", response)
	}
	if len(response.Candidates) != 0 {
		t.Fatal("failed source must not become a bindable Google candidate")
	}
	row.SourceLatitude.Valid = false
	row.SourceLongitude.Valid = false
	response, err = listingLookupResponse(row)
	if err != nil || response.SourceCandidate != nil {
		t.Fatalf("legacy evidence must not invent source coordinates: %+v, %v", response, err)
	}
}

// listingLookupRow builds completed lookup evidence with a stored provider
// response. The DTO never mutates the raw row.
func listingLookupRow(expected int32, raw string) sqlc.LocalListingLookup {
	return sqlc.LocalListingLookup{
		Status:          "completed",
		ExpectedCredits: expected,
		CreditKnown:     true,
		RawResponse:     []byte(raw),
	}
}

func TestListingLookupResponsePlacesRequireBusinessCoordinates(t *testing.T) {
	withCoordinates := `{"places":[{"placeId":"ChIJ-with","title":"Cafe With","address":"Street 1","latitude":27.71,"longitude":85.33}]}`
	withoutCoordinates := `{"places":[{"placeId":"ChIJ-without","title":"Cafe Without","address":"Street 2"}]}`

	// A free zero-credit Places row is only bindable with real business
	// coordinates, exactly like the paid 3-credit Serper row was.
	freeWith, err := listingLookupResponse(listingLookupRow(0, withCoordinates))
	if err != nil || freeWith.ExpectedCredits != 0 || len(freeWith.Candidates) != 1 {
		t.Fatalf("free Places row with coordinates = %#v, %v", freeWith, err)
	}
	if candidate := freeWith.Candidates[0]; candidate.PlaceID != "ChIJ-with" || candidate.Latitude != 27.71 || candidate.Longitude != 85.33 {
		t.Fatalf("free candidate = %#v", candidate)
	}
	freeWithout, err := listingLookupResponse(listingLookupRow(0, withoutCoordinates))
	if err != nil || len(freeWithout.Candidates) != 0 {
		t.Fatalf("free Places row without coordinates must not be bindable: %#v, %v", freeWithout, err)
	}

	// Historical 3-credit Serper evidence keeps its coordinate requirement.
	paidWithout, err := listingLookupResponse(listingLookupRow(3, withoutCoordinates))
	if err != nil || paidWithout.ExpectedCredits != 3 || len(paidWithout.Candidates) != 0 {
		t.Fatalf("historical 3-credit row lost its coordinate check: %#v, %v", paidWithout, err)
	}
	paidWith, err := listingLookupResponse(listingLookupRow(3, withCoordinates))
	if err != nil || len(paidWith.Candidates) != 1 {
		t.Fatalf("historical 3-credit evidence was not preserved: %#v, %v", paidWith, err)
	}

	// Legacy 1-credit rows never required coordinates and must stay readable.
	legacy, err := listingLookupResponse(listingLookupRow(1, withoutCoordinates))
	if err != nil || legacy.ExpectedCredits != 1 || len(legacy.Candidates) != 1 {
		t.Fatalf("legacy 1-credit evidence must survive: %#v, %v", legacy, err)
	}
}

func TestListingLookupResponseNonCompletedPlacesHasNoCandidates(t *testing.T) {
	row := listingLookupRow(0, `{"places":[{"placeId":"ChIJ-x","title":"Cafe","address":"A","latitude":27.7,"longitude":85.3}]}`)
	row.Status = "failed"
	response, err := listingLookupResponse(row)
	if err != nil || len(response.Candidates) != 0 {
		t.Fatalf("failed Places row must not expose bindable candidates: %#v, %v", response, err)
	}
}
