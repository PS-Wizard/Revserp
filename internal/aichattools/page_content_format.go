package aichattools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type rawContentBlock struct {
	Tag  string  `json:"tag"`
	Text string  `json:"text"`
	HTML *string `json:"html"`
}

type pageContentBlock struct {
	Type     string   `json:"type"`
	Level    int      `json:"level,omitempty"`
	Markdown string   `json:"markdown,omitempty"`
	Items    []string `json:"items,omitempty"`
	Alt      string   `json:"alt,omitempty"`
	URL      string   `json:"url,omitempty"`
	Text     string   `json:"text,omitempty"`
}

func escapeMarkdownText(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch r {
		case '\\', '`', '*', '_', '[', ']', '{', '}', '#', '+', '.', '!', '|', '>', '~', '-':
			sb.WriteRune('\\')
			sb.WriteRune(r)
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func textToMarkdown(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = escapeMarkdownText(s)
	s = strings.ReplaceAll(s, "\n", "\\\n")
	return s
}

func trimMarkdownBlock(s string) string {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	for strings.HasPrefix(s, "\\\n") {
		s = strings.TrimLeftFunc(strings.TrimPrefix(s, "\\\n"), unicode.IsSpace)
	}
	for {
		s = strings.TrimRightFunc(s, unicode.IsSpace)
		if strings.HasSuffix(s, "\\") && !strings.HasSuffix(s, "\\\\") {
			s = strings.TrimSuffix(s, "\\")
			continue
		}
		return s
	}
}

func escapeMarkdownUrl(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r == '\\' || r == '(' || r == ')' {
			sb.WriteRune('\\')
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func safeUrl(raw, pageURL string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var u *url.URL
	var err error
	if pageURL != "" {
		base, err2 := url.Parse(pageURL)
		if err2 == nil && base.Scheme != "" {
			u, err = base.Parse(raw)
		} else {
			u, err = url.Parse(raw)
		}
	} else {
		u, err = url.Parse(raw)
	}
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "mailto", "tel":
		return u.String()
	default:
		return ""
	}
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func getImageUrl(n *html.Node, pageURL string) string {
	for _, attr := range []string{"data-src", "data-lazy-src", "data-original", "src"} {
		v := getAttr(n, attr)
		if v == "" {
			continue
		}
		if u := safeUrl(v, pageURL); u != "" {
			return u
		}
	}
	return ""
}

func collectText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(collectText(c))
	}
	return sb.String()
}

func inlineCode(value string) string {
	longest := 0
	cur := 0
	for _, r := range value {
		if r == '`' {
			cur++
			if cur > longest {
				longest = cur
			}
		} else {
			cur = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	padded := value
	if strings.HasPrefix(value, " ") || strings.HasSuffix(value, " ") || strings.HasPrefix(value, "`") || strings.HasSuffix(value, "`") {
		padded = " " + value + " "
	}
	return fence + padded + fence
}

func inlineChildren(n *html.Node, pageURL string) string {
	if n.Type == html.TextNode {
		return textToMarkdown(n.Data)
	}
	if n.Type != html.ElementNode {
		return ""
	}
	tag := strings.ToLower(n.Data)
	var childSB strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		childSB.WriteString(inlineChildren(c, pageURL))
	}
	children := childSB.String()
	switch tag {
	case "strong", "b":
		if children == "" {
			return ""
		}
		return "**" + children + "**"
	case "em", "i":
		if children == "" {
			return ""
		}
		return "*" + children + "*"
	case "s", "del", "strike":
		if children == "" {
			return ""
		}
		return "~~" + children + "~~"
	case "code":
		return inlineCode(collectText(n))
	case "a":
		href := getAttr(n, "href")
		u := ""
		if href != "" {
			u = safeUrl(href, pageURL)
		}
		if u != "" && children != "" {
			return "[" + children + "](" + escapeMarkdownUrl(u) + ")"
		}
		return children
	case "img":
		imgURL := getImageUrl(n, pageURL)
		alt := textToMarkdown(getAttr(n, "alt"))
		if imgURL != "" {
			return "![" + alt + "](" + escapeMarkdownUrl(imgURL) + ")"
		}
		return alt
	case "br":
		return "\\\n"
	default:
		return children
	}
}

func renderInlineHTML(inner, tag, pageURL string) (string, bool) {
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return "", false
	}
	ctx := &html.Node{Type: html.ElementNode, Data: tag, DataAtom: atom.Lookup([]byte(tag))}
	nodes, err := html.ParseFragment(strings.NewReader(inner), ctx)
	if err != nil {
		return "", false
	}
	if len(nodes) == 0 {
		return "", false
	}
	var sb strings.Builder
	for _, n := range nodes {
		sb.WriteString(inlineChildren(n, pageURL))
	}
	md := trimMarkdownBlock(sb.String())
	return md, true
}

func findFirstElement(nodes []*html.Node, tag string) *html.Node {
	tag = strings.ToLower(tag)
	for _, n := range nodes {
		if n.Type == html.ElementNode && strings.ToLower(n.Data) == tag {
			return n
		}
		if found := findFirstElementRec(n, tag); found != nil {
			return found
		}
	}
	return nil
}

func findFirstElementRec(n *html.Node, tag string) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && strings.ToLower(c.Data) == tag {
			return c
		}
		if found := findFirstElementRec(c, tag); found != nil {
			return found
		}
	}
	return nil
}

