package crawler

import (
	"slices"
	"bytes"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// ParsedLink holds one extracted anchor from a page.
type ParsedLink struct {
	TargetURL   string
	AnchorText  string
	IsInternal  bool
	NoFollow    bool
}

// ParsedPage holds the basic extracted facts from one HTML page.
type ParsedPage struct {
	URL             string
	Title           string
	MetaDescription string
	CanonicalURL    string
	Lang            string
	Robots          string
	H1              string
	H1Count         int
	H2Headings      []string
	H3Headings      []string
	Links           []ParsedLink
}

// Parser extracts basic SEO facts and links from HTML documents.
type Parser struct{}

// NewParser builds a plain HTML parser.
func NewParser() *Parser {
	return &Parser{}
}

// ParseHTML extracts page facts and links from one fetched HTML response.
func (parser *Parser) ParseHTML(pageURL string, contentType string, body []byte) (ParsedPage, error) {
	if !strings.Contains(strings.ToLower(contentType), "text/html") {
		return ParsedPage{}, fmt.Errorf("unsupported content type: %s", contentType)
	}

	parsedPageURL, err := NormalizeURL(pageURL, nil)
	if err != nil {
		return ParsedPage{}, fmt.Errorf("normalize page url: %w", err)
	}

	document, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return ParsedPage{}, fmt.Errorf("parse html: %w", err)
	}

	parsedPage := ParsedPage{
		URL:             parsedPageURL.String(),
		Title:           strings.TrimSpace(document.Find("title").First().Text()),
		MetaDescription: strings.TrimSpace(document.Find(`meta[name="description"]`).First().AttrOr("content", "")),
		CanonicalURL:    strings.TrimSpace(document.Find(`link[rel="canonical"]`).First().AttrOr("href", "")),
		Lang:            strings.TrimSpace(document.Find("html").First().AttrOr("lang", "")),
		Robots:          strings.TrimSpace(document.Find(`meta[name="robots"]`).First().AttrOr("content", "")),
	}

	parsedPage.H1Count = document.Find("h1").Length()
	parsedPage.H1 = extractFirstHeadingText(document, "h1")
	parsedPage.H2Headings = extractHeadingTexts(document, "h2")
	parsedPage.H3Headings = extractHeadingTexts(document, "h3")
	parsedPage.Links = extractLinks(document, parsedPageURL)

	if parsedPage.CanonicalURL != "" {
		normalizedCanonicalURL, canonicalErr := NormalizeURL(parsedPage.CanonicalURL, parsedPageURL)
		if canonicalErr == nil {
			parsedPage.CanonicalURL = normalizedCanonicalURL.String()
		}
	}

	return parsedPage, nil
}

// extractFirstHeadingText returns the first non-empty heading text for a selector.
func extractFirstHeadingText(document *goquery.Document, selector string) string {
	for _, headingText := range extractHeadingTexts(document, selector) {
		if headingText != "" {
			return headingText
		}
	}

	return ""
}

// extractHeadingTexts returns normalized heading text values for a selector.
func extractHeadingTexts(document *goquery.Document, selector string) []string {
	var headingTexts []string

	document.Find(selector).Each(func(_ int, selection *goquery.Selection) {
		headingText := normalizeWhitespace(selection.Text())
		if headingText == "" {
			return
		}

		headingTexts = append(headingTexts, headingText)
	})

	return headingTexts
}

// extractLinks returns normalized anchor links from a document.
func extractLinks(document *goquery.Document, pageURL *url.URL) []ParsedLink {
	var parsedLinks []ParsedLink

	document.Find("a[href]").Each(func(_ int, selection *goquery.Selection) {
		rawTargetURL, exists := selection.Attr("href")
		if !exists {
			return
		}

		normalizedTargetURL, err := NormalizeURL(rawTargetURL, pageURL)
		if err != nil {
			return
		}

		relValue, _ := selection.Attr("rel")
		parsedLinks = append(parsedLinks, ParsedLink{
			TargetURL:  normalizedTargetURL.String(),
			AnchorText: normalizeWhitespace(selection.Text()),
			IsInternal: IsInternalURL(pageURL, normalizedTargetURL),
			NoFollow:   hasNoFollow(relValue),
		})
	})

	return parsedLinks
}

// hasNoFollow reports whether a rel attribute contains nofollow.
func hasNoFollow(relValue string) bool {
	return slices.Contains(strings.Fields(strings.ToLower(relValue)), "nofollow")
}

// normalizeWhitespace collapses repeated whitespace into single spaces.
func normalizeWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}
