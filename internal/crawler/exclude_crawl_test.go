package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

type excludedHitLog struct {
	mu         sync.Mutex
	hits       map[string]int
	validators map[string]string
}

func (log *excludedHitLog) record(request *http.Request) {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.hits[request.URL.Path]++
	if validator := request.Header.Get("If-None-Match"); validator != "" {
		log.validators[request.URL.Path] = validator
	}
}

func (log *excludedHitLog) count(path string) int {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.hits[path]
}

func (log *excludedHitLog) total() int {
	log.mu.Lock()
	defer log.mu.Unlock()
	total := 0
	for _, hits := range log.hits {
		total += hits
	}
	return total
}

func excludedTestPage(title string, links ...string) string {
	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html><head><title>` + title + `</title></head><body>`)
	for _, link := range links {
		fmt.Fprintf(&sb, `<a href="%s">%s</a>`, link, link)
	}
	sb.WriteString(`</body></html>`)
	return sb.String()
}

func runExcludedTestCrawl(t *testing.T, serverURL string, rawSnapshot string, maxDepth int, baseline *Baseline, baselineStore baselineReuser) []CrawlResult {
	t.Helper()
	config, err := ConfigFromBaseURLAndSnapshot(serverURL, []byte(rawSnapshot))
	if err != nil {
		t.Fatalf("build crawler config: %v", err)
	}
	config.MaxDepth = maxDepth
	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second).WithExcludedURLs(config.ExcludedURLs)
	runner := NewRunner(config, 2, fetcher, NewParser())
	if baseline != nil {
		runner = runner.WithBaseline(baseline, baselineStore)
	}
	results, err := runner.Run(context.Background(), serverURL)
	if err != nil {
		t.Fatalf("run crawler: %v", err)
	}
	return results
}

func TestRunnerSkipsExcludedPagesAndRedirectTargets(t *testing.T) {
	allowLoopbackDialsForTest(t)
	log := &excludedHitLog{hits: map[string]int{}, validators: map[string]string{}}

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		log.record(request)
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch request.URL.Path {
		case "/":
			fmt.Fprint(writer, excludedTestPage("home", "/about", "/secret", "/blog/1", "/old"))
		case "/old":
			http.Redirect(writer, request, "/secret", http.StatusFound)
		default:
			fmt.Fprint(writer, excludedTestPage(request.URL.Path))
		}
	}))
	defer server.Close()
	serverURL = server.URL

	results := runExcludedTestCrawl(t, serverURL, `{"excluded_url_patterns":["/secret","`+serverURL+`/blog/*"]}`, 2, nil, nil)

	for path, want := range map[string]int{"/": 1, "/about": 1, "/old": 1, "/secret": 0, "/blog/1": 0} {
		if got := log.count(path); got != want {
			t.Errorf("hits for %s = %d, want %d", path, got, want)
		}
	}

	oldFound := false
	for _, result := range results {
		if strings.HasSuffix(result.Job.URL, "/old") {
			oldFound = true
			if result.Fetch.FetchError == nil || !strings.Contains(result.Fetch.FetchError.Error(), "excluded") {
				t.Errorf("redirect into excluded content must report an exclusion error, got %+v", result.Fetch)
			}
			if len(result.Fetch.Body) != 0 {
				t.Error("redirect into excluded content must not keep a body")
			}
		}
		if strings.HasSuffix(result.Fetch.FinalURL, "/secret") || strings.HasSuffix(result.Fetch.FinalURL, "/blog/1") {
			t.Errorf("excluded page has a result: %+v", result.Fetch)
		}
	}
	if !oldFound {
		t.Error("expected a result for /old")
	}
}

func TestRunnerExcludedRootMakesNoRequests(t *testing.T) {
	allowLoopbackDialsForTest(t)
	log := &excludedHitLog{hits: map[string]int{}, validators: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		log.record(request)
		fmt.Fprint(writer, excludedTestPage("home"))
	}))
	defer server.Close()

	config, err := ConfigFromBaseURLAndSnapshot(server.URL, []byte(`{"excluded_url_patterns":["/"]}`))
	if err != nil {
		t.Fatalf("build crawler config: %v", err)
	}
	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second).WithExcludedURLs(config.ExcludedURLs)
	runner := NewRunner(config, 2, fetcher, NewParser())

	_, err = runner.Run(context.Background(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "excluded") {
		t.Fatalf("expected excluded-root error, got %v", err)
	}
	if got := log.total(); got != 0 {
		t.Fatalf("excluded root caused %d requests, want none", got)
	}
}

func TestRunnerWithoutPatternsCrawlsEverything(t *testing.T) {
	allowLoopbackDialsForTest(t)
	log := &excludedHitLog{hits: map[string]int{}, validators: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		log.record(request)
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		if request.URL.Path == "/" {
			fmt.Fprint(writer, excludedTestPage("home", "/secret"))
			return
		}
		fmt.Fprint(writer, excludedTestPage(request.URL.Path))
	}))
	defer server.Close()

	runExcludedTestCrawl(t, server.URL, `{}`, 1, nil, nil)

	if got := log.count("/secret"); got != 1 {
		t.Fatalf("hits for /secret = %d, want 1 without patterns", got)
	}
}

func TestRunnerFiltersSitemapSeeds(t *testing.T) {
	allowLoopbackDialsForTest(t)
	log := &excludedHitLog{hits: map[string]int{}, validators: map[string]string{}}

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		log.record(request)
		switch request.URL.Path {
		case "/sitemap.xml":
			writer.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(writer, `<?xml version="1.0"?><urlset><url><loc>%s/from-sitemap-allowed</loc></url><url><loc>%s/from-sitemap-secret</loc></url></urlset>`, serverURL, serverURL)
		default:
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(writer, excludedTestPage(request.URL.Path))
		}
	}))
	defer server.Close()
	serverURL = server.URL

	runExcludedTestCrawl(t, serverURL, `{"excluded_url_patterns":["/from-sitemap-secret"]}`, 0, nil, nil)

	if got := log.count("/from-sitemap-allowed"); got != 1 {
		t.Errorf("hits for allowed sitemap seed = %d, want 1", got)
	}
	if got := log.count("/from-sitemap-secret"); got != 0 {
		t.Errorf("hits for excluded sitemap seed = %d, want 0", got)
	}
}

type stubBaselineStore struct{}

func (stubBaselineStore) PersistReusedResult(_ context.Context, _ pgtype.UUID, _ *Baseline, _ CrawlResult) error {
	return nil
}

func (stubBaselineStore) PersistReusedResults(_ context.Context, _ pgtype.UUID, _ *Baseline, _ []CrawlResult) error {
	return nil
}

func TestRunnerBaselineReuseSkipsExcluded(t *testing.T) {
	allowLoopbackDialsForTest(t)
	log := &excludedHitLog{hits: map[string]int{}, validators: map[string]string{}}

	var serverURL string
	serveConditional := func(writer http.ResponseWriter, request *http.Request, validator string, links ...string) {
		if request.Header.Get("If-None-Match") == validator {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("ETag", validator)
		fmt.Fprint(writer, excludedTestPage(request.URL.Path, links...))
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		log.record(request)
		switch request.URL.Path {
		case "/":
			serveConditional(writer, request, `"root-v1"`, "/keep", "/skip")
		case "/keep":
			serveConditional(writer, request, `"keep-v1"`)
		case "/skip":
			fmt.Fprint(writer, excludedTestPage("skip"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	normalize := func(path string) string {
		normalized, err := NormalizeURL(serverURL+path, nil)
		if err != nil {
			t.Fatalf("normalize %s: %v", path, err)
		}
		return normalized.String()
	}
	baseline := &Baseline{
		pages: map[string]baselinePage{
			normalize("/"):     {storedURL: serverURL + "/", etag: `"root-v1"`},
			normalize("/keep"): {storedURL: serverURL + "/keep", etag: `"keep-v1"`},
		},
		internalTargetsBySource: map[string][]string{
			serverURL + "/": {serverURL + "/keep", serverURL + "/skip"},
		},
	}

	results := runExcludedTestCrawl(t, serverURL, `{"excluded_url_patterns":["/skip"]}`, 1, baseline, stubBaselineStore{})

	if got := log.count("/skip"); got != 0 {
		t.Errorf("historical reuse fetched excluded /skip %d times, want 0", got)
	}
	if got := log.count("/keep"); got != 1 {
		t.Errorf("hits for /keep = %d, want 1 conditional request", got)
	}
	log.mu.Lock()
	keepValidator := log.validators["/keep"]
	rootValidator := log.validators["/"]
	log.mu.Unlock()
	if keepValidator != `"keep-v1"` || rootValidator != `"root-v1"` {
		t.Errorf("baseline validators not sent: keep=%q root=%q", keepValidator, rootValidator)
	}
	for _, result := range results {
		if !result.NotModified {
			t.Errorf("expected reused (304) result for %q", result.Job.URL)
		}
	}
	if len(results) != 2 {
		t.Errorf("got %d results, want 2 (root and /keep, no /skip)", len(results))
	}
}

func TestFetcherBlocksExcludedDirectFetch(t *testing.T) {
	allowLoopbackDialsForTest(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		fmt.Fprint(writer, excludedTestPage("x"))
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second).
		WithExcludedURLs(mustCompileExcluded(t, "/blocked"))
	result := fetcher.Fetch(context.Background(), server.URL+"/blocked")
	if result.FetchError == nil || !strings.Contains(result.FetchError.Error(), "excluded") {
		t.Fatalf("expected exclusion error, got %+v", result)
	}
	if requests != 0 {
		t.Fatalf("excluded fetch made %d requests, want 0", requests)
	}

	allowed := fetcher.Fetch(context.Background(), server.URL+"/open")
	if allowed.FetchError != nil {
		t.Fatalf("allowed fetch failed: %v", allowed.FetchError)
	}
	if requests != 1 {
		t.Fatalf("allowed fetch made %d total requests, want 1", requests)
	}
}

func TestExcludedPatternsSurviveSnapshotJSON(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"excluded_url_patterns": []string{"/a"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	snapshot, _, err := NormalizeConfigSnapshot(raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(snapshot.ExcludedURLPatterns) != 1 || snapshot.ExcludedURLPatterns[0] != "/a" {
		t.Fatalf("got %q after JSON round trip", snapshot.ExcludedURLPatterns)
	}
}
