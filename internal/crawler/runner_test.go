package crawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestRunnerRunCrawlsInternalPagesUpToMaxDepth(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")

		switch request.URL.Path {
		case "/":
			fmt.Fprint(writer, `<!DOCTYPE html>
								<html><head><title>home</title></head><body>
									<a href="/about">About</a>
									<a href="https://vercel.com">External</a>
								</body></html>`)
		case "/about":
			fmt.Fprint(writer, `<!DOCTYPE html>
								<html><head><title>about</title></head><body>
									<a href="/team">Team</a>
								</body></html>`)
		case "/team":
			fmt.Fprint(writer, `<!DOCTYPE html>
								<html><head><title>team</title></head><body></body></html>`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    1,
		MaxPages:    10,
	}, 2, fetcher, parser)

	results, err := runner.Run(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("run crawler: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	var crawledPaths []string
	for _, result := range results {
		if result.ProcessErr != nil {
			t.Fatalf("unexpected process error: %v", result.ProcessErr)
		}

		crawledPaths = append(crawledPaths, mustParseURL(t, result.Fetch.FinalURL).Path)
	}

	sort.Strings(crawledPaths)

	if crawledPaths[0] != "/" || crawledPaths[1] != "/about" {
		t.Fatalf("got crawled paths %#v", crawledPaths)
	}
}

func TestRunnerRunRespectsMaxPages(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `<!DOCTYPE html>
							<html><head><title>page</title></head><body>
								<a href="/one">One</a>
								<a href="/two">Two</a>
								<a href="/three">Three</a>
							</body></html>`)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    2,
		MaxPages:    2,
	}, 2, fetcher, parser)

	results, err := runner.Run(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("run crawler: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
}

func TestRunnerRunAllowsUnlimitedPagesWhenMaxPagesIsZero(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch request.URL.Path {
		case "/":
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>home</title></head><body><a href="/one">One</a><a href="/two">Two</a><a href="/three">Three</a></body></html>`)
		default:
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>child</title></head><body></body></html>`)
		}
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    1,
		MaxPages:    0,
	}, 2, fetcher, parser)

	results, err := runner.Run(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("run crawler: %v", err)
	}

	if len(results) != 4 {
		t.Fatalf("got %d results, want 4", len(results))
	}
}

func TestRunnerRunSkipsDuplicateFinalURLs(t *testing.T) {
	allowLoopbackDialsForTest(t)

	redirectTargetPath := "/final"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")

		switch request.URL.Path {
		case "/":
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>home</title></head><body><a href="/alias-a">A</a><a href="/alias-b">B</a></body></html>`)
		case "/alias-a", "/alias-b":
			http.Redirect(writer, request, redirectTargetPath, http.StatusMovedPermanently)
		case redirectTargetPath:
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>final</title></head><body></body></html>`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	store := &testResultStore{}
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    1,
		MaxPages:    3,
	}, 2, fetcher, parser).WithStore(store)

	results, err := runner.RunAndPersist(context.Background(), pgtype.UUID{}, server.URL)
	if err != nil {
		t.Fatalf("run and persist crawler: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	if store.persistedCount != 2 {
		t.Fatalf("got persisted count %d, want 2", store.persistedCount)
	}
	if !store.markedCompleted {
		t.Fatalf("expected crawl to be marked completed")
	}
}

func TestRunnerRunHandlesManyDiscoveredLinksWithoutDeadlocking(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")

		switch request.URL.Path {
		case "/":
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>home</title></head><body>`)
			for index := range 20 {
				fmt.Fprintf(writer, `<a href="/page-%d">Page %d</a>`, index, index)
			}
			fmt.Fprint(writer, `</body></html>`)
		default:
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>child</title></head><body></body></html>`)
		}
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    1,
		MaxPages:    21,
	}, 2, fetcher, parser)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	results, err := runner.Run(ctx, server.URL)
	if err != nil {
		t.Fatalf("run crawler: %v", err)
	}

	if len(results) != 21 {
		t.Fatalf("got %d results, want 21", len(results))
	}
}

