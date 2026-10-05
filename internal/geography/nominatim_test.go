package geography

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testUserAgent = "revserp-test/1.0"

// newNominatimTestClient wires a client to an offline httptest server with the
// rate limit disabled so tests stay fast; rate behaviour is exercised
// separately.
func newNominatimTestClient(t *testing.T, handler http.HandlerFunc) *NominatimClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewNominatimClient(server.URL, testUserAgent)
	client.minInterval = 0
	return client
}

func TestNewNominatimClientDefaultsIdentifyingUserAgent(t *testing.T) {
	client := NewNominatimClient("https://nominatim.example.test/", "")
	if client.userAgent != defaultNominatimUserAgent {
		t.Fatalf("userAgent = %q, want default %q", client.userAgent, defaultNominatimUserAgent)
	}
	if client.baseURL != "https://nominatim.example.test" {
		t.Fatalf("baseURL = %q, want trailing slash trimmed", client.baseURL)
	}

	custom := NewNominatimClient("https://nominatim.example.test", testUserAgent)
	if custom.userAgent != testUserAgent {
		t.Fatalf("userAgent = %q, want %q", custom.userAgent, testUserAgent)
	}
}

func TestSearchAddressesSendsContractRequest(t *testing.T) {
	var gotQuery url.Values
	var gotUserAgent string
	var gotAcceptLanguage string
	client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/search" {
			t.Errorf("path = %s, want /search", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		gotUserAgent = r.Header.Get("User-Agent")
		gotAcceptLanguage = r.Header.Get("Accept-Language")
		_, _ = w.Write([]byte(`[{"display_name":"Berlin, Germany","lat":"52.5200","lon":"13.4050","address":{"city":"Berlin","suburb":"Mitte","country_code":"de"}}]`))
	})

	results, err := client.SearchAddresses(context.Background(), "Unter den Linden 1, Berlin")
	if err != nil {
		t.Fatalf("SearchAddresses returned error: %v", err)
	}
	if gotUserAgent != testUserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, testUserAgent)
	}
	if gotQuery.Get("q") != "Unter den Linden 1, Berlin" {
		t.Fatalf("q = %q", gotQuery.Get("q"))
	}
	if gotQuery.Get("format") != "jsonv2" {
		t.Fatalf("format = %q, want jsonv2", gotQuery.Get("format"))
	}
	if gotQuery.Get("addressdetails") != "1" {
		t.Fatalf("addressdetails = %q, want 1", gotQuery.Get("addressdetails"))
	}
	if gotQuery.Get("limit") != "5" {
		t.Fatalf("limit = %q, want 5", gotQuery.Get("limit"))
	}
	// Without this the provider answers in the local script, and that string is
	// substituted straight into a search query nobody would ever type.
	if gotAcceptLanguage != "en" {
		t.Fatalf("Accept-Language = %q, want en", gotAcceptLanguage)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one", results)
	}
	got := results[0]
	if got.DisplayName != "Berlin, Germany" {
		t.Fatalf("display_name = %q", got.DisplayName)
	}
	if got.Latitude != 52.52 {
		t.Fatalf("latitude = %v, want 52.52", got.Latitude)
	}
	if got.Longitude != 13.405 {
		t.Fatalf("longitude = %v, want 13.405", got.Longitude)
	}
	if got.Locality != "Mitte" {
		t.Fatalf("locality = %q, want the smallest level Mitte", got.Locality)
		}
	if got.CountryCode != "de" {
		t.Fatalf("country_code = %q, want de", got.CountryCode)
	}
}

func TestSearchAddressesLocalityPrecedence(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		address string
		want    string
	}{
		// The smallest name a person would use wins, never the widest boundary
		// the provider happens to carry. A metropolitan city is not a place.
		{"suburb over city", `{"city":"City","suburb":"Suburb"}`, "Suburb"},
		{"village over town", `{"town":"Town","village":"Village"}`, "Village"},
		{"village", `{"village":"Village"}`, "Village"},
		{"neighbourhood over suburb", `{"suburb":"Suburb","neighbourhood":"Hood"}`, "Hood"},
		{"ward over municipality and city", `{"city":"City","municipality":"Muni","city_district":"Kathmandu-05"}`, "Kathmandu-05"},
		{"neighbourhood before municipality", `{"neighbourhood":"Hood","municipality":"Muni"}`, "Hood"},
		{"city only as fallback", `{"city":"Kathmandu Metropolitan City"}`, "Kathmandu Metropolitan City"},
		{"municipality", `{"municipality":"Muni"}`, "Muni"},
		{"empty", `{}`, ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `[{"display_name":"X","lat":"1.0","lon":"2.0","address":%s}]`, testCase.address)
			})
			results, err := client.SearchAddresses(context.Background(), "x")
			if err != nil {
				t.Fatalf("SearchAddresses returned error: %v", err)
			}
			if len(results) != 1 {
				t.Fatalf("results = %#v, want one", results)
			}
			if results[0].Locality != testCase.want {
				t.Fatalf("locality = %q, want %q", results[0].Locality, testCase.want)
			}
		})
	}
}

