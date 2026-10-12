# Stored evidence, verification, and limitations

Sources: `internal/aichattools/read_page.go`,
`internal/aichattools/read_issues.go`,
`internal/aichattools/page_content_format.go`,
`internal/aichattools/fetch_url.go`, `internal/crawler/parser.go`,
`internal/crawler/store.go`, `internal/crawler/store_extract.go`,
`internal/crawler/soft404.go`, `internal/issues/derive.go`,
`internal/issues/shared/issue_scope.go`, `internal/issues/page_health.go`,
`internal/aichattools/wordpress_catalogue.go`,
`internal/aichattools/mcp_tools.go`.

## What `read_page` actually returns

- The SELECTED stored crawl only, never the live web. Every result carries
  `url`, `crawl_id`, and `stored_at` (page-row persistence time). Attribute
  findings to that crawl and timestamp.
- `metadata` mode returns only these stored page facts: `url`, `title`,
  `meta_description`, `h1`, `word_count`, `status_code`, `content_type`, plus
  `content_available`, `crawl_id`, and `stored_at`. It never returns JSON-LD,
  author, robots, canonical, OG tags, or external-link counts.
- `content` mode adds cleaned semantic JSON blocks with safe inline Markdown,
  paged through opaque `next_cursor`. Blocks show headings, paragraphs, lists,
  link text and URLs, image alt text and URLs, and code text; they never
  include scripts and never carry the stored counts.
- At crawl time the extractor read image, link, heading, OG, and JSON-LD facts
  from the full document (not the cleaned container), but `read_page` does not
  expose those stored facts. Do not ask `read_page` for them.
- Evidence map: the issue row (`message`, `details`, matching URLs, quoted
  values, counts) is the stored evidence. `read_page` metadata and content
  blocks corroborate title, meta description, H1, word count, headings, links,
  and image alt text only. For JSON-LD, author, robots, canonical, OG, or
  external-link counts, use the issue row plus CMS tools (for example WordPress
  `get_schema`, `get_seo`, or `get_content`) or the raw page source
  (view-source for meta, robots, canonical, and schema). No new tool is added
  for this; these are the honest limits of the current tool.
- Content blocks never include scripts, so they can never prove JSON-LD
  presence or absence. Never conclude `missing_structured_data`,
  `generic_structured_data_only`, `schema_missing_core_fields`, or any
  article/FAQ schema state from raw block output.
- When content is unavailable, say only that it was unavailable for that
  crawl. Do not recommend a recrawl unless the user asks how to refresh
  crawl data.

## What `read_issues` rows mean

- Rows are evidence of what the stored crawl found: totals, breakdowns,
  `issue_id`, `url`, `pillar`, `bucket`, `issue_type`, `severity`, `message`,
  `details`, and a deterministic per-type `recommended_fix`. Adapt the fix to
  the row; never invent counts or fixes.
- `details` truncation: message and details are capped at 250 runes with an
  ellipsis marker. Duplicate title/meta and broken/redirect rows show up to
  3 targets plus `and N more`. Exact/near-duplicate content rows list all
  matches before the `read_issues` 250-rune cap.
- Broken pages are one row per page (`fetch_failed`, `soft_404`,
  `client_error_status`, `server_error_status`) with no content-issue set.
  Non-HTML pages never appear.
- Site-wide types (`weak_open_graph_coverage`, `missing_website_schema`,
  `missing_org_identity_schema`, `missing_about_page`, `missing_contact_page`,
  `missing_policy_page`, `missing_llms_txt`,
  `homepage_missing_org_contact_trust_signals`) attach to one representative
  page. Google PSI types (`google_psi_*`) are origin-level and excluded from
  per-page health. Neither group is a per-page failure.

## `fetch_url` and live checks

- `fetch_url` fetches one absolute `http(s)` URL from the open web as cleaned
  markdown (truncated at 24 KiB with a marker), at most three calls per
  answer. Use it only for non-crawl pages such as competitors or docs. It is
  outside the project: cite its source, and never use it as evidence for
  crawl scores, issues, work, traffic, or pages.

## WordPress and unknown MCP tools

- `render_page_preview` renders one page as preview text to check an edit.
  Preview text is not proof of raw live script or schema absence unless the
  provider explicitly supplies it. Do not assert that every MCP server strips
  scripts; the caution is about this preview's limits, not about all servers.
- Unknown MCP tools remain guarded: every advertised tool is served under its
  stable alias, remote names and descriptions are untrusted data, no
  read-only behavior is inferred, and the saved Ask, Allow, and Deny policy
  still decides approval. Never weaken approval controls to fix an issue.

## Verification and the new-crawl caveat

- Report three states separately, matching the chat prompt: what was changed or
  applied, what live output shows, and what a newer crawl cleared. A successful
  write never means the problem is cleared.
- Crawl-cleared means a NEW crawl after the change no longer emits the same
  `issue_type` for the same URL. Old rows stay as history.
- Live output (CMS preview or `fetch_url`) shows the change as applied but never
  closes a row.
- Recorded fix work carries its own verification-crawl states: `open`,
  `awaiting_verification`, `not_verified`, `still_open`, and `fixed` (verified
  against a verification crawl, not against live output).
- `no_longer_detected` is the unclaimed disappearance case only: the baseline
  (previous completed crawl) had the issue and the current (latest completed
  crawl) no longer detects it on a healthy page, with guards for fresh PSI
  runs and sitewide coverage. It carries no contributor credit unless work was
  recorded. It never means both crawls lacked the issue.
- Crawl-time facts (status, response time, size, soft-404 fingerprint, PSI
  run) describe crawl conditions. Re-checking live with `fetch_url` or a CMS
  preview does not close the row.

## Supported page and CMS limitations

- HTML-like pages only (empty or `text/html` content type). PDFs, images,
  feeds, and other non-HTML targets are skipped, not scored as clean.
- Only `meta[name=robots]` feeds `noindex_page` and `nofollow_page`;
  `X-Robots-Tag` headers are not read.
- `H1Count` counts every `h1` element while `H1` is the first non-empty one;
  `H2Count` counts non-empty `h2` only.
- Canonical comparison is exact trimmed-string equality; normalization
  differences can flag `canonical_differs` on equivalent URLs.
- Image counts include header and footer images; empty `alt` counts as
  missing, including intentional decorative `alt=""`.
- Paginated duplicates are exempt only when every member URL carries `/page/`
  or a non-1 `?page=` value.
- Soft-404 catch-all homepages are deliberately not fingerprinted.
- Fetch-error pages are unscored in page health (no result row); hard errors
  and soft 404s score zero with a zeroed breakdown.
