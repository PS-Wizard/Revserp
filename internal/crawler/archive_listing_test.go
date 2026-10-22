package crawler

import (
	"fmt"
	"strings"
	"testing"
)

func archiveListingTestTitles() []string {
	titles := make([]string, 0, 25)
	for i := 1; i <= 25; i++ {
		titles = append(titles, fmt.Sprintf("Synthetic Post Title %02d about search visibility", i))
	}
	return titles
}

func archiveListingTestExcerpt(i int) string {
	return fmt.Sprintf("Excerpt for post %02d with enough words to look like a summary paragraph for testing.", i)
}

func archiveListingTestCardURL(i int) string {
	return fmt.Sprintf("https://example.com/posts/synthetic-post-%02d/", i)
}

func archiveListingTestDefault(int) string { return "" }

func archiveListingTestPage(h1 string, href, title, excerpt func(int) string) string {
	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html><html lang="en"><head><title>Test Archive</title></head><body>`)
	sb.WriteString(`<nav><a href="/home">Home menu</a></nav>`)
	sb.WriteString(`<main id="content"><h1>` + h1 + `</h1><div class="post-list">`)
	titles := archiveListingTestTitles()
	for i := 1; i <= 25; i++ {
		cardHref := href(i)
		if cardHref == "" {
			cardHref = fmt.Sprintf("/posts/synthetic-post-%02d/", i)
		}
		cardTitle := title(i)
		if cardTitle == "" {
			cardTitle = titles[i-1]
		}
		cardExcerpt := excerpt(i)
		if cardExcerpt == "" {
			cardExcerpt = archiveListingTestExcerpt(i)
		}
		fmt.Fprintf(&sb, `<article class="item"><div class="thumb"><a href="%s"><img src="data:image/gif;base64,xyz" data-src="/thumb-%02d.webp" alt="Thumbnail %02d"><noscript><img src="/thumb-%02d.webp" alt="Thumbnail %02d"></noscript></a></div><div class="wrapper"><div class="number">%02d</div><div class="content"><h2 class="title"><a href="%s">%s</a></h2><p>%s</p></div></div></article>`,
			cardHref, i, i, i, i, i, cardHref, cardTitle, cardExcerpt)
	}
	sb.WriteString(`</div></main><footer>Footer boilerplate signup</footer><script>var archiveInit = true;</script></body></html>`)
	return sb.String()
}

func archiveListingTestAssertOrder(t *testing.T, haystack string, needles []string) {
	t.Helper()
	offset := 0
	for _, needle := range needles {
		idx := strings.Index(haystack[offset:], needle)
		if idx < 0 {
			t.Fatalf("missing %q in order in %.200q", needle, haystack)
		}
		offset += idx + len(needle)
	}
}

func archiveListingTestAssertClean(t *testing.T, parsed ParsedPage) {
	t.Helper()
	joined := parsed.VisibleText
	for _, block := range parsed.ContentBlocks {
		joined += "\n" + block.Text + "\n" + block.Html
	}
	for _, leak := range []string{"Home menu", "Footer boilerplate", "archiveInit", "<script", "<noscript", "data-src", "data:image/gif", "javascript:", "onclick", "onerror"} {
		if strings.Contains(joined, leak) {
			t.Fatalf("extraction output leaks %q", leak)
		}
	}
}

func archiveListingTestAssertNoListing(t *testing.T, parsed ParsedPage, titles []string) {
	t.Helper()
	var h2Texts []string
	for _, block := range parsed.ContentBlocks {
		if block.Tag == "h2" {
			h2Texts = append(h2Texts, block.Text)
		}
	}
	joined := strings.Join(h2Texts, "\n")
	count := 0
	for _, title := range titles {
		if strings.Contains(joined, title) {
			count++
		}
	}
	if count == len(titles) {
		t.Fatalf("native page must not become a concatenated h2 listing: %d titles", count)
	}
}

func TestArchiveListingExtractorVersionDiffersFromLegacy(t *testing.T) {
	if MainContentExtractorVersion == "go-trafilatura/v2@2.2.6" {
		t.Fatalf("extractor version must carry the archive policy suffix, got %q", MainContentExtractorVersion)
	}
}

func TestArchiveListingKeepsOwnHeadingAndAllCardTitles(t *testing.T) {
	titles := archiveListingTestTitles()
	headings := []string{"Archive Heading Alpha", "Archive Heading Beta"}
	texts := make(map[string]string, len(headings))
	for _, h1 := range headings {
		t.Run(h1, func(t *testing.T) {
			body := archiveListingTestPage(h1, archiveListingTestDefault, archiveListingTestDefault, archiveListingTestDefault)
			parsed, err := NewParser().ParseHTML("https://example.com/archive/", "text/html", []byte(body))
			if err != nil {
				t.Fatalf("parse html: %v", err)
			}
			if parsed.ContentExtractor != MainContentExtractorVersion {
				t.Fatalf("got extractor %q, want %q", parsed.ContentExtractor, MainContentExtractorVersion)
			}
			if parsed.ContentExtractionStatus != ContentExtractionOK {
				t.Fatalf("got status %q err %q", parsed.ContentExtractionStatus, parsed.ContentExtractionError)
			}
			if parsed.H1 != h1 {
				t.Fatalf("got h1 %q, want own heading %q", parsed.H1, h1)
			}
			for _, block := range parsed.ContentBlocks {
				if block.Tag == "h1" && strings.Contains(block.Html, "<h1>") {
					t.Fatalf("h1 block must carry inner html, got %q", block.Html)
				}
			}
			var needles []string
			needles = append(needles, h1)
			for i, title := range titles {
				needles = append(needles, title, archiveListingTestExcerpt(i+1))
			}
			archiveListingTestAssertOrder(t, parsed.VisibleText, needles)
			var blockTexts []string
			for _, block := range parsed.ContentBlocks {
				blockTexts = append(blockTexts, block.Text)
			}
			archiveListingTestAssertOrder(t, strings.Join(blockTexts, "\n"), needles)
			if len(parsed.ContentBlocks) != 51 {
				t.Fatalf("got %d blocks, want 51", len(parsed.ContentBlocks))
			}
			links := 0
			for _, block := range parsed.ContentBlocks {
				if block.Tag != "h2" {
					continue
				}
				if strings.Contains(block.Html, "<h2>") {
					t.Fatalf("h2 block must carry inner html, got %q", block.Html)
				}
				want := `<a href="` + archiveListingTestCardURL(links+1) + `">` + titles[links] + `</a>`
				if !strings.Contains(block.Html, want) {
					t.Fatalf("h2 block %d missing ordered safe link %q in %q", links+1, want, block.Html)
				}
				links++
			}
			if links != 25 {
				t.Fatalf("got %d linked h2 blocks, want 25", links)
			}
			archiveListingTestAssertClean(t, parsed)
			if parsed.Title != "Test Archive" {
				t.Fatalf("got title %q, seo facts must stay unchanged", parsed.Title)
			}
			texts[h1] = parsed.VisibleText
		})
	}
	if texts[headings[0]] == texts[headings[1]] {
		t.Fatalf("variant texts must differ by their own heading")
	}
}

func TestArchiveListingIgnoresCardsOutsideMain(t *testing.T) {
	long := strings.Repeat("Lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt. ", 8)
	var feed strings.Builder
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&feed, `<article><h2><a href="/outside-%d">Outside Card %d</a></h2></article>`, i, i)
	}
	body := `<!DOCTYPE html><html><head><title>Outside</title></head><body><aside><div class="feed">` + feed.String() +
		`</div></aside><main><article><h1>Real Article</h1><p>Marker sentence alpha seven. ` + long + `</p><p>` + long + `</p></article></main></body></html>`
	parsed, err := NewParser().ParseHTML("https://example.com/outside", "text/html", []byte(body))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	if parsed.ContentExtractor != MainContentExtractorVersion {
		t.Fatalf("got extractor %q", parsed.ContentExtractor)
	}
	if parsed.ContentExtractionStatus != ContentExtractionOK {
		t.Fatalf("got status %q err %q", parsed.ContentExtractionStatus, parsed.ContentExtractionError)
	}
	if parsed.H1 != "Real Article" {
		t.Fatalf("got h1 %q", parsed.H1)
	}
	if !strings.Contains(parsed.VisibleText, "Marker sentence alpha seven.") {
		t.Fatalf("article prose lost: %.200q", parsed.VisibleText)
	}
	if strings.Contains(parsed.VisibleText, "Outside Card") {
		t.Fatalf("sidebar cards leaked into text: %.200q", parsed.VisibleText)
	}
	archiveListingTestAssertNoListing(t, parsed, []string{"Outside Card 1", "Outside Card 2", "Outside Card 3"})
}

func TestArchiveListingKeepsArticleWithNestedRelatedCards(t *testing.T) {
	long := strings.Repeat("Lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt. ", 8)
	body := `<!DOCTYPE html><html><head><title>Article</title></head><body><main><article><h1>Full Article Title</h1>` +
		`<p>Marker sentence alpha seven. ` + long + `</p><p>` + long + `</p>` +
		`<section class="related"><article><h2><a href="/related-1">Related One</a></h2></article>` +
		`<article><h2><a href="/related-2">Related Two</a></h2></article></section>` +
		`</article></main></body></html>`
	parsed, err := NewParser().ParseHTML("https://example.com/article", "text/html", []byte(body))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	if parsed.ContentExtractor != MainContentExtractorVersion {
		t.Fatalf("got extractor %q", parsed.ContentExtractor)
	}
	if parsed.ContentExtractionStatus != ContentExtractionOK {
		t.Fatalf("got status %q err %q", parsed.ContentExtractionStatus, parsed.ContentExtractionError)
	}
	if !strings.Contains(parsed.VisibleText, "Marker sentence alpha seven.") {
		t.Fatalf("article prose lost: %.200q", parsed.VisibleText)
	}
	if strings.Contains(parsed.VisibleText, "Related One") || strings.Contains(parsed.VisibleText, "Related Two") {
		t.Fatalf("nested related cards must not join article text: %.200q", parsed.VisibleText)
	}
	archiveListingTestAssertNoListing(t, parsed, []string{"Related One", "Related Two"})
}

func TestArchiveListingDoesNotConcatenateMultipleMains(t *testing.T) {
	var alpha, beta, titles []string
	for i := 1; i <= 5; i++ {
		alpha = append(alpha, fmt.Sprintf(`<article><h2><a href="/alpha-%d">Alpha Title %d</a></h2></article>`, i, i))
		beta = append(beta, fmt.Sprintf(`<article><h2><a href="/beta-%d">Beta Title %d</a></h2></article>`, i, i))
		titles = append(titles, fmt.Sprintf("Alpha Title %d", i), fmt.Sprintf("Beta Title %d", i))
	}
	body := `<!DOCTYPE html><html><head><title>Two mains</title></head><body><main><h1>First</h1><div>` +
		strings.Join(alpha, "") + `</div></main><main><h1>Second</h1><div>` + strings.Join(beta, "") +
		`</div></main></body></html>`
	parsed, err := NewParser().ParseHTML("https://example.com/two-mains", "text/html", []byte(body))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	if parsed.ContentExtractor != MainContentExtractorVersion {
		t.Fatalf("got extractor %q", parsed.ContentExtractor)
	}
	if parsed.ContentExtractionStatus != ContentExtractionOK {
		t.Fatalf("got status %q err %q", parsed.ContentExtractionStatus, parsed.ContentExtractionError)
	}
	archiveListingTestAssertNoListing(t, parsed, titles)
}

func TestArchiveListingDropsUnsafeCardLinks(t *testing.T) {
	h1 := "Unsafe Link Archive"
	href := func(i int) string {
		if i == 5 {
			return "javascript:alert(document.cookie)"
		}
		return ""
	}
	title := func(i int) string {
		if i == 8 {
			return `Q&amp;A <em>Report</em> 08`
		}
		return ""
	}
	excerpt := func(i int) string {
		if i == 12 {
			return `Tom &amp; Jerry <b>best</b> moments`
		}
		return ""
	}
	body := archiveListingTestPage(h1, href, title, excerpt)
	body = strings.Replace(body, `<h2 class="title"><a href="/posts/synthetic-post-03/">`, `<h2 class="title"><a href="/posts/synthetic-post-03/" onclick="stealCookies()">`, 1)
	body = strings.Replace(body, `data-src="/thumb-04.webp"`, `data-src="/thumb-04.webp" onerror="steal()"`, 1)
	parsed, err := NewParser().ParseHTML("https://example.com/unsafe-archive/", "text/html", []byte(body))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	if parsed.ContentExtractor != MainContentExtractorVersion {
		t.Fatalf("got extractor %q", parsed.ContentExtractor)
	}
	if parsed.ContentExtractionStatus != ContentExtractionOK {
		t.Fatalf("got status %q err %q", parsed.ContentExtractionStatus, parsed.ContentExtractionError)
	}
	titles := archiveListingTestTitles()
	titles[7] = "Q&A Report 08"
	var needles []string
	needles = append(needles, h1)
	for i, title := range titles {
		if i+1 == 12 {
			needles = append(needles, title, "Tom & Jerry best moments")
			continue
		}
		needles = append(needles, title, archiveListingTestExcerpt(i+1))
	}
	archiveListingTestAssertOrder(t, parsed.VisibleText, needles)
	archiveListingTestAssertClean(t, parsed)
	joined := ""
	for _, block := range parsed.ContentBlocks {
		joined += "\n" + block.Html
	}
	if strings.Contains(joined, "<em>") || strings.Contains(joined, "<b>") {
		t.Fatalf("card markup must be escaped, got %.300q", joined)
	}
	if !strings.Contains(joined, "Q&amp;A Report 08") || !strings.Contains(joined, "Tom &amp; Jerry best moments") {
		t.Fatalf("entities must stay escaped, got %.300q", joined)
	}
	for _, block := range parsed.ContentBlocks {
		if block.Tag == "h2" && strings.Contains(block.Text, titles[4]) && strings.Contains(block.Html, "<a href") {
			t.Fatalf("unsafe href must drop the link but keep the title: %q", block.Html)
		}
	}
}

func TestArchiveListingKeepsMainProseBesideSiblingRelatedCards(t *testing.T) {
	long := strings.Repeat("Lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt. ", 8)
	body := `<!DOCTYPE html><html><head><title>Sibling related</title></head><body><main><h1>Main Article Title</h1>` +
		`<p>Marker sentence alpha seven. ` + long + `</p><p>` + long + `</p>` +
		`<section class="related"><article><h2><a href="/related-1">Related One</a></h2></article>` +
		`<article><h2><a href="/related-2">Related Two</a></h2></article></section>` +
		`</main></body></html>`
	parsed, err := NewParser().ParseHTML("https://example.com/sibling-related", "text/html", []byte(body))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	if parsed.ContentExtractor != MainContentExtractorVersion {
		t.Fatalf("got extractor %q", parsed.ContentExtractor)
	}
	if parsed.ContentExtractionStatus != ContentExtractionOK {
		t.Fatalf("got status %q err %q", parsed.ContentExtractionStatus, parsed.ContentExtractionError)
	}
	if parsed.H1 != "Main Article Title" {
		t.Fatalf("got h1 %q", parsed.H1)
	}
	if !strings.Contains(parsed.VisibleText, "Marker sentence alpha seven.") {
		t.Fatalf("main article prose dropped: %.200q", parsed.VisibleText)
	}
	archiveListingTestAssertNoListing(t, parsed, []string{"Related One", "Related Two"})
}

func TestArchiveListingPreservesExcerptsAndTextBoundaries(t *testing.T) {
	title := func(i int) string {
		if i == 1 {
			return `First<br>Post<script>inertTitle</script>`
		}
		return ""
	}
	excerpt := func(i int) string {
		if i == 1 {
			return `First<br>excerpt<script>inertExcerpt</script></p><p>Second excerpt<noscript>inertNoscript</noscript>`
		}
		return ""
	}
	body := archiveListingTestPage(`Archive<script>inertHeading</script>`, archiveListingTestDefault, title, excerpt)
	parsed, err := NewParser().ParseHTML("https://example.com/archive/", "text/html", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	archiveListingTestAssertOrder(t, parsed.VisibleText, []string{"Archive", "First Post", "First excerpt", "Second excerpt"})
	if len(parsed.ContentBlocks) != 52 {
		t.Fatalf("blocks = %d, want 52", len(parsed.ContentBlocks))
	}
	for _, block := range parsed.ContentBlocks {
		if strings.Contains(block.Text, "inert") || strings.Contains(block.Html, "inert") {
			t.Fatalf("inert content leaked: %#v", block)
		}
	}
	archiveListingTestAssertClean(t, parsed)
}
