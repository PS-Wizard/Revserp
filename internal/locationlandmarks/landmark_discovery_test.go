package locationlandmarks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const testAPIKey = "test-google-key"

type placesReply struct {
	status   int
	body     string
	location string
}

func okPlacesReply(body string) placesReply { return placesReply{status: http.StatusOK, body: body} }

type fakePlaces struct {
	mu sync.Mutex

	server *httptest.Server
	reply  placesReply

	requests   int
	methods    []string
	paths      []string
	apiKeys    []string
	fieldMasks []string
	bodies     []placesSearchNearbyRequest
	rawBodies  []string
}

func newFakePlaces(t *testing.T, reply placesReply) *fakePlaces {
	t.Helper()
	fake := &fakePlaces{reply: reply}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.requests++
		fake.methods = append(fake.methods, r.Method)
		fake.paths = append(fake.paths, r.URL.Path)
		fake.apiKeys = append(fake.apiKeys, r.Header.Get("X-Goog-Api-Key"))
		fake.fieldMasks = append(fake.fieldMasks, r.Header.Get("X-Goog-FieldMask"))
		raw, _ := io.ReadAll(r.Body)
		fake.rawBodies = append(fake.rawBodies, string(raw))
		var decoded placesSearchNearbyRequest
		if err := json.Unmarshal([]byte(string(raw)), &decoded); err == nil {
			fake.bodies = append(fake.bodies, decoded)
		}
		if fake.reply.location != "" {
			w.Header().Set("Location", fake.reply.location)
		}
		status := fake.reply.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(fake.reply.body))
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakePlaces) client() *Client {
	return NewClient(Config{APIKey: testAPIKey, BaseURL: f.server.URL, Timeout: 5 * time.Second})
}

func (f *fakePlaces) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func (f *fakePlaces) lastBody(t *testing.T) placesSearchNearbyRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		t.Fatal("no decoded request body")
	}
	return f.bodies[len(f.bodies)-1]
}

func placesJSON(id, name string, lat, lon float64, primaryType string) string {
	return fmt.Sprintf(`{"id":%q,"displayName":{"text":%q},"location":{"latitude":%v,"longitude":%v},"primaryType":%q}`, id, name, lat, lon, primaryType)
}

func placesResponse(entries ...string) string {
	return `{"places":[` + strings.Join(entries, ",") + `]}`
}

var wantAllowlist = []string{
	"tourist_attraction", "museum", "art_gallery", "park", "national_park",
	"zoo", "aquarium", "botanical_garden", "plaza", "hindu_temple",
	"buddhist_temple", "church", "mosque", "synagogue", "historical_landmark",
	"shopping_mall", "university", "stadium", "train_station",
	"performing_arts_theater", "campground",
}

func TestPlacesPinnedRequestShape(t *testing.T) {
	fake := newFakePlaces(t, okPlacesReply(placesResponse(
		placesJSON("ChIJ1", "Garden of Dreams", 27.706, 85.31, "tourist_attraction"),
		placesJSON("ChIJ2", "National Museum", 27.716, 85.32, "museum"),
	)))
	landmarks, err := fake.client().Discover(context.Background(), 27.7, 85.3)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if fake.count() != 1 {
		t.Fatalf("requests = %d, want 1", fake.count())
	}
	fake.mu.Lock()
	method, fieldMask, apiKey := fake.methods[0], fake.fieldMasks[0], fake.apiKeys[0]
	fake.mu.Unlock()
	if method != http.MethodPost {
		t.Fatalf("method = %q, want POST", method)
	}
	if fieldMask != "places.id,places.displayName,places.location,places.primaryType" {
		t.Fatalf("field mask = %q", fieldMask)
	}
	if apiKey != testAPIKey {
		t.Fatalf("api key header missing or wrong")
	}
	body := fake.lastBody(t)
	if body.MaxResultCount != 20 {
		t.Fatalf("maxResultCount = %d, want 20", body.MaxResultCount)
	}
	if body.RankPreference != "POPULARITY" {
		t.Fatalf("rankPreference = %q", body.RankPreference)
	}
	if body.LanguageCode != "en" {
		t.Fatalf("languageCode = %q", body.LanguageCode)
	}
	if body.LocationRestriction.Circle.Radius != 5000 {
		t.Fatalf("radius = %v, want 5000", body.LocationRestriction.Circle.Radius)
	}
	if body.LocationRestriction.Circle.Center.Latitude != 27.7 || body.LocationRestriction.Circle.Center.Longitude != 85.3 {
		t.Fatalf("center = %+v", body.LocationRestriction.Circle.Center)
	}
	if len(body.IncludedPrimaryTypes) != len(wantAllowlist) {
		t.Fatalf("includedPrimaryTypes = %d, want %d", len(body.IncludedPrimaryTypes), len(wantAllowlist))
	}
	for i, want := range wantAllowlist {
		if body.IncludedPrimaryTypes[i] != want {
			t.Fatalf("includedPrimaryTypes[%d] = %q, want %q", i, body.IncludedPrimaryTypes[i], want)
		}
	}
	if len(landmarks) != 2 {
		t.Fatalf("landmarks = %d, want 2", len(landmarks))
	}
	first := landmarks[0]
	if first.Provider != "google_places" || first.ProviderRef != "places/ChIJ1" {
		t.Fatalf("first = %+v, want provider google_places ref places/ChIJ1", first)
	}
	if len(first.Categories) != 1 || first.Categories[0] != "tourist_attraction" {
		t.Fatalf("categories = %v", first.Categories)
	}
	if first.StraightLineM <= 0 || first.FetchedAt.IsZero() {
		t.Fatalf("straight line or fetched_at missing: %+v", first)
	}
}

