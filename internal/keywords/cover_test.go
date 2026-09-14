package keywords

import (
	"strconv"
	"strings"
	"testing"
)

func TestBuildSeedsAddsLocationOnceAndSkipsShort(t *testing.T) {
	specs := buildSeeds([]string{"  Plumber ", "plumber", "ab", "emergency plumber"}, "Midtown")
	got := make([]string, 0, len(specs))
	geoCount := 0
	for _, spec := range specs {
		got = append(got, spec.keyword)
		if spec.geo {
			geoCount++
		}
	}
	want := []string{"Plumber", "emergency plumber", "Midtown"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("seeds = %v, want %v", got, want)
	}
	if geoCount != 1 {
		t.Fatalf("geo seeds = %d, want 1", geoCount)
	}
}

func TestBuildSeedsDoesNotDuplicateLocationAlreadyInKeywords(t *testing.T) {
	specs := buildSeeds([]string{"Midtown", "plumber"}, "midtown")
	if len(specs) != 2 {
		t.Fatalf("len = %d, want 2", len(specs))
	}
	if specs[0].geo || specs[1].geo {
		t.Fatalf("expected no extra geo row when location is already a keyword: %#v", specs)
	}
}

func TestBuildSeedsCapsAtMax(t *testing.T) {
	keywords := make([]string, MaxSeeds+5)
	for i := range keywords {
		keywords[i] = "kw-" + strconv.Itoa(i)
	}
	specs := buildSeeds(keywords, "Austin")
	if len(specs) != MaxSeeds {
		t.Fatalf("len = %d, want %d", len(specs), MaxSeeds)
	}
	if specs[len(specs)-1].geo {
		t.Fatal("geo should not displace keywords when already at cap")
	}
}

func TestCoverStates(t *testing.T) {
	pages := []Page{
		{URL: "https://example.com/plumber", Title: "Emergency Plumber", H1: "Call us"},
		{URL: "https://example.com/about", Title: "About", H1: "About the team"},
		{URL: "https://example.com/services/plumber", Title: "Local Plumber", H1: "Our plumbers"},
		{URL: "https://example.com/services/drain", Title: "Drain cleaning", H1: "Drain cleaning"},
	}

	seeds := Cover(pages, []string{"emergency plumber", "about", "plumber", "roofer"}, "")
	byKeyword := map[string]Seed{}
	for _, seed := range seeds {
		byKeyword[seed.Keyword] = seed
	}

	if byKeyword["emergency plumber"].State != StateLikelyTargeted {
		t.Fatalf("emergency plumber = %s, want likely_targeted", byKeyword["emergency plumber"].State)
	}
	if byKeyword["about"].State != StateLikelyTargeted {
		t.Fatalf("about = %s, want likely_targeted", byKeyword["about"].State)
	}
	if byKeyword["plumber"].State != StateCannibalized {
		t.Fatalf("plumber = %s, want cannibalized (title+h1 on two pages)", byKeyword["plumber"].State)
	}
	if byKeyword["roofer"].State != StateNoLandingPage {
		t.Fatalf("roofer = %s, want no_landing_page", byKeyword["roofer"].State)
	}
}

func TestCoverURLOnlyIsTargetedNotCannibalized(t *testing.T) {
	pages := []Page{
		{URL: "https://example.com/plumber", Title: "Home", H1: "Welcome"},
		{URL: "https://example.com/blog/plumber", Title: "Blog", H1: "Posts"},
	}
	seeds := Cover(pages, []string{"plumber"}, "")
	if len(seeds) != 1 {
		t.Fatalf("len = %d", len(seeds))
	}
	if seeds[0].State != StateLikelyTargeted {
		t.Fatalf("state = %s, want likely_targeted (URL-only hits do not cannibalize)", seeds[0].State)
	}
	if len(seeds[0].Matches) != 2 {
		t.Fatalf("matches = %d, want 2 url fields", len(seeds[0].Matches))
	}
	for _, match := range seeds[0].Matches {
		if match.Field != FieldURL {
			t.Fatalf("field = %s, want url", match.Field)
		}
	}
}

func TestCoverDoesNotReadBody(t *testing.T) {
	pages := []Page{
		{URL: "https://example.com/contact", Title: "Contact", H1: "Get in touch"},
	}
	seeds := Cover(pages, []string{"plumber"}, "")
	if seeds[0].State != StateNoLandingPage {
		t.Fatalf("body-less page mentioning nothing in title/h1/url should be no_landing_page, got %s", seeds[0].State)
	}
}

func TestCoverEmptyKeywords(t *testing.T) {
	seeds := Cover([]Page{{URL: "https://example.com/", Title: "Home", H1: "Home"}}, nil, "")
	if seeds == nil || len(seeds) != 0 {
		t.Fatalf("got %#v, want empty slice", seeds)
	}
}

func TestCoverSamePageTitleAndH1IsTargetedNotCannibalized(t *testing.T) {
	pages := []Page{
		{URL: "https://example.com/plumber", Title: "Emergency Plumber", H1: "Plumber services here"},
	}
	seeds := Cover(pages, []string{"plumber"}, "")
	if len(seeds) != 1 {
		t.Fatalf("len = %d, want 1", len(seeds))
	}
	seed := seeds[0]
	if seed.State != StateLikelyTargeted {
		t.Fatalf("state = %s, want likely_targeted (title+h1 on the same page is one page)", seed.State)
	}
	fields := map[Field]int{}
	for _, match := range seed.Matches {
		fields[match.Field]++
	}
	if fields[FieldTitle] != 1 || fields[FieldH1] != 1 {
		t.Fatalf("matches = %#v, want one title and one h1 match on the same page", seed.Matches)
	}
}

