package crawler

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"io"
	"net/url"
	"strings"
)

const (
	// maxSitemapDocuments caps how many sitemap files (index + children) we fetch per crawl.
	maxSitemapDocuments = 100
	// maxSitemapURLs caps how many page URLs we seed from sitemaps regardless of MaxPages.
	maxSitemapURLs = 50000
)

// sitemapDocument captures both sitemap-index (<sitemap>) and url-set (<url>) entries.
// encoding/xml matches by local element name, so XML namespaces are ignored.
type sitemapDocument struct {
	Sitemaps []sitemapLoc `xml:"sitemap"`
	URLs     []sitemapLoc `xml:"url"`
}

type sitemapLoc struct {
	Loc string `xml:"loc"`
}

// DiscoverSitemapURLs fetches robots.txt and /sitemap.xml, recursively expands any
// nested sitemap-index files, and returns same-host page URLs to seed the crawl.
//
// It is best-effort: any fetch/parse failure is skipped, and an empty result means
// the caller should fall back to plain link-following BFS from the root.
func DiscoverSitemapURLs(ctx context.Context, fetcher *Fetcher, rootURL *url.URL, allowedHost string, limit int) []string {
	if fetcher == nil || rootURL == nil {
		return nil
	}

	pending := candidateSitemapURLs(ctx, fetcher, rootURL)
	visitedDocs := make(map[string]struct{}, len(pending))
	seenURLs := make(map[string]struct{})
	var discovered []string

	for len(pending) > 0 && len(visitedDocs) < maxSitemapDocuments {
		sitemapURL := pending[0]
		pending = pending[1:]
		if _, seen := visitedDocs[sitemapURL]; seen {
			continue
		}
		visitedDocs[sitemapURL] = struct{}{}

		document, ok := fetchSitemapDocument(ctx, fetcher, sitemapURL)
		if !ok {
			continue
		}

		// Sitemap-index: enqueue child sitemaps.
		for _, entry := range document.Sitemaps {
			childURL := strings.TrimSpace(entry.Loc)
			if childURL != "" {
				pending = append(pending, childURL)
			}
		}

		// URL-set: collect same-host page URLs.
		for _, entry := range document.URLs {
			normalized, err := NormalizeURL(strings.TrimSpace(entry.Loc), nil)
			if err != nil {
				continue
			}
			if allowedHost != "" && !IsAllowedHost(allowedHost, normalized.Hostname()) {
				continue
			}
			normalizedURL := normalized.String()
			if _, exists := seenURLs[normalizedURL]; exists {
				continue
			}
			seenURLs[normalizedURL] = struct{}{}
			discovered = append(discovered, normalizedURL)

			if len(discovered) >= maxSitemapURLs || (limit > 0 && len(discovered) >= limit) {
				return discovered
			}
		}
	}

	return discovered
}

// candidateSitemapURLs returns sitemap URLs declared in robots.txt plus the conventional /sitemap.xml.
func candidateSitemapURLs(ctx context.Context, fetcher *Fetcher, rootURL *url.URL) []string {
	candidates := make([]string, 0, 4)
	seen := make(map[string]struct{})
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		if _, exists := seen[candidate]; exists {
			return
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}

	for _, declared := range sitemapsFromRobots(ctx, fetcher, rootURL) {
		add(declared)
	}
	add(rootURL.Scheme + "://" + rootURL.Host + "/sitemap.xml")

	return candidates
}

// sitemapsFromRobots reads robots.txt and returns any declared Sitemap: URLs.
func sitemapsFromRobots(ctx context.Context, fetcher *Fetcher, rootURL *url.URL) []string {
	robotsURL := rootURL.Scheme + "://" + rootURL.Host + "/robots.txt"
	result := fetcher.Fetch(ctx, robotsURL)
	if result.FetchError != nil || result.StatusCode < 200 || result.StatusCode >= 300 {
		return nil
	}

	var sitemaps []string
	for _, line := range strings.Split(string(result.Body), "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < len("sitemap:") || !strings.EqualFold(trimmed[:len("sitemap:")], "sitemap:") {
			continue
		}
		if value := strings.TrimSpace(trimmed[len("sitemap:"):]); value != "" {
			sitemaps = append(sitemaps, value)
		}
	}
	return sitemaps
}

// fetchSitemapDocument fetches and parses one sitemap URL, transparently gunzipping .xml.gz payloads.
func fetchSitemapDocument(ctx context.Context, fetcher *Fetcher, sitemapURL string) (sitemapDocument, bool) {
	result := fetcher.Fetch(ctx, sitemapURL)
	if result.FetchError != nil || result.StatusCode < 200 || result.StatusCode >= 300 || len(result.Body) == 0 {
		return sitemapDocument{}, false
	}

	body := result.Body
	// .xml.gz sitemaps are served as a gzip payload (not transfer-encoding), so gunzip by magic bytes.
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return sitemapDocument{}, false
		}
		defer reader.Close()
		decompressed, err := io.ReadAll(reader)
		if err != nil {
			return sitemapDocument{}, false
		}
		body = decompressed
	}

	var document sitemapDocument
	if err := xml.Unmarshal(body, &document); err != nil {
		return sitemapDocument{}, false
	}
	return document, true
}
