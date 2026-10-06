package googleplaces

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every test here points the client at a local httptest server. Nothing in
// this file may reach Google Places, and no paid SKU may be requested.

const placesListingDefaultEndpoint = "https://places.googleapis.com/v1/places:searchText"

func TestPlacesListingFieldMaskIsPinned(t *testing.T) {
	const want = "places.id,places.displayName,places.location,places.formattedAddress"
	if PlacesListingFieldMask != want {
		t.Fatalf("field mask = %q, want the exact four-field mask %q", PlacesListingFieldMask, want)
	}
}

func TestNewPlacesListingClientDefaultsToGoogleTextSearch(t *testing.T) {
	client := NewPlacesListingClient("key", "")
	if client.endpoint != placesListingDefaultEndpoint {
		t.Fatalf("default endpoint = %q, want %q", client.endpoint, placesListingDefaultEndpoint)
	}
}

func TestPlacesListingLookupSendsOneBiasedTextSearch(t *testing.T) {
	calls := 0
	var method, apiKey, fieldMask, contentType string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		method = r.Method
		apiKey = r.Header.Get("X-Goog-Api-Key")
		fieldMask = r.Header.Get("X-Goog-FieldMask")
		contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"places":[{"id":"ChIJ-remote","displayName":{"text":"Noodles and Company"},"formattedAddress":"8221 N Kings Hwy, Myrtle Beach","location":{"latitude":33.755,"longitude":-78.813}}]}`))
	}))
	defer server.Close()

	// The search area is Kathmandu; the business Places returns is 12,000 km away.
	response, err := NewPlacesListingClient("places-key", server.URL).LookupMapsListing(context.Background(), "Noodles and Company", 27.71056, 85.27716)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want exactly one request and no retry", calls)
	}
	if method != http.MethodPost {
		t.Fatalf("method = %q, want POST", method)
	}
	if apiKey != "places-key" {
		t.Fatalf("X-Goog-Api-Key = %q", apiKey)
	}
	if fieldMask != PlacesListingFieldMask {
		t.Fatalf("X-Goog-FieldMask = %q, want %q", fieldMask, PlacesListingFieldMask)
	}
	if contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}

	if body["textQuery"] != "Noodles and Company" || body["languageCode"] != "en" {
		t.Fatalf("textQuery/languageCode = %v/%v", body["textQuery"], body["languageCode"])
	}
	if _, forbidden := body["locationRestriction"]; forbidden {
		t.Fatal("request must never send locationRestriction; the bias is intentional")
	}
	bias, ok := body["locationBias"].(map[string]any)
	if !ok {
		t.Fatalf("locationBias = %v, want an object", body["locationBias"])
	}
	circle, ok := bias["circle"].(map[string]any)
	if !ok {
		t.Fatalf("locationBias.circle = %v", bias["circle"])
	}
	if circle["radius"] != float64(50000) {
		t.Fatalf("locationBias.circle.radius = %v, want 50000", circle["radius"])
	}
	center, ok := circle["center"].(map[string]any)
	if !ok {
		t.Fatalf("locationBias.circle.center = %v", circle["center"])
	}
	if center["latitude"] != 27.71056 || center["longitude"] != 85.27716 {
		t.Fatalf("search-area centre = %v, want the Kathmandu search centre", center)
	}

	if response.Credits != 0 {
		t.Fatalf("Credits = %d, want 0 application credits", response.Credits)
	}
	if len(response.Places) != 1 {
		t.Fatalf("places = %d, want 1", len(response.Places))
	}
	place := response.Places[0]
	if place.PlaceID != "ChIJ-remote" || place.Title != "Noodles and Company" || place.Address != "8221 N Kings Hwy, Myrtle Beach" {
		t.Fatalf("mapped place = %#v", place)
	}
	if place.Latitude == nil || *place.Latitude != 33.755 || place.Longitude == nil || *place.Longitude != -78.813 {
		t.Fatalf("business coordinates = %v/%v, want the remote business coordinates", place.Latitude, place.Longitude)
	}
	// The returned business is nowhere near the search centre it was biased to.
	if place.HasMapCoordinates() && *place.Latitude == 27.71056 {
		t.Fatal("business coordinates must not be replaced by the search-area centre")
	}
}

func TestPlacesListingLookupEmptyResponseIsEmptyNotAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	response, err := NewPlacesListingClient("key", server.URL).LookupMapsListing(context.Background(), "no such cafe", 27.7, 85.3)
	if err != nil {
		t.Fatalf("valid empty 200 must not be an error: %v", err)
	}
	if response.Places == nil || len(response.Places) != 0 || response.Credits != 0 {
		t.Fatalf("empty response = %#v, want a non-nil empty list", response)
	}
}

func TestPlacesListingLookupMissingCoordinatesStayMissingZeroStaysZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"places":[
			{"id":"zero","displayName":{"text":"Zero Cafe"},"location":{"latitude":0,"longitude":0}},
			{"id":"missing","displayName":{"text":"Missing Cafe"}},
			{"id":"lat-only","displayName":{"text":"Half Cafe"},"location":{"latitude":1.5}}
		]}`))
	}))
	defer server.Close()

	response, err := NewPlacesListingClient("key", server.URL).LookupMapsListing(context.Background(), "cafe", 27.7, 85.3)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(response.Places) != 3 {
		t.Fatalf("places = %d, want 3", len(response.Places))
	}
	zero := response.Places[0]
	if zero.Latitude == nil || *zero.Latitude != 0 || zero.Longitude == nil || *zero.Longitude != 0 {
		t.Fatalf("explicit 0,0 must stay a real zero, got %v/%v", zero.Latitude, zero.Longitude)
	}
	missing := response.Places[1]
	if missing.Latitude != nil || missing.Longitude != nil {
		t.Fatalf("missing location must stay nil, got %v/%v", missing.Latitude, missing.Longitude)
	}
	partial := response.Places[2]
	if partial.Latitude == nil || *partial.Latitude != 1.5 || partial.Longitude != nil {
		t.Fatalf("a single coordinate must stay distinct, got %v/%v", partial.Latitude, partial.Longitude)
	}
}

