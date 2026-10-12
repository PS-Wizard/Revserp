# PageSpeed repairs (pillar `pagespeed`)

Scope: per-page crawl signals plus one origin-level Google PSI result.
Sources: `internal/issues/page_speed/derive.go`,
`internal/issues/page_speed/consts.go`, `internal/worker/google_psi.go`,
`internal/issues/shared/issue_scope.go`, `internal/issues/page_health.go`.

## Crawl-signal issues (buckets `server_responsiveness`, `page_weight`)

### slow_response_time
Flag: `ResponseTimeMs` over 1000 ms (fetch duration in milliseconds).
Severity medium. Details state the measured milliseconds.
Inspect: row details count; compare across pages before blaming one template.
Fix: reduce server response time with backend performance work and caching.
Non-fix: shrinking images only (payload size does not set this timer).
Verify: new crawl response time at or under 1000 ms.
Limitation: single crawl-time sample, not a percentile.

### moderate_page_size
Flag: `SizeBytes` over 1 MiB (1048576 bytes) and at or under 3 MiB.
Severity medium. Details state megabytes and `recommended: under 1MB`.
Fix: trim page weight where possible.
Verify: new crawl size at or under 1 MiB.

### large_page_size
Flag: `SizeBytes` over 3 MiB (3145728 bytes). Takes precedence over the
moderate flag (`else if`). Severity high. Details state megabytes and
`recommended: under 3MB`.
Fix: compress assets, remove unnecessary payloads, load fewer heavy resources.
Non-fix: lazy-loading below-the-fold images while the initial payload stays
over 3 MiB.
Verify: new crawl size at or under 3 MiB.
False positive: none beyond a genuinely heavy payload; the boundary is exact.

## Google PSI origin issues (bucket `psi_cwv`)

These seven types are origin-level, not page-addressable: they describe the
one Google PageSpeed mobile run stored in `crawl.google_psi_results` for the
crawl's tested URL. They are excluded from per-page health scoring and must
not be read as per-page failures.

Emission rule: only when the stored mobile run reports success. Each metric
emits only past its threshold; passing metrics emit nothing.

### google_psi_mobile_performance

Flag: mobile performance score under 50 is high, 50-89 is medium, 90 or more
emits nothing.

### google_psi_fcp

Flag: First Contentful Paint over 3.0 s is high, over 1.8 s is medium.

### google_psi_lcp

Flag: Largest Contentful Paint over 4.0 s is high, over 2.5 s is medium.

### google_psi_cls

Flag: Cumulative Layout Shift over 0.25 is high, over 0.1 is medium.

### google_psi_fid

Flag: First Input Delay (from `max-potential-fid`) over 300 ms is high, over
100 ms is medium.

### google_psi_speed_index

Flag: Speed Index over 5.8 s is high, over 3.4 s is medium.

### google_psi_tti

Flag: Time to Interactive over 7.3 s is high, over 3.8 s is medium.

Inspect: row `url` is the PSI-tested origin URL, not every affected page.
Fix: address the named lab metric on the tested template (performance work,
render-blocking payload, layout stability, interactivity). Do not claim a
site-wide PSI pass from one origin run.
Non-fix: fixing an unrelated page and expecting the origin row to clear.
Verify: a new crawl with a fresh PSI run no longer emits the metric row.
Caveat: PSI is lab data attached to the crawl; rerunning the crawl without a
fresh PSI run does not re-verify the fix.
