# Crawl pages parity audit

## Verdict

Current `crawl_pages` is close, but not full parity with the old backend.

It is enough for most old issue derivation, but it intentionally loses parity in a few places.

## Covered by current `crawl_pages`

These old raw inputs are still represented:

- `url`
- `status_code`
- `content_type`
- `size` -> `size_bytes`
- `depth`
- `title`
- `meta_description`
- `h1`
- `h1_count`
- `h2_count`
- `h3_count`
- `heading_outline`
- `word_count`
- `canonical_url`
- `lang`
- `viewport`
- `robots`
- `author`
- `og_tags`
- `json_ld`
- `image_count`
- `images_without_alt_count`
- `images_without_dimensions`
- `external_links`
- `internal_links`
- `response_time_ms`
- `javascript_rendered`
- `is_internal`

This is enough for most old SEO, content, technical, mobile, accessibility, performance, indexability, and duplicate-content checks.

## Accepted parity losses

### `twitter_tags`
Old backend used this for:
- `Missing Twitter Card Tags`

Current schema drops it.

Accepted consequence:
- we will not have parity for that issue unless we add `twitter_tags` back later.

### `schema_org`
Old backend used:
- `json_ld` OR `schema_org`

Current schema keeps `json_ld` but drops `schema_org`.

Accepted consequence:
- pages that only expose microdata / Schema.org itemprops may be falsely treated as having no structured data.

## Deferred to `crawl_links`

### `linked_from`
Old backend used `linked_from` for inbound internal-link issue checks.

We intentionally do **not** keep that on `crawl_pages`.

Plan:
- model link relationships in `crawl_links`
- derive inbound-link counts from link rows instead of storing `linked_from` on the page row

Accepted consequence for now:
- full parity for internal-link issues is blocked until `crawl_links` exists.
