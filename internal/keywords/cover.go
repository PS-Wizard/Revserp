package keywords

import (
	"strings"

	"github.com/ps-wizard/revserp/internal/businessprofile"
)

const (
	// MaxSeeds caps the matrix so a huge profile cannot turn GET into a scan.
	MaxSeeds = 50
	// MinSeedLength drops 1–2 character tokens that match almost every URL.
	MinSeedLength = 3
)

type Field string

const (
	FieldTitle Field = "title"
	FieldH1    Field = "h1"
	FieldURL   Field = "url"
)

type State string

const (
	StateLikelyTargeted State = "likely_targeted"
	StateNoLandingPage  State = "no_landing_page"
	StateCannibalized   State = "cannibalized"
)

// Page is the skinny crawl row Cover matches against. Caller must already
// filter to scoreable HTML pages (healthy, text/html).
type Page struct {
	URL   string
	Title string
	H1    string
}

type Match struct {
	URL   string `json:"url"`
	Field Field  `json:"field"`
}

type Seed struct {
	Keyword string  `json:"keyword"`
	Geo     bool    `json:"geo,omitempty"`
	State   State   `json:"state"`
	Matches []Match `json:"matches"`
}

type seedSpec struct {
	keyword string
	geo     bool
	needle  string
}

// buildSeeds returns the coverage row list: profile keywords as-is, plus at
// most one geo string from primary_location as written. No "keyword + city"
// combos. Drops empty / short seeds. Caps at MaxSeeds (keywords first).
func buildSeeds(targetKeywords []string, primaryLocation string) []seedSpec {
	normalized := businessprofile.NormalizeTargetKeywords(targetKeywords)
	specs := make([]seedSpec, 0, min(len(normalized)+1, MaxSeeds))
	seen := make(map[string]struct{}, MaxSeeds)

	for _, keyword := range normalized {
		if len(specs) >= MaxSeeds {
			break
		}
		spec, ok := newSeedSpec(keyword, false, seen)
		if !ok {
			continue
		}
		specs = append(specs, spec)
	}

	if len(specs) >= MaxSeeds {
		return specs
	}
	location := strings.TrimSpace(primaryLocation)
	if spec, ok := newSeedSpec(location, true, seen); ok {
		specs = append(specs, spec)
	}
	return specs
}

func newSeedSpec(keyword string, geo bool, seen map[string]struct{}) (seedSpec, bool) {
	trimmed := strings.TrimSpace(keyword)
	if trimmed == "" {
		return seedSpec{}, false
	}
	needle := strings.ToLower(trimmed)
	if len([]rune(needle)) < MinSeedLength {
		return seedSpec{}, false
	}
	if _, exists := seen[needle]; exists {
		return seedSpec{}, false
	}
	seen[needle] = struct{}{}
	return seedSpec{keyword: trimmed, geo: geo, needle: needle}, true
}

// Cover rebuilds the matrix from scratch. Never persist the result.
func Cover(pages []Page, targetKeywords []string, primaryLocation string) []Seed {
	specs := buildSeeds(targetKeywords, primaryLocation)
	if len(specs) == 0 {
		return []Seed{}
	}

	indexed := make([]indexedPage, 0, len(pages))
	for _, page := range pages {
		indexed = append(indexed, indexedPage{
			url:   page.URL,
			title: strings.ToLower(page.Title),
			h1:    strings.ToLower(page.H1),
			urlL:  strings.ToLower(page.URL),
		})
	}

	out := make([]Seed, 0, len(specs))
	for _, spec := range specs {
		out = append(out, coverSeed(indexed, spec))
	}
	return out
}

type indexedPage struct {
	url   string
	title string
	h1    string
	urlL  string
}

func coverSeed(pages []indexedPage, spec seedSpec) Seed {
	matches := make([]Match, 0)
	titleOrH1 := make(map[string]struct{})

	for _, page := range pages {
		if strings.Contains(page.title, spec.needle) {
			matches = append(matches, Match{URL: page.url, Field: FieldTitle})
			titleOrH1[page.url] = struct{}{}
		}
		if strings.Contains(page.h1, spec.needle) {
			matches = append(matches, Match{URL: page.url, Field: FieldH1})
			titleOrH1[page.url] = struct{}{}
		}
		if strings.Contains(page.urlL, spec.needle) {
			matches = append(matches, Match{URL: page.url, Field: FieldURL})
		}
	}

	state := StateNoLandingPage
	if len(titleOrH1) >= 2 {
		state = StateCannibalized
	} else if len(matches) > 0 {
		state = StateLikelyTargeted
	}

	return Seed{
		Keyword: spec.keyword,
		Geo:     spec.geo,
		State:   state,
		Matches: matches,
	}
}
