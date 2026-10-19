package crawler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// ParsedLink holds one extracted anchor from a page.
type ParsedLink struct {
	TargetURL  string
	AnchorText string
	IsInternal bool
	NoFollow   bool
}

// ParsedHeading holds one extracted heading in document order.
type ParsedHeading struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// ParsedBlock holds one block of main content in document order.
type ParsedBlock struct {
	Tag  string `json:"tag"`
	Text string `json:"text"`
	Html string `json:"html,omitempty"`
}

// ParsedPage holds the basic extracted facts from one HTML page.
type ParsedPage struct {
	URL             string
	Title           string
	MetaDescription string
	Author          string
	CanonicalURL    string
	Lang            string
	Viewport        string
	Robots          string
	VisibleText     string
	ContentBlocks   []ParsedBlock
	// PageTextLength is the length of the whole body text with script, style and
	// noscript removed. The render decision uses it to tell an empty JavaScript
	// shell from a short but complete page. It is never stored.
	PageTextLength          int
	ImageCount              int
	ImagesWithoutAltCount   int
	ImagesWithoutDimensions int
	OGTags                  map[string]string
	JSONLDBlocks            []string
	H1                      string
	H1Count                 int
	H2Headings              []string
	H3Headings              []string
	HeadingOutline          []ParsedHeading
	Links                   []ParsedLink
	// ContentExtractor records which main-content engine produced VisibleText
	// and ContentBlocks. It always carries engine provenance when extraction
	// is attempted so the persistence layer can gate 304 reuse by version.
	ContentExtractor string
	// ContentExtractionStatus is ok only for usable body blocks, empty when
	// the engine found no main content, and failed when the native call
	// errored. There is no whole-body fallback: failures keep SEO facts but
	// store no invented body text.
	ContentExtractionStatus string
	// ContentExtractionError carries the native error for logging. It is not
	// persisted to the database.
	ContentExtractionError string
}

// Parser extracts basic SEO facts and links from HTML documents.
type Parser struct{}

// NewParser builds a plain HTML parser.
func NewParser() *Parser {
	return &Parser{}
}

// utf8Replacement substitutes one undecodable byte run in fetched HTML.
var utf8Replacement = []byte("\uFFFD")

// normalizeHTMLBytes decodes one fetched HTML body to UTF-8 following its
// declared charset, replacing any undecodable bytes with U+FFFD.
//
// A truncated multi-byte sequence (as served by nationallife.com.np, which
// declares UTF-8 but ends its meta description mid-sequence) would otherwise
// flow through goquery into crawl_pages/crawl_links and abort the insert
// with Postgres SQLSTATE 22021. Correctly encoded bodies are returned
// unchanged; the fetch response body itself is never mutated.
func normalizeHTMLBytes(body []byte, contentType string) []byte {
	if len(body) == 0 {
		return body
	}
	_, name, certain := charset.DetermineEncoding(body, contentType)
	// The charset sniffer samples only the first 1024 bytes. If those are all
	// ASCII, it can guess Windows-1252 for a valid UTF-8 page with no header.
	if utf8.Valid(body) && (name == "utf-8" || (name == "windows-1252" && !certain)) {
		return body
	}
	decodedReader, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return bytes.ToValidUTF8(body, utf8Replacement)
	}
	decoded, err := io.ReadAll(decodedReader)
	if err != nil {
		return bytes.ToValidUTF8(body, utf8Replacement)
	}
	if !utf8.Valid(decoded) {
		decoded = bytes.ToValidUTF8(decoded, utf8Replacement)
	}
	return decoded
}

