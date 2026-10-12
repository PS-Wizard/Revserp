# AEO repairs (pillar `aeo`)

Scope: page issues apply per healthy HTML-like page; site-wide issues attach
to one representative page (shallowest depth, homepage preferred) and are not
per-page failures. Sources: `internal/issues/aeo/derive.go`,
`internal/issues/aeo/consts.go`, `internal/issues/aeo/author_helpers.go`,
`internal/issues/aeo/page_helpers.go`, `internal/issues/aeo/schema_helpers.go`,
`internal/crawler/parser.go`, `internal/crawler/store.go`.

## Article classification (gate for most checks below)

A page is article-like when either condition holds:

1. Its stored JSON-LD has an article-like `@type` (`Article`,
   `BlogPosting`, `NewsArticle`, `TechArticle`) at the top level, in a top-level
   array, in an `@graph` array, or in a JSON string that itself embeds JSON.
   The check reads only the node's own `@type` (string or string array); it
   does not walk arbitrary nested objects. A generic `WebPage` alone does not
   qualify. (Other checks such as generic-type, `FAQPage`, `WebSite`, and
   org-identity collection do walk arbitrary nested objects; this
   article-like gate does not.)
2. Or its `WordCount` is 300 or more and its URL contains `/blog/`,
   `/article/`, `/articles/`, `/guides/`, or `/news/`.

Non-article pages skip author, citation, and article-schema checks entirely.
A 250-word `/blog/` post is not article-like (word gate fails). A 900-word
`/pricing/` page without article schema is not article-like (path gate fails).

## External-link extraction filter (citation counts)

`ExternalLinks` counts every `<a href>` in the full document (header, footer,
and body) whose normalized URL is not internal to the crawl root host.
There is no `nofollow` exclusion, no boilerplate exclusion, and no
per-target deduplication in this stored count: repeated links to the same
external URL each count. The derivation's internal in/out counts are deduped
and exclude self-links; this citation count is not.

A footer social link therefore satisfies `ExternalLinks >= 1` for the
detector even when it is not a real editorial citation. Treat a passing count
as detector-satisfied, not as citation-quality proof.

Citation flags apply only to article-like pages, evaluated in this order.
Detector clearance needs the stored `ExternalLinks` count past the threshold;
`read_page` never returns that count (see `references/evidence.md`).

### long_article_has_no_citations

Flag: `WordCount >= 800` and `ExternalLinks == 0`. Severity high.

### long_article_has_weak_citations

Flag: `WordCount >= 1200` and `ExternalLinks <= 1`. Severity high. A 900-word
page with 1 link does not meet this flag (word gate fails); it falls to
`weak_external_citations`.

### missing_external_citations

Flag: `ExternalLinks < 1`. Severity medium.

### weak_external_citations

Flag: `WordCount >= 600` and `ExternalLinks <= 1`. Severity low.

Inspect: the row `details` count is the only stored-count evidence. `read_page`
content shows link text and URLs in markdown but never returns the stored
`ExternalLinks` count, so do not reconcile the two. Fix: add
credible external sources supporting important claims. Non-fix: adding footer,
navigation, or repeated social links to inflate the count. Verify: a new crawl
raises the stored `ExternalLinks` past the flag's threshold.

## Author signals (`expertise`)

Author source priority in the crawler: `meta[name=author]` content, else
`meta[property=article:author]` content, else JSON-LD `author` from
`Article`, `BlogPosting`, `NewsArticle`, `TechArticle`, or `WebPage` nodes
(`name` field, first match, `@graph` aware). Stored as the page `Author`
string.

The detector's plain-signal check reads the stored `Author` plus OG keys
`og:author` and `article:author`. Known extraction limit: the crawler only
persists `meta[property]` values starting with `og:` into `OGTags`, so an
`article:author` meta property reaches the detector through the `Author`
field, not through `OGTags["article:author"]`. Do not promise OG
`article:author` coverage the extractor does not store.

### missing_author_signal
Flag: article-like page with empty `Author` and empty OG `og:author` and
`article:author`. Severity medium.
Fix: add clear author attribution in visible content, metadata, or schema.
Verify: new crawl stores a non-empty author signal.

### weak_author_signal
Flag: article-like page whose author value (page author or either OG author
key), lowercased and trimmed, is exactly `admin`, `team`, `staff`, or
`editor`. Severity low.
Fix: attribute a specific credible person or source.
Detector limit: only the exact single tokens `admin`, `team`, `staff`, `editor`
(lowercased, trimmed) flag. Renaming `Admin` to `admin team` clears the row
because the two-word string is not in the set, but it remains weak
attribution quality. Treat row-cleared separately from genuinely fixed.
Verify: new crawl author value is outside the generic set.

### article_missing_publisher_identity
Flag: article-like page with JSON-LD present, but no article-like node
carries a non-empty `author`, `publisher`, or `mainEntityOfPage`. Unlike the
narrow article-like gate, this identity check walks all nested objects, arrays,
`@graph` entries, and embedded JSON strings. A field counts when
`valueHasContent` passes: non-empty string, array with a content entry, or
any non-empty object. Severity high.
Fix: expose at least one of those identity fields on the article node.
Verify: new crawl JSON-LD contains the identity field on the article node.

### author_signal_not_supported_by_schema
Flag: article-like page with a plain author signal (`Author` or OG author
key non-empty) and JSON-LD present, but no normalized
(case-insensitive, trimmed) name match between the plain names and the schema
`author`/`publisher` identity names collected recursively. Severity high.
Fix: align visible attribution with matching schema identity (same name in
both places).
Non-fix: adding an unrelated organization name to schema while the byline
names someone else.
Verify: new crawl plain name matches a schema identity name.