func TestPlacesZeroRequestsOnInvalidInput(t *testing.T) {
	fake := newFakePlaces(t, okPlacesReply(placesResponse()))
	noKey := NewClient(Config{BaseURL: fake.server.URL, Timeout: 5 * time.Second})
	if _, err := noKey.Discover(context.Background(), 27.7, 85.3); err == nil {
		t.Fatal("missing key must error")
	}
	for _, coords := range [][2]float64{{95, 85}, {27, 181}, {math.NaN(), 85}, {27, math.Inf(1)}} {
		if _, err := fake.client().Discover(context.Background(), coords[0], coords[1]); err == nil {
			t.Fatalf("coords %v must error", coords)
		}
	}
	if got := fake.count(); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func TestPlacesBusyFailsAfterOneRequestWithoutDelay(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply placesReply
	}{
		{"too-many-requests", placesReply{status: 429, body: "slow down"}},
		{"gateway-timeout", placesReply{status: 504, body: "<html>timeout</html>"}},
		{"resource-exhausted", placesReply{status: 429, body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota"}}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakePlaces(t, tc.reply)
			start := time.Now()
			landmarks, err := fake.client().Discover(context.Background(), 27.7, 85.3)
			if elapsed := time.Since(start); elapsed >= time.Second {
				t.Fatalf("busy took %v, want no retry delay", elapsed)
			}
			if !errors.Is(err, ErrProviderBusy) {
				t.Fatalf("error = %v, want ErrProviderBusy", err)
			}
			if landmarks != nil {
				t.Fatalf("landmarks = %+v, want nil", landmarks)
			}
			if got := fake.count(); got != 1 {
				t.Fatalf("requests = %d, want 1", got)
			}
		})
	}
}

func TestPlacesRedirectIsSingleRequestError(t *testing.T) {
	fake := newFakePlaces(t, placesReply{status: http.StatusTemporaryRedirect, body: "redirect", location: "https://example.test/other"})
	landmarks, err := fake.client().Discover(context.Background(), 27.7, 85.3)
	if err == nil {
		t.Fatal("redirect must error")
	}
	if errors.Is(err, ErrProviderBusy) {
		t.Fatalf("redirect must not be busy: %v", err)
	}
	if landmarks != nil {
		t.Fatalf("landmarks = %+v, want nil", landmarks)
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("requests = %d, want 1 (no redirect follow)", got)
	}
}

func TestPlacesEmptyStaysEmpty(t *testing.T) {
	for _, body := range []string{`{"places":[]}`, `{}`} {
		fake := newFakePlaces(t, okPlacesReply(body))
		landmarks, err := fake.client().Discover(context.Background(), 27.7, 85.3)
		if err != nil {
			t.Fatalf("empty body %q error: %v", body, err)
		}
		if landmarks == nil || len(landmarks) != 0 {
			t.Fatalf("landmarks = %+v, want non-nil empty", landmarks)
		}
		if got := fake.count(); got != 1 {
			t.Fatalf("requests = %d, want 1", got)
		}
	}
}

