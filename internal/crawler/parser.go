package crawler

import (
	"bytes"
	"fmt"
	"net/url"
	"slices"
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
	URL                      string
	Title                    string
	MetaDescription          string
	CanonicalURL             string
	Lang                     string
	Viewport                 string
	Robots                   string
	VisibleText              string
	ImageCount               int
	ImagesWithoutAltCount    int
	ImagesWithoutDimensions  int
	OGTags                   map[string]string
	JSONLDBlocks             []string
	H1                       string
	H1Count                  int
	H2Headings               []string
	H3Headings               []string
	Links                    []ParsedLink
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

	imageCount, imagesWithoutAltCount, imagesWithoutDimensions := extractImageCounts(document)
	parsedPage := ParsedPage{
		URL:                     parsedPageURL.String(),
		Title:                   strings.TrimSpace(document.Find("title").First().Text()),
		MetaDescription:         strings.TrimSpace(document.Find(`meta[name="description"]`).First().AttrOr("content", "")),
		CanonicalURL:            strings.TrimSpace(document.Find(`link[rel="canonical"]`).First().AttrOr("href", "")),
		Lang:                    strings.TrimSpace(document.Find("html").First().AttrOr("lang", "")),
		Viewport:                strings.TrimSpace(document.Find(`meta[name="viewport"]`).First().AttrOr("content", "")),
		Robots:                  strings.TrimSpace(document.Find(`meta[name="robots"]`).First().AttrOr("content", "")),
		VisibleText:             extractVisibleText(document),
		ImageCount:              imageCount,
		ImagesWithoutAltCount:   imagesWithoutAltCount,
		ImagesWithoutDimensions: imagesWithoutDimensions,
		OGTags:                  extractOGTags(document),
		JSONLDBlocks:            extractJSONLDBlocks(document),
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

// extractOGTags returns Open Graph meta tags keyed by property name.
func extractOGTags(document *goquery.Document) map[string]string {
	ogTags := make(map[string]string)

	document.Find(`meta[property]`).Each(func(_ int, selection *goquery.Selection) {
		propertyName := strings.TrimSpace(selection.AttrOr("property", ""))
		if !strings.HasPrefix(strings.ToLower(propertyName), "og:") {
			return
		}

		contentValue := strings.TrimSpace(selection.AttrOr("content", ""))
		if contentValue == "" {
			return
		}

		ogTags[propertyName] = contentValue
	})

	if len(ogTags) == 0 {
		return nil
	}

	return ogTags
}

// extractJSONLDBlocks returns non-empty JSON-LD script contents from a document.
func extractJSONLDBlocks(document *goquery.Document) []string {
	var jsonLDBlocks []string

	document.Find(`script[type="application/ld+json"]`).Each(func(_ int, selection *goquery.Selection) {
		jsonLDBlock := strings.TrimSpace(selection.Text())
		if jsonLDBlock == "" {
			return
		}

		jsonLDBlocks = append(jsonLDBlocks, jsonLDBlock)
	})

	return jsonLDBlocks
}

// extractImageCounts returns basic image counts from a document.
func extractImageCounts(document *goquery.Document) (int, int, int) {
	imageCount := 0
	imagesWithoutAltCount := 0
	imagesWithoutDimensions := 0

	document.Find("img").Each(func(_ int, selection *goquery.Selection) {
		imageCount++

		altValue := strings.TrimSpace(selection.AttrOr("alt", ""))
		if altValue == "" {
			imagesWithoutAltCount++
		}

		widthValue := strings.TrimSpace(selection.AttrOr("width", ""))
		heightValue := strings.TrimSpace(selection.AttrOr("height", ""))
		if widthValue == "" || heightValue == "" {
			imagesWithoutDimensions++
		}
	})

	return imageCount, imagesWithoutAltCount, imagesWithoutDimensions
}

// extractVisibleText returns normalized body text with obvious non-content nodes removed.
func extractVisibleText(document *goquery.Document) string {
	bodySelection := document.Find("body").First()
	if bodySelection.Length() == 0 {
		return ""
	}

	bodyClone := bodySelection.Clone()
	bodyClone.Find("script, style, noscript").Remove()

	return normalizeWhitespace(bodyClone.Text())
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
