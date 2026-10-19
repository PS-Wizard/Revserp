package crawler

import (
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/html"
)

// realisticFixtureBody builds a synthetic article long enough for the native
// structured path, covering nav, links, image, ordered/nested list, br,
// indented code, and table without copying any private sample HTML.
func realisticFixtureBody() string {
	long := strings.Repeat("Lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt. ", 4)
	return `<!DOCTYPE html><html lang="en"><head><title>Brewing Guide</title>` +
		`<meta name="description" content="A brewing guide.">` +
		`<link rel="canonical" href="/brewing-guide"></head><body>` +
		`<nav><a href="/home">Home menu directory</a><a href="/about">About menu page</a></nav>` +
		`<main><article>` +
		`<h1>How to brew coffee</h1>` +
		`<p>Intro paragraph with a <a href="/beans">bean guide</a> and <strong>bold</strong> text. ` + long + `</p>` +
		`<p>Line break check one<br>two and more ` + long + `</p>` +
		`<h2>Steps</h2>` +
		`<ol><li>Grind beans</li><li>Boil water<ul><li>Filtered supply</li><li>Tap supply</li></ul></li><li>Pour slowly</li></ol>` +
		`<pre><code>    first line indented
second line
  third indented</code></pre>` +
		`<table><tr><th>Alpha</th><th>Beta</th></tr><tr><td>one</td><td>two</td></tr></table>` +
		`<figure><img src="/coffee.jpg" alt="Coffee cup"><figcaption>A cup</figcaption></figure>` +
		`<blockquote>A great quote here with <a href="/source">source link</a></blockquote>` +
		`<p>Closing paragraph ` + long + `</p>` +
		`</article></main><footer>Footer boilerplate signup</footer>` +
		`</body></html>`
}

func findBlock(blocks []ParsedBlock, tag string) (ParsedBlock, bool) {
	for _, block := range blocks {
		if block.Tag == tag {
			return block, true
		}
	}
	return ParsedBlock{}, false
}

