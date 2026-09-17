package tinyfish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const defaultTimeout = 30 * time.Second

// Client talks to the TinyFish Search and Fetch APIs. Both share the same API
// key and rate limits, so one client covers both endpoints.
type Client struct {
	apiKey    string
	searchURL string
	fetchURL  string
	http      *http.Client
}

// NewClient builds a client against the given endpoints. A non-positive
// timeout falls back to 30s; the caller decides whether an empty key makes the
// client unusable.
func NewClient(apiKey, searchURL, fetchURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		apiKey:    apiKey,
		searchURL: searchURL,
		fetchURL:  fetchURL,
		http:      &http.Client{Timeout: timeout},
	}
}

// searchResponse mirrors only the fields this product consumes. date is a
// string|null on the wire; JSON leaves a null as the empty string.
type searchResponse struct {
	Results []struct {
		Position int    `json:"position"`
		Title    string `json:"title"`
		URL      string `json:"url"`
		Snippet  string `json:"snippet"`
		SiteName string `json:"site_name"`
		Date     string `json:"date"`
	} `json:"results"`
}

// Search runs a query and returns at most limit results. The API has no limit
// parameter, so results are sliced client-side; limit <= 0 returns everything.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	endpoint, err := url.Parse(c.searchURL)
	if err != nil {
		return nil, fmt.Errorf("tinyfish search: %w", err)
	}
	params := endpoint.Query()
	params.Set("query", query)
	params.Set("page", "1")
	endpoint.RawQuery = params.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("tinyfish search: %w", err)
	}
	request.Header.Set("X-API-Key", c.apiKey)
	request.Header.Set("Accept", "application/json")

	var decoded searchResponse
	if err := c.do(request, &decoded); err != nil {
		return nil, fmt.Errorf("tinyfish search: %w", err)
	}

	results := make([]SearchResult, 0, len(decoded.Results))
	for _, item := range decoded.Results {
		if limit > 0 && len(results) >= limit {
			break
		}
		results = append(results, SearchResult{
			Position: item.Position,
			Title:    item.Title,
			URL:      item.URL,
			Snippet:  item.Snippet,
			SiteName: item.SiteName,
			Date:     item.Date,
		})
	}
	return results, nil
}

// fetchResponse is the Fetch payload. Each entry is either a success shape or
// an error shape carrying a non-empty "error". Null string fields decode to "".
type fetchResponse struct {
	Results []struct {
		Error         string `json:"error"`
		URL           string `json:"url"`
		FinalURL      string `json:"final_url"`
		Title         string `json:"title"`
		Description   string `json:"description"`
		PublishedDate string `json:"published_date"`
		Format        string `json:"format"`
		Text          string `json:"text"`
	} `json:"results"`
}

// Fetch retrieves one URL as markdown content.
func (c *Client) Fetch(ctx context.Context, rawURL string) (FetchResult, error) {
	payload, err := json.Marshal(map[string]any{
		"urls":   []string{rawURL},
		"format": "markdown",
	})
	if err != nil {
		return FetchResult{}, fmt.Errorf("tinyfish fetch: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.fetchURL, bytes.NewReader(payload))
	if err != nil {
		return FetchResult{}, fmt.Errorf("tinyfish fetch: %w", err)
	}
	request.Header.Set("X-API-Key", c.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")

	var decoded fetchResponse
	if err := c.do(request, &decoded); err != nil {
		return FetchResult{}, fmt.Errorf("tinyfish fetch: %w", err)
	}
	if len(decoded.Results) == 0 {
		return FetchResult{}, fmt.Errorf("tinyfish fetch: response contained no result")
	}

	result := decoded.Results[0]
	if result.Error != "" {
		return FetchResult{}, fmt.Errorf("tinyfish fetch: %s", result.Error)
	}
	return FetchResult{
		URL:           result.URL,
		FinalURL:      result.FinalURL,
		Title:         result.Title,
		Description:   result.Description,
		PublishedDate: result.PublishedDate,
		Format:        result.Format,
		Text:          result.Text,
	}, nil
}

// do sends the request and decodes a JSON body. It never includes the API key
// in an error: the key only ever travels in the request header.
func (c *Client) do(request *http.Request, out any) error {
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", response.StatusCode, truncate(raw, 300))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func truncate(raw []byte, max int) string {
	if len(raw) > max {
		return string(raw[:max])
	}
	return string(raw)
}
