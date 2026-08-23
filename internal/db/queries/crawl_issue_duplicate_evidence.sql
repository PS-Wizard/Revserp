-- name: DeleteCrawlIssueGroupsForCrawl :exec
DELETE FROM crawl_issue_groups WHERE crawl_id = $1;

-- name: CreateCrawlIssueGroup :one
INSERT INTO crawl_issue_groups (
    crawl_id,
    issue_type
) VALUES (
    $1,
    $2
)
RETURNING id;

-- name: CreateCrawlIssueGroupMembers :copyfrom
INSERT INTO crawl_issue_group_members (
    group_id,
    crawl_page_id,
    url
) VALUES (
    $1,
    $2,
    $3
);

-- name: ListCrawlIssueGroupsForCrawl :many
SELECT id, crawl_id, issue_type, created_at
FROM crawl_issue_groups
WHERE crawl_id = $1
ORDER BY created_at, id;

-- name: ListCrawlIssueGroupMembersForGroup :many
SELECT group_id, crawl_page_id, url
FROM crawl_issue_group_members
WHERE group_id = $1
ORDER BY url;

-- name: DeleteCrawlIssueRelationsForCrawl :exec
DELETE FROM crawl_issue_relations WHERE crawl_id = $1;

-- name: CreateCrawlIssueRelations :copyfrom
INSERT INTO crawl_issue_relations (
    crawl_id,
    issue_type,
    left_crawl_page_id,
    right_crawl_page_id,
    similarity
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5
);

-- name: ListCrawlIssueRelationsForCrawl :many
SELECT id, crawl_id, issue_type, left_crawl_page_id, right_crawl_page_id, similarity, created_at
FROM crawl_issue_relations
WHERE crawl_id = $1
ORDER BY created_at, id;

-- name: LinkCrawlIssuesToIssueGroup :exec
UPDATE crawl_issues
SET issue_group_id = $1
WHERE crawl_id = $2
  AND issue_type = $3
  AND crawl_page_id = ANY($4::uuid[]);
