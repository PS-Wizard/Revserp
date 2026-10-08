package serper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	mapsGridLiveEndpoint  = "https://google.serper.dev/maps"
	mapsGridLiveQuery     = "coffee"
	mapsGridLiveLatitude  = 37.7749
	mapsGridLiveLongitude = -122.4194
)

func newMapsServer(t *testing.T, responseBody string) (*Client, *string) {
	t.Helper()
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		requestBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(server.Close)
	return NewClient("test-key", server.URL, "", ""), &requestBody
}

func TestMapsAtRequestViewport(t *testing.T) {
	client, requestBody := newMapsServer(t, `{"ll":"@37.774900,-122.419400,14z","credits":3,"places":[{"position":1,"title":"Example Cafe"}]}`)

	response, err := client.MapsAt(context.Background(), "coffee", 37.7749, -122.4194)
	if err != nil {
		t.Fatalf("MapsAt returned error: %v", err)
	}

	// Latitude first, literal z suffix, English hl, and no gl field.
	const wantRequest = `{"hl":"en","ll":"@37.774900,-122.419400,14z","q":"coffee"}`
	if *requestBody != wantRequest {
		t.Fatalf("request body = %s, want %s", *requestBody, wantRequest)
	}
	if response.LL != "@37.774900,-122.419400,14z" {
		t.Fatalf("echoed ll = %q", response.LL)
	}
	if response.Credits != 3 {
		t.Fatalf("credits = %d, want 3", response.Credits)
	}
	if len(response.Places) != 1 || response.Places[0].Title != "Example Cafe" {
		t.Fatalf("places = %#v", response.Places)
	}
}

func TestFormatMapsViewport(t *testing.T) {
	viewport, err := FormatMapsViewport(37.7749, -122.4194)
	if err != nil {
		t.Fatalf("FormatMapsViewport returned error: %v", err)
	}
	if want := "@37.774900,-122.419400,14z"; viewport != want {
		t.Fatalf("viewport = %q, want %q", viewport, want)
	}

	for _, testCase := range []struct {
		name      string
		latitude  float64
		longitude float64
	}{
		{"latitude out of range", 91, 0},
		{"longitude out of range", 0, -181},
		{"latitude NaN", math.NaN(), 0},
		{"longitude infinite", 0, math.Inf(-1)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := FormatMapsViewport(testCase.latitude, testCase.longitude); err == nil {
				t.Fatalf("FormatMapsViewport(%v,%v) returned nil error", testCase.latitude, testCase.longitude)
			}
		})
	}
}