func TestPlacesMalformedAndOverLimitError(t *testing.T) {
	fake := newFakePlaces(t, okPlacesReply(`{"places":`))
	if _, err := fake.client().Discover(context.Background(), 27.7, 85.3); err == nil {
		t.Fatal("malformed JSON must error")
	} else if errors.Is(err, ErrProviderBusy) {
		t.Fatalf("malformed must not be busy: %v", err)
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}

	entries := make([]string, 0, 21)
	for i := 0; i < 21; i++ {
		entries = append(entries, placesJSON(fmt.Sprintf("id-%d", i), fmt.Sprintf("Place %d", i), 27.71, 85.31, "museum"))
	}
	over := newFakePlaces(t, okPlacesReply(placesResponse(entries...)))
	landmarks, err := over.client().Discover(context.Background(), 27.7, 85.3)
	if err == nil {
		t.Fatal("21 entries must error, no truncation")
	}
	if landmarks != nil {
		t.Fatalf("landmarks = %+v, want nil on over-limit", landmarks)
	}
	if got := over.count(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestPlacesKeepsDistantEntriesSkipsInvalidDedupes(t *testing.T) {
	farLat, farLon := 27.745, 85.345 // >2km from 27.7,85.3, inside the 5km circle
	body := placesResponse(strings.Join([]string{
		placesJSON("ChIJfar", "Far Park", farLat, farLon, "park"),
		placesJSON("ChIJfar", "Far Park Dup", farLat, farLon, "park"),
		`{"id":"ChIJnoname","displayName":{"text":""},"location":{"latitude":27.71,"longitude":85.31},"primaryType":"museum"}`,
		`{"id":"","displayName":{"text":"No ID"},"location":{"latitude":27.71,"longitude":85.31},"primaryType":"museum"}`,
		`{"id":"ChIJbank","displayName":{"text":"Bank"},"location":{"latitude":27.71,"longitude":85.31},"primaryType":"bank"}`,
		`{"id":"ChIJnoloc","displayName":{"text":"No Loc"},"primaryType":"museum"}`,
		placesJSON("ChIJnear", "Near Zoo", 27.701, 85.301, "zoo"),
	}, ","))
	fake := newFakePlaces(t, okPlacesReply(body))
	landmarks, err := fake.client().Discover(context.Background(), 27.7, 85.3)
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if fake.count() != 1 {
		t.Fatalf("requests = %d, want exactly 1 (no downstream calls)", fake.count())
	}
	if len(landmarks) != 2 {
		t.Fatalf("landmarks = %+v, want Far Park + Near Zoo only", landmarks)
	}
	if landmarks[0].ProviderRef != "places/ChIJfar" {
		t.Fatalf("first ref = %q", landmarks[0].ProviderRef)
	}
	wantStraight := int(math.Round(straightLineMeters(27.7, 85.3, farLat, farLon)))
	if wantStraight <= 2000 {
		t.Fatalf("fixture distance = %d, want >2000 to prove no cutoff", wantStraight)
	}
	if landmarks[0].StraightLineM != wantStraight {
		t.Fatalf("straight = %d, want %d", landmarks[0].StraightLineM, wantStraight)
	}
}

func TestPlacesNonBusyStatusIsSingleRequestError(t *testing.T) {
	fake := newFakePlaces(t, placesReply{status: http.StatusInternalServerError, body: "boom"})
	if _, err := fake.client().Discover(context.Background(), 27.7, 85.3); err == nil || errors.Is(err, ErrProviderBusy) {
		t.Fatalf("error = %v, want non-busy", err)
	}
	if got := fake.count(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

func TestStraightLineMetersMatchesKnownDistance(t *testing.T) {
	got := straightLineMeters(0, 0, 0, 1)
	if math.Abs(got-111194.93) > 1 {
		t.Fatalf("straight line = %.2f, want about 111194.93", got)
	}
	if got := straightLineMeters(27.7, 85.3, 27.7, 85.3); got != 0 {
		t.Fatalf("straight line to same point = %v, want 0", got)
	}
}

func TestPlacesProviderErrorRedactsAPIKey(t *testing.T) {
	fake := newFakePlaces(t, placesReply{status: http.StatusTooManyRequests, body: `{"error":{"message":"` + testAPIKey + `","status":"RESOURCE_EXHAUSTED"}}`})
	_, err := fake.client().Discover(context.Background(), 39.68912, -104.94016)
	if !errors.Is(err, ErrProviderBusy) {
		t.Fatalf("error = %v, want provider busy", err)
	}
	if strings.Contains(err.Error(), testAPIKey) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("provider error does not redact credentials: %v", err)
	}
	if fake.count() != 1 {
		t.Fatalf("requests = %d, want 1", fake.count())
	}
}
