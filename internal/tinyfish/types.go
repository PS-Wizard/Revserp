// Package tinyfish is a client for the TinyFish Search and Fetch APIs. Search
// and fetch are free on every plan but rate limited globally (30 queries per
// minute, 150 fetched URLs per minute), so callers must budget their usage.
//
// The types here are the package contract: the tool layer imports them and its
// tests substitute fakes, so the wire format stays inside this package.
package tinyfish

// SearchResult is one ranked web result. Fields mirror the API response, minus
// the ones this product does not use (authors, venue, citation counts).
type SearchResult struct {
	Position int
	Title    string
	URL      string
	Snippet  string
	SiteName string
	Date     string
}

// FetchResult is one fetched URL reduced to clean content. Format is the value
// the API returned ("markdown" in every request this product makes).
type FetchResult struct {
	URL           string
	FinalURL      string
	Title         string
	Description   string
	PublishedDate string
	Format        string
	Text          string
}
