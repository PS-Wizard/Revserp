package sqlc

import (
	"strings"
	"testing"
)

func TestListCrawlLinksForCrawlByUserResolvesOverPagedRows(t *testing.T) {
	for _, want := range []string{
		"WITH page AS MATERIALIZED",
		"om.user_id = $2",
		"ORDER BY cl.created_at ASC",
		"LIMIT $3",
		"OFFSET $4",
		"digest(cp.url_key, 'sha256') = digest(page.target_url_key, 'sha256')",
		"cp.url_key = page.target_url_key",
		"cp.crawl_id = page.crawl_id",
		"page.is_internal IS TRUE",
		"AS target_status",
		"ORDER BY page.created_at ASC",
	} {
		if !strings.Contains(listCrawlLinksForCrawlByUser, want) {
			t.Errorf("list SQL missing %q", want)
		}
	}
	limitAt := strings.Index(listCrawlLinksForCrawlByUser, "LIMIT $3")
	pagesAt := strings.Index(listCrawlLinksForCrawlByUser, "FROM crawl_pages")
	if limitAt < 0 || pagesAt < 0 || limitAt > pagesAt {
		t.Fatal("list SQL must paginate inside the page CTE before touching crawl_pages")
	}
	if strings.Contains(listCrawlLinksForCrawlByUser, "UPDATE crawl_links") {
		t.Fatal("list SQL must stay read-only: no status mass-update")
	}
}

func TestGetCrawlLinkByIDForUserDerivesSameCrawlStatus(t *testing.T) {
	for _, want := range []string{
		"om.user_id = $2",
		"cp.crawl_id = cl.crawl_id",
		"digest(cp.url_key, 'sha256') = digest(cl.target_url_key, 'sha256')",
		"cp.url_key = cl.target_url_key",
		"cl.is_internal IS TRUE",
		"AS target_status",
		"LIMIT 1",
	} {
		if !strings.Contains(getCrawlLinkByIDForUser, want) {
			t.Errorf("get SQL missing %q", want)
		}
	}
	if strings.Contains(getCrawlLinkByIDForUser, "UPDATE crawl_links") {
		t.Fatal("get SQL must stay read-only: no status mass-update")
	}
}

func TestListInternalLinkFactsForCrawlStaysPlainScan(t *testing.T) {
	const columns = "SELECT source_url, target_url, target_url_key, target_status"
	if !strings.Contains(listInternalLinkFactsForCrawl, columns) {
		t.Errorf("facts SQL missing %q", columns)
	}
	for _, want := range []string{"is_internal = TRUE", "ORDER BY created_at ASC"} {
		if !strings.Contains(listInternalLinkFactsForCrawl, want) {
			t.Errorf("facts SQL missing %q", want)
		}
	}
	if strings.Contains(listInternalLinkFactsForCrawl, "crawl_pages") {
		t.Fatal("facts SQL must not join pages: the lookup applies the rule in memory")
	}
}

func TestListInternalCrawlLinksForCrawlKeepsRawLinks(t *testing.T) {
	if strings.Contains(listInternalCrawlLinksForCrawl, "crawl_pages") {
		t.Fatal("site-graph links must stay raw: no status computation that slows the scan")
	}
}