// toValidUTF8String replaces any invalid UTF-8 in value with U+FFFD.
// It is the string-level companion to normalizeHTMLBytes for short
// already-parsed values (such as truncated error messages) that reach the store.
func toValidUTF8String(value string) string {
	if utf8.ValidString(value) {
		return value
	}

	return strings.ToValidUTF8(value, string(utf8.RuneError))
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

	body = normalizeHTMLBytes(body, contentType)

	// SEO facts keep the original goquery parse (scripting enabled), so
	// noscript contents stay invisible to image/link/heading selectors exactly
	// as before; only extraction output changed engines.
	document, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return ParsedPage{}, fmt.Errorf("parse html: %w", err)
	}

	imageCount, imagesWithoutAltCount, imagesWithoutDimensions := extractImageCounts(document)
	jsonLDBlocks := extractJSONLDBlocks(document)
	parsedPage := ParsedPage{
		URL:                     parsedPageURL.String(),
		Title:                   strings.TrimSpace(document.Find("title").First().Text()),
		MetaDescription:         strings.TrimSpace(document.Find(`meta[name="description"]`).First().AttrOr("content", "")),
		CanonicalURL:            strings.TrimSpace(document.Find(`link[rel="canonical"]`).First().AttrOr("href", "")),
		Lang:                    strings.TrimSpace(document.Find("html").First().AttrOr("lang", "")),
		Viewport:                strings.TrimSpace(document.Find(`meta[name="viewport"]`).First().AttrOr("content", "")),
		Robots:                  strings.TrimSpace(document.Find(`meta[name="robots"]`).First().AttrOr("content", "")),
		PageTextLength:          extractPageTextLength(document),
		ImageCount:              imageCount,
		ImagesWithoutAltCount:   imagesWithoutAltCount,
		ImagesWithoutDimensions: imagesWithoutDimensions,
		OGTags:                  extractOGTags(document),
		JSONLDBlocks:            jsonLDBlocks,
		H1Count:                 document.Find("h1").Length(),
		H1:                      extractFirstHeadingText(document, "h1"),
		H2Headings:              extractHeadingTexts(document, "h2"),
		H3Headings:              extractHeadingTexts(document, "h3"),
		HeadingOutline:          extractHeadingOutline(document),
		Author:                  extractAuthor(document, jsonLDBlocks),
		Links:                   extractLinks(document, parsedPageURL),
	}

	if parsedPage.CanonicalURL != "" {
		normalizedCanonicalURL, canonicalErr := NormalizeURL(parsedPage.CanonicalURL, parsedPageURL)
		if canonicalErr == nil {
			parsedPage.CanonicalURL = normalizedCanonicalURL.String()
		}
	}

	// Text and blocks come from one native extraction over its own parse.
	// Scripting stays disabled there to match upstream trafilatura, which
	// recovers fallback images from noscript markup.
	var contentBlocks []ParsedBlock
	var visibleText, status, extractionErr string
	nativeDoc, nativeErr := html.ParseWithOptions(bytes.NewReader(body), html.ParseOptionEnableScripting(false))
	if nativeErr != nil {
		status, extractionErr = ContentExtractionFailed, nativeErr.Error()
	} else {
		contentBlocks, visibleText, status, extractionErr = extractMainContent(nativeDoc, parsedPageURL)
	}
	parsedPage.VisibleText = visibleText
	parsedPage.ContentBlocks = contentBlocks
	parsedPage.ContentExtractor = MainContentExtractorVersion
	parsedPage.ContentExtractionStatus = status
	parsedPage.ContentExtractionError = extractionErr

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

// extractHeadingOutline returns normalized headings in document order.
func extractHeadingOutline(document *goquery.Document) []ParsedHeading {
	var headingOutline []ParsedHeading

	document.Find("h1, h2, h3, h4, h5, h6").Each(func(_ int, selection *goquery.Selection) {
		headingText := normalizeWhitespace(selection.Text())
		if headingText == "" {
			return
		}

		headingLevel := headingLevelFromSelection(selection)
		if headingLevel == 0 {
			return
		}

		headingOutline = append(headingOutline, ParsedHeading{
			Level: headingLevel,
			Text:  headingText,
		})
	})

	return headingOutline
}