func TestSearchAddressesEmptyResultsAreNonNil(t *testing.T) {
	client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	results, err := client.SearchAddresses(context.Background(), "nowhere")
	if err != nil {
		t.Fatalf("SearchAddresses returned error: %v", err)
	}
	if results == nil {
		t.Fatal("results = nil, want non-nil empty slice")
	}
	if len(results) != 0 {
		t.Fatalf("results = %#v, want empty", results)
	}
}

func TestSearchAddressesBlankAddressSendsNoRequest(t *testing.T) {
	var called atomic.Bool
	client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
	})
	results, err := client.SearchAddresses(context.Background(), "   ")
	if err != nil {
		t.Fatalf("SearchAddresses returned error: %v", err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("results = %#v, want non-nil empty slice", results)
	}
	if called.Load() {
		t.Fatal("blank address triggered a provider request")
	}
}

func TestSearchAddressesRejectsMalformedCoordinates(t *testing.T) {
	for _, body := range []string{
		`[{"display_name":"x","lon":"2.0"}]`,
		`[{"display_name":"x","lat":"","lon":"2.0"}]`,
		`[{"display_name":"x","lat":"not-a-number","lon":"2.0"}]`,
		`[{"display_name":"x","lat":"95.0","lon":"2.0"}]`,
		`[{"display_name":"x","lat":"1.0","lon":"200.0"}]`,
		`[{"display_name":"x","lat":"NaN","lon":"2.0"}]`,
	} {
		t.Run(body, func(t *testing.T) {
			client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			})
			if _, err := client.SearchAddresses(context.Background(), "x"); err == nil {
				t.Fatalf("SearchAddresses(%s) returned nil error", body)
			}
		})
	}
}

func TestSearchAddressesProviderStatusIsNotNoMatch(t *testing.T) {
	client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	results, err := client.SearchAddresses(context.Background(), "x")
	if err == nil {
		t.Fatal("SearchAddresses returned nil error for status 500")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("error = %v, want status 500", err)
	}
	if results != nil {
		t.Fatalf("results = %#v, want nil on provider error", results)
	}
}

func TestSearchAddressesDecodeErrorIsNotNoMatch(t *testing.T) {
	client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"not":"an array"}`))
	})
	results, err := client.SearchAddresses(context.Background(), "x")
	if err == nil {
		t.Fatal("SearchAddresses returned nil error for malformed body")
	}
	if results != nil {
		t.Fatalf("results = %#v, want nil on decode error", results)
	}
}

func TestSearchAddressesRejectsOversizedBody(t *testing.T) {
	client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", maxNominatimResponseBytes+16)))
	})
	if _, err := client.SearchAddresses(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want an oversized-body error", err)
	}
}

func TestReverseAddressSendsContractRequest(t *testing.T) {
	var gotQuery url.Values
	var gotUserAgent string
	client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/reverse" {
			t.Errorf("path = %s, want /reverse", r.URL.Path)
		}
		gotQuery = r.URL.Query()
		gotUserAgent = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"display_name":"Berlin","lat":"52.5200","lon":"13.4050","address":{"city":"Berlin","country_code":"de"}}`))
	})

	result, err := client.ReverseAddress(context.Background(), 52.52, 13.405)
	if err != nil {
		t.Fatalf("ReverseAddress returned error: %v", err)
	}
	if gotUserAgent != testUserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUserAgent, testUserAgent)
	}
	if gotQuery.Get("lat") != "52.52" {
		t.Fatalf("lat = %q, want 52.52", gotQuery.Get("lat"))
	}
	if gotQuery.Get("lon") != "13.405" {
		t.Fatalf("lon = %q, want 13.405", gotQuery.Get("lon"))
	}
	if gotQuery.Get("format") != "jsonv2" {
		t.Fatalf("format = %q, want jsonv2", gotQuery.Get("format"))
	}
	if gotQuery.Get("addressdetails") != "1" {
		t.Fatalf("addressdetails = %q, want 1", gotQuery.Get("addressdetails"))
	}
	if result.Latitude != 52.52 || result.Longitude != 13.405 {
		t.Fatalf("coordinates = %v,%v want 52.52,13.405", result.Latitude, result.Longitude)
	}
	if result.Locality != "Berlin" || result.CountryCode != "de" {
		t.Fatalf("locality/country = %q/%q", result.Locality, result.CountryCode)
	}
}