## Structured data (`answerability`, `trust`)

JSON-LD presence means stored bytes are not empty, `null`, or `[]` (raw-bytes
check only). Malformed JSON still counts as present, so it does not emit
`missing_structured_data`; instead its type and core-field checks fail to
parse and the page typically emits `schema_missing_core_fields`. Type
collection for generic, `FAQPage`, `WebSite`, and org-identity checks walks
arbitrary nested objects, arrays, `@graph`, and embedded JSON strings; the
narrow article-like gate above does not. `read_page` never returns JSON-LD,
author, robots, canonical, OG, or external-link counts, so there is no
stored JSON-LD evidence path through `read_page`. Treat the issue row
(`message`, `details`, matching types or counts) as the stored evidence;
confirm live markup only through CMS tools (for example WordPress `get_schema`
or `get_content`) or the raw page source. `read_page` blocks never
contain scripts, so never conclude JSON-LD absence from raw block output.

### schema_missing_core_fields

Flag: no single node in the stored JSON-LD has non-empty `name` AND (`url` OR
`description`) non-empty, checked recursively through nested objects, arrays,
and embedded JSON. The fields must sit in the same node; a `name` on one node
plus a `url` on another does not satisfy the detector. A field counts as non-empty when
its value is a non-empty string, an array with a content entry, or any non-empty
object (even `{"@type": "Person"}` with no `name`/`url` inside counts,
because any non-empty map counts). Severity high. Fix: expand the same node
with the missing core field. Verify: new crawl JSON-LD has a qualifying node.

### missing_structured_data
Flag: no meaningful JSON-LD on the page. Severity high.
Fix: add page-appropriate JSON-LD with a type matching the page purpose.
Verify: new crawl stores JSON-LD and emits no such row.

### generic_structured_data_only
Flag: meaningful JSON-LD exists but every collected `@type` is in
`Thing`, `WebPage`, `CollectionPage`, or `ItemPage`. Severity medium.
Fix: replace generic schema with a specific type describing the real content.
Verify: new crawl type set includes a non-generic type.

### article_missing_article_schema
Flag: article-like page with JSON-LD present but no article-like type in it.
Severity medium.
Fix: add `Article` or `BlogPosting` structured data.
Verify: new crawl JSON-LD carries the article type.

### faq_like_page_missing_faq_schema
Flag: page looks like an FAQ and stored JSON-LD has no `FAQPage` type.
FAQ-like means URL contains `/faq` (case-insensitive after lowercasing), or
at least 2 headings whose text contains `?`, or visible text contains at
least 3 `?` characters. Severity high.
Fix: add `FAQPage` structured data reflecting the question-and-answer
content.
Non-fix: adding `FAQPage` markup to a page with no real questions.
Verify: new crawl JSON-LD carries `FAQPage` or the page stops matching the
FAQ-like rule intentionally.

## Trust and identity (`trust`, `authoritativeness`, `experience`)

### missing_og_tags
Flag: stored OG bytes are empty, `null`, or `{}`. The extractor only keeps
`meta[property]` starting with `og:` that has non-empty content; `twitter:*`
tags never satisfy this detector. Severity low.
Fix: add core Open Graph tags for sharing previews.
Verify: new crawl stores non-empty OG tags.

### weak_open_graph_coverage
Flag: site-wide. Fewer than half of crawled pages expose OG tags
(`covered / total < 0.5`). Attached to the site representative page.
Severity high.
Fix: roll out core OG tags across important pages.
Verify: new crawl coverage at or above one half.

### missing_website_schema
Flag: site-wide. No crawled page's JSON-LD carries `WebSite`. Severity high.
Fix: add `WebSite` structured data at the site level.
Verify: new crawl has a page with `WebSite` schema.

### missing_org_identity_schema
Flag: site-wide. No crawled page's JSON-LD carries `Organization`,
`LocalBusiness`, `Person`, or `Corporation`. Severity high.
Fix: add the appropriate identity schema for the site or business.
Verify: new crawl has a page with one of those types.

### missing_https
Flag: page URL starts with `http://` after trimming and lowercasing.
Severity medium.
Fix: serve over HTTPS and update internal references.
Verify: new crawl URL uses `https://`.

### missing_llms_txt
Flag: site-wide. Crawl site facts record `HasLlmsTxt` as known-false
(`/llms.txt` convention from llmstxt.org). Attached to the site
representative page. Severity high.
Fix: add an `/llms.txt` file at the site root listing key pages in Markdown.
Non-fix: adding an unlinked file at a nested path.
Verify: new crawl records `HasLlmsTxt` true.

### missing_about_page
Flag: site-wide. No crawled URL (lowercased) contains `/about`, `/company`,
or `/team`. Severity medium.
Fix: add an About page explaining who is behind the site.
Verify: new crawl discovers a matching URL.

### missing_contact_page
Flag: site-wide. No crawled URL contains `/contact` or `/support`.
Severity medium. Same fix and verification shape as above.

### missing_policy_page
Flag: site-wide. No crawled URL contains `/privacy`, `/terms`, `/policy`,
or `/policies`. Severity low.
Fix: add privacy, terms, or similar policy pages.
Verify: new crawl discovers a matching URL.
Limitation: URL-substring matching only; an unlinked or uncrawled policy page
does not count.

### homepage_missing_org_contact_trust_signals
Flag: homepage exists (zero URL path segments) and all four hold: no org
identity schema anywhere, no about page, no contact page, and the homepage's
own JSON-LD lacks article publisher identity. Severity high. Attached to the
homepage.
Fix: strengthen homepage trust signals with organization identity, about and
contact coverage, and supporting schema.
Verify: new crawl breaks at least one of the four conditions intentionally.
