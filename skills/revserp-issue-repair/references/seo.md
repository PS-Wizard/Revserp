# SEO repairs (pillar `seo`)

Scope: healthy HTML-like pages only. Non-HTML content types are skipped
entirely. Broken pages (`fetch_failed`, `client_error_status`,
`server_error_status`, `soft_404`) emit one status issue and never the content
set below. Sources: `internal/issues/seo/derive.go`,
`internal/issues/seo/consts.go`, `internal/issues/seo/derive_helpers.go`,
`internal/issues/seo/derive_broken.go`, `internal/issues/seo/duplicates.go`,
`internal/issues/derive.go`, `internal/crawler/parser.go`,
`internal/crawler/store.go`, `internal/crawler/soft404.go`.

Stored fields used here: `Title` (first `<title>`), `MetaDescription`
(first `meta[name=description]`), `H1` (first non-empty `h1`),
`H1Count` (all `h1` elements), `H2Count` (non-empty `h2` only),
`WordCount` (`countWords(VisibleText)`), `VisibleText` (cleaned main text),
`CanonicalURL`, `Viewport`, `Lang`, `Robots` (only `meta[name=robots]`),
image counts (full document), link counts, `HeadingOutline` JSON,
`Depth`, `StatusCode`, `FetchError`, `Soft404`. These are crawl-time stored facts
used by the detector; only a subset (`title`, `meta_description`, `h1`,
`word_count`, `status_code`, `content_type` plus content blocks) is exposed
through `read_page` (see `references/evidence.md`).

Character lengths are `len()` of the trimmed string (bytes, not runes).

## SERP metadata (`serp_metadata`)

### missing_title
Flag: trimmed title length is 0. Severity high.
Inspect: `read_page` metadata title; empty means detector saw no `<title>`.
Fix: add one descriptive `<title>`.
Non-fix: stuffing keywords or copying another page's title (creates
`duplicate_title`).
Verify: new crawl shows a non-empty title and no `missing_title` row.
False positives: none beyond a genuinely empty title tag.

### title_too_long
Flag: title length over 60. Severity medium. Message states the count and
`recommended: 30-60`.
Inspect: exact character count in row details.
Fix: shorten to roughly 30-60 characters, main intent first.
Non-fix: truncating mid-word or deleting the brand when the brand disambiguates.
Verify: new crawl title length within range.
Limitation: byte count, not pixel width; search engines may rewrite display.

### title_too_short
Flag: non-empty title under 30 characters. Severity medium.
Fix: expand so the title describes the page intent while staying concise.
Non-fix: padding with filler or repeated brand text.
Verify: new crawl length 30-60.

### duplicate_title
Flag: normalized titles equal across 2 or more pages. Normalization is
lowercase plus single-spacing; empty titles are ignored. Pure-pagination
groups (every URL has `/page/` in the path or `?page=` other than 1) are
skipped. Severity high.
Inspect: row details list matching URLs (up to 3 plus `and N more`); duplicate
evidence groups carry the member set.
Fix: give each page a unique intent-matching title.
Non-fix: changing only case or whitespace (normalization ignores it).
Verify: new crawl emits no `duplicate_title` for those URLs.
False positive: templated titles that are intentionally identical still flag;
decide intentionally, do not dismiss as a bug.

### missing_meta_description
Flag: trimmed meta description length is 0. Severity medium.
Fix: add `meta[name=description]` summarizing the page.
Non-fix: copying the title verbatim into the description.
Verify: new crawl description present.

### meta_description_too_long
Flag: description over 160 characters. Severity medium.
Fix: shorten so the key summary fits a typical snippet.
Verify: new crawl length within 120-160.

### meta_description_too_short
Flag: non-empty description under 120 characters. Severity medium.
Fix: expand with a fuller summary while staying concise.
Verify: new crawl length 120-160.

### duplicate_meta_description
Flag: same normalization and pagination rule as `duplicate_title`.
Severity medium.
Fix: write a unique description per page.
Verify: new crawl emits no such row.

## Content structure (`content_structure`)

### missing_h1
Flag: trimmed first-H1 text is empty. Severity high.
Fix: add one primary `h1` matching the page topic.
Non-fix: hiding an empty `h1` with CSS or stuffing multiple `h1`s.
Verify: new crawl `H1` non-empty.

### multiple_h1
Flag: `H1Count` over 1 (counts every `h1` element, even empty ones).
Severity medium.
Fix: keep one primary `h1`; demote the rest to `h2` or lower.
Non-fix: hiding extra `h1`s with display rules; the parser still counts them.
Verify: new crawl `H1Count` is 1.

### title_h1_mismatch
Flag: both title and H1 non-empty, neither contains the other after
lowercase single-space normalization, and trigram similarity under 0.32.
Severity medium. Details quote both values.
Inspect: compare the quoted title and H1 in row details.
Fix: align both on the same page intent.
Non-fix: making them byte-identical when intent differs across sections.
Verify: new crawl emits no mismatch row.
False positive: deliberately different but related title/H1 near the threshold
can flag; adjust wording intentionally.

