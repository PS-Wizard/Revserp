package googlesuggest

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, maxResponseBytes int64, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewClient(server.URL, maxResponseBytes, time.Second)
}

func TestSuggestParsesChromeShape(t *testing.T) {
	body := `["ai visibility",["ai visibility checker","ai visibility tools"],["",""],[],{"google:clientdata":{"bpc":false},"google:suggestrelevance":[1250,601]}]`
	var gotQuery, gotClient string
	client := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotClient = r.URL.Query().Get("client")
		_, _ = io.WriteString(w, body)
	})

	got, err := client.Suggest(context.Background(), "ai visibility", Options{})
	if err != nil {
		t.Fatalf("Suggest error: %v", err)
	}
	want := []Suggestion{
		{Phrase: "ai visibility checker", Seed: "ai visibility"},
		{Phrase: "ai visibility tools", Seed: "ai visibility"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Suggest = %+v, want %+v", got, want)
	}
	if gotQuery != "ai visibility" || gotClient != ClientChrome {
		t.Fatalf("query=%q client=%q, want chrome default", gotQuery, gotClient)
	}
}

func TestSuggestParsesFirefoxShape(t *testing.T) {
	// Firefox returns a shorter envelope with only two elements.
	body := `["ai visibility",["a","b"]]`
	client := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("client"); got != ClientFirefox {
			t.Errorf("client = %q, want firefox", got)
		}
		_, _ = io.WriteString(w, body)
	})

	got, err := client.Suggest(context.Background(), "ai visibility", Options{Client: ClientFirefox})
	if err != nil {
		t.Fatalf("Suggest error: %v", err)
	}
	want := []Suggestion{{Phrase: "a", Seed: "ai visibility"}, {Phrase: "b", Seed: "ai visibility"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Suggest = %+v, want %+v", got, want)
	}
}

func TestSuggestIgnoresObjectElementZero(t *testing.T) {
	// Some locales put an object in element 0; only element 1 matters.
	body := `[{"suggestion":true},["x","y"]]`
	client := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	})

	got, err := client.Suggest(context.Background(), "seed", Options{})
	if err != nil {
		t.Fatalf("Suggest error: %v", err)
	}
	want := []Suggestion{{Phrase: "x", Seed: "seed"}, {Phrase: "y", Seed: "seed"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Suggest = %+v, want %+v", got, want)
	}
}

func TestSuggestShapeErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"invalid json", `not json`},
		{"not an array", `{"seed":"x"}`},
		{"too few elements", `["only one"]`},
		{"element one wrong type", `["seed","not a list"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := client.Suggest(context.Background(), "seed", Options{})
			if err == nil || !strings.Contains(err.Error(), "shape problem") {
				t.Fatalf("Suggest error = %v, want a response shape problem", err)
			}
		})
	}
}

func TestSuggestNon200(t *testing.T) {
	client := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := client.Suggest(context.Background(), "seed", Options{})
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("Suggest error = %v, want status 500", err)
	}
}

func TestSuggestOversizedBody(t *testing.T) {
	client := newTestClient(t, 8, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `["seed",["a very long phrase that exceeds the cap"]]`)
	})
	_, err := client.Suggest(context.Background(), "seed", Options{})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Suggest error = %v, want an oversize error", err)
	}
}

func TestSuggestTrimsAndDedupes(t *testing.T) {
	body := `["seed",[" a ","A","","  ","b"]]`
	client := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	})
	got, err := client.Suggest(context.Background(), " seed ", Options{})
	if err != nil {
		t.Fatalf("Suggest error: %v", err)
	}
	want := []Suggestion{{Phrase: "a", Seed: "seed"}, {Phrase: "b", Seed: "seed"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Suggest = %+v, want %+v (trim, drop empties, keep first spelling)", got, want)
	}
}

func TestSuggestRejectsEmptySeed(t *testing.T) {
	client := NewClient("", 0, time.Second)
	if _, err := client.Suggest(context.Background(), "   ", Options{}); err == nil {
		t.Fatal("Suggest with a blank seed = nil error, want an error")
	}
}

func TestSuggestCacheServesSecondCall(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `["seed",["one","two"]]`)
	})

	first, err := client.Suggest(context.Background(), "seed", Options{})
	if err != nil {
		t.Fatalf("first Suggest error: %v", err)
	}
	second, err := client.Suggest(context.Background(), "seed", Options{})
	if err != nil {
		t.Fatalf("second Suggest error: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("upstream requests = %d, want 1 (second call served from cache)", requests.Load())
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cached result = %+v, want %+v", second, first)
	}
}

func TestExpandCapsUpstreamRequests(t *testing.T) {
	var requests atomic.Int32
	client := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `["seed",["one"]]`)
	})

	// Three seeds (bare + a + b) but only two attempts are allowed.
	if _, err := client.Expand(context.Background(), "seo audit", "ab", Options{}, 2); err != nil {
		t.Fatalf("Expand error: %v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("upstream requests = %d, want exactly 2", requests.Load())
	}
}

func TestExpandDedupesAcrossSeedsAndRecordsSource(t *testing.T) {
	responses := map[string]string{
		"seo audit":   `["seo audit",["seo audit","seo audit tools"]]`,
		"seo audit a": `["seo audit a",["seo audit tools","seo audit agency"]]`,
		"seo audit b": `["seo audit b",["seo audit basics"]]`,
	}
	client := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		body, ok := responses[r.URL.Query().Get("q")]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, body)
	})

	got, err := client.Expand(context.Background(), "seo audit", "ab", Options{}, 3)
	if err != nil {
		t.Fatalf("Expand error: %v", err)
	}
	want := []Suggestion{
		{Phrase: "seo audit", Seed: "seo audit"},
		{Phrase: "seo audit tools", Seed: "seo audit"},
		{Phrase: "seo audit agency", Seed: "seo audit a"},
		{Phrase: "seo audit basics", Seed: "seo audit b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Expand = %+v, want %+v in seed order", got, want)
	}
}

func TestExpandReturnsPartialResults(t *testing.T) {
	responses := map[string]string{
		"seo audit":   `["seo audit",["seo audit"]]`,
		"seo audit a": `["seo audit a",["seo audit agency"]]`,
	}
	client := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		body, ok := responses[r.URL.Query().Get("q")]
		if !ok {
			// "seo audit b" fails; the rest must still come through.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, body)
	})

	got, err := client.Expand(context.Background(), "seo audit", "ab", Options{}, 3)
	if err != nil {
		t.Fatalf("Expand error = %v, want partial results", err)
	}
	want := []Suggestion{
		{Phrase: "seo audit", Seed: "seo audit"},
		{Phrase: "seo audit agency", Seed: "seo audit a"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Expand = %+v, want %+v", got, want)
	}
}

func TestExpandErrorsOnlyWhenAllAttemptsFail(t *testing.T) {
	client := newTestClient(t, 0, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := client.Expand(context.Background(), "seo audit", "ab", Options{}, 3); err == nil {
		t.Fatal("Expand = nil error, want an error when every attempt failed")
	}
}
