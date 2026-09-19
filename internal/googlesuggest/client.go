// Package googlesuggest reads phrase completions from Google autocomplete. The
// endpoint is undocumented and unversioned, so the client treats every response
// as untrusted: it caps the body, parses tolerantly, and never reads the
// relevance array as demand.
package googlesuggest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultEndpoint is the Suggest completion endpoint. Callers rarely need to
// override it, but tests point the client at a local server.
const DefaultEndpoint = "https://suggestqueries.google.com/complete/search"

// Client values select how many completions the endpoint returns. Chrome is the
// wider default; firefox returns a shorter list.
const (
	ClientChrome  = "chrome"
	ClientFirefox = "firefox"
)

const (
	defaultTimeout          = 8 * time.Second
	defaultMaxResponseBytes = 1 << 20
	cacheTTL                = 15 * time.Minute
	cacheMaxEntries         = 512
	expandConcurrency       = 4
	defaultExpandLetters    = "abcdefghijklmnopqrstuvwxyz"
	// A full expansion is the bare seed plus one seed per letter, so 27.
	defaultExpandRequests = 27
	// userAgent mimics a desktop browser: the endpoint answers simpler agents
	// with a shorter or empty list.
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// Suggestion is one completion, tagged with the seed query that produced it so
// an expanded call can show which letter surfaced the phrase.
type Suggestion struct {
	Phrase string
	Seed   string
}

// Options are the optional request hints. Client is empty for ClientChrome.
// Language (hl) and Country (gl) are soft hints: Google ignores them for some
// queries, so they narrow nothing and must not be presented as a filter.
type Options struct {
	Client   string
	Language string
	Country  string
}

// Client is a thread-safe autocomplete reader with an in-memory cache.
type Client struct {
	endpoint         string
	maxResponseBytes int64
	http             *http.Client

	mu    sync.Mutex
	cache map[cacheKey]cacheEntry
}

type cacheKey struct {
	client   string
	language string
	country  string
	seed     string
}

type cacheEntry struct {
	phrases  []Suggestion
	storedAt time.Time
}

// NewClient builds a client. A non-positive timeout means 8s, a non-positive
// maxResponseBytes means 1 MiB, and an empty endpoint means DefaultEndpoint.
func NewClient(endpoint string, maxResponseBytes int64, timeout time.Duration) *Client {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = DefaultEndpoint
	}
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		endpoint:         endpoint,
		maxResponseBytes: maxResponseBytes,
		http:             &http.Client{Timeout: timeout},
		cache:            make(map[cacheKey]cacheEntry),
	}
}

// Suggest returns the completions Google shows for one seed. An empty seed is
// rejected before any request. A cached result costs no upstream request.
func (c *Client) Suggest(ctx context.Context, seed string, opts Options) ([]Suggestion, error) {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return nil, errors.New("googlesuggest: seed is required")
	}
	client := opts.Client
	if client == "" {
		client = ClientChrome
	}
	key := cacheKey{client: client, language: opts.Language, country: opts.Country, seed: seed}
	if cached, ok := c.lookup(key); ok {
		return cached, nil
	}

	phrases, err := c.fetch(ctx, seed, client, opts)
	if err != nil {
		return nil, err
	}
	suggestions := make([]Suggestion, 0, len(phrases))
	for _, phrase := range phrases {
		suggestions = append(suggestions, Suggestion{Phrase: phrase, Seed: seed})
	}
	c.store(key, suggestions)
	return suggestions, nil
}