func TestReverseAddressRejectsInvalidCoordinates(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		latitude  float64
		longitude float64
	}{
		{"latitude above range", 90.5, 0},
		{"latitude below range", -90.5, 0},
		{"latitude NaN", math.NaN(), 0},
		{"latitude infinite", math.Inf(1), 0},
		{"longitude above range", 0, 180.5},
		{"longitude below range", 0, -180.5},
		{"longitude NaN", 0, math.NaN()},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var called atomic.Bool
			client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				called.Store(true)
			})
			if _, err := client.ReverseAddress(context.Background(), testCase.latitude, testCase.longitude); err == nil {
				t.Fatalf("ReverseAddress(%v,%v) returned nil error", testCase.latitude, testCase.longitude)
			}
			if called.Load() {
				t.Fatal("invalid coordinates triggered a provider request")
			}
		})
	}
}

func TestReverseAddressRejectsMalformedResponse(t *testing.T) {
	for _, body := range []string{
		`not json`,
		`{"display_name":"x"}`,
		`{}`,
	} {
		t.Run(body, func(t *testing.T) {
			client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			})
			if _, err := client.ReverseAddress(context.Background(), 1, 2); err == nil {
				t.Fatalf("ReverseAddress(%s) returned nil error", body)
			}
		})
	}
}

func TestReverseAddressProviderStatusIsNotNoMatch(t *testing.T) {
	client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	})
	if _, err := client.ReverseAddress(context.Background(), 1, 2); err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("error = %v, want status 502", err)
	}
}

func TestNominatimClientRateLimitSerializesRequests(t *testing.T) {
	const interval = 40 * time.Millisecond
	var mu sync.Mutex
	var times []time.Time
	client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		_, _ = w.Write([]byte(`[]`))
	})
	client.minInterval = interval

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.SearchAddresses(context.Background(), "x"); err != nil {
				t.Errorf("SearchAddresses returned error: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(times) != 3 {
		t.Fatalf("provider requests = %d, want 3", len(times))
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap < interval/2 {
			t.Fatalf("requests %d and %d spaced %v, want about %v", i-1, i, gap, interval)
		}
	}
}

func TestNominatimClientRateLimitWaitHonoursContext(t *testing.T) {
	var calls atomic.Int32
	client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`[]`))
	})
	client.minInterval = time.Hour

	if _, err := client.SearchAddresses(context.Background(), "first"); err != nil {
		t.Fatalf("first SearchAddresses returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := client.SearchAddresses(ctx, "second"); !errors.Is(err, context.Canceled) {
		t.Fatalf("second SearchAddresses error = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider requests = %d, want 1; cancelled wait must not send", got)
	}
}

func TestNominatimClientPreCancelledContextSendsNoRequest(t *testing.T) {
	var called atomic.Bool
	client := newNominatimTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.SearchAddresses(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchAddresses error = %v, want context.Canceled", err)
	}
	if _, err := client.ReverseAddress(ctx, 1, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReverseAddress error = %v, want context.Canceled", err)
	}
	if called.Load() {
		t.Fatal("pre-cancelled context triggered a provider request")
	}
}

func TestNominatimSerializesHTTPRequests(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	var once sync.Once
	defer once.Do(func() { close(release) })
	client := NewNominatimClient(server.URL, testUserAgent)
	client.minInterval = 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 2)
	go func() { _, err := client.SearchAddresses(ctx, "first"); done <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first request did not start")
	}
	go func() { _, err := client.SearchAddresses(ctx, "second"); done <- err }()
	select {
	case <-entered:
		t.Error("second request overlapped the first HTTP request")
	case <-time.After(50 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
