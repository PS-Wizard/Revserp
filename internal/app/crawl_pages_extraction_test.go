package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestBuildCrawlPageResponseIncludesExtractionProvenance(t *testing.T) {
	row := crawlPageRowData{
		Url:                     "https://example.com/article",
		Title:                   pgText("Article"),
		ContentExtractor:        pgText("go-trafilatura/v2@2.2.6"),
		ContentExtractionStatus: pgText("ok"),
	}

	response := buildCrawlPageResponse(row)
	if response.ContentExtractor != "go-trafilatura/v2@2.2.6" {
		t.Fatalf("got content_extractor %q, want engine version", response.ContentExtractor)
	}
	if response.ContentExtractionStatus != "ok" {
		t.Fatalf("got content_extraction_status %q, want ok", response.ContentExtractionStatus)
	}

	body, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"content_extractor"`, `"content_extraction_status"`} {
		if !strings.Contains(string(body), key) {
			t.Fatalf("detail response should include %s, got %s", key, body)
		}
	}
}

func TestBuildCrawlPageResponseOmitsMissingExtractionProvenance(t *testing.T) {
	// Legacy rows predate the extractor: NULL provenance stays empty and is
	// omitted from the JSON payload.
	row := crawlPageRowData{
		Url:   "https://example.com/legacy",
		Title: pgText("Legacy"),
	}

	response := buildCrawlPageResponse(row)
	if response.ContentExtractor != "" || response.ContentExtractionStatus != "" {
		t.Fatalf("got provenance %q/%q, want empty", response.ContentExtractor, response.ContentExtractionStatus)
	}

	body, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"content_extractor"`, `"content_extraction_status"`} {
		if strings.Contains(string(body), key) {
			t.Fatalf("detail response should omit %s for legacy rows, got %s", key, body)
		}
	}
}

func TestNewCrawlPageResponseFromGetRowCarriesExtractionProvenance(t *testing.T) {
	var id, crawlID pgtype.UUID
	_ = id.Scan("00000000-0000-0000-0000-000000000010")
	_ = crawlID.Scan("00000000-0000-0000-0000-000000000002")

	response := newCrawlPageResponseFromGetRow(sqlc.GetCrawlPageByIDForUserRow{
		ID:                      id,
		CrawlID:                 crawlID,
		Url:                     "https://example.com/article",
		ContentExtractor:        pgText("go-trafilatura/v2@2.2.6"),
		ContentExtractionStatus: pgText("failed"),
	})
	if response.ContentExtractor != "go-trafilatura/v2@2.2.6" {
		t.Fatalf("got content_extractor %q, want engine version", response.ContentExtractor)
	}
	if response.ContentExtractionStatus != "failed" {
		t.Fatalf("got content_extraction_status %q, want failed", response.ContentExtractionStatus)
	}
}
