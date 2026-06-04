1. What the old system had

Issue detection in old code

From ../revseo/src/core/issue_detector.py.

### Titles / meta

- [x] Missing title
- [ ] Title too long
- [ ] Title too short
- [x] Missing meta description
- [ ] Meta description too long
- [ ] Meta description too short

### Headings / content

- [x] Missing H1
- [x] Multiple H1
- [ ] Missing H2 on long pages
- [ ] Skipped heading levels
- [x] Thin content

### Technical / status

- [ ] 3xx redirect info
- [ ] 4xx client errors
- [ ] 5xx server errors
- [x] Missing canonical
- [ ] Canonical different from page URL

### Mobile / accessibility / media

- [ ] Missing viewport
- [ ] Missing language attribute
- [x] Images without alt
- [x] Images missing dimensions
- [ ] Too many images on page

### Social / structured data

- [x] Missing Open Graph tags
- [ ] Missing Twitter card tags
- [ ] No structured data

### Performance / indexability

- [x] Slow response time
- [ ] Moderate response time
- [ ] Large page size
- [ ] Moderate page size
- [x] Noindex
- [ ] Nofollow meta robots

### Link graph issues

- [ ] Low internal links in
- [ ] No internal links out
- [ ] Low internal links out

### Cross-page issues

- [ ] Duplicate content detection

────────────────────────────────────────────────────────────────────────────────

2. What you already have in the new backend

Infra / auth / orgs

- [x] Supabase JWT auth
- [x] local user bootstrap
- [x] default org bootstrap
- [x] org membership model
- [x] projects
- [x] crawls
- [x] queue worker
- [x] JS fallback renderer

Crawl facts you already persist

- [x] title
- [x] meta description
- [x] h1 / h1_count
- [x] h2_count / h3_count
- [x] word_count
- [x] visible_text
- [x] canonical_url
- [x] lang
- [x] viewport
- [x] robots
- [x] image_count
- [x] images_without_alt_count
- [x] images_without_dimensions
- [x] internal_links / external_links
- [x] response_time_ms
- [x] size_bytes
- [x] status_code
- [x] og_tags
- [x] json_ld
- [x] javascript_rendered
- [x] crawl_links table
- [x] crawl_issues table

Issues already implemented now

- [x] missing_title
- [x] missing_meta_description
- [x] missing_h1
- [x] multiple_h1
- [x] thin_content
- [x] missing_canonical
- [x] noindex_page
- [x] missing_og_tags
- [x] images_missing_alt
- [x] images_missing_dimensions
- [x] slow_response_time

────────────────────────────────────────────────────────────────────────────────

3. What you can implement next with current backend support

These are low-friction because the data already exists.

Easy next issues

- [ ] title_too_long
- [ ] title_too_short
- [ ] meta_description_too_long
- [ ] meta_description_too_short
- [ ] missing_viewport
- [ ] missing_lang
- [ ] large_page_size
- [ ] moderate_page_size
- [ ] nofollow_page
- [ ] canonical_differs
- [ ] missing_structured_data
- [ ] missing_h2_on_long_page

Probably also implementable now

- [ ] skipped_heading_levels
   You currently store heading_outline but it’s still TODO / null, so not until that extraction is wired.

So correction:
- skipped heading levels is not actually ready yet.

Link-based issues you can probably do soon

Using current tables:
- [ ] no_internal_links_out
- [ ] low_internal_links_out

Using crawl graph aggregation:
- [ ] low_internal_links_in
   This needs counting inbound internal links from crawl_links, but that’s straightforward.

Cross-page issues possible now

- [ ] duplicate titles
- [ ] duplicate meta descriptions

These are easier and more reliable than the old fuzzy duplicate-content check.

────────────────────────────────────────────────────────────────────────────────

4. What the old system had that your backend does not fully support yet

Not fully supported by current extraction

- [ ] missing Twitter card tags
   You intentionally dropped twitter_tags.
- [ ] skipped heading levels
   heading_outline not populated yet.
- [ ] full structured-data parity via both JSON-LD + schema.org microdata
   You have json_ld, but not schema_org.
- [ ] author-based AEO parity
   You dropped/reduced author extraction.
- [ ] citation-like AEO signals
   Not modeled.
- [ ] old duplicate-content style similarity check
   Old code used title/meta/h1/word-count similarity, but you’d need to define whether you even want that exact behavior.

Link parity gaps

Old backend relied on linked_from on page results for inbound-link issues.
New system moved link relationships into crawl_links, which is better, but means:
- you need DB aggregation logic rather than page-row direct data

That’s fine, just different.

6. Best staged implementation plan from here

Phase 1 — finish issue coverage from current facts

Recommended next batch:
- [ ] no_internal_links_out
- [ ] low_internal_links_out
- [ ] low_internal_links_in

That gets you close to “useful core parity”.

Phase 2 — scoring

Then implement:
- [ ] seo_score
- [ ] aeo_score
- [ ] overall_score

I would not do PageSpeed score seriously yet.

Phase 3 — maybe later

- [ ] duplicate titles / meta descriptions
- [ ] fuzzy duplicate content
- [ ] Twitter card extraction
- [ ] heading outline extraction
- [ ] richer AEO signals
- [ ] optional PSI enrichment

────────────────────────────────────────────────────────────────────────────────

7. My practical verdict

Done enough for “crawl core”?

Yes:
- crawling is real
- auth is real
- org/project/crawl model is real
- issue derivation now exists

Main backend gaps still remaining

- [ ] broader issue coverage
- [ ] scoring
- [ ] invites/org onboarding flows
- [ ] maybe OAuth frontend wiring, but backend auth model is already okay

What you cannot honestly claim yet

- full old-backend scoring parity
- full old issue parity
- trustworthy PageSpeed parity

────────────────────────────────────────────────────────────────────────────────

8. Recommended next move

Do not jump to scoring yet.

First finish the rest of the easy issue detectors from current fields.
That gives scoring something stable to sit on.

Best next batch:
- title/meta length
- viewport/lang
- page size
- nofollow
- canonical differs
- missing structured data
- link in/out counts

If you want, I can turn that into a tight implementation checklist grouped by file and we can knock out the next batch.

──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
~/Projects/revserp (main)
↑11M ↓340k R63M $49.627 (sub) 49.9%/272k (auto)                                                                                               gpt-5.4 • medium


