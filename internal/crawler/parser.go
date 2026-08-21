package crawler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/PuerkitoBio/goquery"
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
	URL                     string
	Title                   string
	MetaDescription         string
	Author                  string
	CanonicalURL            string
	Lang                    string
	Viewport                string
	Robots                  string
	VisibleText             string
	ContentBlocks           []ParsedBlock
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
	contentBlocks, visibleText := extractContentBlocks(document)
	parsedPage := ParsedPage{
		URL:                     parsedPageURL.String(),
		Title:                   strings.TrimSpace(document.Find("title").First().Text()),
		MetaDescription:         strings.TrimSpace(document.Find(`meta[name="description"]`).First().AttrOr("content", "")),
		CanonicalURL:            strings.TrimSpace(document.Find(`link[rel="canonical"]`).First().AttrOr("href", "")),
		Lang:                    strings.TrimSpace(document.Find("html").First().AttrOr("lang", "")),
		Viewport:                strings.TrimSpace(document.Find(`meta[name="viewport"]`).First().AttrOr("content", "")),
		Robots:                  strings.TrimSpace(document.Find(`meta[name="robots"]`).First().AttrOr("content", "")),
		VisibleText:             visibleText,
		ContentBlocks:           contentBlocks,
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
	parsedPage.HeadingOutline = extractHeadingOutline(document)
	parsedPage.Author = extractAuthor(document, parsedPage.JSONLDBlocks)
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

// extractContentBlocks returns structured main-content blocks and cleaned visible text.
// It picks the main container (main/article/[role=main] else body), removes
// boilerplate (header/nav/footer/aside and scripts), then walks block elements
// in document order. Visible text is the blocks joined with paragraph breaks.
func extractContentBlocks(document *goquery.Document) ([]ParsedBlock, string) {
	container := document.Find("main, article, [role=main]").First()
	if container.Length() == 0 {
		container = document.Find("body").First()
		if container.Length() == 0 {
			return nil, ""
		}
	}

	clone := container.Clone()
	clone.Find("header, nav, footer, aside, [role=navigation], [role=banner], [role=contentinfo], script, style, noscript, iframe, form").Remove()
	clone.Find(".cookie, .nav, .navbar, .footer, .sidebar, .cookie-banner, .site-header, .site-footer, .site-nav").Remove()

	var blocks []ParsedBlock
	var texts []string
	clone.Find("h1, h2, h3, h4, h5, h6, p, ul, ol, blockquote, img, pre").Each(func(_ int, selection *goquery.Selection) {
		tag := goquery.NodeName(selection)
		if tag == "img" {
			src := strings.TrimSpace(selection.AttrOr("src", ""))
			alt := normalizeWhitespace(selection.AttrOr("alt", ""))
			if src == "" && alt == "" {
				return
			}
			text := alt
			if text == "" {
				text = src
			}
			html, _ := goquery.OuterHtml(selection)
			if html == "" {
				html = "<img src=\"" + src + "\" alt=\"" + alt + "\">"
			}
			blocks = append(blocks, ParsedBlock{Tag: tag, Text: text, Html: strings.TrimSpace(html)})
			return
		}
		if tag == "ul" || tag == "ol" {
			var items []string
			selection.Find("li").Each(func(_ int, li *goquery.Selection) {
				ttt := normalizeWhitespace(li.Text())
				if ttt != "" {
					items = append(items, ttt)
				}
			})
			if len(items) == 0 {
				return
			}
			text := strings.Join(items, " ")
			html, _ := selection.Html()
			blocks = append(blocks, ParsedBlock{Tag: tag, Text: text, Html: strings.TrimSpace(html)})
			texts = append(texts, text)
			return
		}
		text := normalizeWhitespace(selection.Text())
		if text == "" {
			return
		}
		html, _ := selection.Html()
		blocks = append(blocks, ParsedBlock{Tag: tag, Text: text, Html: strings.TrimSpace(html)})
		texts = append(texts, text)
	})

	if len(blocks) == 0 {
		return nil, ""
	}

	return blocks, strings.Join(texts, "\n\n")
}

// extractVisibleText returns normalized visible text for backward compat.
// It delegates to extractContentBlocks and returns only the visible text.
func extractVisibleText(document *goquery.Document) string {
	_, visibleText := extractContentBlocks(document)
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
