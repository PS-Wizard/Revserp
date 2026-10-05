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