// headingLevelFromSelection returns the numeric heading level for one heading element.
func headingLevelFromSelection(selection *goquery.Selection) int {
	switch goquery.NodeName(selection) {
	case "h1":
		return 1
	case "h2":
		return 2
	case "h3":
		return 3
	case "h4":
		return 4
	case "h5":
		return 5
	case "h6":
		return 6
	default:
		return 0
	}
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

// extractAuthor returns the strongest author signal available from metadata or JSON-LD.
func extractAuthor(document *goquery.Document, jsonLDBlocks []string) string {
	authorSignal := extractMetaAuthor(document)
	if authorSignal != "" {
		return authorSignal
	}

	return extractJSONLDAuthor(jsonLDBlocks)
}

// extractMetaAuthor returns a normalized author value from common metadata fields.
func extractMetaAuthor(document *goquery.Document) string {
	var authorSignal string

	document.Find(`meta[name], meta[property]`).EachWithBreak(func(_ int, selection *goquery.Selection) bool {
		metaName := strings.ToLower(strings.TrimSpace(selection.AttrOr("name", "")))
		metaProperty := strings.ToLower(strings.TrimSpace(selection.AttrOr("property", "")))
		if metaName != "author" && metaProperty != "article:author" {
			return true
		}

		authorSignal = normalizeWhitespace(selection.AttrOr("content", ""))
		return authorSignal == ""
	})

	return authorSignal
}

// extractJSONLDAuthor returns the first author name found in relevant JSON-LD blocks.
func extractJSONLDAuthor(jsonLDBlocks []string) string {
	for _, jsonLDBlock := range jsonLDBlocks {
		var parsedJSONLD any
		if err := json.Unmarshal([]byte(jsonLDBlock), &parsedJSONLD); err != nil {
			continue
		}

		if authorSignal := extractJSONLDAuthorFromValue(parsedJSONLD); authorSignal != "" {
			return authorSignal
		}
	}

	return ""
}

// extractJSONLDAuthorFromValue walks one JSON-LD value and returns the first author name it finds.
func extractJSONLDAuthorFromValue(value any) string {
	switch typedValue := value.(type) {
	case map[string]any:
		if graphEntries, ok := typedValue["@graph"].([]any); ok {
			for _, graphEntry := range graphEntries {
				if authorSignal := extractJSONLDAuthorFromValue(graphEntry); authorSignal != "" {
					return authorSignal
				}
			}
		}

		if !hasRelevantJSONLDType(typedValue) {
			return ""
		}

		return extractJSONLDAuthorName(typedValue["author"])
	case []any:
		for _, entry := range typedValue {
			if authorSignal := extractJSONLDAuthorFromValue(entry); authorSignal != "" {
				return authorSignal
			}
		}
	}

	return ""
}

// hasRelevantJSONLDType reports whether one JSON-LD node is article-like enough for author extraction.
func hasRelevantJSONLDType(jsonLDNode map[string]any) bool {
	switch nodeType := jsonLDNode["@type"].(type) {
	case string:
		return isRelevantJSONLDType(nodeType)
	case []any:
		for _, rawType := range nodeType {
			typeName, ok := rawType.(string)
			if ok && isRelevantJSONLDType(typeName) {
				return true
			}
		}
	}

	return false
}

// isRelevantJSONLDType reports whether a JSON-LD type commonly carries authorship signals.
func isRelevantJSONLDType(typeName string) bool {
	switch strings.TrimSpace(typeName) {
	case "Article", "BlogPosting", "NewsArticle", "TechArticle", "WebPage":
		return true
	default:
		return false
	}
}

// extractJSONLDAuthorName normalizes one JSON-LD author value into a plain string.
func extractJSONLDAuthorName(value any) string {
	switch typedValue := value.(type) {
	case string:
		return normalizeWhitespace(typedValue)
	case map[string]any:
		return normalizeWhitespace(stringValue(typedValue["name"]))
	case []any:
		for _, entry := range typedValue {
			if authorSignal := extractJSONLDAuthorName(entry); authorSignal != "" {
				return authorSignal
			}
		}
	}

	return ""
}

// stringValue returns one string value from a loosely typed JSON field.
func stringValue(value any) string {
	stringValue, ok := value.(string)
	if !ok {
		return ""
	}

	return stringValue
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

// extractPageTextLength returns the byte length of the whole body text with
// script, style and noscript removed. It is computed once per parse.
func extractPageTextLength(document *goquery.Document) int {
	body := document.Find("body").First()
	if body.Length() == 0 {
		return 0
	}
	clone := body.Clone()
	clone.Find("script, style, noscript").Remove()
	return len(normalizeWhitespace(clone.Text()))
}

// extractVisibleText returns visible text via the same native extraction.
func extractVisibleText(document *goquery.Document) string {
	if document == nil || len(document.Nodes) == 0 {
		return ""
	}
	raw, err := document.Html()
	if err != nil || raw == "" {
		return ""
	}
	nativeDoc, err := html.ParseWithOptions(bytes.NewReader([]byte(raw)), html.ParseOptionEnableScripting(false))
	if err != nil {
		return ""
	}
	_, visibleText, status, _ := extractMainContent(nativeDoc, nil)
	if status != ContentExtractionOK {
		return ""
	}
	return visibleText
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