func renderListItem(li *html.Node, pageURL string) string {
	var contentSB strings.Builder
	var nestedParts []string
	for c := li.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			t := strings.ToLower(c.Data)
			if t == "ul" || t == "ol" {
				nestedMD := nestedListToMarkdown(c, pageURL)
				if nestedMD != "" {
					lines := strings.Split(nestedMD, "\n")
					for i, line := range lines {
						lines[i] = "    " + line
					}
					nestedParts = append(nestedParts, strings.Join(lines, "\n"))
				}
				continue
			}
		}
		contentSB.WriteString(inlineChildren(c, pageURL))
	}
	content := trimMarkdownBlock(contentSB.String())
	content = strings.ReplaceAll(content, "\n", "\n  ")
	if len(nestedParts) > 0 {
		joined := strings.Join(nestedParts, "\n")
		if content != "" {
			return content + "\n" + joined
		}
		return joined
	}
	return content
}

func nestedListToMarkdown(listNode *html.Node, pageURL string) string {
	ordered := strings.ToLower(listNode.Data) == "ol"
	var items []string
	idx := 0
	for c := listNode.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || strings.ToLower(c.Data) != "li" {
			continue
		}
		idx++
		content := renderListItem(c, pageURL)
		if content == "" {
			continue
		}
		marker := "-"
		if ordered {
			marker = fmt.Sprintf("%d.", idx)
		}
		items = append(items, marker+" "+content)
	}
	return strings.Join(items, "\n")
}

func renderListBlock(inner, tag, pageURL string) ([]string, bool) {
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return nil, false
	}
	ctx := &html.Node{Type: html.ElementNode, Data: tag, DataAtom: atom.Lookup([]byte(tag))}
	nodes, err := html.ParseFragment(strings.NewReader(inner), ctx)
	if err != nil {
		return nil, false
	}
	var liNodes []*html.Node
	for _, n := range nodes {
		if n.Type == html.ElementNode && strings.ToLower(n.Data) == "li" {
			liNodes = append(liNodes, n)
		} else if n.Type == html.ElementNode {
			var collect func(*html.Node)
			collect = func(node *html.Node) {
				for c := node.FirstChild; c != nil; c = c.NextSibling {
					if c.Type == html.ElementNode && strings.ToLower(c.Data) == "li" {
						liNodes = append(liNodes, c)
					} else {
						collect(c)
					}
				}
			}
			collect(n)
		}
	}
	if len(liNodes) == 0 {
		return nil, false
	}
	var items []string
	for _, li := range liNodes {
		md := renderListItem(li, pageURL)
		if md == "" {
			continue
		}
		items = append(items, md)
	}
	if len(items) == 0 {
		return nil, false
	}
	return items, true
}