### missing_h2_on_long_page
Flag: `WordCount` 300 or more and `H2Count` is 0. Severity medium.
Fix: break the page into sections with descriptive `h2` subheadings.
Non-fix: adding empty `h2`s (empty headings are not counted).
Verify: new crawl `H2Count` above 0.

### skipped_heading_levels
Flag: `HeadingOutline` JSON (non-empty `h1`-`h6` in document order) has a jump
of more than one level (for example `H2` to `H4`). Needs at least 2 headings.
Severity medium. Details name each jump.
Fix: restructure so levels progress without skipping.
Non-fix: restyling text to look like a heading without using heading tags.
Verify: new crawl outline has no jumps.

## Content quality (`content_quality`)

### thin_content
Flag: `WordCount` above 0 and under 150. Severity medium.
`WordCount` is whitespace-separated tokens of cleaned `VisibleText` (see
`references/evidence.md`), not raw HTML length.
Fix: add useful original content answering the page intent.
Non-fix: copying boilerplate or footer text to inflate the count (cleaning
strips much of it).
Verify: new crawl `WordCount` 150 or more and no `thin_content` row.

### near_empty_visible_content
Flag: whitespace-separated tokens of `VisibleText` under 25. Severity high
when 0 (`empty visible content`), else medium. Details state the count.
Fix: add substantial visible content beyond placeholder or utility copy.
Non-fix: adding only hidden text, `alt` text, or scripts (not visible text).
Verify: new crawl token count 25 or more.
False positive: JavaScript-only pages parsed without rendering have little
visible text; that is a coverage limitation, not proof of real emptiness.

### exact_duplicate_content
Flag: SHA256 of normalized `Title`, `MetaDescription`, `H1`, `VisibleText`
(lowercase, single-spaced, non-empty joined with newline) matches across 2 or
more pages. Severity high. Details name matching URLs.
Fix: decide the consolidation intentionally: keep one canonical source with unique
copy and remove or redirect true duplicates. Canonical or redirect alone is
best-practice consolidation, not detector clearance: the detector compares
normalized content hashes and ignores canonical tags, so identical copy still
flags until the normalized content actually differs or the duplicate pages leave
the crawled set.
Non-fix: changing only punctuation spacing the normalizer collapses.
Verify: a new crawl emits no exact-duplicate row for the set because the
normalized content differs or the set is gone, not merely because a canonical
tag was added.
False positive: intentionally mirrored pages (print variants, staged copies)
still flag.

### near_duplicate_content
Flag: candidate pairs share at least 2 word bigrams (stop words removed) and
weighted character-trigram similarity 0.58 or more (visible text 0.55,
title 0.15, meta description 0.15, H1 0.15). Exact-hash matches are excluded
from this check. Severity medium. Relations carry pairwise similarity.
Fix: differentiate with unique content, or consolidate when intent overlaps.
As with exact duplicates, canonical or redirect is best practice but not
clearance: the similarity check ignores canonical tags.
Non-fix: shuffling stop words only.
Verify: a new crawl emits no near-duplicate relation for the pair because the
content is actually distinct or consolidated out of the set.
Limitation: expensive pairwise check uses shingle prefiltering; very short
pages may never become candidates.

## Indexability (`indexability`)

### missing_canonical
Flag: trimmed canonical URL is empty. Severity medium.
Fix: add `<link rel="canonical">` with the preferred absolute page URL.
Verify: new crawl canonical present.

### malformed_canonical
Flag: canonical is not an absolute HTTP or HTTPS URL (relative URL, wrong
scheme, or unparsable). Severity high. Details quote the value.
Fix: replace with a valid absolute `http(s)` URL.
Verify: new crawl canonical well-formed.

### canonical_differs
Flag: trimmed page URL string differs from trimmed canonical string (exact
string comparison, not URL normalization). Severity medium.
Inspect: confirm the canonical target is intentional (variants, pagination).
Fix: point the canonical at this page's preferred URL unless cross-page
canonicalization is deliberate.
Verify: an unintended difference is gone when the new crawl canonical equals the
page URL. An intentional cross-page canonical stays flagged by design (the
check is exact string inequality); verification then means the row is
acknowledged as intended, not cleared.

### canonical_points_to_non_indexable_page
Flag: canonical target exists in the same crawl facts and is non-indexable:
its `meta[name=robots]` contains `noindex` (case-insensitive) or its status
is 400 or above. Severity high.
Fix: point at an indexable preferred page, or make the target indexable.
Verify: new crawl target is indexable.

### noindex_page
Flag: lowercased `meta[name=robots]` contains `noindex`. Severity high.
Only HTML `meta[name=robots]` is read; `X-Robots-Tag` headers are not.
Fix: remove `noindex` when the page should be eligible for search results.
Non-fix: removing it only in a preview environment while production still
serves it.
Verify: new crawl robots value has no `noindex`.

### nofollow_page
Flag: lowercased `meta[name=robots]` contains `nofollow`. Severity medium.
Fix: remove `nofollow` when search engines should follow the page's links.
Verify: new crawl robots value has no `nofollow`.

## Technical SEO (`technical_seo`)

