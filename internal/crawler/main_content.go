package crawler

import (
	"bytes"
	"net/url"
	"strings"

	"github.com/go-shiori/dom"
	trafilatura "github.com/markusmobius/go-trafilatura/v2"
	"golang.org/x/net/html"
)

// MainContentExtractorVersion identifies the engine that produced the stored
// visible text and content blocks. The persistence layer reuses baseline rows
// on 304 only when the extractor version matches and the status is ok,
// otherwise a stale legacy extraction would be copied forward forever.
const MainContentExtractorVersion = "go-trafilatura/v2@2.2.6"

// Extraction statuses recorded on ParsedPage. Only ok carries usable body
// blocks; empty means the engine found no main content and failed means the
// native call errored. There is no whole-body or legacy-parser fallback: a
// cache or extraction failure keeps SEO facts but stores no invented body.
const (
	ContentExtractionOK     = "ok"
	ContentExtractionEmpty  = "empty"
	ContentExtractionFailed = "failed"
)

// supportedContentTag reports whether a ContentNode tag maps directly to a
// persisted ParsedBlock tag.
func supportedContentTag(tag string) bool {
	switch tag {
	case "h1", "h2", "h3", "h4", "h5", "h6", "p", "ul", "ol", "blockquote", "pre", "table", "img":
		return true
	default:
		return false
	}
}

// extractMainContent runs one native Go-Trafilatura extraction and derives
// both blocks and visible text from the same returned ContentNode.
func extractMainContent(nativeDoc *html.Node, pageURL *url.URL) ([]ParsedBlock, string, string, string) {
	opts := trafilatura.Options{
		OriginalURL:     pageURL,
		Focus:           trafilatura.Balanced,
		ExcludeComments: true,
		ExcludeTables:   false,
		IncludeLinks:    true,
		IncludeImages:   true,
		EnableFallback:  true,
		HtmlDateMode:    trafilatura.Disabled,
	}
	result, err := trafilatura.ExtractDocument(nativeDoc, opts)
	if err != nil {
		return nil, "", ContentExtractionFailed, err.Error()
	}
	if result == nil || result.ContentNode == nil {
		return nil, "", ContentExtractionFailed, "trafilatura returned no result"
	}
	blocks := contentBlocksFromNode(result.ContentNode)
	if len(blocks) == 0 {
		return nil, "", ContentExtractionEmpty, ""
	}
	// Visible text is the native plain text of the same returned ContentNode
	// (upstream plainText(postBody)), so blocks and text share one extraction
	// with boundary semantics maintained upstream.
	return blocks, result.ContentText, ContentExtractionOK, ""
}

// contentBlocksFromNode converts one native ContentNode into persisted blocks.
func contentBlocksFromNode(contentNode *html.Node) []ParsedBlock {
	var blocks []ParsedBlock
	var walk func(node *html.Node)
	walk = func(node *html.Node) {
		var pendingText, pendingHTML strings.Builder
		flushText := func() {
			if pendingText.Len() == 0 {
				return
			}
			text := normalizeWhitespace(pendingText.String())
			pendingText.Reset()
			htmlStr := strings.TrimSpace(pendingHTML.String())
			pendingHTML.Reset()
			if text == "" {
				return
			}
			if htmlStr == "" {
				htmlStr = html.EscapeString(text)
			}
			blocks = append(blocks, ParsedBlock{Tag: "p", Text: text, Html: htmlStr})
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			// Direct text keeps editor-visible copy the native text already
			// has; renderer-escaped output cannot become code.
			if child.Type == html.TextNode {
				if strings.TrimSpace(child.Data) == "" {
					continue
				}
				pendingText.WriteString(child.Data)
				pendingHTML.WriteString(dom.OuterHTML(child))
				continue
			}
			if child.Type != html.ElementNode {
				continue
			}
			flushText()
			tag := strings.ToLower(dom.TagName(child))
			switch {
			case tag == "img":
				if block, ok := imageBlockFromNode(child); ok {
					blocks = append(blocks, block)
				}
			case tag == "code":
				// Native code arrives nested; pre keeps newlines and indentation.
				if block, ok := preBlockFromNode(child); ok {
					blocks = append(blocks, block)
				}
			case supportedContentTag(tag):
				if block, ok := textBlockFromNode(child, tag); ok {
					blocks = append(blocks, block)
				}
			case tag == "" || tag == "done":
				continue
			case tag == "br" || tag == "hr" || tag == "script" || tag == "style" || tag == "noscript" || tag == "template":
				continue
			default:
				// Unknown wrappers map to p when inline-only; containers with
				// block children unwrap so descendants are not duplicated.
				if hasBlockElementChild(child) {
					walk(child)
					continue
				}
				if block, ok := fallbackParagraphFromNode(child); ok {
					blocks = append(blocks, block)
				}
			}
		}
		flushText()
	}
	walk(contentNode)
	return blocks
}

