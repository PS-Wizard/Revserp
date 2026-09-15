package tinyfish

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewClient("test-key", server.URL, server.URL, time.Second), server
}

func TestSearchHappyPath(t *testing.T) {
	var gotQuery, gotPage, gotKey string
	body := `{"results":[
		{"position":1,"title":"A","url":"https://a.example","snippet":"sa","site_name":"A","date":"2024-01-01"},
		{"position":2,"title":"B","url":"https://b.example","snippet":"sb","site_name":"B","date":null},
		{"position":3,"title":"C","url":"https://c.example","snippet":"sc","site_name":"C","date":null}
	]}`
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		gotPage = r.URL.Query().Get("page")
		gotKey = r.Header.Get("X-API-Key")
		_, _ = io.WriteString(w, body)
	})

	t.Run("all results when limit zero", func(t *testing.T) {
		results, err := client.Search(context.Background(), "golang", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotQuery != "golang" || gotPage != "1" {
			t.Fatalf("params: query=%q page=%q", gotQuery, gotPage)
		}
		if gotKey != "test-key" {
			t.Fatalf("X-API-Key header = %q", gotKey)
		}
		if len(results) != 3 {
			t.Fatalf("got %d results, want 3", len(results))
		}
		first := results[0]
		if first.Position != 1 || first.Title != "A" || first.URL != "https://a.example" ||
			first.Snippet != "sa" || first.SiteName != "A" || first.Date != "2024-01-01" {
			t.Fatalf("mapping wrong: %+v", first)
		}
		if results[1].Date != "" {
			t.Fatalf("null date should map to empty string, got %q", results[1].Date)
		}
	})

	t.Run("slices to limit", func(t *testing.T) {
		results, err := client.Search(context.Background(), "golang", 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("got %d results, want 2", len(results))
		}
	})
}

func TestFetchHappyPath(t *testing.T) {
	var gotUrls []string
	var gotFormat, gotKey, gotContentType string
	body := `{"results":[{
		"url":"https://a.example","final_url":"https://a.example/final","title":"A",
		"description":"desc","published_date":null,"format":"markdown","text":null
	}]}`
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if urls, ok := req["urls"].([]any); ok {
			for _, u := range urls {
				gotUrls = append(gotUrls, u.(string))
			}
		}
		gotFormat, _ = req["format"].(string)
		gotKey = r.Header.Get("X-API-Key")
		gotContentType = r.Header.Get("Content-Type")
		_, _ = io.WriteString(w, body)
	})

	result, err := client.Fetch(context.Background(), "https://a.example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotUrls) != 1 || gotUrls[0] != "https://a.example" || gotFormat != "markdown" {
		t.Fatalf("request body wrong: urls=%v format=%q", gotUrls, gotFormat)
	}
	if gotKey != "test-key" {
		t.Fatalf("X-API-Key header = %q", gotKey)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type header = %q", gotContentType)
	}
	want := FetchResult{
		URL:         "https://a.example",
		FinalURL:    "https://a.example/final",
		Title:       "A",
		Description: "desc",
		Format:      "markdown",
	}
	if result != want {
		t.Fatalf("mapping wrong:\n got %+v\nwant %+v", result, want)
	}
	if result.Text != "" || result.PublishedDate != "" {
		t.Fatalf("null fields should be empty, got text=%q date=%q", result.Text, result.PublishedDate)
	}
}

func TestFetchErrorShape(t *testing.T) {
	body := `{"results":[{"url":"https://a.example","error":"could not reach host"}]}`
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	})

	_, err := client.Fetch(context.Background(), "https://a.example")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "could not reach host") {
		t.Fatalf("error should surface API message, got %v", err)
	}
}

func TestFetchEmptyResults(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	})

	_, err := client.Fetch(context.Background(), "https://a.example")
	if err == nil || !strings.Contains(err.Error(), "no result") {
		t.Fatalf("expected no-result error, got %v", err)
	}
}

func TestNon2xx(t *testing.T) {
	tests := []struct {
		name string
		call func(*Client) error
	}{
		{"search", func(c *Client) error { _, err := c.Search(context.Background(), "x", 0); return err }},
		{"fetch", func(c *Client) error { _, err := c.Fetch(context.Background(), "x"); return err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusTeapot)
				_, _ = io.WriteString(w, `{"error":"boom"}`)
			})
			err := tc.call(client)
			if err == nil || !strings.Contains(err.Error(), "418") {
				t.Fatalf("expected status code in error, got %v", err)
			}
		})
	}
}

func TestMalformedJSON(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{not json`)
	})

	if _, err := client.Search(context.Background(), "x", 0); err == nil {
		t.Fatal("search: expected decode error")
	}
	if _, err := client.Fetch(context.Background(), "x"); err == nil {
		t.Fatal("fetch: expected decode error")
	}
}

func TestNewClientDefaultTimeout(t *testing.T) {
	client := NewClient("k", "http://a", "http://b", 0)
	if client.http.Timeout != defaultTimeout {
		t.Fatalf("timeout = %v, want %v", client.http.Timeout, defaultTimeout)
	}
}