### missing_viewport
Flag: trimmed viewport content is empty. Severity high.
Fix: add `<meta name="viewport">` for mobile rendering.
Verify: new crawl viewport present.

### missing_lang
Flag: trimmed `<html lang>` is empty. Severity medium.
Fix: add `lang` to the root HTML element.
Verify: new crawl lang present.

### fetch_failed
Flag: `FetchError` non-empty (network failure, DNS, TLS, blocked, or page
with no status and no parsed content). Severity high. Details carry the
capped error string (500 bytes). One issue per page; no content issues.
Fix: make the URL reachable; check DNS, TLS, server errors, rate limiting, or
bot protection. When the page no longer exists, remove internal links to it.
Non-fix: retrying display text without fixing reachability.
Verify: new crawl fetches the URL with a status code.
Caveat: the error is crawl-time evidence, not a live diagnosis.

### client_error_status
Flag: status 400-499 on a persisted page row. Severity high.
Fix: fix the underlying client error so the page responds successfully.
Verify: new crawl status under 400.

### server_error_status
Flag: status 500 or above. Severity high.
Fix: fix the server error.
Verify: new crawl status under 400.

### soft_404
Flag: page answered 2xx but matches the site's not-found response. Primary
signal is the crawl's soft-404 fingerprint (probe of a random nonexistent URL;
title plus H1 match, or identical visible-text hash). Fallback is not-found
wording in title or H1 on pages with 120 or fewer visible words. Severity
high. Details state the HTTP status and the soft-404 meaning.
Fix: return a real 404 or 410 for URLs that do not exist.
Non-fix: rewriting the not-found wording while still answering 200 (still a
soft 404, plus the broken inbound link stays hidden).
Verify: new crawl returns 404/410 for the nonexistent URL and no `soft_404`.
False positive guard: articles legitimately about 404 errors are protected by
the 120-word bound, and a site answering unknown URLs with its homepage is
not fingerprinted (catch-all behavior is a separate problem).

## Media (`media_optimization`)

### images_missing_alt
Flag: `ImageCount` above 0 and `ImagesWithoutAltCount` above 0. Counts cover
every `<img>` in the full document (including header and footer). Empty `alt`
counts as missing, so decorative images with `alt=""` still flag. Severity low.
Inspect: `read_page` content image blocks show `alt` presence per image.
Fix: add descriptive `alt` to meaningful images; keep decorative `alt=""` only
when the flag is knowingly accepted.
Verify: new crawl missing-alt count is 0.

### images_missing_dimensions
Flag: `ImageCount` above 0 and `ImagesWithoutDimensions` above 0. An image
counts when `width` is empty or `height` is empty. Severity low.
Fix: set explicit `width` and `height` attributes where possible.
Verify: new crawl missing-dimensions count is 0.

### too_many_images_on_page
Flag: `ImageCount` 10 or more and (`WordCount` 0 or below, or words per image
under 50). Severity low. Details state image and word counts plus the
50-words-per-image threshold.
Fix: remove unnecessary images or add supporting content.
Non-fix: hiding images with CSS (parser still counts them).
Verify: new crawl ratio at or above 50 words per image, or image count below 10.

## Internal linking (`internal_linking`)

Deduped distinct URLs per source page; self-links (`source == target`) are
excluded; link-target statuses resolve after the crawl loop from fetched
pages, so unresolved targets do not flag.

### no_internal_links_out
Flag: distinct internal targets from this page is 0. Severity medium.
Fix: add relevant internal links to other important pages.
Verify: new crawl outbound count above 0.

### low_internal_links_out
Flag: distinct internal targets 1 or 2. Severity low. Details state the count.
Fix: add relevant internal links.
Verify: new crawl outbound count above 2.

### orphan_like_page
Flag: `Depth` above 0 and distinct inbound internal sources is 0. Severity high.
The homepage (`Depth` 0) never flags.
Fix: link to the page from relevant navigation or contextual sources.
Non-fix: adding only external or self links (neither counts as inbound).
Verify: new crawl inbound count above 0.

### low_internal_links_in
Flag: `Depth` above 0 and distinct inbound sources 1 or 2. Severity medium.
Fix: add more inbound internal links.
Verify: new crawl inbound count above 2.

### very_deep_page
Flag: `Depth` 4 or more. Severity medium. Details state the crawl depth.
`Depth` is discovery depth from the crawl root, not URL segment count.
Fix: link from stronger parent, hub, or navigation pages to reduce depth.
Non-fix: shortening the URL slug alone (depth comes from link distance).
Verify: new crawl depth under 4.

### internal_links_to_broken_pages
Flag: this page links to at least one internal target with resolved status
400 or above. Severity high. Details list up to 3 targets plus `and N more`.
Fix: update or remove links pointing at broken targets; fix or redirect the
target when it should exist.
Verify: new crawl emits no broken-target row for the source page.

### internal_links_to_redirects
Flag: this page links to at least one internal target with resolved status
300-399. Severity medium. Same details truncation.
Fix: point links directly at the final destination URL.
Non-fix: leaving the redirect chain because the destination renders; the
extra hop remains.
Verify: new crawl emits no redirect-target row for the source page.
