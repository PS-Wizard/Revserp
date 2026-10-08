package app

import (
	"strings"
	"testing"
	"time"

	"github.com/ps-wizard/revserp/internal/googleplaces"
)

// Pure validation for the project-scoped Places listing search. No database,
// no provider calls: these run with empty DB URLs and live flags off.

func TestValidateLocationListingSearchQuery(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		wantErr           bool
	}{
		{"typed name", "Sun Nepal Life Insurance", "Sun Nepal Life Insurance", false},
		{"extra spacing collapses", "  sun   nepal  life ", "sun nepal life", false},
		{"blank is rejected", "   ", "", true},
		{"empty is rejected", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateLocationListingSearchQuery(tc.input)
			if tc.wantErr != (err != nil) {
				t.Fatalf("query %q err = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("query = %q, want %q", got, tc.want)
			}
		})
	}
	if _, err := validateLocationListingSearchQuery(strings.Repeat("x", 501)); err == nil {
		t.Fatal("501-byte query must fail before a billable request")
	}
}

func TestLocationListingSearchCandidatesFromSavedEvidence(t *testing.T) {
	raw := `{"places":[
		{"placeId":"ChIJ-keep","title":"Kept Cafe","address":"Street 1","latitude":27.71,"longitude":85.33},
		{"placeId":"ChIJ-keep","title":"Kept Cafe Repeat","address":"Street 1","latitude":27.71,"longitude":85.33},
		{"placeId":"ChIJ-nocoords","title":"No Coords","address":"Street 2"},
		{"placeId":"ChIJ-half","title":"Half Coords","address":"Street 3","latitude":27.7},
		{"placeId":"","title":"No Identity","address":"Street 4","latitude":27.7,"longitude":85.3},
		{"placeId":"ChIJ-zero","title":"Zero Cafe","address":"Null Island","latitude":0,"longitude":0}
	]}`
	candidates, err := locationListingSearchCandidates([]byte(raw))
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %#v, want the kept and the explicit-zero rows only", candidates)
	}
	if candidates[0].PlaceID != "ChIJ-keep" || candidates[0].Title != "Kept Cafe" || candidates[0].Latitude != 27.71 || candidates[0].Longitude != 85.33 {
		t.Fatalf("kept candidate = %#v", candidates[0])
	}
	if candidates[1].PlaceID != "ChIJ-zero" || candidates[1].Latitude != 0 || candidates[1].Longitude != 0 {
		t.Fatalf("explicit 0,0 is real finite evidence and stays bindable: %#v", candidates[1])
	}

	empty, err := locationListingSearchCandidates([]byte(`{"places":[]}`))
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty evidence = %#v, %v; want an empty array, never null", empty, err)
	}
	if _, err := locationListingSearchCandidates([]byte(`{"places":`)); err == nil {
		t.Fatal("malformed saved evidence must fail instead of binding")
	}
}

func TestLocationListingSearchExpiry(t *testing.T) {
	now := time.Now().UTC()
	if locationListingSearchExpired(now.Add(time.Hour), now) {
		t.Fatal("fresh evidence must not read as expired")
	}
	if !locationListingSearchExpired(now.Add(-time.Second), now) {
		t.Fatal("past evidence must read as expired")
	}
	if !locationListingSearchExpired(now, now) {
		t.Fatal("evidence expiring exactly now must read as expired")
	}
}

func TestLocationListingSearchClientSelection(t *testing.T) {
	injected := googleplaces.NewPlacesListingClient("stub-key", "http://127.0.0.1:1/unused-places-stub")
	wired := &App{PlacesListings: injected}
	if got := wired.locationListingSearchClient(); got != injected {
		t.Fatal("wired app must use its own injected Places client")
	}
	other := &App{PlacesListings: googleplaces.NewPlacesListingClient("other-key", "http://127.0.0.1:1/other-stub")}
	if other.locationListingSearchClient() == injected {
		t.Fatal("app instances must not share Places clients")
	}
	if bare := (&App{}).locationListingSearchClient(); bare == nil {
		t.Fatal("handmade fixture without an injected client must still get a default client")
	}
}
