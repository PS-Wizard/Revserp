package crawler

import (
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/go-shiori/dom"
	"golang.org/x/net/html"
)

func extractArchiveListingBlocks(nativeDoc *html.Node, pageURL *url.URL) ([]ParsedBlock, string, bool) {
	mains := goquery.NewDocumentFromNode(nativeDoc).Find("main")
	if mains.Length() != 1 {
		return nil, "", false
	}
	main := mains.First()
	heading, ok := archivePageHeading(main)
	if !ok {
		return nil, "", false
	}
	cards, ok := archiveListingCards(main)
	if !ok {
		return nil, "", false
	}
	blocks := []ParsedBlock{{Tag: "h1", Text: heading, Html: html.EscapeString(heading)}}
	texts := []string{heading}
	recognized := true
	cards.Each(func(_ int, card *goquery.Selection) {
		if !recognized {
			return
		}
		title, link, tag, ok := archiveCardTitleLink(card, pageURL)
		if !ok {
			recognized = false
			return
		}
		headingHTML := html.EscapeString(title)
		if link != "" {
			headingHTML = `<a href="` + html.EscapeString(link) + `">` + html.EscapeString(title) + `</a>`
		}
		blocks = append(blocks, ParsedBlock{Tag: tag, Text: title, Html: headingHTML})
		texts = append(texts, title)
		for _, excerpt := range archiveCardExcerpts(card) {
			blocks = append(blocks, ParsedBlock{Tag: "p", Text: excerpt, Html: html.EscapeString(excerpt)})
			texts = append(texts, excerpt)
		}
	})
	if !recognized {
		return nil, "", false
	}
	return blocks, strings.Join(texts, "\n\n"), true
}

func archivePageHeading(main *goquery.Selection) (string, bool) {
	heading := main.Find("h1")
	if heading.Length() != 1 || heading.ParentsFiltered("article, nav, aside, footer, noscript, script, style, template").Length() > 0 {
		return "", false
	}
	text := archivePlainText(heading)
	return text, text != ""
}

func archiveListingCards(main *goquery.Selection) (*goquery.Selection, bool) {
	cards := main.Find("article")
	if cards.Length() < 2 {
		return nil, false
	}
	parent := cards.First().Parent().Get(0)
	qualified := true
	cards.Each(func(_ int, card *goquery.Selection) {
		if !qualified || card.Parent().Get(0) != parent {
			qualified = false
			return
		}
		if card.ParentsFiltered("article, nav, aside, header, footer, noscript, script, style, template").Length() > 0 {
			qualified = false
			return
		}
		if card.Find("article").Length() > 0 {
			qualified = false
		}
	})
	if !qualified || archiveOutsideProse(main) {
		return nil, false
	}
	return cards, true
}

func archiveOutsideProse(main *goquery.Selection) bool {
	prose := false
	main.Find("p, pre, ul, ol, dl, table, blockquote, h2, h3, h4, h5, h6").Each(func(_ int, sel *goquery.Selection) {
		if prose || sel.ParentsFiltered("article, nav, aside, header, footer, noscript, script, style, template").Length() > 0 {
			return
		}
		if archivePlainText(sel) != "" {
			prose = true
		}
	})
	return prose
}

func archiveCardTitleLink(card *goquery.Selection, pageURL *url.URL) (string, string, string, bool) {
	title := ""
	link := ""
	tag := ""
	card.Find("h1, h2, h3, h4, h5, h6").EachWithBreak(func(_ int, h *goquery.Selection) bool {
		anchor := h.Find("a[href]").First()
		if anchor.Length() == 0 {
			return true
		}
		href := strings.TrimSpace(anchor.AttrOr("href", ""))
		text := archivePlainText(anchor)
		if href == "" || text == "" {
			return true
		}
		title = text
		tag = goquery.NodeName(h)
		if target, err := NormalizeURL(href, pageURL); err == nil {
			link = target.String()
		}
		return false
	})
	if title == "" {
		return "", "", "", false
	}
	return title, link, tag, true
}

func archivePlainText(sel *goquery.Selection) string {
	clone := sel.Clone()
	clone.Find("script, style, noscript, template").Remove()
	return normalizeWhitespace(dom.InnerText(clone.Get(0)))
}

func archiveCardExcerpts(card *goquery.Selection) []string {
	var excerpts []string
	card.Find("p").Each(func(_ int, sel *goquery.Selection) {
		if sel.ParentsFiltered("noscript, script, style, template").Length() > 0 {
			return
		}
		if text := archivePlainText(sel); text != "" {
			excerpts = append(excerpts, text)
		}
	})
	return excerpts
}