func TestRunnerRunAndPersistCallsStore(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>home</title></head><body><a href="/about">About</a></body></html>`)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	store := &testResultStore{}
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    1,
		MaxPages:    2,
	}, 2, fetcher, parser).WithStore(store)

	results, err := runner.RunAndPersist(context.Background(), pgtype.UUID{}, server.URL)
	if err != nil {
		t.Fatalf("run and persist crawler: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	if store.persistedCount != 2 {
		t.Fatalf("got persisted count %d, want 2", store.persistedCount)
	}

	if !store.markedRunning {
		t.Fatalf("expected crawl to be marked running")
	}

	if !store.markedCompleted {
		t.Fatalf("expected crawl to be marked completed")
	}

	if store.completedDiscovered != 2 || store.completedCrawled != 2 || store.completedMaxDepth != 1 {
		t.Fatalf("got final counters discovered=%d crawled=%d maxDepth=%d", store.completedDiscovered, store.completedCrawled, store.completedMaxDepth)
	}
}

func TestRunnerRunAndPersistFailsOnStoreError(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>home</title></head><body></body></html>`)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    0,
		MaxPages:    1,
	}, 1, fetcher, parser).WithStore(&testResultStore{persistErr: fmt.Errorf("boom")})

	results, err := runner.RunAndPersist(context.Background(), pgtype.UUID{}, server.URL)
	if err == nil {
		t.Fatalf("expected persist error but got nil")
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	failingStore := runner.store.(*testResultStore)
	if !failingStore.markedRunning {
		t.Fatalf("expected crawl to be marked running")
	}
	if !failingStore.markedFailed {
		t.Fatalf("expected crawl to be marked failed")
	}
	if failingStore.completedDiscovered != 1 || failingStore.completedCrawled != 1 || failingStore.completedMaxDepth != 0 {
		t.Fatalf("got failed counters discovered=%d crawled=%d maxDepth=%d", failingStore.completedDiscovered, failingStore.completedCrawled, failingStore.completedMaxDepth)
	}
}

