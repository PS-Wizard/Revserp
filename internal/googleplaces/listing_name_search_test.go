package googleplaces

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The name search exists for the bound-location flow: the caller has no draft
// coordinates, so the request must not carry a location bias at all.

func TestPlacesNameSearchSendsUnbiasedTextSearch(t *testing.T) {
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
		_, _ = w.Write([]byte(`{"places":[{"id":"ChIJ-cafe","displayName":{"text":"Zero Cafe"},"formattedAddress":"Street 1","location":{"latitude":27.71,"longitude":85.33}}]}`))
	}))
	defer server.Close()

	response, err := NewPlacesListingClient("places-key", server.URL).SearchListingsByName(context.Background(), "  Zero Cafe ")
	if err != nil {
		t.Fatalf("search: %v", err)
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
	if body["textQuery"] != "Zero Cafe" || body["languageCode"] != "en" {
		t.Fatalf("typed name was not sent trimmed with languageCode=en: %v", body)
	}
	if _, forbidden := body["locationBias"]; forbidden {
		t.Fatalf("name search must not send locationBias, a zero coordinate would fake a Gulf of Guinea bias: %v", body)
	}
	if _, forbidden := body["locationRestriction"]; forbidden {
		t.Fatalf("name search must never restrict results: %v", body)
	}

	if response.Credits != 0 {
		t.Fatalf("Credits = %d, want 0 application credits", response.Credits)
	}
	if len(response.Places) != 1 {
		t.Fatalf("places = %d, want 1", len(response.Places))
	}
	place := response.Places[0]
	if place.PlaceID != "ChIJ-cafe" || place.Title != "Zero Cafe" || place.Address != "Street 1" {
		t.Fatalf("mapped place = %#v", place)
	}
	if place.Latitude == nil || *place.Latitude != 27.71 || place.Longitude == nil || *place.Longitude != 85.33 {
		t.Fatalf("business coordinates = %v/%v", place.Latitude, place.Longitude)
	}
}

func TestPlacesNameSearchRejectsBlankQueryBeforeAnyRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	for _, query := range []string{"", "   ", "\t\n "} {
		if _, err := NewPlacesListingClient("key", server.URL).SearchListingsByName(context.Background(), query); err == nil {
			t.Fatalf("blank query %q must fail before a billable request", query)
		}
	}
	if calls != 0 {
		t.Fatalf("calls = %d, want no request for blank queries", calls)
	}
}

func TestPlacesNameSearchErrorsNeverRetryAndHideCredentials(t *testing.T) {
	const secret = "places-name-secret-key"
	calls := 0
	counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer counting.Close()

	response, err := NewPlacesListingClient(secret, counting.URL).SearchListingsByName(context.Background(), "cafe")
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
}
