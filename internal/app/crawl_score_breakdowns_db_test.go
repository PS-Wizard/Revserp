package app

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestScoreBreakdownIssueURLsWorkFilteringIntegration(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('score-breakdown-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}
	var otherUser pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('score-breakdown-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&otherUser); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, otherUser) })
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, otherUser); err != nil {
		t.Fatal(err)
	}

	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'score-breakdown-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatal(err)
	}

	// Two completed crawls: historical and latest. Latest has later completed_at.
	var historicalCrawlID, latestCrawlID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now() - interval '2 hour', now() - interval '3 hour') RETURNING id`, projectID).Scan(&historicalCrawlID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at, created_at) VALUES ($1,'completed', now(), now() - interval '1 hour') RETURNING id`, projectID).Scan(&latestCrawlID); err != nil {
		t.Fatal(err)
	}

	// Verify work_actions_enabled logic: only latest should be enabled.
	latestID, err := queries.GetLatestCompletedCrawlIDForScoreBreakdown(ctx, projectID)
	if err != nil {
		t.Fatalf("GetLatestCompletedCrawlIDForScoreBreakdown: %v", err)
	}
	if latestID != latestCrawlID {
		t.Fatalf("latest = %s want %s", latestID.String(), latestCrawlID.String())
	}
	if latestID == historicalCrawlID {
		t.Fatalf("historical should not be latest")
	}

	// Pages for latest crawl.
	for _, u := range []string{"https://example.com/a", "https://example.com/b", "https://example.com/c", "https://example.com/d", "https://example.com/e"} {
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1,$2,200)`, latestCrawlID, u); err != nil {
			t.Fatal(err)
		}
	}
	// Issue rows for latest crawl: 5 distinct URLs, one duplicate severity test.
	// Use pillar seo, bucket serp_metadata, issue_type missing_title
	pillar, bucket, issueType := "seo", "serp_metadata", "missing_title"
	insertIssue := func(url, severity string) pgtype.UUID {
		var id pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO crawl_issues (crawl_id, url, pillar, bucket, issue_type, severity, message, details) VALUES ($1,$2,$3,$4,$5,$6,'msg','det') RETURNING id`, latestCrawlID, url, pillar, bucket, issueType, severity).Scan(&id); err != nil {
			t.Fatalf("insert issue %s: %v", url, err)
		}
		return id
	}
	_ = insertIssue("https://example.com/a", "high") // no work
	_ = insertIssue("https://example.com/b", "high") // awaiting
	_ = insertIssue("https://example.com/c", "high") // still_open
	_ = insertIssue("https://example.com/d", "high") // fixed (latest)
	_ = insertIssue("https://example.com/e", "low")  // duplicate url: low then high, should keep high
	_ = insertIssue("https://example.com/e", "high")

	// Helper to create work item+attempt
	createWork := func(url, subjectKind, subjectKey, status string, locked bool, contributor pgtype.UUID) pgtype.UUID {
		var workItemID pgtype.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO issue_work_items (project_id, subject_kind, subject_key, pillar, bucket, issue_type) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (project_id, subject_kind, subject_key, pillar, bucket, issue_type) DO UPDATE SET updated_at=now() RETURNING id`, projectID, subjectKind, subjectKey, pillar, bucket, issueType).Scan(&workItemID); err != nil {
			t.Fatalf("work item %s: %v", url, err)
		}
		var attemptID pgtype.UUID
		// Use non-null source_crawl_id (latest) for attempts
		if locked {
			if err := pool.QueryRow(ctx, `INSERT INTO issue_work_attempts (work_item_id, source_crawl_id, status, locked_at) VALUES ($1,$2,$3, now()) RETURNING id`, workItemID, latestCrawlID, status).Scan(&attemptID); err != nil {
				t.Fatalf("attempt locked %s: %v", url, err)
			}
		} else {
			if err := pool.QueryRow(ctx, `INSERT INTO issue_work_attempts (work_item_id, source_crawl_id, status) VALUES ($1,$2,$3) RETURNING id`, workItemID, latestCrawlID, status).Scan(&attemptID); err != nil {
				t.Fatalf("attempt %s: %v", url, err)
			}
		}
		if contributor.Valid {
			if _, err := pool.Exec(ctx, `INSERT INTO issue_work_attempt_contributors (attempt_id, user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, attemptID, contributor); err != nil {
				t.Fatalf("contributor %s: %v", url, err)
			}
		}
		return attemptID
	}

	// b: awaiting_verification, contributed by me, locked
	bAttempt := createWork("https://example.com/b", "page", "https://example.com/b", "awaiting_verification", true, userID)
	_ = bAttempt
	// c: still_open, no contributor, not locked
	createWork("https://example.com/c", "page", "https://example.com/c", "still_open", false, pgtype.UUID{})
	// d: two attempts, older awaiting, newer fixed (latest fixed should exclude from marked_done)
	var dWorkItem pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO issue_work_items (project_id, subject_kind, subject_key, pillar, bucket, issue_type) VALUES ($1,'page',$2,$3,$4,$5) ON CONFLICT (project_id, subject_kind, subject_key, pillar, bucket, issue_type) DO UPDATE SET updated_at=now() RETURNING id`, projectID, "https://example.com/d", pillar, bucket, issueType).Scan(&dWorkItem); err != nil {
		t.Fatal(err)
	}
	var olderID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO issue_work_attempts (work_item_id, source_crawl_id, status, created_at) VALUES ($1,$2,'awaiting_verification', now() - interval '1 hour') RETURNING id`, dWorkItem, latestCrawlID).Scan(&olderID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO issue_work_attempt_contributors (attempt_id, user_id) VALUES ($1,$2)`, olderID, userID); err != nil {
		t.Fatal(err)
	}
	var newerID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO issue_work_attempts (work_item_id, source_crawl_id, status, created_at) VALUES ($1,$2,'fixed', now()) RETURNING id`, dWorkItem, latestCrawlID).Scan(&newerID); err != nil {
		t.Fatal(err)
	}
	_ = newerID
	// e: also create work for e to test distinct counting (high severity should win, work on e)
	createWork("https://example.com/e", "page", "https://example.com/e", "not_verified", false, otherUser)

	// Helper to count
	count := func(workStatus string) int64 {
		t.Helper()
		n, err := queries.CountDistinctCrawlIssueURLsByTypeForCrawlByUser(ctx, sqlc.CountDistinctCrawlIssueURLsByTypeForCrawlByUserParams{
			CrawlID:    latestCrawlID,
			UserID:     userID,
			Pillar:     pillar,
			Bucket:     bucket,
			IssueType:  issueType,
			WorkStatus: workStatus,
		})
		if err != nil {
			t.Fatalf("count %s: %v", workStatus, err)
		}
		return n
	}
	if got := count("all"); got != 5 {
		t.Fatalf("count all = %d want 5", got)
	}
	// marked_done = awaiting_verification + not_verified = b and e => 2 (d is fixed, should not count)
	if got := count("marked_done"); got != 2 {
		t.Fatalf("count marked_done = %d want 2", got)
	}
	// needs_action = rest = a,c,d => 3
	if got := count("needs_action"); got != 3 {
		t.Fatalf("count needs_action = %d want 3", got)
	}

	// List all
	rows, err := queries.ListDistinctCrawlIssueURLsByTypeForCrawlByUser(ctx, sqlc.ListDistinctCrawlIssueURLsByTypeForCrawlByUserParams{
		CrawlID:    latestCrawlID,
		UserID:     userID,
		Pillar:     pillar,
		Bucket:     bucket,
		IssueType:  issueType,
		WorkStatus: "all",
		Limit:      10,
		Offset:     0,
	})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("list all len=%d want 5", len(rows))
	}
	// Ensure ordered by url asc
	if rows[0].Url != "https://example.com/a" || rows[1].Url != "https://example.com/b" {
		t.Fatalf("ordering wrong %#v", rows)
	}
	// Check distinct severity for e is high (max)
	for _, r := range rows {
		if r.Url == "https://example.com/e" && r.Severity != "high" {
			t.Fatalf("duplicate url severity should be high, got %s", r.Severity)
		}
	}
	// Check work fields
	byURL := map[string]sqlc.ListDistinctCrawlIssueURLsByTypeForCrawlByUserRow{}
	for _, r := range rows {
		byURL[r.Url] = r
	}
	// b should be awaiting, locked, contributed_by_me true
	br := byURL["https://example.com/b"]
	if br.WorkStatus != "awaiting_verification" || !br.WorkLocked || !br.ContributedByMe || br.WorkAttemptID == "" {
		t.Fatalf("b work wrong: %+v", br)
	}
	// c still_open, not locked, not contributed
	cr := byURL["https://example.com/c"]
	if cr.WorkStatus != "still_open" || cr.WorkLocked || cr.ContributedByMe {
		t.Fatalf("c work wrong: %+v", cr)
	}
	// d latest fixed => work should be empty (null -> empty string), not counted as marked_done
	dr := byURL["https://example.com/d"]
	if dr.WorkStatus != "" || dr.WorkAttemptID != "" || dr.WorkLocked || dr.ContributedByMe {
		t.Fatalf("d fixed should have no active work, got %+v", dr)
	}
	// e not_verified but contributed by other user, so contributed_by_me false for our user
	er := byURL["https://example.com/e"]
	if er.WorkStatus != "not_verified" || er.ContributedByMe {
		t.Fatalf("e work wrong: %+v", er)
	}
	if er.WorkLocked {
		t.Fatalf("e should not be locked")
	}

	// Pagination: limit 2 offset 1 should give b,c
	paged, err := queries.ListDistinctCrawlIssueURLsByTypeForCrawlByUser(ctx, sqlc.ListDistinctCrawlIssueURLsByTypeForCrawlByUserParams{
		CrawlID:    latestCrawlID,
		UserID:     userID,
		Pillar:     pillar,
		Bucket:     bucket,
		IssueType:  issueType,
		WorkStatus: "all",
		Limit:      2,
		Offset:     1,
	})
	if err != nil {
		t.Fatalf("paged: %v", err)
	}
	if len(paged) != 2 || paged[0].Url != "https://example.com/b" || paged[1].Url != "https://example.com/c" {
		t.Fatalf("pagination wrong: %#v", paged)
	}

	// Filtered lists should match counts
	for _, ws := range []string{"marked_done", "needs_action"} {
		n := count(ws)
		l, err := queries.ListDistinctCrawlIssueURLsByTypeForCrawlByUser(ctx, sqlc.ListDistinctCrawlIssueURLsByTypeForCrawlByUserParams{
			CrawlID:    latestCrawlID,
			UserID:     userID,
			Pillar:     pillar,
			Bucket:     bucket,
			IssueType:  issueType,
			WorkStatus: ws,
			Limit:      10,
			Offset:     0,
		})
		if err != nil {
			t.Fatalf("list %s: %v", ws, err)
		}
		if int64(len(l)) != n {
			t.Fatalf("list %s len %d != count %d", ws, len(l), n)
		}
	}

	// Tenancy: other user cannot see same counts without membership? Already owner, but test isolation via different project
	// Ensure count for non-member returns 0 (create issue in other project)
	var otherProject pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'other','https://other.example.com') RETURNING id`, orgID).Scan(&otherProject); err != nil {
		t.Fatal(err)
	}
	var otherCrawl pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status, completed_at) VALUES ($1,'completed', now()) RETURNING id`, otherProject).Scan(&otherCrawl); err != nil {
		t.Fatal(err)
	}
	otherCount, err := queries.CountDistinctCrawlIssueURLsByTypeForCrawlByUser(ctx, sqlc.CountDistinctCrawlIssueURLsByTypeForCrawlByUserParams{
		CrawlID:    otherCrawl,
		UserID:     userID,
		Pillar:     pillar,
		Bucket:     bucket,
		IssueType:  issueType,
		WorkStatus: "all",
	})
	if err != nil {
		t.Fatalf("other count: %v", err)
	}
	if otherCount != 0 {
		t.Fatalf("other project count should be 0, got %d", otherCount)
	}
}
