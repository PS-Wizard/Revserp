package crawler

import (
	"strings"
	"testing"
)

func TestParserParseHTML(t *testing.T) {
	htmlDocument := []byte(`
	<!DOCTYPE html>
	<html lang="en">
	<head>
		<title> Test Page </title>
		<meta name="description" content=" A useful description. ">
		<meta name="author" content=" Jane Doe ">
		<meta name="viewport" content="width=device-width, initial-scale=1">
		<meta name="robots" content="index,follow">
		<meta property="og:title" content="OG Test Page">
		<meta property="og:type" content="website">
		<link rel="canonical" href="/canonical-page">
		<script type="application/ld+json">{"@context":"https://schema.org","@type":"WebPage"}</script>
	<body>
		<h1>   Main Heading   </h1>
		<p>This is a useful paragraph with real body text.</p>
		<h2>Overview</h2>
		<h2>Features</h2>
		<h3>Fast</h3>
		<h3>Reliable</h3>
		<img src="/hero.jpg" alt="Hero image" width="1200" height="630">
		<img src="/logo.jpg" alt="">
		<img src="/icon.jpg">
		<a href="/about"> About Us </a>
		<a href="https://vercel.com" rel="nofollow noopener">Vercel</a>
		<script>console.log('ignore me')</script>
	</body>
	</html>
`)

	parser := NewParser()
	parsedPage, err := parser.ParseHTML("https://revketer.ai/", "text/html; charset=utf-8", htmlDocument)
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}

	if parsedPage.URL != "https://revketer.ai/" {
		t.Fatalf("got url %q", parsedPage.URL)
	}

	if parsedPage.Title != "Test Page" {
		t.Fatalf("got title %q", parsedPage.Title)
	}

	if parsedPage.MetaDescription != "A useful description." {
		t.Fatalf("got meta description %q", parsedPage.MetaDescription)
	}

	if parsedPage.Author != "Jane Doe" {
		t.Fatalf("got author %q", parsedPage.Author)
	}

	if parsedPage.CanonicalURL != "https://revketer.ai/canonical-page" {
		t.Fatalf("got canonical url %q", parsedPage.CanonicalURL)
	}

	if parsedPage.Lang != "en" {
		t.Fatalf("got lang %q", parsedPage.Lang)
	}

	if parsedPage.Viewport != "width=device-width, initial-scale=1" {
		t.Fatalf("got viewport %q", parsedPage.Viewport)
	}

	if parsedPage.Robots != "index,follow" {
		t.Fatalf("got robots %q", parsedPage.Robots)
	}

	if len(parsedPage.OGTags) != 2 {
		t.Fatalf("got %d og tags", len(parsedPage.OGTags))
	}

	if parsedPage.OGTags["og:title"] != "OG Test Page" {
		t.Fatalf("got og:title %q", parsedPage.OGTags["og:title"])
	}

	if parsedPage.OGTags["og:type"] != "website" {
		t.Fatalf("got og:type %q", parsedPage.OGTags["og:type"])
	}

	if len(parsedPage.JSONLDBlocks) != 1 {
		t.Fatalf("got %d json-ld blocks", len(parsedPage.JSONLDBlocks))
	}

	if parsedPage.JSONLDBlocks[0] != `{"@context":"https://schema.org","@type":"WebPage"}` {
		t.Fatalf("got json-ld block %q", parsedPage.JSONLDBlocks[0])
	}

	if parsedPage.VisibleText != "Main Heading\n\nThis is a useful paragraph with real body text.\n\nOverview\n\nFeatures\n\nFast\n\nReliable" {
		t.Fatalf("got visible text %q", parsedPage.VisibleText)
	}

	if len(parsedPage.ContentBlocks) != 9 {
		t.Fatalf("got %d content blocks", len(parsedPage.ContentBlocks))
	}

	if parsedPage.ContentBlocks[0].Tag != "h1" || parsedPage.ContentBlocks[0].Text != "Main Heading" {
		t.Fatalf("got first block %#v", parsedPage.ContentBlocks[0])
	}

	if parsedPage.ContentBlocks[1].Tag != "p" || parsedPage.ContentBlocks[1].Text != "This is a useful paragraph with real body text." {
		t.Fatalf("got second block %#v", parsedPage.ContentBlocks[1])
	}

	if parsedPage.ContentBlocks[6].Tag != "img" || parsedPage.ContentBlocks[6].Text != "Hero image" {
		t.Fatalf("got img block %#v", parsedPage.ContentBlocks[6])
	}

	if parsedPage.ImageCount != 3 {
		t.Fatalf("got image count %d", parsedPage.ImageCount)
	}

	if parsedPage.ImagesWithoutAltCount != 2 {
		t.Fatalf("got images without alt count %d", parsedPage.ImagesWithoutAltCount)
	}

	if parsedPage.ImagesWithoutDimensions != 2 {
		t.Fatalf("got images without dimensions count %d", parsedPage.ImagesWithoutDimensions)
	}

	if parsedPage.H1 != "Main Heading" {
		t.Fatalf("got h1 %q", parsedPage.H1)
	}

	if parsedPage.H1Count != 1 {
		t.Fatalf("got h1 count %d", parsedPage.H1Count)
	}

	if len(parsedPage.H2Headings) != 2 {
		t.Fatalf("got %d h2 headings", len(parsedPage.H2Headings))
	}

	if parsedPage.H2Headings[0] != "Overview" || parsedPage.H2Headings[1] != "Features" {
		t.Fatalf("got h2 headings %#v", parsedPage.H2Headings)
	}

	if len(parsedPage.H3Headings) != 2 {
		t.Fatalf("got %d h3 headings", len(parsedPage.H3Headings))
	}

	if parsedPage.H3Headings[0] != "Fast" || parsedPage.H3Headings[1] != "Reliable" {
		t.Fatalf("got h3 headings %#v", parsedPage.H3Headings)
	}

	if len(parsedPage.HeadingOutline) != 5 {
		t.Fatalf("got %d heading outline entries", len(parsedPage.HeadingOutline))
	}
	if parsedPage.HeadingOutline[0].Level != 1 || parsedPage.HeadingOutline[0].Text != "Main Heading" {
		t.Fatalf("got first heading outline entry %#v", parsedPage.HeadingOutline[0])
	}
	if parsedPage.HeadingOutline[1].Level != 2 || parsedPage.HeadingOutline[1].Text != "Overview" {
		t.Fatalf("got second heading outline entry %#v", parsedPage.HeadingOutline[1])
	}
	if parsedPage.HeadingOutline[2].Level != 2 || parsedPage.HeadingOutline[2].Text != "Features" {
		t.Fatalf("got third heading outline entry %#v", parsedPage.HeadingOutline[2])
	}
	if parsedPage.HeadingOutline[3].Level != 3 || parsedPage.HeadingOutline[3].Text != "Fast" {
		t.Fatalf("got fourth heading outline entry %#v", parsedPage.HeadingOutline[3])
	}
	if parsedPage.HeadingOutline[4].Level != 3 || parsedPage.HeadingOutline[4].Text != "Reliable" {
		t.Fatalf("got fifth heading outline entry %#v", parsedPage.HeadingOutline[4])
	}

	if len(parsedPage.Links) != 2 {
		t.Fatalf("got %d links", len(parsedPage.Links))
	}

	firstLink := parsedPage.Links[0]
	if firstLink.TargetURL != "https://revketer.ai/about" {
		t.Fatalf("got first link target %q", firstLink.TargetURL)
	}

	if firstLink.AnchorText != "About Us" {
		t.Fatalf("got first link text %q", firstLink.AnchorText)
	}

	if !firstLink.IsInternal {
		t.Fatalf("expected first link to be internal")
	}

	if firstLink.NoFollow {
		t.Fatalf("expected first link to not be nofollow")
	}

	secondLink := parsedPage.Links[1]
	if secondLink.TargetURL != "https://vercel.com/" {
		t.Fatalf("got second link target %q", secondLink.TargetURL)
	}

	if secondLink.AnchorText != "Vercel" {
		t.Fatalf("got second link text %q", secondLink.AnchorText)
	}

	if secondLink.IsInternal {
		t.Fatalf("expected second link to be external")
	}

	if !secondLink.NoFollow {
		t.Fatalf("expected second link to be nofollow")
	}
}