func TestRunnerRunAndPersistFlushesTailBatch(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")

		switch request.URL.Path {
		case "/":
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>home</title></head><body><a href="/one">One</a><a href="/two">Two</a></body></html>`)
		case "/one", "/two":
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>child</title></head><body></body></html>`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	store := &testResultStore{}
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    1,
		MaxPages:    3,
	}, 2, fetcher, parser).WithStore(store)

	results, err := runner.RunAndPersist(context.Background(), pgtype.UUID{}, server.URL)
	if err != nil {
		t.Fatalf("run and persist crawler: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	// Three pages is not a multiple of persistBatchSize, so the last partial
	// batch is only persisted by the tail flush.
	if store.persistedCount != 3 {
		t.Fatalf("got persisted count %d, want 3", store.persistedCount)
	}
}

// cancellingStore cancels the crawl context from inside a batch write and then
// waits long enough for the worker pool to shut down and close its results
// channel. That is the only way to put the runner into the state where it can
// take the pool-closed exit: it is busy inside the store call while the pool
// closes, so when it next selects, the closed channel and the cancellation
// signal are both ready and it picks between them at random. Cancelling while
// the runner sits idle in its select always loses that race to the cancellation
// signal, which is why a plain cancel test never reaches this branch.
type cancellingStore struct {
	testResultStore
	cancel context.CancelFunc
}

func (store *cancellingStore) PersistResults(ctx context.Context, crawlID pgtype.UUID, rootURL string, results []CrawlResult) error {
	store.cancel()
	// Let the workers unwind and the pool close the results channel while the
	// runner is still inside this call.
	time.Sleep(30 * time.Millisecond)
	return store.testResultStore.PersistResults(ctx, crawlID, rootURL, results)
}

func TestRunnerRunAndPersistMarksFailedWhenPoolClosesWithWorkOutstanding(t *testing.T) {
	allowLoopbackDialsForTest(t)

	// Every page links to 200 fresh internal URLs, so the frontier never drains
	// and the crawl is still running when the batch write cancels it.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>page</title></head><body>`)
		for index := range 200 {
			fmt.Fprintf(writer, `<a href="%sc%d">child</a>`, request.URL.Path, index)
		}
		fmt.Fprint(writer, `</body></html>`)
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()

	// Which exit the runner takes out of that select is a coin flip, so one
	// iteration proves little. The iterations are independent, and the missing
	// failed mark shows up on every one that loses the flip.
	for attempt := range 20 {
		ctx, cancel := context.WithCancel(context.Background())
		store := &cancellingStore{cancel: cancel}
		runner := NewRunner(CrawlerConfig{
			AllowedHost: mustParseURL(t, server.URL).Host,
			MaxDepth:    2,
			MaxPages:    500,
		}, 4, fetcher, parser).WithStore(store)

		_, _ = runner.RunAndPersist(ctx, pgtype.UUID{}, server.URL)
		cancel()

		if !store.markedFailed {
			t.Fatalf("attempt %d: expected crawl to be marked failed when the pool closed with work outstanding", attempt)
		}
	}
}

type testResultStore struct {
	persistedCount      int
	persistErr          error
	markedRunning       bool
	markedCompleted     bool
	markedFailed        bool
	completedDiscovered int
	completedCrawled    int
	completedMaxDepth   int
	completedHasLlmsTxt pgtype.Bool
}

func (store *testResultStore) MarkCrawlRunning(_ context.Context, _ pgtype.UUID) error {
	store.markedRunning = true
	return nil
}

func (store *testResultStore) MarkCrawlCompleted(_ context.Context, _ pgtype.UUID, urlsDiscovered int, urlsCrawled int, maxDepthReached int, hasLlmsTxt pgtype.Bool) error {
	store.markedCompleted = true
	store.completedDiscovered = urlsDiscovered
	store.completedCrawled = urlsCrawled
	store.completedMaxDepth = maxDepthReached
	store.completedHasLlmsTxt = hasLlmsTxt
	return nil
}

func (store *testResultStore) MarkCrawlFailed(_ context.Context, _ pgtype.UUID, urlsDiscovered int, urlsCrawled int, maxDepthReached int, _ string) error {
	store.markedFailed = true
	store.completedDiscovered = urlsDiscovered
	store.completedCrawled = urlsCrawled
	store.completedMaxDepth = maxDepthReached
	return nil
}

func (store *testResultStore) PersistResult(ctx context.Context, crawlID pgtype.UUID, rootURL string, result CrawlResult) error {
	return store.PersistResults(ctx, crawlID, rootURL, []CrawlResult{result})
}

func (store *testResultStore) PersistResults(_ context.Context, _ pgtype.UUID, _ string, results []CrawlResult) error {
	if store.persistErr != nil {
		return store.persistErr
	}

	store.persistedCount += len(results)
	return nil
}

func (store *testResultStore) PersistReusedResult(ctx context.Context, crawlID pgtype.UUID, baseline *Baseline, result CrawlResult) error {
	return store.PersistReusedResults(ctx, crawlID, baseline, []CrawlResult{result})
}

func (store *testResultStore) PersistReusedResults(_ context.Context, _ pgtype.UUID, _ *Baseline, results []CrawlResult) error {
	if store.persistErr != nil {
		return store.persistErr
	}

	store.persistedCount += len(results)
	return nil
}

func TestRunnerRunSkipsSitemapSeedWhenConfigured(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")

		switch request.URL.Path {
		case "/":
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>home</title></head><body><a href="/linked">Linked</a></body></html>`)
		case "/linked":
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>linked</title></head><body></body></html>`)
		case "/sitemap.xml":
			writer.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(writer, `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>http://%s/sitemap-only</loc></url></urlset>`, request.Host)
		case "/sitemap-only":
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>sitemap only</title></head><body></body></html>`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	runner := NewRunner(CrawlerConfig{
		AllowedHost:     mustParseURL(t, server.URL).Host,
		MaxDepth:        2,
		MaxPages:        10,
		SkipSitemapSeed: true,
	}, 2, fetcher, parser)

	results, err := runner.Run(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("run crawler: %v", err)
	}

	paths := make([]string, 0, len(results))
	for _, result := range results {
		paths = append(paths, mustParseURL(t, result.Fetch.FinalURL).Path)
	}
	sort.Strings(paths)

	want := []string{"/", "/linked"}
	if !slicesEqual(paths, want) {
		t.Fatalf("got crawled paths %#v, want %#v", paths, want)
	}
}

func TestRunnerRunSeedsFromSitemapByDefault(t *testing.T) {
	allowLoopbackDialsForTest(t)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/":
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>home</title></head><body></body></html>`)
		case "/sitemap.xml":
			writer.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(writer, `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>http://%s/sitemap-only</loc></url></urlset>`, request.Host)
		case "/sitemap-only":
			fmt.Fprint(writer, `<!DOCTYPE html><html><head><title>sitemap only</title></head><body></body></html>`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	fetcher := NewFetcher(5*time.Second, "", 0, time.Second, 15*time.Second)
	parser := NewParser()
	runner := NewRunner(CrawlerConfig{
		AllowedHost: mustParseURL(t, server.URL).Host,
		MaxDepth:    1,
		MaxPages:    10,
	}, 2, fetcher, parser)

	results, err := runner.Run(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("run crawler: %v", err)
	}

	paths := make([]string, 0, len(results))
	for _, result := range results {
		paths = append(paths, mustParseURL(t, result.Fetch.FinalURL).Path)
	}
	sort.Strings(paths)

	if len(paths) != 2 {
		t.Fatalf("got %d results, want 2; paths=%#v", len(paths), paths)
	}
	if paths[0] != "/" || paths[1] != "/sitemap-only" {
		t.Fatalf("got crawled paths %#v", paths)
	}
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (store *testResultStore) UpdateCrawlProgress(_ context.Context, _ pgtype.UUID, _ int, _ int) (bool, error) {
	return true, nil
}

func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse url %q: %v", rawURL, err)
	}

	return parsedURL
}