func TestPlacesListingLookupErrorsNeverRetryAndHideCredentials(t *testing.T) {
	const secret = "places-test-secret-key"
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"provider 500", http.StatusInternalServerError, `{"error":{"message":"boom"}}`},
		{"rate limited 429", http.StatusTooManyRequests, `{"error":{"message":"quota"}}`},
		{"malformed 200 body", http.StatusOK, `{"places":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			response, err := NewPlacesListingClient(secret, server.URL).LookupMapsListing(context.Background(), "cafe", 27.7, 85.3)
			if err == nil {
				t.Fatal("want an error")
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want exactly one request and no retry", calls)
			}
			if response.Credits != 0 || len(response.Places) != 0 {
				t.Fatalf("failed response = %#v, want zero-credit empty evidence", response)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked the API key: %v", err)
			}
			if strings.Contains(err.Error(), "boom") || strings.Contains(err.Error(), "quota") {
				t.Fatalf("error echoed the provider body: %v", err)
			}
		})
	}
}

func TestPlacesListingLookupTransportErrorHidesKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close()

	_, err := NewPlacesListingClient("transport-secret", endpoint).LookupMapsListing(context.Background(), "cafe", 27.7, 85.3)
	if err == nil {
		t.Fatal("want a transport error")
	}
	if strings.Contains(err.Error(), "transport-secret") {
		t.Fatalf("transport error leaked the API key: %v", err)
	}
}

func TestPlacesListingLookupRejectsBadInputBeforeAnyRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client := NewPlacesListingClient("key", server.URL)

	for _, tc := range []struct {
		name             string
		query            string
		latitude, longitude float64
	}{
		{"blank query", "   ", 27.7, 85.3},
		{"nan latitude", "cafe", math.NaN(), 85.3},
		{"latitude out of range", "cafe", 120, 85.3},
		{"infinite longitude", "cafe", 27.7, math.Inf(1)},
		{"longitude out of range", "cafe", 27.7, 200},
	} {
		if _, err := client.LookupMapsListing(context.Background(), tc.query, tc.latitude, tc.longitude); err == nil {
			t.Errorf("%s: want an error", tc.name)
		}
	}
	if calls != 0 {
		t.Fatalf("%d requests were sent for invalid input; validation must run first", calls)
	}
}

func TestPlacesListingLookupNeverFollowsRedirects(t *testing.T) {
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected++ }))
	defer target.Close()
	originCalls := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCalls++
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	response, err := NewPlacesListingClient("redirect-secret", origin.URL).LookupMapsListing(context.Background(), "cafe", 27.7, 85.3)
	if err == nil {
		t.Fatal("a redirect must surface as a failed non-2xx response, never be followed")
	}
	if originCalls != 1 {
		t.Fatalf("origin calls = %d, want exactly one request", originCalls)
	}
	if redirected != 0 {
		t.Fatalf("redirect target received %d requests; the API key must not be replayed to another host", redirected)
	}
	if response.Credits != 0 || len(response.Places) != 0 {
		t.Fatalf("redirect response = %#v, want zero-credit empty evidence", response)
	}
}