func TestParserParseHTMLRejectsNonHTMLContent(t *testing.T) {
	parser := NewParser()

	_, err := parser.ParseHTML("https://revketer.ai/file.pdf", "application/pdf", []byte("not html"))
	if err == nil {
		t.Fatalf("expected non-html content type to fail")
	}
}

func TestParserParseHTMLBuildsHeadingOutlineInDocumentOrder(t *testing.T) {
	htmlDocument := []byte(`
	<!DOCTYPE html>
	<html>
	<body>
		<h1>Intro</h1>
		<section><h3>Skipped level</h3></section>
		<h2>Back to section</h2>
		<h4>Deep detail</h4>
		<h2>   </h2>
	</body>
	</html>
	`)

	parser := NewParser()
	parsedPage, err := parser.ParseHTML("https://revketer.ai/outline", "text/html", htmlDocument)
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}

	if len(parsedPage.HeadingOutline) != 4 {
		t.Fatalf("got %d heading outline entries", len(parsedPage.HeadingOutline))
	}
	if parsedPage.HeadingOutline[0].Level != 1 || parsedPage.HeadingOutline[0].Text != "Intro" {
		t.Fatalf("got heading outline entry %#v", parsedPage.HeadingOutline[0])
	}
	if parsedPage.HeadingOutline[1].Level != 3 || parsedPage.HeadingOutline[1].Text != "Skipped level" {
		t.Fatalf("got heading outline entry %#v", parsedPage.HeadingOutline[1])
	}
	if parsedPage.HeadingOutline[2].Level != 2 || parsedPage.HeadingOutline[2].Text != "Back to section" {
		t.Fatalf("got heading outline entry %#v", parsedPage.HeadingOutline[2])
	}
	if parsedPage.HeadingOutline[3].Level != 4 || parsedPage.HeadingOutline[3].Text != "Deep detail" {
		t.Fatalf("got heading outline entry %#v", parsedPage.HeadingOutline[3])
	}
}