// Expand queries the bare seed and the seed with each letter appended, then
// merges the results. maxRequests is a hard cap on upstream attempts; a
// non-positive value means 27. A sub-request that fails is skipped unless every
// attempt failed, so a throttled letter never loses the whole call.
func (c *Client) Expand(ctx context.Context, seed, letters string, opts Options, maxRequests int) ([]Suggestion, error) {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return nil, errors.New("googlesuggest: seed is required")
	}
	if letters == "" {
		letters = defaultExpandLetters
	}
	if maxRequests <= 0 {
		maxRequests = defaultExpandRequests
	}

	seeds := make([]string, 0, 1+len(letters))
	seeds = append(seeds, seed)
	for _, letter := range letters {
		seeds = append(seeds, seed+" "+string(letter))
	}

	results := make([][]Suggestion, len(seeds))
	var failures int
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, expandConcurrency)

	attempts := 0
	for index, subSeed := range seeds {
		if attempts >= maxRequests {
			break
		}
		attempts++
		wg.Add(1)
		sem <- struct{}{}
		go func(index int, subSeed string) {
			defer wg.Done()
			defer func() { <-sem }()
			suggestions, err := c.Suggest(ctx, subSeed, opts)
			if err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
				return
			}
			results[index] = suggestions
		}(index, subSeed)
	}
	wg.Wait()

	if attempts > 0 && failures == attempts {
		return nil, fmt.Errorf("googlesuggest: all %d expansion requests failed", attempts)
	}

	// Merge in seed order so output is stable, dropping phrases already seen
	// from an earlier seed.
	seen := make(map[string]struct{})
	combined := make([]Suggestion, 0)
	for _, group := range results {
		for _, suggestion := range group {
			key := strings.ToLower(suggestion.Phrase)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			combined = append(combined, suggestion)
		}
	}
	return combined, nil
}

// fetch performs one upstream request and returns the normalized phrase list.
func (c *Client) fetch(ctx context.Context, seed, client string, opts Options) ([]string, error) {
	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		return nil, fmt.Errorf("googlesuggest: parse endpoint: %w", err)
	}
	params := endpoint.Query()
	params.Set("client", client)
	params.Set("q", seed)
	if opts.Language != "" {
		params.Set("hl", opts.Language)
	}
	if opts.Country != "" {
		params.Set("gl", opts.Country)
	}
	endpoint.RawQuery = params.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("googlesuggest: build request: %w", err)
	}
	request.Header.Set("User-Agent", userAgent)

	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("googlesuggest: request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("googlesuggest: status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("googlesuggest: read response: %w", err)
	}
	if int64(len(raw)) > c.maxResponseBytes {
		return nil, fmt.Errorf("googlesuggest: response exceeds %d bytes", c.maxResponseBytes)
	}
	return parsePhrases(raw)
}

// parsePhrases reads the completion envelope. Only element 1 matters; element 0
// varies by locale (sometimes an object), and elements 2+ are noise.
func parsePhrases(raw []byte) ([]string, error) {
	var envelope []json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("googlesuggest: response shape problem: %w", err)
	}
	if len(envelope) < 2 {
		return nil, fmt.Errorf("googlesuggest: response shape problem: expected at least 2 elements")
	}
	var phrases []string
	if err := json.Unmarshal(envelope[1], &phrases); err != nil {
		return nil, fmt.Errorf("googlesuggest: response shape problem: element 1 is not a string list: %w", err)
	}
	return normalizePhrases(phrases), nil
}

// normalizePhrases trims each phrase, drops empties, and dedupes
// case-insensitively while keeping the first spelling and order.
func normalizePhrases(phrases []string) []string {
	seen := make(map[string]struct{}, len(phrases))
	normalized := make([]string, 0, len(phrases))
	for _, phrase := range phrases {
		phrase = strings.TrimSpace(phrase)
		if phrase == "" {
			continue
		}
		key := strings.ToLower(phrase)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, phrase)
	}
	return normalized
}

// lookup returns a cached, non-expired result as a copy so callers cannot
// mutate the cache.
func (c *Client) lookup(key cacheKey) ([]Suggestion, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.cache[key]
	if !ok {
		return nil, false
	}
	if time.Since(entry.storedAt) > cacheTTL {
		delete(c.cache, key)
		return nil, false
	}
	return append([]Suggestion(nil), entry.phrases...), true
}

// store purges lazily: expired entries first, then the oldest entries until the
// cache is under the cap, so the map never grows without bound.
func (c *Client) store(key cacheKey, phrases []Suggestion) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for existing, entry := range c.cache {
		if now.Sub(entry.storedAt) > cacheTTL {
			delete(c.cache, existing)
		}
	}
	for len(c.cache) >= cacheMaxEntries {
		oldestKey, found := cacheKey{}, false
		var oldest time.Time
		for existing, entry := range c.cache {
			if !found || entry.storedAt.Before(oldest) {
				oldestKey, oldest, found = existing, entry.storedAt, true
			}
		}
		if !found {
			break
		}
		delete(c.cache, oldestKey)
	}
	c.cache[key] = cacheEntry{phrases: append([]Suggestion(nil), phrases...), storedAt: now}
}