func TestCoverURLOnlyPlusTitleIsTargetedNotCannibalized(t *testing.T) {
	// Mixed case: one title hit (a real titleOrH1 page) plus one URL-only hit.
	// Cannibalization needs 2+ distinct titleOrH1 pages, so this stays targeted.
	pages := []Page{
		{URL: "https://example.com/emergency", Title: "Emergency Plumber", H1: "Call us"},
		{URL: "https://example.com/blog/plumber", Title: "Blog", H1: "Posts"},
	}
	seeds := Cover(pages, []string{"plumber"}, "")
	if len(seeds) != 1 {
		t.Fatalf("len = %d, want 1", len(seeds))
	}
	seed := seeds[0]
	if seed.State != StateLikelyTargeted {
		t.Fatalf("state = %s, want likely_targeted (one titleOrH1 page + one URL-only page)", seed.State)
	}
	fields := map[Field]int{}
	for _, match := range seed.Matches {
		fields[match.Field]++
	}
	if fields[FieldTitle] != 1 || fields[FieldURL] != 1 {
		t.Fatalf("matches = %#v, want one title and one url match", seed.Matches)
	}
}

func TestCoverLocationOnlySeedWhenKeywordsEmpty(t *testing.T) {
	pages := []Page{
		{URL: "https://example.com/midtown", Title: "Midtown Plumbers", H1: "Welcome"},
	}
	seeds := Cover(pages, nil, "Midtown")
	if len(seeds) != 1 {
		t.Fatalf("len = %d, want 1 (location-only seed)", len(seeds))
	}
	seed := seeds[0]
	if seed.Keyword != "Midtown" || !seed.Geo {
		t.Fatalf("seed = %#v, want keyword Midtown with geo true", seed)
	}
	if seed.State != StateLikelyTargeted {
		t.Fatalf("state = %s, want likely_targeted (geo seed matches title)", seed.State)
	}
}

func TestCoverSkipsShortLocationSeeds(t *testing.T) {
	for _, location := range []string{"NY", "ab", "  x  "} {
		seeds := Cover(nil, nil, location)
		if len(seeds) != 0 {
			t.Fatalf("location %q produced %d seeds, want 0 (under 3 runes)", location, len(seeds))
		}
	}
}

func TestCoverCaseInsensitiveMatch(t *testing.T) {
	pages := []Page{
		{URL: "https://example.com/one", Title: "EMERGENCY PLUMBER", H1: "24/7"},
	}
	seeds := Cover(pages, []string{"Plumber"}, "")
	if len(seeds) != 1 || seeds[0].State != StateLikelyTargeted {
		t.Fatalf("seed = %#v, want likely_targeted (case-insensitive substring)", seeds)
	}
	if len(seeds[0].Matches) != 1 || seeds[0].Matches[0].Field != FieldTitle {
		t.Fatalf("matches = %#v, want one title match", seeds[0].Matches)
	}
}

func TestCoverNilPagesAndKeywordsDoNotPanic(t *testing.T) {
	// Keywords nil + location set still yields the geo seed.
	seeds := Cover(nil, nil, "Midtown")
	if len(seeds) != 1 || !seeds[0].Geo {
		t.Fatalf("got %#v, want one geo seed", seeds)
	}
	if seeds[0].Matches == nil || len(seeds[0].Matches) != 0 {
		t.Fatalf("geo seed matches = %#v, want non-nil empty", seeds[0].Matches)
	}
	// Nothing at all: still a non-nil empty result.
	empty := Cover(nil, nil, "")
	if empty == nil || len(empty) != 0 {
		t.Fatalf("got %#v, want non-nil empty slice", empty)
	}
}

func TestCoverSubstringMatchIsIntentionalNotTokenMatch(t *testing.T) {
	// Locked rule: substring, not token boundary. Do not "fix" this to word-boundary matching.
	pages := []Page{
		{URL: "https://example.com/blog/post", Title: "Emergency Plumber Services", H1: "Learn"},
	}
	seeds := Cover(pages, []string{"plumber"}, "")
	if seeds[0].State != StateLikelyTargeted {
		t.Fatalf("state = %s, want likely_targeted: 'plumber' substrings 'Emergency Plumber Services' by design", seeds[0].State)
	}
}

func TestBuildSeedsRuneLengthUnicode(t *testing.T) {
	// Minimum seed length is runes, not bytes: 3-rune CJK kept, 2-rune dropped.
	specs := buildSeeds([]string{"水管工", "水管"}, "")
	if len(specs) != 1 {
		t.Fatalf("len = %d, want 1 (3-rune CJK kept, 2-rune dropped)", len(specs))
	}
	if specs[0].keyword != "水管工" {
		t.Fatalf("keyword = %q, want 水管工", specs[0].keyword)
	}
}

func TestCoverNoLandingPageMatchesNonNil(t *testing.T) {
	pages := []Page{
		{URL: "https://example.com/about", Title: "About us", H1: "Our story"},
	}
	seeds := Cover(pages, []string{"plumber"}, "")
	if len(seeds) != 1 || seeds[0].State != StateNoLandingPage {
		t.Fatalf("seed = %#v, want no_landing_page", seeds)
	}
	if seeds[0].Matches == nil {
		t.Fatal("Matches must be non-nil empty slice for no_landing_page")
	}
	if len(seeds[0].Matches) != 0 {
		t.Fatalf("matches = %#v, want empty", seeds[0].Matches)
	}
}