func TestParserParseHTMLExtractsAuthorFromJSONLD(t *testing.T) {
	htmlDocument := []byte(`
	<!DOCTYPE html>
	<html>
	<head>
		<script type="application/ld+json">{"@context":"https://schema.org","@graph":[{"@type":"Organization","name":"Revserp"},{"@type":"Article","author":{"@type":"Person","name":"Avery Stone"}}]}</script>
	</head>
	<body><h1>Article</h1></body>
	</html>
	`)

	parser := NewParser()
	parsedPage, err := parser.ParseHTML("https://revketer.ai/blog/post", "text/html", htmlDocument)
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}

	if parsedPage.Author != "Avery Stone" {
		t.Fatalf("got author %q", parsedPage.Author)
	}
}

func TestParserExtractsTextFromDivsInMain(t *testing.T) {
	htmlDocument := []byte(`
	<!DOCTYPE html>
	<html>
	<body>
		<main>
			<div class="notice">Notice published 2018</div>
			<div class="body">Full notice text lives in a plain div.</div>
		</main>
	</body>
	</html>
	`)

	parser := NewParser()
	parsedPage, err := parser.ParseHTML("https://revketer.ai/notices", "text/html", htmlDocument)
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}

	if !strings.Contains(parsedPage.VisibleText, "Notice published 2018") {
		t.Fatalf("expected div notice date in visible text, got %q", parsedPage.VisibleText)
	}

	if !strings.Contains(parsedPage.VisibleText, "Full notice text lives in a plain div.") {
		t.Fatalf("expected div body text in visible text, got %q", parsedPage.VisibleText)
	}
}

func TestParserDropsHeaderAndFooterInsideMain(t *testing.T) {
	htmlDocument := []byte(`<main><header>Site header</header><div>Real content</div><footer>Site footer</footer></main>`)

	parser := NewParser()
	parsedPage, err := parser.ParseHTML("https://revketer.ai/page", "text/html", htmlDocument)
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}

	if strings.Contains(parsedPage.VisibleText, "Site header") {
		t.Fatalf("expected header text to be removed, got %q", parsedPage.VisibleText)
	}

	if strings.Contains(parsedPage.VisibleText, "Site footer") {
		t.Fatalf("expected footer text to be removed, got %q", parsedPage.VisibleText)
	}

	if !strings.Contains(parsedPage.VisibleText, "Real content") {
		t.Fatalf("expected main content to survive, got %q", parsedPage.VisibleText)
	}
}

func TestParserDropsReadMoreFromVisibleTextButKeepsBlocks(t *testing.T) {
	htmlDocument := []byte(`<main><div>Ad release 2018</div><p>Read More</p><p>Read More</p><p>Read More</p></main>`)

	parser := NewParser()
	parsedPage, err := parser.ParseHTML("https://revketer.ai/ads", "text/html", htmlDocument)
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}

	if !strings.Contains(parsedPage.VisibleText, "Ad release 2018") {
		t.Fatalf("expected short real title to survive, got %q", parsedPage.VisibleText)
	}

	if strings.Contains(parsedPage.VisibleText, "Read More") {
		t.Fatalf("expected read more labels dropped from visible text, got %q", parsedPage.VisibleText)
	}

	readMoreBlocks := 0
	for _, block := range parsedPage.ContentBlocks {
		if block.Text == "Read More" {
			readMoreBlocks++
		}
	}

	if readMoreBlocks != 3 {
		t.Fatalf("got %d read more content blocks, want 3", readMoreBlocks)
	}
}

func TestParserKeepsImageSrcOutOfVisibleText(t *testing.T) {
	htmlDocument := []byte(`<main><p>Real text</p><img src="/photo.jpg" alt=""></main>`)

	parser := NewParser()
	parsedPage, err := parser.ParseHTML("https://revketer.ai/gallery", "text/html", htmlDocument)
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}

	if strings.Contains(parsedPage.VisibleText, "/photo.jpg") {
		t.Fatalf("expected image src out of visible text, got %q", parsedPage.VisibleText)
	}

	foundImageBlock := false
	for _, block := range parsedPage.ContentBlocks {
		if block.Tag == "img" && block.Text == "/photo.jpg" {
			foundImageBlock = true
		}
	}

	if !foundImageBlock {
		t.Fatalf("expected image src in content blocks, got %#v", parsedPage.ContentBlocks)
	}
}
