package app

import (
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestLoadWorkspaceDiffRequiresCurrentEvidence(t *testing.T) {
	_, pool, ctx := newFeaturesTestQueries(t)
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('workspace-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID) })
	orgID := createFeaturesTestOrg(t, ctx, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}
	var projectID, baselineID, currentID, baselinePageID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1, 'workspace-test', 'https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at) VALUES ($1, 'completed', now() - interval '1 hour') RETURNING id`, projectID).Scan(&baselineID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at) VALUES ($1, 'completed', now()) RETURNING id`, projectID).Scan(&currentID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1, 'https://example.com/a', 200) RETURNING id`, baselineID).Scan(&baselinePageID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO crawl_issues (crawl_id, crawl_page_id, url, pillar, bucket, issue_type, severity, message, details) VALUES ($1, $2, 'https://example.com/a', 'seo', 'serp_metadata', 'missing_title', 'high', 'missing', 'missing')`, baselineID, baselinePageID); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest("GET", "/", nil)
	rows, err := (&App{DB: pool}).loadWorkspaceDiff(request, baselineID, currentID, userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ChangeType != "not_verified" {
		t.Fatalf("got %#v", rows)
	}
	compareParams := sqlc.ListCompareCrawlIssueURLsByTypeForCrawlByUserParams{
		CrawlID: baselineID, CrawlID_2: currentID, UserID: userID, Pillar: "seo", Bucket: "serp_metadata", IssueType: "missing_title", Column7: "", Limit: 10,
	}
	compared, err := sqlc.New(pool).ListCompareCrawlIssueURLsByTypeForCrawlByUser(ctx, compareParams)
	if err != nil {
		t.Fatal(err)
	}
	if len(compared) != 1 || compared[0].ChangeType != "not_verified" {
		t.Fatalf("got compare rows %#v", compared)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1, 'https://example.com/a', 200)`, currentID); err != nil {
		t.Fatal(err)
	}
	rows, err = (&App{DB: pool}).loadWorkspaceDiff(request, baselineID, currentID, userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ChangeType != "no_longer_detected" {
		t.Fatalf("got %#v", rows)
	}
	compared, err = sqlc.New(pool).ListCompareCrawlIssueURLsByTypeForCrawlByUser(ctx, compareParams)
	if err != nil {
		t.Fatal(err)
	}
	if len(compared) != 1 || compared[0].ChangeType != "no_longer_detected" {
		t.Fatalf("got compare rows %#v", compared)
	}

}
