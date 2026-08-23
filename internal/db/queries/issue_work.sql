-- name: GetWorkableCrawlIssueByIDForUser :one
SELECT
    ci.id,
    ci.crawl_id,
    ci.crawl_page_id,
    ci.url,
    ci.pillar,
    ci.bucket,
    ci.issue_type,
    ci.issue_group_id,
    p.id AS project_id
FROM crawl_issues AS ci
INNER JOIN crawls AS c ON c.id = ci.crawl_id
INNER JOIN projects AS p ON p.id = c.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE ci.id = sqlc.arg(issue_id)
  AND om.user_id = sqlc.arg(user_id)
  AND c.status = 'completed'
  AND c.id = (
      SELECT latest.id FROM crawls AS latest
      WHERE latest.project_id = c.project_id AND latest.status = 'completed'
      ORDER BY latest.completed_at DESC NULLS LAST, latest.created_at DESC, latest.id DESC
      LIMIT 1
  )
LIMIT 1;

-- name: UpsertIssueWorkItem :one
INSERT INTO issue_work_items (
    project_id,
    subject_kind,
    subject_key,
    pillar,
    bucket,
    issue_type,
    source_crawl_issue_id,
    source_issue_group_id
) VALUES (
    sqlc.arg(project_id), sqlc.arg(subject_kind), sqlc.arg(subject_key),
    sqlc.arg(pillar), sqlc.arg(bucket), sqlc.arg(issue_type),
    sqlc.arg(source_crawl_issue_id), sqlc.narg(source_issue_group_id)
)
ON CONFLICT (project_id, subject_kind, subject_key, pillar, bucket, issue_type)
DO UPDATE SET
    source_crawl_issue_id = EXCLUDED.source_crawl_issue_id,
    source_issue_group_id = EXCLUDED.source_issue_group_id,
    updated_at = now()
RETURNING *;

-- name: GetActiveIssueWorkAttemptForWorkItem :one
SELECT *
FROM issue_work_attempts
WHERE work_item_id = $1
  AND status IN ('awaiting_verification', 'not_verified')
  AND locked_at IS NULL
ORDER BY created_at DESC
LIMIT 1;

-- name: CreateIssueWorkAttempt :one
INSERT INTO issue_work_attempts (work_item_id, source_crawl_id)
VALUES ($1, $2)
RETURNING *;

-- name: AddIssueWorkAttemptContributor :one
INSERT INTO issue_work_attempt_contributors (attempt_id, user_id)
SELECT $1, $2
FROM issue_work_attempts AS attempt
WHERE attempt.id = $1
  AND attempt.locked_at IS NULL
  AND attempt.status IN ('awaiting_verification', 'not_verified')
FOR KEY SHARE OF attempt
ON CONFLICT (attempt_id, user_id) DO UPDATE
SET marked_done_at = issue_work_attempt_contributors.marked_done_at
RETURNING attempt_id, user_id, marked_done_at;

-- name: ListIssueWorkAttemptContributors :many
SELECT attempt_id, user_id, marked_done_at
FROM issue_work_attempt_contributors
WHERE attempt_id = $1
ORDER BY marked_done_at ASC;

-- name: RemoveIssueWorkAttemptContributor :execrows
DELETE FROM issue_work_attempt_contributors AS contributor
USING issue_work_attempts AS attempt
WHERE contributor.attempt_id = $1
  AND contributor.user_id = $2
  AND attempt.id = contributor.attempt_id
  AND attempt.locked_at IS NULL
  AND attempt.status IN ('awaiting_verification', 'not_verified');

-- name: DeleteEmptyUnlockedIssueWorkAttempt :execrows
DELETE FROM issue_work_attempts AS attempt
WHERE attempt.id = $1
  AND attempt.locked_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM issue_work_attempt_contributors AS contributor
      WHERE contributor.attempt_id = attempt.id
  );