func TestMapsAtRejectsBadViewportEcho(t *testing.T) {
	for _, testCase := range []struct {
		name string
		ll   string
	}{
		// Valid recentered coordinates are accepted with the drift recorded;
		// only malformed echoes reject. A swapped latitude is still out of range.
		{"swapped coordinates", "@-122.419400,37.774900,14z"},
		{"missing zoom suffix", "@37.774900,-122.419400,140"},
		{"zoom above range", "@37.774900,-122.419400,23z"},
		{"negative zoom", "@37.774900,-122.419400,-1z"},
		{"zoom NaN", "@37.774900,-122.419400,NaNz"},
		{"zoom infinite", "@37.774900,-122.419400,+Infz"},
		{"missing at prefix", "37.774900,-122.419400,14z"},
		{"empty echo", ""},
		{"echoed latitude NaN", "@NaN,-122.419400,14z"},
		{"echoed longitude NaN", "@37.774900,NaN,14z"},
		{"echoed latitude infinite", "@+Inf,-122.419400,14z"},
		{"echoed latitude out of range", "@95.000000,-122.419400,14z"},
		{"echoed longitude out of range", "@37.774900,200.000000,14z"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"ll":%q,"credits":3,"places":[{"position":1,"title":"Example Cafe"}]}`, testCase.ll)
			client, _ := newMapsServer(t, body)

			response, err := client.MapsAt(context.Background(), "coffee", 37.7749, -122.4194)
			if err == nil {
				t.Fatalf("MapsAt with echoed ll %q returned nil error", testCase.ll)
			}
			// The call was already charged, so the decoded credits survive the
			// echo error even though the ranks are unusable.
			if response.Credits != 3 {
				t.Fatalf("credits = %d, want 3 retained with error", response.Credits)
			}
			if len(response.Places) != 1 {
				t.Fatalf("places = %#v, want retained decoded places", response.Places)
			}
		})
	}
}

// TestMapsAtAcceptsEchoedRoundedPrecision allows the provider echoing extra
// decimals of the same six-decimal request.
func TestMapsAtAcceptsEchoedRoundedPrecision(t *testing.T) {
	client, _ := newMapsServer(t, `{"ll":"@37.774900123,-122.419400456,14z","credits":3,"places":[]}`)
	if _, err := client.MapsAt(context.Background(), "coffee", 37.7749, -122.4194); err != nil {
		t.Fatalf("echo with trailing precision rejected: %v", err)
	}
}

// TestMapsAtAcceptsKilometreViewportDrift is the user-failure regression:
// provider recenters of 2.5-3.3km are accepted with the measured delta,
// returned LL, and credits preserved. Ranks stay approximate by construction.
func TestMapsAtAcceptsKilometreViewportDrift(t *testing.T) {
	client, _ := newMapsServer(t, `{"ll":"@37.744900,-122.419400,14z","credits":3,"places":[{"position":1,"title":"Example Cafe"}]}`)
	response, err := client.MapsAt(context.Background(), "coffee", 37.7749, -122.4194)
	if err != nil {
		t.Fatalf("kilometre drift rejected: %v", err)
	}
	if response.Credits != 3 || len(response.Places) != 1 {
		t.Fatalf("charged evidence lost: %#v", response)
	}
	if response.LL != "@37.744900,-122.419400,14z" {
		t.Fatalf("returned ll not preserved: %q", response.LL)
	}
	if response.ViewportDriftM == nil || *response.ViewportDriftM < 3000 {
		t.Fatalf("display drift not recorded: %v", response.ViewportDriftM)
	}
}

// TestMapsAtRejectsMissingPlacesArray proves a missing/null places array is
// malformed, not an empty result: only a real [] counts as empty downstream.
func TestMapsAtRejectsMissingPlacesArray(t *testing.T) {
	for _, body := range []string{
		`{"ll":"@37.774900,-122.419400,14z","credits":3}`,
		`{"ll":"@37.774900,-122.419400,14z","credits":3,"places":null}`,
	} {
		client, _ := newMapsServer(t, body)
		response, err := client.MapsAt(context.Background(), "coffee", 37.7749, -122.4194)
		if err == nil || !strings.Contains(err.Error(), "missing places array") {
			t.Fatalf("body %s: want missing-array rejection, got %v", body, err)
		}
		if response.Credits != 3 {
			t.Fatalf("body %s: credits = %d, want 3 retained with error", body, response.Credits)
		}
	}
}

func TestMapsAtAcceptsMeasuredNepalNormalization(t *testing.T) {
	client, _ := newMapsServer(t, `{"ll":"@27.7154309,85.3403057,14z","credits":3,"places":[]}`)
	response, err := client.MapsAt(context.Background(), "life insurance", 27.715444, 85.340306)
	if err != nil {
		t.Fatalf("live-reference normalization rejected: %v", err)
	}
	if response.ViewportDriftM == nil || *response.ViewportDriftM < 1.4 || *response.ViewportDriftM > 1.6 {
		t.Fatalf("measured drift = %v, want about 1.46 metres", response.ViewportDriftM)
	}
}

// TestMapsAtPreservesDecodedCreditsOnDecodeError covers a body that decodes
// credits before failing on a later field type, so the charged count survives.
func TestMapsAtPreservesDecodedCreditsOnDecodeError(t *testing.T) {
	client, _ := newMapsServer(t, `{"credits":3,"ll":123}`)
	response, err := client.MapsAt(context.Background(), "coffee", 37.7749, -122.4194)
	if err == nil {
		t.Fatal("MapsAt returned nil error for malformed response body")
	}
	if response.Credits != 3 {
		t.Fatalf("credits = %d, want 3 preserved from the decoded prefix", response.Credits)
	}
}

func TestMapsAtRejectsInvalidCoordinates(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		latitude  float64
		longitude float64
	}{
		{"latitude above range", 90.5, 0},
		{"latitude below range", -90.5, 0},
		{"longitude above range", 0, 180.5},
		{"longitude below range", 0, -180.5},
		{"latitude NaN", math.NaN(), 0},
		{"longitude NaN", 0, math.NaN()},
		{"latitude infinite", math.Inf(1), 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var called atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called.Store(true)
				_, _ = w.Write([]byte(`{"ll":"@0.000000,0.000000,14z","credits":3}`))
			}))
			defer server.Close()
			client := NewClient("test-key", server.URL, "", "")

			if _, err := client.MapsAt(context.Background(), "coffee", testCase.latitude, testCase.longitude); err == nil {
				t.Fatalf("MapsAt(%v,%v) returned nil error, want coordinate validation error", testCase.latitude, testCase.longitude)
			}
			if called.Load() {
				t.Fatalf("MapsAt(%v,%v) sent a request despite invalid coordinates", testCase.latitude, testCase.longitude)
			}
		})
	}
}

func TestMapsRequestUnchanged(t *testing.T) {
	client, requestBody := newMapsServer(t, `{"ll":"@1.000000,2.000000,10z","credits":3}`)

	if _, err := client.Maps(context.Background(), "coffee"); err != nil {
		t.Fatalf("Maps returned error: %v", err)
	}
	if want := `{"q":"coffee"}`; *requestBody != want {
		t.Fatalf("Maps request body = %s, want %s", *requestBody, want)
	}
}

// TestMapsGridLiveFixture exercises the recorded real Serper response once the
// live test has written it, proving the provider's own ll/credits shape parses
// under the offline suite.
func TestMapsGridLiveFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "maps-grid-live.json"))
	if os.IsNotExist(err) {
		t.Skip("testdata/maps-grid-live.json not recorded; run TestMapsAtLiveGridViewportEcho with LOCAL_SEO_SERPER_LIVE=1 to record it")
	}
	if err != nil {
		t.Fatalf("read live fixture: %v", err)
	}

	var response MapsResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatalf("decode live fixture: %v", err)
	}
	latitude, longitude, zoom, err := parseMapsLLViewport(response.LL)
	if err != nil {
		t.Fatalf("parse live fixture viewport %q: %v", response.LL, err)
	}
	if zoom != serperMapsZoom {
		t.Fatalf("live fixture zoom = %g, want %d", zoom, serperMapsZoom)
	}
	if latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		t.Fatalf("live fixture coordinates out of range: %v,%v", latitude, longitude)
	}
	if response.Credits <= 0 {
		t.Fatalf("live fixture credits = %d, want a positive charged count", response.Credits)
	}
}

// TestMapsAtLiveGridViewportEcho is the single paid Serper call, opt-in only.
// It verifies the provider echoes the fixed zoom-14 viewport and charges 3
// credits, then records the response for the offline fixture test above. The
// API key is read from SERPER_KEY and never written anywhere.
func TestMapsAtLiveGridViewportEcho(t *testing.T) {
	if os.Getenv("LOCAL_SEO_SERPER_LIVE") != "1" {
		t.Skip("set LOCAL_SEO_SERPER_LIVE=1 and SERPER_KEY to run the live Serper maps test")
	}
	apiKey := os.Getenv("SERPER_KEY")
	if apiKey == "" {
		t.Fatal("SERPER_KEY must be set for the live Serper maps test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := NewClient(apiKey, mapsGridLiveEndpoint, "", "")

	response, mapsErr := client.MapsAt(ctx, mapsGridLiveQuery, mapsGridLiveLatitude, mapsGridLiveLongitude)
	if mapsErr != nil {
		t.Fatalf("live MapsAt: %v", mapsErr)
	}

	fixture, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		t.Fatalf("encode live fixture: %v", err)
	}
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatalf("create testdata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "maps-grid-live.json"), append(fixture, '\n'), 0o644); err != nil {
		t.Fatalf("write live fixture: %v", err)
	}

	if response.Credits != 3 {
		t.Fatalf("live credits = %d, want 3 for one maps call", response.Credits)
	}
	t.Logf("live maps ok: ll=%q places=%d credits=%d", response.LL, len(response.Places), response.Credits)
}

func TestMapsAtAcceptsProviderNormalizedZoom(t *testing.T) {
	for _, zoom := range []string{"18.0536", "17.0536", "15", "14.0"} {
		t.Run(zoom, func(t *testing.T) {
			viewport := "@27.7358925,85.3221043," + zoom + "z"
			body := fmt.Sprintf(`{"ll":%q,"credits":3,"places":[{"position":1,"title":"Example Cafe"}]}`, viewport)
			client, _ := newMapsServer(t, body)
			response, err := client.MapsAt(context.Background(), "coffee", 27.7358925, 85.3221043)
			if err != nil {
				t.Fatalf("normalized viewport rejected: %v", err)
			}
			if response.LL != viewport || response.Credits != 3 || len(response.Places) != 1 {
				t.Fatalf("saved evidence changed: %#v", response)
			}
		})
	}
}
