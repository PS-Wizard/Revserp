package crawler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/url"
	"regexp"
	"strings"
)

// softNotFoundMaxWordCount bounds the phrase heuristic to thin pages. Without
// it, an article legitimately titled "How to fix 404 errors" would be flagged
// as a soft 404.
const softNotFoundMaxWordCount = 120

// softNotFoundPhrase matches the wording a not-found page uses in its title or
// H1. It is only ever a secondary signal: the probe fingerprint is primary.
var softNotFoundPhrase = regexp.MustCompile(`(?i)(\b404\b|page not found|not found|no longer (exists|available)|does ?n['o]?t exist|never existed|page (you are|you're|being) looking for)`)

// SoftNotFoundFingerprint describes a site's soft-404 response: the body an
// origin serves for a URL that does not exist, while answering 2xx instead of
// 404. Real pages matching it are soft 404s.
type SoftNotFoundFingerprint struct {
	ProbeURL    string
	Title       string
	H1          string
	ContentHash string
}

// DetectSoftNotFound requests a URL under rootURL that cannot exist. A correct
// origin answers 404 and this returns nil — nothing to fingerprint, so no page
// can be misflagged. An origin that answers 2xx has a soft-404 handler, and the
// returned fingerprint identifies it.
//
// A probe that fails to fetch or parse returns nil: soft-404 detection is a
// best-effort enrichment and must never fail a crawl.
func DetectSoftNotFound(ctx context.Context, fetcher *Fetcher, parser *Parser, rootURL string) *SoftNotFoundFingerprint {
	probeURL, err := buildSoftNotFoundProbeURL(rootURL)
	if err != nil {
		return nil
	}

	result := fetcher.Fetch(ctx, probeURL)
	if result.FetchError != nil {
		log.Printf("soft-404 probe failed: url=%q error=%v", probeURL, result.FetchError)
		return nil
	}
	if result.StatusCode < 200 || result.StatusCode > 299 {
		log.Printf("soft-404 probe: origin correctly returned %d for a nonexistent url", result.StatusCode)
		return nil
	}
	if !strings.Contains(strings.ToLower(result.ContentType), "text/html") {
		return nil
	}

	parsedProbe, err := parser.ParseHTML(result.FinalURL, result.ContentType, result.Body)
	if err != nil {
		log.Printf("soft-404 probe parse failed: url=%q error=%v", probeURL, err)
		return nil
	}

	// The probe body must actually read as a not-found page. Some sites answer
	// any unknown URL with their homepage; fingerprinting that would match the
	// real homepage and every page resembling it, so the fingerprint is worse
	// than useless. (That catch-all behavior is its own SEO problem, but it is a
	// different one and is not detected here.)
	if !titleOrH1SaysNotFound(&parsedProbe) {
		log.Printf("soft-404 probe: origin answered %d for a nonexistent url, but the body is not a not-found page (likely a catch-all); skipping fingerprint", result.StatusCode)
		return nil
	}

	fingerprint := &SoftNotFoundFingerprint{
		ProbeURL:    probeURL,
		Title:       normalizeSoftNotFoundText(parsedProbe.Title),
		H1:          normalizeSoftNotFoundText(parsedProbe.H1),
		ContentHash: hashVisibleText(parsedProbe.VisibleText),
	}

	log.Printf("soft-404 handler detected: probe=%q status=%d title=%q h1=%q",
		probeURL, result.StatusCode, fingerprint.Title, fingerprint.H1)
	return fingerprint
}

// Matches reports whether one parsed page is this site's soft-404 response.
// Title and H1 must both agree, or the visible text must be byte-identical;
// either alone is too weak on templated sites.
func (fingerprint *SoftNotFoundFingerprint) Matches(parsedPage *ParsedPage) bool {
	if fingerprint == nil || parsedPage == nil {
		return false
	}

	if fingerprint.ContentHash != "" && fingerprint.ContentHash == hashVisibleText(parsedPage.VisibleText) {
		return true
	}

	title := normalizeSoftNotFoundText(parsedPage.Title)
	h1 := normalizeSoftNotFoundText(parsedPage.H1)
	if fingerprint.Title == "" || fingerprint.H1 == "" {
		return false
	}
	return fingerprint.Title == title && fingerprint.H1 == h1
}

// LooksLikeSoftNotFound is the probe-independent fallback: not-found wording in
// the title or H1 on a page too thin to be real content. It catches soft 404s
// whose body varies per URL, which the fingerprint cannot match.
//
// The word-count bound is what keeps an article legitimately titled "How to fix
// 404 errors" from being flagged. A 404 template whose message appears only in
// body text, with a generic title and no H1, is not detected.
func LooksLikeSoftNotFound(parsedPage *ParsedPage) bool {
	if parsedPage == nil {
		return false
	}
	if countWords(parsedPage.VisibleText) > softNotFoundMaxWordCount {
		return false
	}
	return titleOrH1SaysNotFound(parsedPage)
}

func titleOrH1SaysNotFound(parsedPage *ParsedPage) bool {
	if parsedPage == nil {
		return false
	}
	return softNotFoundPhrase.MatchString(parsedPage.Title) || softNotFoundPhrase.MatchString(parsedPage.H1)
}

// buildSoftNotFoundProbeURL builds a URL under the crawl root that cannot
// plausibly exist. The random suffix keeps a cached or pre-seeded path from
// answering the probe.
func buildSoftNotFoundProbeURL(rootURL string) (string, error) {
	parsedRoot, err := url.Parse(strings.TrimSpace(rootURL))
	if err != nil {
		return "", err
	}

	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}

	parsedRoot.Path = "/revserp-not-found-probe-" + hex.EncodeToString(token)
	parsedRoot.RawQuery = ""
	parsedRoot.Fragment = ""
	return parsedRoot.String(), nil
}

func normalizeSoftNotFoundText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

func hashVisibleText(value string) string {
	normalized := normalizeSoftNotFoundText(value)
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}
