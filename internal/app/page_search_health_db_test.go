package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestPageSearchHealthQueryContracts(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('page-search-health-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}

	var outsiderID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('page-search-health-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&outsiderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, outsiderID) })

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'page-search-health-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}

	var crawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now()) RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatal(err)
	}
	var otherCrawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now()) RETURNING id`, projectID).Scan(&otherCrawlID); err != nil {
		t.Fatal(err)
	}

	insertPage := func(url, title string, healthScore *int16) pgtype.UUID {
		t.Helper()
		var id pgtype.UUID
		var hs pgtype.Int2
		if healthScore != nil {
			hs = pgtype.Int2{Int16: *healthScore, Valid: true}
		}
		if err := pool.QueryRow(ctx, `INSERT INTO crawl_pages (crawl_id, url, title, health_score, status_code) VALUES ($1,$2,$3,$4,200) RETURNING id`, crawlID, url, pgText(title), hs).Scan(&id); err != nil {
			t.Fatalf("insert page %s: %v", url, err)
		}
		return id
	}

	score87 := int16(87)
	score92 := int16(92)
	pagePricing := insertPage("https://example.com/pricing", "Pricing Page", &score87)
	pageAbout := insertPage("https://example.com/about", "About Us", nil)
	pageSpecial := insertPage("https://example.com/blog_100%_special", "100% Special_Blog", &score92)

	// --- Search: case-insensitive URL/title ---
	rows, err := queries.SearchCrawlPagesForUser(ctx, sqlc.SearchCrawlPagesForUserParams{CrawlID: crawlID, UserID: userID, Query: "pricing", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("search pricing: %v", err)
	}
	if len(rows) != 1 || rows[0].Url != "https://example.com/pricing" {
		t.Fatalf("case-insensitive url search pricing got %#v want 1 pricing row", rows)
	}

	rows, err = queries.SearchCrawlPagesForUser(ctx, sqlc.SearchCrawlPagesForUserParams{CrawlID: crawlID, UserID: userID, Query: "PRICING", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("search PRICING: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("case-insensitive upper PRICING got %d want 1", len(rows))
	}

	rows, err = queries.SearchCrawlPagesForUser(ctx, sqlc.SearchCrawlPagesForUserParams{CrawlID: crawlID, UserID: userID, Query: "about us", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("search title: %v", err)
	}
	if len(rows) != 1 || rows[0].Url != "https://example.com/about" {
		t.Fatalf("title search got %#v", rows)
	}

	// Literal, not LIKE wildcard: "%" should match only the special page, not all
	rows, err = queries.SearchCrawlPagesForUser(ctx, sqlc.SearchCrawlPagesForUserParams{CrawlID: crawlID, UserID: userID, Query: "%", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("search %%: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(strings.ToLower(rows[0].Url), "%") {
		t.Fatalf("literal %% search should match only special page, got %#v", rows)
	}

	rows, err = queries.SearchCrawlPagesForUser(ctx, sqlc.SearchCrawlPagesForUserParams{CrawlID: crawlID, UserID: userID, Query: "_", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("search _: %v", err)
	}
	if len(rows) != 1 || !strings.Contains(rows[0].Url, "_") {
		t.Fatalf("literal _ search got %#v", rows)
	}

	// Empty query returns all in relevance order (empty => URL ASC)
	rows, err = queries.SearchCrawlPagesForUser(ctx, sqlc.SearchCrawlPagesForUserParams{CrawlID: crawlID, UserID: userID, Query: "", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("empty query len %d want 3", len(rows))
	}
	// URLs ASC when query empty (lowercase URLs to avoid collation ambiguity)
	if rows[0].Url != "https://example.com/about" || rows[1].Url != "https://example.com/blog_100%_special" || rows[2].Url != "https://example.com/pricing" {
		t.Fatalf("empty query order wrong %#v", rows)
	}

	// Count reports full filtered total independent of offset/limit
	total, err := queries.CountCrawlPagesSearchForUser(ctx, sqlc.CountCrawlPagesSearchForUserParams{CrawlID: crawlID, UserID: userID, Query: ""})
	if err != nil {
		t.Fatalf("count all: %v", err)
	}
	if total != 3 {
		t.Fatalf("count all %d want 3", total)
	}
	// With offset past end, count still 3 but list empty
	rows, err = queries.SearchCrawlPagesForUser(ctx, sqlc.SearchCrawlPagesForUserParams{CrawlID: crawlID, UserID: userID, Query: "", Limit: 10, Offset: 10})
	if err != nil {
		t.Fatalf("offset past end: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("past offset should be empty got %d", len(rows))
	}
	total2, err := queries.CountCrawlPagesSearchForUser(ctx, sqlc.CountCrawlPagesSearchForUserParams{CrawlID: crawlID, UserID: userID, Query: ""})
	if err != nil {
		t.Fatalf("count offset past: %v", err)
	}
	if total2 != 3 {
		t.Fatalf("count with offset independent %d want 3", total2)
	}
	// Limit 1 should not affect count
	rows, err = queries.SearchCrawlPagesForUser(ctx, sqlc.SearchCrawlPagesForUserParams{CrawlID: crawlID, UserID: userID, Query: "", Limit: 1, Offset: 0})
	if err != nil {
		t.Fatalf("limit 1: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("limit 1 got %d", len(rows))
	}
	total3, err := queries.CountCrawlPagesSearchForUser(ctx, sqlc.CountCrawlPagesSearchForUserParams{CrawlID: crawlID, UserID: userID, Query: ""})
	if err != nil {
		t.Fatalf("count limit 1: %v", err)
	}
	if total3 != 3 {
		t.Fatalf("count limit independent %d want 3", total3)
	}

	// Filtered count
	totalPricing, err := queries.CountCrawlPagesSearchForUser(ctx, sqlc.CountCrawlPagesSearchForUserParams{CrawlID: crawlID, UserID: userID, Query: "pricing"})
	if err != nil {
		t.Fatalf("count pricing: %v", err)
	}
	if totalPricing != 1 {
		t.Fatalf("count pricing %d want 1", totalPricing)
	}

	// GetCrawlPageHealthForUser returns 87
	row, err := queries.GetCrawlPageHealthForUser(ctx, sqlc.GetCrawlPageHealthForUserParams{CrawlID: crawlID, PageID: pagePricing, UserID: userID})
	if err != nil {
		t.Fatalf("health 87: %v", err)
	}
	if !row.HealthScore.Valid || row.HealthScore.Int16 != 87 {
		t.Fatalf("health score got %+v want 87", row.HealthScore)
	}
	if row.Url != "https://example.com/pricing" {
		t.Fatalf("health url %q", row.Url)
	}
	// Also check special page 92
	row, err = queries.GetCrawlPageHealthForUser(ctx, sqlc.GetCrawlPageHealthForUserParams{CrawlID: crawlID, PageID: pageSpecial, UserID: userID})
	if err != nil {
		t.Fatalf("health 92: %v", err)
	}
	if row.HealthScore.Int16 != 92 {
		t.Fatalf("health 92 got %d", row.HealthScore.Int16)
	}

	// NULL score -> ErrNoRows
	_, err = queries.GetCrawlPageHealthForUser(ctx, sqlc.GetCrawlPageHealthForUserParams{CrawlID: crawlID, PageID: pageAbout, UserID: userID})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("NULL score should be ErrNoRows got %v", err)
	}

	// Wrong crawl ID
	_, err = queries.GetCrawlPageHealthForUser(ctx, sqlc.GetCrawlPageHealthForUserParams{CrawlID: otherCrawlID, PageID: pagePricing, UserID: userID})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wrong crawl should be ErrNoRows got %v", err)
	}

	// Outside org
	_, err = queries.GetCrawlPageHealthForUser(ctx, sqlc.GetCrawlPageHealthForUserParams{CrawlID: crawlID, PageID: pagePricing, UserID: outsiderID})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("outsider should be ErrNoRows got %v", err)
	}

	// Also verify outsider cannot search
	rows, err = queries.SearchCrawlPagesForUser(ctx, sqlc.SearchCrawlPagesForUserParams{CrawlID: crawlID, UserID: outsiderID, Query: "", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("outsider search: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("outsider search should be empty got %d", len(rows))
	}
	totalOut, err := queries.CountCrawlPagesSearchForUser(ctx, sqlc.CountCrawlPagesSearchForUserParams{CrawlID: crawlID, UserID: outsiderID, Query: ""})
	if err != nil {
		t.Fatalf("outsider count: %v", err)
	}
	if totalOut != 0 {
		t.Fatalf("outsider count %d want 0", totalOut)
	}
}