func TestMainContentRealisticArticle(t *testing.T) {
	parsed, err := NewParser().ParseHTML("https://example.com/brewing-guide", "text/html", []byte(realisticFixtureBody()))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	if parsed.ContentExtractor != MainContentExtractorVersion {
		t.Fatalf("got extractor %q", parsed.ContentExtractor)
	}
	if parsed.ContentExtractionStatus != ContentExtractionOK {
		t.Fatalf("got status %q err %q", parsed.ContentExtractionStatus, parsed.ContentExtractionError)
	}
	// SEO facts come from the original DOM, not the extraction.
	if parsed.Title != "Brewing Guide" {
		t.Fatalf("got title %q", parsed.Title)
	}
	if parsed.H1 != "How to brew coffee" {
		t.Fatalf("got h1 %q", parsed.H1)
	}
	if len(parsed.H2Headings) != 1 || parsed.H2Headings[0] != "Steps" {
		t.Fatalf("got h2 %#v", parsed.H2Headings)
	}
	if parsed.CanonicalURL != "https://example.com/brewing-guide" {
		t.Fatalf("got canonical %q", parsed.CanonicalURL)
	}
	if len(parsed.Links) < 4 {
		t.Fatalf("got %d links, want nav plus body links", len(parsed.Links))
	}
	if parsed.ImageCount != 1 {
		t.Fatalf("got image count %d", parsed.ImageCount)
	}
	// Chrome stays out of persisted text.
	if strings.Contains(parsed.VisibleText, "Home menu directory") {
		t.Fatalf("nav leaked into visible text: %q", parsed.VisibleText)
	}
	if strings.Contains(parsed.VisibleText, "Footer boilerplate") {
		t.Fatalf("footer leaked into visible text: %q", parsed.VisibleText)
	}
	// Body copy survives in the shared native text.
	for _, want := range []string{"How to brew coffee", "bean guide", "Grind beans", "Pour slowly", "A great quote here"} {
		if !strings.Contains(parsed.VisibleText, want) {
			t.Fatalf("visible text missing %q: %q", want, parsed.VisibleText)
		}
	}
	// Block tags cover the canonical set incl. table for the frontend worker.
	for _, tag := range []string{"h1", "p", "ol", "pre", "table", "img", "blockquote"} {
		if _, ok := findBlock(parsed.ContentBlocks, tag); !ok {
			t.Fatalf("missing %s block in %#v", tag, parsed.ContentBlocks)
		}
	}
	// Code keeps first-line indentation and newlines; inner HTML keeps spaces.
	pre, _ := findBlock(parsed.ContentBlocks, "pre")
	if !strings.HasPrefix(pre.Text, "    first line indented") {
		t.Fatalf("code first-line indentation lost: %q", pre.Text)
	}
	if !strings.Contains(pre.Text, "\nsecond line\n  third indented") {
		t.Fatalf("code newlines lost: %q", pre.Text)
	}
	if !strings.Contains(pre.Html, "    first line indented") {
		t.Fatalf("code html spaces lost: %q", pre.Html)
	}
	// Nested and ordered list text keeps word boundaries with markup intact.
	ol, _ := findBlock(parsed.ContentBlocks, "ol")
	for _, want := range []string{"Grind beans", "Boil water", "Filtered supply", "Pour slowly"} {
		if !strings.Contains(ol.Text, want) {
			t.Fatalf("list text missing %q: %q", want, ol.Text)
		}
	}
	if strings.Contains(ol.Text, "beansBoil") || strings.Contains(ol.Text, "waterFiltered") {
		t.Fatalf("list words concatenated: %q", ol.Text)
	}
	if !strings.Contains(ol.Html, "<ul>") || !strings.Contains(ol.Html, "<li>") {
		t.Fatalf("nested list markup lost: %q", ol.Html)
	}
	// br inside a paragraph keeps a word boundary instead of joining words.
	foundBR := false
	for _, block := range parsed.ContentBlocks {
		if block.Tag == "p" && strings.Contains(block.Text, "one two and more") {
			foundBR = true
		}
		if strings.Contains(block.Text, "onetwo") {
			t.Fatalf("br words concatenated: %q", block.Text)
		}
	}
	if !foundBR {
		t.Fatalf("br paragraph missing in %#v", parsed.ContentBlocks)
	}
	// Table cells keep boundaries; inner HTML keeps rows and cells.
	table, _ := findBlock(parsed.ContentBlocks, "table")
	for _, want := range []string{"Alpha", "Beta", "one", "two"} {
		if !strings.Contains(table.Text, want) {
			t.Fatalf("table text missing %q: %q", want, table.Text)
		}
	}
	if strings.Contains(table.Text, "AlphaBeta") || strings.Contains(table.Text, "onetwo") {
		t.Fatalf("table cells concatenated: %q", table.Text)
	}
	if !strings.Contains(table.Html, "<tr>") || !strings.Contains(table.Html, "<td>") {
		t.Fatalf("table markup lost: %q", table.Html)
	}
	// Inline formatting and links survive inside paragraph HTML.
	foundInline := false
	for _, block := range parsed.ContentBlocks {
		// Native resolves relative hrefs against the page URL via OriginalURL.
		if block.Tag == "p" && strings.Contains(block.Html, `bean guide</a>`) && strings.Contains(block.Html, "<strong>bold</strong>") {
			foundInline = true
		}
	}
	if !foundInline {
		t.Fatalf("inline formatting/links lost in %#v", parsed.ContentBlocks)
	}
	// Image keeps outer HTML for the editor; native text holds no image prose.
	img, _ := findBlock(parsed.ContentBlocks, "img")
	if !strings.Contains(img.Html, "<img") || !strings.Contains(img.Html, "/coffee.jpg") {
		t.Fatalf("image outer html lost: %q", img.Html)
	}
	if strings.Contains(parsed.VisibleText, "/coffee.jpg") {
		t.Fatalf("image src leaked into visible text: %q", parsed.VisibleText)
	}
}