// hasBlockElementChild reports whether a node wraps block-level content.
func hasBlockElementChild(node *html.Node) bool {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode {
			continue
		}
		tag := strings.ToLower(dom.TagName(child))
		if supportedContentTag(tag) || tag == "code" || tag == "div" || tag == "section" || tag == "article" || tag == "figure" || tag == "main" || tag == "li" || tag == "tr" || tag == "td" || tag == "th" {
			return true
		}
	}
	return false
}

// textBlockFromNode builds one block for a supported tag.
func textBlockFromNode(node *html.Node, tag string) (ParsedBlock, bool) {
	switch tag {
	case "pre":
		return preBlockFromNode(node)
	default:
		// dom.InnerText inserts boundary spaces (br becomes a break) while
		// dom.TextContent would join "one<br>two" into "onetwo".
		text := normalizeWhitespace(dom.InnerText(node))
		if text == "" {
			return ParsedBlock{}, false
		}
		return ParsedBlock{Tag: tag, Text: text, Html: strings.TrimSpace(dom.InnerHTML(node))}, true
	}
}

// preBlockFromNode persists native code as pre. Text stays untrimmed so
// first-line indentation survives; HTML serializes child by child because
// dom.InnerHTML trims spaces that bare code content needs.
func preBlockFromNode(node *html.Node) (ParsedBlock, bool) {
	raw := dom.TextContent(node)
	if strings.TrimSpace(raw) == "" {
		return ParsedBlock{}, false
	}
	return ParsedBlock{Tag: "pre", Text: raw, Html: renderInnerPreserveSpaces(node)}, true
}

// renderInnerPreserveSpaces serializes child nodes without trimming, keeping
// leading spaces that dom.InnerHTML would strip from bare code content.
func renderInnerPreserveSpaces(node *html.Node) string {
	var buf bytes.Buffer
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if err := html.Render(&buf, child); err != nil {
			return ""
		}
	}
	return buf.String()
}

// fallbackParagraphFromNode maps unknown inline wrappers to a valid p block.
func fallbackParagraphFromNode(node *html.Node) (ParsedBlock, bool) {
	text := normalizeWhitespace(dom.InnerText(node))
	if text == "" {
		return ParsedBlock{}, false
	}
	return ParsedBlock{Tag: "p", Text: text, Html: strings.TrimSpace(dom.InnerHTML(node))}, true
}

// imageBlockFromNode keeps the outer img HTML so the editor can render it.
func imageBlockFromNode(node *html.Node) (ParsedBlock, bool) {
	src := strings.TrimSpace(dom.GetAttribute(node, "src"))
	alt := normalizeWhitespace(dom.GetAttribute(node, "alt"))
	if src == "" && alt == "" {
		return ParsedBlock{}, false
	}
	text := alt
	if text == "" {
		text = src
	}
	outer := strings.TrimSpace(dom.OuterHTML(node))
	if outer == "" {
		outer = `<img src="` + src + `" alt="` + alt + `">`
	}
	return ParsedBlock{Tag: "img", Text: text, Html: outer}, true
}