func formatPageContentBlocks(raw []byte, pageURL string) ([]pageContentBlock, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []pageContentBlock{}, nil
	}
	var raws []rawContentBlock
	if err := json.Unmarshal(trimmed, &raws); err != nil {
		return nil, err
	}
	if raws == nil {
		return []pageContentBlock{}, nil
	}
	out := make([]pageContentBlock, 0, len(raws))
	for _, b := range raws {
		tag := strings.ToLower(strings.TrimSpace(b.Tag))
		switch tag {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			level := int(tag[1] - '0')
			var md string
			var ok bool
			if b.HTML != nil && strings.TrimSpace(*b.HTML) != "" {
				md, ok = renderInlineHTML(*b.HTML, tag, pageURL)
				if !ok {
					md = trimMarkdownBlock(textToMarkdown(b.Text))
				}
			} else {
				md = trimMarkdownBlock(textToMarkdown(b.Text))
			}
			if md == "" {
				continue
			}
			out = append(out, pageContentBlock{Type: "heading", Level: level, Markdown: md})
		case "p":
			var md string
			var ok bool
			if b.HTML != nil && strings.TrimSpace(*b.HTML) != "" {
				md, ok = renderInlineHTML(*b.HTML, tag, pageURL)
				if !ok {
					md = trimMarkdownBlock(textToMarkdown(b.Text))
				}
			} else {
				md = trimMarkdownBlock(textToMarkdown(b.Text))
			}
			if md == "" {
				continue
			}
			out = append(out, pageContentBlock{Type: "paragraph", Markdown: md})
		case "blockquote":
			var md string
			var ok bool
			if b.HTML != nil && strings.TrimSpace(*b.HTML) != "" {
				md, ok = renderInlineHTML(*b.HTML, tag, pageURL)
				if !ok {
					md = trimMarkdownBlock(textToMarkdown(b.Text))
				}
			} else {
				md = trimMarkdownBlock(textToMarkdown(b.Text))
			}
			if md == "" {
				continue
			}
			out = append(out, pageContentBlock{Type: "blockquote", Markdown: md})
		case "ul", "ol":
			var items []string
			var ok bool
			if b.HTML != nil && strings.TrimSpace(*b.HTML) != "" {
				items, ok = renderListBlock(*b.HTML, tag, pageURL)
				if !ok {
					fallback := trimMarkdownBlock(textToMarkdown(b.Text))
					if fallback == "" {
						continue
					}
					items = []string{fallback}
				}
			} else {
				fallback := trimMarkdownBlock(textToMarkdown(b.Text))
				if fallback == "" {
					continue
				}
				items = []string{fallback}
			}
			if len(items) == 0 {
				continue
			}
			typ := "unordered_list"
			if tag == "ol" {
				typ = "ordered_list"
			}
			out = append(out, pageContentBlock{Type: typ, Items: items})
		case "img":
			var alt, u string
			if b.HTML != nil && strings.TrimSpace(*b.HTML) != "" {
				ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
				nodes, err := html.ParseFragment(strings.NewReader(*b.HTML), ctx)
				var imgNode *html.Node
				if err == nil {
					imgNode = findFirstElement(nodes, "img")
				}
				if imgNode != nil {
					alt = strings.TrimSpace(getAttr(imgNode, "alt"))
					u = getImageUrl(imgNode, pageURL)
				} else {
					alt = strings.TrimSpace(b.Text)
				}
			} else {
				alt = strings.TrimSpace(b.Text)
			}
			if strings.TrimSpace(alt) == "" && strings.TrimSpace(u) == "" {
				continue
			}
			block := pageContentBlock{Type: "image"}
			if strings.TrimSpace(alt) != "" {
				block.Alt = alt
			}
			if strings.TrimSpace(u) != "" {
				block.URL = u
			}
			if block.Alt == "" && block.URL == "" {
				continue
			}
			out = append(out, block)
		case "pre":
			var txt string
			if b.HTML != nil && strings.TrimSpace(*b.HTML) != "" {
				ctx := &html.Node{Type: html.ElementNode, Data: "pre", DataAtom: atom.Pre}
				nodes, err := html.ParseFragment(strings.NewReader(*b.HTML), ctx)
				if err == nil && len(nodes) > 0 {
					var sb strings.Builder
					for _, n := range nodes {
						sb.WriteString(collectText(n))
					}
					txt = sb.String()
					txt = strings.ReplaceAll(strings.ReplaceAll(txt, "\r\n", "\n"), "\r", "\n")
					if strings.TrimSpace(txt) == "" {
						txt = b.Text
					}
				} else {
					txt = b.Text
				}
			} else {
				txt = b.Text
			}
			if strings.TrimSpace(txt) == "" {
				continue
			}
			out = append(out, pageContentBlock{Type: "code", Text: txt})
		default:
			continue
		}
	}
	if out == nil {
		out = []pageContentBlock{}
	}
	return out, nil
}