-- name: GetIssueWorkAttemptByIDForUser :one
SELECT wa.*, wi.project_id
FROM issue_work_attempts AS wa
INNER JOIN issue_work_items AS wi ON wi.id = wa.work_item_id
INNER JOIN projects AS p ON p.id = wi.project_id
INNER JOIN organization_members AS om ON om.org_id = p.organization_id
WHERE wa.id = $1
  AND om.user_id = $2
LIMIT 1
FOR UPDATE OF wa;

-- name: LockIssueWorkAttemptsForCrawlStart :many
UPDATE issue_work_attempts AS wa
SET locked_at = now(), verification_crawl_id = c.id
FROM issue_work_items AS wi, crawls AS c
WHERE wi.id = wa.work_item_id
  AND c.id = $1
  AND wi.project_id = c.project_id
  AND wa.status IN ('awaiting_verification', 'not_verified')
  AND wa.locked_at IS NULL
  AND wa.created_at <= COALESCE(c.started_at, now())
RETURNING wa.id;

-- name: ReleaseIssueWorkAttemptsForFailedCrawl :exec
UPDATE issue_work_attempts
SET locked_at = NULL, verification_crawl_id = NULL
WHERE verification_crawl_id = $1
  AND status IN ('awaiting_verification', 'not_verified');

-- name: ListIssueWorkAttemptsForVerification :many
SELECT
    wa.id AS attempt_id,
    wa.work_item_id,
    wa.source_crawl_id,
    wi.subject_kind,
    wi.subject_key,
    wi.pillar,
    wi.bucket,
    wi.issue_type,
    wi.source_issue_group_id
FROM issue_work_attempts AS wa
INNER JOIN issue_work_items AS wi ON wi.id = wa.work_item_id
INNER JOIN crawls AS c ON c.id = sqlc.arg(crawl_id)
WHERE wi.project_id = c.project_id
  AND wa.verification_crawl_id = c.id
  AND (wa.source_crawl_id IS NULL OR wa.source_crawl_id <> c.id)
  AND wa.status IN ('awaiting_verification', 'not_verified')
  AND wa.locked_at IS NOT NULL
ORDER BY wa.created_at, wa.id;

