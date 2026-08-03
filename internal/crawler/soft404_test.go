package crawler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const notFoundTemplate = `<html><head><title>Page not found</title></head>
<body><nav>Home Services About</nav><h1>Sorry! Page not found.</h1>
<p>The page you are looking for was moved, removed, renamed or never existed.</p></body></html>`

// detectAgainst runs the probe against a handler and returns the fingerprint.
func detectAgainst(t *testing.T, handler http.HandlerFunc) (*SoftNotFoundFingerprint, string) {
	t.Helper()
	allowLoopbackDialsForTest(t)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	fetcher := NewFetcher(5*time.Second, "test-agent", 0, time.Millisecond, time.Millisecond)
	fingerprint := DetectSoftNotFound(context.Background(), fetcher, NewParser(), server.URL)
	return fingerprint, server.URL
}

func TestDetectSoftNotFoundIgnoresCorrect404(t *testing.T) {
	fingerprint, _ := detectAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(notFoundTemplate))
	})

	if fingerprint != nil {
		t.Errorf("fingerprint recorded for an origin that correctly returns 404: %+v", fingerprint)
	}
}

func TestDetectSoftNotFoundFingerprintsSoft404(t *testing.T) {
	fingerprint, _ := detectAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(notFoundTemplate))
	})

	if fingerprint == nil {
		t.Fatal("no fingerprint recorded for an origin serving a 200 not-found page")
	}
	if fingerprint.Title != "page not found" {
		t.Errorf("Title = %q", fingerprint.Title)
	}
	if fingerprint.H1 != "sorry! page not found." {
		t.Errorf("H1 = %q", fingerprint.H1)
	}
	if fingerprint.ContentHash == "" {
		t.Error("ContentHash is empty")
	}
}

// A site answering unknown URLs with its homepage must not be fingerprinted:
// the fingerprint would match the real homepage and everything resembling it.
func TestDetectSoftNotFoundSkipsCatchAllHomepage(t *testing.T) {
	fingerprint, _ := detectAgainst(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>RevKeter</title></head><body><h1>Grow your revenue</h1><p>We help teams win.</p></body></html>`))
	})

	if fingerprint != nil {
		t.Errorf("fingerprint recorded for a catch-all homepage: %+v", fingerprint)
	}
}

func TestDetectSoftNotFoundProbeURLIsUnderRootAndUnpredictable(t *testing.T) {
	var probedPaths []string
	handler := func(w http.ResponseWriter, r *http.Request) {
		probedPaths = append(probedPaths, r.URL.Path)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(notFoundTemplate))
	}

	first, _ := detectAgainst(t, handler)
	second, _ := detectAgainst(t, handler)
	if first == nil || second == nil {
		t.Fatal("expected fingerprints for both probes")
	}
	if first.ProbeURL == second.ProbeURL {
		t.Error("probe URL is identical across runs; a cached path could answer it")
	}
	for _, path := range probedPaths {
		if !strings.HasPrefix(path, "/revserp-not-found-probe-") {
			t.Errorf("probe path %q is not the expected probe path", path)
		}
	}
}

func TestFingerprintMatches(t *testing.T) {
	fingerprint := &SoftNotFoundFingerprint{
		Title:       "page not found",
		H1:          "sorry! page not found.",
		ContentHash: hashVisibleText("Sorry! Page not found. The page was moved."),
	}

	tests := []struct {
		name string
		page *ParsedPage
		want bool
	}{
		{"identical text", &ParsedPage{VisibleText: "Sorry! Page not found. The page was moved."}, true},
		{"title and h1 agree", &ParsedPage{Title: "Page Not Found", H1: "Sorry! Page not found.", VisibleText: "different body"}, true},
		{"only title agrees", &ParsedPage{Title: "Page Not Found", H1: "Our services", VisibleText: "x"}, false},
		{"real page", &ParsedPage{Title: "Pricing", H1: "Plans", VisibleText: "Our plans"}, false},
		{"nil page", nil, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := fingerprint.Matches(test.page); got != test.want {
				t.Errorf("Matches() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestNilFingerprintMatchesNothing(t *testing.T) {
	var fingerprint *SoftNotFoundFingerprint
	if fingerprint.Matches(&ParsedPage{Title: "Page not found"}) {
		t.Error("a nil fingerprint must not match")
	}
}

// A fingerprint with an empty title or H1 must not match on the other alone,
// or every page sharing one blank field would be flagged.
func TestFingerprintWithMissingTitleOrH1DoesNotMatchOnOne(t *testing.T) {
	fingerprint := &SoftNotFoundFingerprint{H1: "sorry! page not found.", ContentHash: hashVisibleText("a")}
	if fingerprint.Matches(&ParsedPage{Title: "", H1: "Sorry! Page not found.", VisibleText: "b"}) {
		t.Error("matched with an empty fingerprint title")
	}
}

func TestLooksLikeSoftNotFound(t *testing.T) {
	longBody := strings.Repeat("word ", softNotFoundMaxWordCount+50)

	tests := []struct {
		name string
		page *ParsedPage
		want bool
	}{
		{"thin 404 title", &ParsedPage{Title: "404 - Not Found", VisibleText: "nothing here"}, true},
		{"thin 404 h1", &ParsedPage{H1: "This page no longer exists", VisibleText: "nothing here"}, true},
		{"article about 404s", &ParsedPage{Title: "How to fix 404 errors", VisibleText: longBody}, false},
		{"ordinary page", &ParsedPage{Title: "Pricing", H1: "Plans", VisibleText: "short"}, false},
		{"nil", nil, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := LooksLikeSoftNotFound(test.page); got != test.want {
				t.Errorf("LooksLikeSoftNotFound() = %v, want %v", got, test.want)
			}
		})
	}
}
