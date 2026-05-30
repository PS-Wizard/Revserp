package crawler

import "testing"

func TestParserParseHTML(t *testing.T) {
	htmlDocument := []byte(`
	<!DOCTYPE html>
	<html lang="en">
	<head>
		<title> Test Page </title>
		<meta name="description" content=" A useful description. ">
		<meta name="viewport" content="width=device-width, initial-scale=1">
		<meta name="robots" content="index,follow">
		<meta property="og:title" content="OG Test Page">
		<meta property="og:type" content="website">
		<link rel="canonical" href="/canonical-page">
		<script type="application/ld+json">{"@context":"https://schema.org","@type":"WebPage"}</script>
	</head>
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

	if parsedPage.VisibleText != "Main Heading This is a useful paragraph with real body text. Overview Features Fast Reliable About Us Vercel" {
		t.Fatalf("got visible text %q", parsedPage.VisibleText)
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