-- name: GetPageIssueVerificationEvidence :one
SELECT
    EXISTS (
        SELECT 1 FROM crawl_pages AS cp
        WHERE cp.crawl_id = sqlc.arg(crawl_id)
          AND cp.url = sqlc.arg(url)
          AND cp.fetch_error IS NULL
          AND cp.soft_404 = FALSE
          AND cp.status_code BETWEEN 200 AND 299
    ) AS page_observed,
    EXISTS (
        SELECT 1 FROM crawl_issues AS ci
        WHERE ci.crawl_id = sqlc.arg(crawl_id)
          AND ci.url = sqlc.arg(url)
          AND ci.pillar = sqlc.arg(pillar)
          AND ci.bucket = sqlc.arg(bucket)
          AND ci.issue_type = sqlc.arg(issue_type)
    ) AS issue_present,
    CASE
        WHEN sqlc.arg(bucket)::text = 'psi_cwv' THEN COALESCE(c.google_psi_results #>> '{0,mobile,success}', 'false') = 'true'
        ELSE TRUE
    END AS required_evidence_observed
FROM crawls AS c
WHERE c.id = sqlc.arg(crawl_id);

-- name: GetSiteIssueVerificationEvidence :one
WITH coverage AS (
	SELECT EXISTS (
		SELECT 1 FROM crawl_pages AS baseline_page
		WHERE baseline_page.crawl_id = sqlc.arg(source_crawl_id)
		  AND baseline_page.fetch_error IS NULL AND baseline_page.soft_404 = FALSE
		  AND baseline_page.status_code BETWEEN 200 AND 299
	) AND NOT EXISTS (
		SELECT 1 FROM crawl_pages AS baseline_page
		WHERE baseline_page.crawl_id = sqlc.arg(source_crawl_id)
		  AND baseline_page.fetch_error IS NULL AND baseline_page.soft_404 = FALSE
		  AND baseline_page.status_code BETWEEN 200 AND 299
		  AND NOT EXISTS (
			SELECT 1 FROM crawl_pages AS current_page
			WHERE current_page.crawl_id = sqlc.arg(crawl_id)
			  AND current_page.url = baseline_page.url
			  AND current_page.fetch_error IS NULL AND current_page.soft_404 = FALSE
			  AND current_page.status_code BETWEEN 200 AND 299
		  )
	) AS coverage_observed
)
SELECT coverage.coverage_observed,
	EXISTS (
		SELECT 1 FROM crawl_issues AS issue
		WHERE issue.crawl_id = sqlc.arg(crawl_id)
		  AND issue.pillar = sqlc.arg(pillar)
		  AND issue.bucket = sqlc.arg(bucket)
		  AND issue.issue_type = sqlc.arg(issue_type)
	) AS issue_present
FROM coverage;

-- name: GetDuplicateGroupVerificationEvidence :one
WITH original_members AS (
    SELECT member.url FROM crawl_issue_group_members AS member
    WHERE member.group_id = sqlc.arg(source_group_id)
), member_coverage AS (
    SELECT COUNT(*) >= 2 AND NOT EXISTS (
        SELECT 1 FROM original_members AS original
        WHERE NOT EXISTS (
            SELECT 1 FROM crawl_pages AS cp
            WHERE cp.crawl_id = sqlc.arg(crawl_id)
	          AND cp.url = original.url
	          AND cp.fetch_error IS NULL
	          AND cp.soft_404 = FALSE
	          AND cp.status_code BETWEEN 200 AND 299
        )
    ) AS all_members_observed
	FROM original_members
), exact_relationship AS (
    SELECT EXISTS (
        SELECT 1
        FROM crawl_issue_groups AS current_group
        INNER JOIN crawl_issue_group_members AS current_member ON current_member.group_id = current_group.id
        INNER JOIN original_members AS original ON original.url = current_member.url
        WHERE current_group.crawl_id = sqlc.arg(crawl_id)
          AND current_group.issue_type = sqlc.arg(issue_type)
        GROUP BY current_group.id
        HAVING COUNT(DISTINCT original.url) >= 2
    ) AS remains
), near_relationship AS (
    SELECT EXISTS (
        SELECT 1
        FROM crawl_issue_relations AS current_relation
        INNER JOIN crawl_pages AS current_left ON current_left.id = current_relation.left_crawl_page_id
        INNER JOIN crawl_pages AS current_right ON current_right.id = current_relation.right_crawl_page_id
        WHERE current_relation.crawl_id = sqlc.arg(crawl_id)
          AND current_relation.issue_type = sqlc.arg(issue_type)
          AND EXISTS (
			  SELECT 1
			  FROM crawl_issue_relations AS original_relation
			  INNER JOIN crawl_issue_groups AS source_group ON source_group.id = sqlc.arg(source_group_id) AND source_group.crawl_id = original_relation.crawl_id
			  INNER JOIN crawl_pages AS original_left ON original_left.id = original_relation.left_crawl_page_id
			  INNER JOIN crawl_pages AS original_right ON original_right.id = original_relation.right_crawl_page_id
			  WHERE original_relation.issue_type = sqlc.arg(issue_type)
			    AND original_left.url IN (SELECT url FROM original_members)
			    AND original_right.url IN (SELECT url FROM original_members)
			    AND LEAST(original_left.url, original_right.url) = LEAST(current_left.url, current_right.url)
			    AND GREATEST(original_left.url, original_right.url) = GREATEST(current_left.url, current_right.url)
		  )
    ) AS remains
)
SELECT
    member_coverage.all_members_observed,
    (CASE WHEN sqlc.arg(issue_type)::text = 'near_duplicate_content'
         THEN near_relationship.remains ELSE exact_relationship.remains END)::boolean AS issue_present
FROM member_coverage, exact_relationship, near_relationship;

-- name: UpdateIssueWorkAttemptVerification :exec
UPDATE issue_work_attempts
SET status = sqlc.arg(status),
    verification_crawl_id = sqlc.arg(verification_crawl_id),
    verified_at = now(),
    locked_at = CASE WHEN sqlc.arg(status)::text = 'not_verified' THEN NULL ELSE locked_at END
WHERE id = sqlc.arg(attempt_id);

-- name: UpdateIssueWorkItemStatus :exec
UPDATE issue_work_items
SET status = $2, updated_at = now()
WHERE id = $1;

-- name: UpdateIssueWorkItemStatusAfterVerification :exec
UPDATE issue_work_items AS item
SET status = CASE WHEN EXISTS (
        SELECT 1 FROM issue_work_attempts AS newer
        WHERE newer.work_item_id = item.id
          AND newer.id <> sqlc.arg(attempt_id)
          AND newer.status IN ('awaiting_verification', 'not_verified')
    ) THEN 'awaiting_verification' ELSE sqlc.arg(status)::text END,
    updated_at = now()
WHERE item.id = sqlc.arg(work_item_id);

-- name: ListVerifiedIssueWorkAttemptsForProject :many
SELECT
    wa.id AS attempt_id,
    wa.status,
    wa.verified_at,
    wa.verification_crawl_id,
    wi.id AS work_item_id,
    wi.subject_kind,
    wi.subject_key,
    wi.pillar,
    wi.bucket,
    wi.issue_type,
    COALESCE(array_agg(DISTINCT u.email) FILTER (WHERE u.id IS NOT NULL), ARRAY[]::text[])::text[] AS contributor_emails
FROM issue_work_attempts AS wa
INNER JOIN issue_work_items AS wi ON wi.id = wa.work_item_id
LEFT JOIN crawls AS vc ON vc.id = wa.verification_crawl_id
LEFT JOIN issue_work_attempt_contributors AS wc ON wc.attempt_id = wa.id
LEFT JOIN users AS u ON u.id = wc.user_id
WHERE wi.project_id = sqlc.arg(project_id)
  AND wa.status = 'fixed'
  AND wa.verified_at >= sqlc.arg('from')
  AND wa.verified_at < sqlc.arg('to')
  AND (
    sqlc.narg(user_id)::uuid IS NULL
    OR EXISTS (
        SELECT 1 FROM issue_work_attempt_contributors AS f
        WHERE f.attempt_id = wa.id AND f.user_id = sqlc.narg(user_id)
    )
  )
GROUP BY wa.id, wi.id
ORDER BY wa.verified_at DESC;

-- name: ListIssueWorkspaceDiff :many
WITH baseline AS (
    SELECT ci.id, ci.url, ci.pillar, ci.bucket, ci.issue_type, ci.severity, ci.message, ci.details, ci.issue_group_id
    FROM crawl_issues ci
    JOIN crawls c ON c.id = ci.crawl_id
    JOIN projects p ON p.id = c.project_id
    JOIN organization_members om ON om.org_id = p.organization_id
    WHERE ci.crawl_id = sqlc.arg(baseline_id)
      AND om.user_id = sqlc.arg(user_id)
      AND (sqlc.arg(url_filter)::text = '' OR ci.url = sqlc.arg(url_filter))
), current AS (
    SELECT ci.id, ci.url, ci.pillar, ci.bucket, ci.issue_type, ci.severity, ci.message, ci.details, ci.issue_group_id
    FROM crawl_issues ci
    JOIN crawls c ON c.id = ci.crawl_id
    JOIN projects p ON p.id = c.project_id
    JOIN organization_members om ON om.org_id = p.organization_id
    WHERE ci.crawl_id = sqlc.arg(current_id)
      AND om.user_id = sqlc.arg(user_id)
      AND (sqlc.arg(url_filter)::text = '' OR ci.url = sqlc.arg(url_filter))
), identities AS (
    SELECT url, pillar, bucket, issue_type FROM baseline
    UNION
    SELECT url, pillar, bucket, issue_type FROM current
)
SELECT
    i.url AS url,
    i.pillar AS pillar,
    i.bucket AS bucket,
    i.issue_type AS issue_type,
    COALESCE(cur.severity, base.severity, '')::text AS severity,
    COALESCE(cur.message, base.message, '')::text AS message,
    COALESCE(cur.details, base.details, '')::text AS details,
    COALESCE(base.id::text, ''::text)::text AS baseline_issue_id,
    COALESCE(cur.id::text, ''::text)::text AS current_issue_id,
    CASE
        WHEN base.id IS NULL THEN 'new'
        WHEN cur.id IS NOT NULL THEN 'still_open'
        WHEN cp.id IS NULL OR cp.fetch_error IS NOT NULL OR cp.soft_404 OR cp.status_code NOT BETWEEN 200 AND 299 THEN 'not_verified'
        WHEN i.bucket = 'psi_cwv' AND NOT EXISTS (SELECT 1 FROM crawls evidence_crawl WHERE evidence_crawl.id = sqlc.arg(current_id) AND COALESCE(evidence_crawl.google_psi_results #>> '{0,mobile,success}', 'false') = 'true') THEN 'not_verified'
        WHEN i.issue_type IN ('weak_open_graph_coverage', 'missing_website_schema', 'missing_org_identity_schema', 'missing_about_page', 'missing_contact_page', 'missing_policy_page', 'missing_llms_txt', 'homepage_missing_org_contact_trust_signals') AND EXISTS (
            SELECT 1 FROM crawl_pages baseline_coverage
            WHERE baseline_coverage.crawl_id = sqlc.arg(baseline_id) AND baseline_coverage.fetch_error IS NULL AND baseline_coverage.soft_404 = FALSE AND baseline_coverage.status_code BETWEEN 200 AND 299
              AND NOT EXISTS (SELECT 1 FROM crawl_pages current_coverage WHERE current_coverage.crawl_id = sqlc.arg(current_id) AND current_coverage.url = baseline_coverage.url AND current_coverage.fetch_error IS NULL AND current_coverage.soft_404 = FALSE AND current_coverage.status_code BETWEEN 200 AND 299)
        ) THEN 'not_verified'
        WHEN base.issue_group_id IS NOT NULL AND EXISTS (
            SELECT 1 FROM crawl_issue_group_members original_member
            WHERE original_member.group_id = base.issue_group_id
              AND NOT EXISTS (
                  SELECT 1 FROM crawl_pages member_page
                  WHERE member_page.crawl_id = sqlc.arg(current_id)
                    AND member_page.url = original_member.url
                    AND member_page.fetch_error IS NULL
                    AND member_page.soft_404 = FALSE
                    AND member_page.status_code BETWEEN 200 AND 299
              )
        ) THEN 'not_verified'
        ELSE 'no_longer_detected'
    END::text AS change_type,
    (cp.id IS NOT NULL AND cp.fetch_error IS NULL AND NOT cp.soft_404 AND cp.status_code BETWEEN 200 AND 299)::boolean AS current_page_seen
FROM identities i
LEFT JOIN baseline base USING (url, pillar, bucket, issue_type)
LEFT JOIN current cur USING (url, pillar, bucket, issue_type)
LEFT JOIN crawl_pages cp ON cp.crawl_id = sqlc.arg(current_id) AND cp.url = i.url
ORDER BY i.url, i.pillar, i.bucket, i.issue_type;

-- name: ListIssueWorkspaceWork :many
SELECT
    wi.id::text AS work_item_id,
    attempt.id::text AS attempt_id,
    COALESCE(member.url, wi.subject_key)::text AS url,
    wi.subject_kind AS subject_kind,
    wi.pillar AS pillar,
    wi.bucket AS bucket,
    wi.issue_type AS issue_type,
    attempt.status AS status,
    COALESCE(attempt.verification_crawl_id::text, ''::text)::text AS verification_crawl_id,
    COALESCE(array_agg(contributor.user_id::text) FILTER (WHERE contributor.user_id IS NOT NULL), ARRAY[]::text[])::text[] AS contributors
FROM issue_work_items wi
JOIN crawls current_crawl ON current_crawl.id = sqlc.arg(current_id) AND current_crawl.project_id = wi.project_id
JOIN LATERAL (
    SELECT wa.* FROM issue_work_attempts wa
    WHERE wa.work_item_id = wi.id ORDER BY wa.created_at DESC, wa.id DESC LIMIT 1
) attempt ON TRUE
LEFT JOIN crawl_issue_group_members member ON wi.subject_kind = 'group' AND member.group_id = wi.source_issue_group_id
LEFT JOIN issue_work_attempt_contributors contributor ON contributor.attempt_id = attempt.id
WHERE (attempt.verification_crawl_id = sqlc.arg(current_id) OR (
    attempt.status IN ('awaiting_verification', 'not_verified') AND current_crawl.id = (
        SELECT latest.id FROM crawls latest
        WHERE latest.project_id = current_crawl.project_id AND latest.status = 'completed'
        ORDER BY latest.completed_at DESC NULLS LAST, latest.created_at DESC, latest.id DESC LIMIT 1
    )
))
  AND (sqlc.arg(url_filter)::text = '' OR COALESCE(member.url, wi.subject_key) = sqlc.arg(url_filter))
GROUP BY wi.id, attempt.id, attempt.status, attempt.verification_crawl_id, member.url
ORDER BY COALESCE(member.url, wi.subject_key), wi.pillar, wi.bucket, wi.issue_type;

-- name: ReadIssueWorkItems :many
SELECT
    wi.id AS id,
    wi.subject_kind AS subject_kind,
    COALESCE(member.url, wi.subject_key)::text AS subject_key,
    wi.pillar AS pillar,
    wi.bucket AS bucket,
    wi.issue_type AS issue_type,
    wi.status AS item_status,
    COALESCE(latest_attempt.status, ''::text)::text AS attempt_status,
    (SELECT COUNT(*)::bigint FROM issue_work_attempts wa WHERE wa.work_item_id = wi.id) AS attempt_count,
    latest_attempt.verified_at AS verified_at,
    wi.created_at AS item_created_at,
    wi.updated_at AS item_updated_at,
    latest_attempt.created_at AS attempt_created_at,
    COALESCE(array_agg(DISTINCT u.email) FILTER (WHERE u.id IS NOT NULL), ARRAY[]::text[])::text[] AS contributor_emails
FROM issue_work_items wi
LEFT JOIN LATERAL (
    SELECT wa.* FROM issue_work_attempts wa WHERE wa.work_item_id = wi.id ORDER BY wa.created_at DESC, wa.id DESC LIMIT 1
) latest_attempt ON TRUE
LEFT JOIN LATERAL (
    SELECT m.url
    FROM crawl_issue_group_members m
    WHERE wi.subject_kind = 'group' AND m.group_id = wi.source_issue_group_id
    ORDER BY m.url
    LIMIT 1
) member ON TRUE
LEFT JOIN issue_work_attempt_contributors wc ON wc.attempt_id = latest_attempt.id
LEFT JOIN users u ON u.id = wc.user_id
WHERE wi.project_id = sqlc.arg(project_id)
  AND (sqlc.arg(pillar)::text = '' OR wi.pillar = sqlc.arg(pillar))
  AND (sqlc.arg(bucket)::text = '' OR wi.bucket = sqlc.arg(bucket))
  AND (sqlc.arg(issue_type)::text = '' OR wi.issue_type = sqlc.arg(issue_type))
GROUP BY wi.id, latest_attempt.id, latest_attempt.status, latest_attempt.verified_at, latest_attempt.created_at, member.url
ORDER BY wi.updated_at DESC, wi.id;