func TestMainContentEmptyPage(t *testing.T) {
	// A contentless page carries no inventable body: the native call errors,
	// provenance is still stamped, and SEO facts survive.
	parsed, err := NewParser().ParseHTML("https://example.com/empty", "text/html", []byte(`<!DOCTYPE html><html><head><title>Empty</title></head><body><script>var x = 1;</script></body></html>`))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	if parsed.ContentExtractor != MainContentExtractorVersion {
		t.Fatalf("got extractor %q", parsed.ContentExtractor)
	}
	if parsed.ContentExtractionStatus != ContentExtractionFailed {
		t.Fatalf("got status %q", parsed.ContentExtractionStatus)
	}
	if len(parsed.ContentBlocks) != 0 || parsed.VisibleText != "" {
		t.Fatalf("expected no body, got %#v %q", parsed.ContentBlocks, parsed.VisibleText)
	}
	if parsed.Title != "Empty" {
		t.Fatalf("got title %q", parsed.Title)
	}
	if parsed.ContentExtractionError == "" {
		t.Fatalf("expected native error for logging")
	}
}

func TestMainContentEmptyNodeMapsToEmpty(t *testing.T) {
	// Explicit empty branch: a successful extraction whose node yields no
	// usable blocks reports empty rather than inventing body text.
	doc, err := html.Parse(strings.NewReader(`<!DOCTYPE html><html><body><br><hr></body></html>`))
	if err != nil {
		t.Fatalf("parse root: %v", err)
	}
	var node *html.Node
	var find func(n *html.Node)
	find = func(n *html.Node) {
		if node != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "body" {
			node = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	if node == nil {
		t.Fatalf("no body node")
	}
	blocks := contentBlocksFromNode(node)
	if len(blocks) != 0 {
		t.Fatalf("expected no blocks, got %#v", blocks)
	}
}

func TestMainContentUnicodeAndConcurrent(t *testing.T) {
	const nepali = "जीवन बीमा"
	body := `<!DOCTYPE html><html lang="ne"><head><title>Valid</title>` +
		`<meta name="description" content="A valid description with Nepali ` + nepali + `."></head>` +
		`<body><main><article><h1>` + nepali + ` guide</h1>` +
		`<p>Body ` + nepali + ` plus ` + strings.Repeat("Lorem ipsum dolor sit amet consectetur adipiscing elit. ", 10) + `</p>` +
		`<p>More ` + strings.Repeat("sed do eiusmod tempor incididunt ut labore et dolore. ", 10) + `</p>` +
		`</article></main></body></html>`
	parsed, err := NewParser().ParseHTML("https://example.com/nepali", "text/html", []byte(body))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	if parsed.ContentExtractionStatus != ContentExtractionOK {
		t.Fatalf("got status %q err %q", parsed.ContentExtractionStatus, parsed.ContentExtractionError)
	}
	if !strings.Contains(parsed.VisibleText, nepali) {
		t.Fatalf("unicode lost from visible text: %q", parsed.VisibleText)
	}
	found := false
	for _, block := range parsed.ContentBlocks {
		if strings.Contains(block.Text, nepali) {
			found = true
		}
	}
	if !found {
		t.Fatalf("unicode lost from blocks: %#v", parsed.ContentBlocks)
	}
	// Native extraction must be safe under concurrent parses; no network used.
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := NewParser().ParseHTML("https://example.com/concurrent", "text/html", []byte(realisticFixtureBody()))
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
}

func TestContentBlocksKeepsDirectTextInOrder(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<!DOCTYPE html><html><body>Before<div>inside<p>paragraph</p>after literal &lt;script&gt; text</div>Tail</body></html>`))
	if err != nil {
		t.Fatalf("parse root: %v", err)
	}
	var body *html.Node
	var find func(n *html.Node)
	find = func(n *html.Node) {
		if body != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "body" {
			body = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	if body == nil {
		t.Fatalf("no body node")
	}
	blocks := contentBlocksFromNode(body)
	var texts []string
	for _, block := range blocks {
		texts = append(texts, block.Text)
	}
	want := []string{"Before", "inside", "paragraph", "after literal <script> text", "Tail"}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("got texts %q, want %q", texts, want)
	}
	for _, block := range blocks {
		if !strings.Contains(block.Text, "literal") {
			continue
		}
		if !strings.Contains(block.Html, "&lt;script&gt;") || strings.Contains(block.Html, "<script>") {
			t.Fatalf("literal script text must stay escaped, got html %q", block.Html)
		}
	}
}
