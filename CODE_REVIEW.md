# revserp Backend — Code Review

**Date:** 2026-06-24
**Scope:** All hand-written Go in `internal/` and `cmd/` (~16k LOC; generated `sqlc` and `*_test.go` excluded).
**Method:** Seven focused reviews across auth/security, core handlers, crawl/scoring handlers, AI handlers, the crawler engine, the scoring engine, and workers/GSC/entrypoints. Findings were spot-checked against the source; corrections noted inline.

---

## Executive Summary

The codebase is generally well-structured: consistent use of `sqlc`, clean handler/middleware separation, tenant-scoped queries (`...ForUser`), and good test coverage in the crawler and scoring packages. The highest-value problems cluster in a few themes:

1. **Secrets exposure** — Supabase tokens stored in plaintext; LLM/PSI API keys placed in URLs (logged everywhere).
2. **SSRF** — the crawler fetches arbitrary user-supplied URLs with no private-IP blocking; the cloud metadata endpoint is reachable.
3. **Resource exhaustion** — no response-body size caps (crawler, LLM, GSC/PSI), several unbounded full-table loads, no per-crawl timeout, no LLM rate/cost limits.
4. **Privilege & lifecycle** — an email-suffix admin bootstrap that can't be revoked; suspended users keep live sessions; no graceful drain on worker shutdown; no worker panic recovery.
5. **Scoring determinism** — several scores are computed by summing over Go maps, making results vary run-to-run; one integer-division bug mis-flags pages.

### Severity counts

| Severity | Count |
|----------|------:|
| Critical | 8 |
| High | 16 |
| Medium | 17 |
| Low | 11 |

> **Note on a corrected finding:** an initial review flagged `handleAdminPreviewScoringConfig` and the `/internal/scoring-config` endpoints as "no authentication / any user can overwrite global scoring." This is **incorrect** — `routes.go:41-43` wrap them in `platformAdminOnly` and `routes.go:106` sits inside the `requirePlatformAdmin` group. The real (much milder) issue is a defense-in-depth smell: the handler uses a zero-UUID sentinel to *skip* the crawl-ownership check and relies entirely on route middleware. See M-13.

---

## Critical

### C-1. Email-suffix admin bootstrap grants unrevokable platform-admin
- **Location:** `internal/app/admin_helpers.go:16-22`
- **Issue:** `isPlatformAdmin` returns `true` for any user whose email ends in `@revketer.ai`, regardless of the DB flag. Anyone who signs up (or is invited) with such an email is permanently a platform admin. `handleAdminRemoveAdmin` clears the DB flag but the suffix path keeps the bypass alive, so "remove admin" is silently a no-op for those accounts. If email is attacker-influenceable at the OAuth/Supabase layer, this is full admin takeover.
- **Fix:** Remove the suffix shortcut; make `isPlatformAdmin` a pure DB-flag check. Bootstrap the first admin via a migration or one-time CLI command.

### C-2. Supabase access & refresh tokens stored in plaintext
- **Location:** `internal/auth/session_manager.go:72-78`
- **Issue:** `SupabaseAccessToken` / `SupabaseRefreshToken` are persisted unencrypted. The refresh token is a long-lived credential to mint new access tokens for the user. Any DB read (backup leak, replica, SQLi, insider) yields full impersonation against Supabase.
- **Fix:** Encrypt both at rest with the AES-GCM helper already in `internal/gsc/crypto.go` (after hardening it — see H-3). At minimum encrypt the refresh token.

### C-3. SSRF — crawler reaches private/loopback/metadata addresses
- **Location:** `internal/crawler/fetcher.go:18-56`, `internal/crawler/normalize.go:10-46`, renderer at `renderer.go:76-85`
- **Issue:** `NormalizeURL` validates scheme/host shape but never resolves the host or blocks RFC-1918, loopback, or link-local (`169.254.169.254` — the cloud metadata endpoint). The `http.Client` has no IP-validating `DialContext` and no `CheckRedirect`, so a public page can `302` to an internal address and the client follows it. The renderer subprocess receives the URL unvalidated and may have its own network stack.
- **Fix:** Add a custom `net.Dialer.DialContext` that resolves and rejects private/reserved IPs; add a `CheckRedirect` that re-applies the same blocklist and caps redirects (≤5). Validate the URL before spawning the renderer.

### C-4. Unbounded response body — memory exhaustion / decompression bomb
- **Location:** `internal/crawler/fetcher.go:43` (`io.ReadAll(response.Body)`)
- **Issue:** No size cap on fetched bodies; a multi-GB (or gzip-bomb) response exhausts the heap. The 10s fetch timeout limits wall-clock, not bytes. Same pattern in GSC/PSI responses (H-12).
- **Fix:** `io.ReadAll(io.LimitReader(body, maxBytes+1))` with a configurable `maxBytes` (e.g. 10 MB), erroring if exceeded.

### C-5. LLM API key embedded in request URL → leaked to every log sink
- **Location:** `internal/ai/gemini.go:118` (`...:generateContent?key=%s`); same pattern for PSI in `internal/worker/google_psi.go:151`
- **Issue:** The key is a query parameter, so it appears in access logs, proxy/LB logs, cloud request logs, and any transport-error string (`net/http` embeds the URL in errors). These are live, long-lived secrets.
- **Fix:** Send via header — Gemini: `req.Header.Set("x-goog-api-key", key)`; PSI: keep the key out of any logged/error URL (redact the `key` param before building error strings).

### C-6. No rate limiting or cost cap on LLM endpoints
- **Location:** `internal/app/ai_fix.go:58`, `internal/app/ai_conversations.go:328`
- **Issue:** Every authenticated user can call AI-fix / conversation endpoints without limit; each call dispatches a full prompt to a paid provider. No per-user/org/global rate limit or token budget. A single account (or a compromised one) can run up unbounded spend.
- **Fix:** Add rate-limiting middleware (`golang.org/x/time/rate` or Redis sliding window keyed on user/org) on the AI routes, plus a per-org daily/monthly token budget checked before dispatch.

### C-7. Integer-division bug mis-flags "too many images"
- **Location:** `internal/issues/seo/derive_helpers.go:100`
- **Issue:** `pageFact.WordCount/pageFact.ImageCount < threshold` is integer division of two `int32`s. Truncation under-counts the true ratio (e.g. 99 words / 2 images = 49 < 50 → flagged), so pages whose real ratio is in `[50.0, 50.99…]` are falsely flagged.
- **Fix:** `float64(WordCount)/float64(ImageCount) < float64(threshold)`.

### C-8. Nondeterministic scoring from summing over Go maps
- **Location:** `internal/issues/shared/scoring_helpers.go:106` (bucket penalty accumulated while ranging a `map`); related ordering issues at `seo/duplicates.go:85,107,200` and `shared/helpers.go:38-43`
- **Issue:** Bucket penalties are summed while iterating `map[string]*IssueGroup`. Go randomizes map order, and floating-point addition isn't associative, so the *same* inputs yield slightly different bucket/overall scores across runs — and different DB row order, triggering spurious "changed" detection.
- **Fix:** Collect keys into a slice, `sort.Strings`, then iterate in sorted order for all penalty/score accumulation and issue-row emission.

---

## High

### H-1. Concurrent token refresh revokes the session (DoS / forced logout)
- **Location:** `internal/auth/session_manager.go:107-128`
- **Issue:** `AuthenticateRequest` runs per-request. Within the refresh-skew window, concurrent requests each call `supabaseClient.Refresh` with the same one-time-use refresh token. The first succeeds and invalidates it; the rest get 4xx → the 4xx branch revokes the session, logging the user out under load. (The code correctly keeps the session on transient/5xx errors, so this is specifically the concurrent-4xx race.)
- **Fix:** Serialize refresh per session with `singleflight.Group` keyed on session ID so only one refresh runs and the result is shared.

### H-2. Config validation ignores all auth/crypto secrets
- **Location:** `internal/config/config.go:92-97`
- **Issue:** `Validate()` only checks `DATABASE_URL`. `SupabaseJWTIssuer`, `SupabaseJWKSURL`, `SupabaseAnonKey`, and `GoogleTokenEncryptionSecret` may be empty at boot. An empty encryption secret yields a deterministic, publicly-derivable key (`sha256("")`); an empty JWKS URL undermines verification.
- **Fix:** Require all of them in `Validate()`; enforce a minimum length (≥32) on the encryption secret.

### H-3. Weak key derivation — bare SHA-256 of the passphrase
- **Location:** `internal/gsc/crypto.go:68-71`
- **Issue:** `deriveEncryptionKey` is a single unsalted SHA-256 — not a KDF. A low-entropy env secret is brute-forceable offline.
- **Fix:** Use HKDF (`golang.org/x/crypto/hkdf`, stdlib-adjacent) for a high-entropy secret, or `argon2.IDKey`/`scrypt` if the secret may be human-chosen.

### H-4. `UpdateActiveOrganization` doesn't scope to the owning user
- **Location:** `internal/auth/session_manager.go:168-179`
- **Issue:** Updates the session row by `sessionID` + `activeOrgID` with no `user_id` constraint and no membership check in the function. Correctness depends entirely on every caller checking membership first.
- **Fix:** Add `userID` param and `WHERE id = $1 AND user_id = $2`; verify org membership before calling.

### H-5. Suspending/deleting a user does not revoke live sessions
- **Location:** `internal/app/admin_users.go:122-158`
- **Issue:** Setting status to `suspended` blocks new logins, but existing session cookies keep working until natural expiry (the `requireActiveUser` check does re-query status — see note below). **Verify:** `requireActiveUser` *does* re-load the user and reject non-`active` on every request (`admin_helpers.go:78-89`), which substantially mitigates this. The residual risk is any code path that authenticates without passing through `requireActiveUser`.
- **Fix:** On suspend/delete, also call a `RevokeAllSessionsForUser`, and confirm `requireActiveUser` wraps every authenticated route.

### H-6. `requireActiveUser` fails open when identity is missing
- **Location:** `internal/app/admin_helpers.go:67-70`
- **Issue:** When `IdentityFromContext` returns `!ok`, it calls `next.ServeHTTP` and proceeds. If session middleware ever fails to populate identity, the active-user gate is skipped instead of denying. (Same fail-open on `pgx.ErrNoRows`.)
- **Fix:** Return `401` when identity is missing; decide explicitly whether an unknown user should be allowed.

### H-7. No panic recovery in the worker loop — a single bad job kills a worker permanently
- **Location:** `internal/worker/worker.go:72`
- **Issue:** `runLoop` has no `recover()`. Any panic in `runCrawl` or a callee kills that goroutine forever; the pool silently shrinks toward zero with no log or restart.
- **Fix:** Wrap each iteration's body in a deferred `recover()` that logs `debug.Stack()` and continues.

### H-8. `Run()` swallows worker exits — silent total failure or hang
- **Location:** `internal/worker/worker.go:55-68`
- **Issue:** `runLoop` only ever sends `nil`. If a goroutine dies early, `Run` blocks forever waiting for its slot; if all die, `Run` returns `nil` and the process "exits cleanly" with no work being done.
- **Fix:** Use `errgroup.WithContext`; have `runLoop` return errors and propagate.

### H-9. No graceful drain on worker shutdown — crawls stuck in `running`
- **Location:** `cmd/worker/main.go:40`
- **Issue:** On SIGTERM the context cancels and `Run` returns immediately, abandoning any in-flight `runCrawl`. The crawl row stays `running` forever and can't be re-claimed due to the uniqueness constraint.
- **Fix:** Separate "stop claiming new work" from "finish in-flight"; give in-flight crawls a bounded drain window, or mark abandoned crawls failed on shutdown.

### H-10. No per-crawl timeout — one slow site blocks a worker slot indefinitely
- **Location:** `internal/worker/worker.go:111`
- **Issue:** `runCrawl` gets the raw process context. A hung site (slow headers, redirect loop, slow PSI) occupies the goroutine forever; with the default `concurrency=1` this halts all crawl processing.
- **Fix:** `ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)` around `runCrawl`.

### H-11. DB pool has no connection limits or lifetimes
- **Location:** `internal/db/db.go:10`
- **Issue:** `pgxpool.New` with defaults — unbounded connection lifetime, default max conns. Idle/stale connections accumulate and are never pruned.
- **Fix:** `ParseConfig` then set `MaxConns`, `MinConns`, `MaxConnLifetime`, `MaxConnIdleTime`; `NewWithConfig`.

### H-12. No body-size cap on GSC/PSI responses
- **Location:** `internal/gsc/service.go:114`, `internal/worker/google_psi.go:117`
- **Issue:** `io.ReadAll` / `json.NewDecoder` on external API bodies with no limit — same exhaustion risk as C-4 in the worker/API process.
- **Fix:** `io.LimitReader(body, 10<<20)`.

### H-13. OAuth token exchange decodes before checking HTTP status
- **Location:** `internal/gsc/service.go:193-199`
- **Issue:** Body is unmarshaled into `TokenResponse` before the status code is checked, so a 4xx with a parseable body and a 200 with a broken body take confusingly different error paths, and a partial token could be returned.
- **Fix:** Check `StatusCode` first; only unmarshal on 2xx.

### H-14. Unbounded full-table loads on export & scoring-preview paths
- **Location:** `internal/app/crawl_score_breakdown_exports.go:116`, `internal/app/scoring_config.go:180-186` (`ListCrawlIssuesForCrawl` / `ListCrawlPagesForCrawl`, no `LIMIT`)
- **Issue:** A large crawl loads its entire issue/page set into memory in one request; the export path then filters in Go rather than SQL. OOM vector and slow responses.
- **Fix:** Cap rows in SQL (or stream with a cursor); push pillar/bucket/type filters into the query; document a hard export limit.

### H-15. Spreadsheet formula injection in CSV and XLSX exports
- **Location:** `internal/app/crawl_score_breakdown_exports.go:54-69`; `internal/app/crawl_score_breakdown_workbook.go:215-218,256-259,297-300`
- **Issue:** Crawled, attacker-controllable strings (`URL`, `Message`, `Details`, `RecommendedFix`) are written verbatim. A value starting with `=`, `+`, `-`, or `@` executes as a formula when the file is opened. `encoding/csv` and excelize do not guard against this.
- **Fix:** Prefix any cell whose first char is `= + - @ \t \r` with a `'`. Apply to both export paths.

### H-16. Prompt injection via crawled content forwarded to the LLM
- **Location:** `internal/app/ai_fix_prompt.go:29-43,91-108`
- **Issue:** Crawled fields (`URL`, `Message`, `Details`, `CurrentTitle`, etc.) and business-profile fields are concatenated into the prompt as plain text alongside a plain-text `Final instruction:` delimiter. A crawled page can embed `\nFinal instruction: ignore previous instructions…`.
- **Fix:** Use a structured prompt (JSON/XML data nodes) and/or strip newlines/control chars from all untrusted fields before interpolation.

---

## Medium

### M-1. Request-body size limit bypassed in two admin handlers
- **Location:** `internal/app/admin_organizations.go:102-103`, `internal/app/admin_ai_config.go:83`
- **Issue:** Both use `json.NewDecoder(r.Body).Decode(&req)` directly, skipping the `http.MaxBytesReader` enforced by the `readJSON` helper.
- **Fix:** Use `readJSON(r, &req)`.

### M-2. AI message content not size-capped before decode/use
- **Location:** `internal/app/ai_fix.go:66-89`, `internal/app/ai_conversation_responses.go:149`
- **Issue:** `readJSON` runs before any per-message cap; `normalizeMessageRequest` trims but doesn't enforce `maxAIFixMessageLength` on the new message, so a multi-MB `content` is decoded and injected into the prompt.
- **Fix:** `http.MaxBytesReader` on these routes; `truncateAIFixText(content, maxAIFixMessageLength)` on the new message.

### M-3. Unbounded conversation history fetched, then trimmed in Go
- **Location:** `internal/app/ai_conversation_turn.go:94`
- **Issue:** `ListAIMessagesForConversationForUser` has no `LIMIT`; the full history is loaded into memory before `normalizeAIFixMessages` caps to 10.
- **Fix:** `ORDER BY created_at DESC LIMIT N` in SQL, reverse in Go.

### M-4. LLM provider error returned verbatim to the client
- **Location:** `internal/app/ai_fix.go:163`, `internal/app/ai_conversations.go:372`
- **Issue:** `writeJSONError(w, 502, err.Error())` leaks upstream provider response bodies (up to 2 MB), model names, and internal diagnostics. Same pattern for Supabase errors (`internal/auth/supabase_client.go:171-187`).
- **Fix:** Log server-side; return a generic "AI provider unavailable". Cap any embedded upstream body to ~512 bytes (`deepseek.go:87`, `gemini.go:93`).

### M-5. Per-request N+1 on list endpoints (redundant auth preflight)
- **Location:** `internal/app/crawl_pages.go:251`, `crawl_issues.go:180`, `crawl_links.go:137`
- **Issue:** Each list handler does `GetCrawlByIDForUser` (auth) + a scoped `Count...ByUser` + a scoped `List...ByUser` — three round-trips where the count/list queries already enforce access via their join.
- **Fix:** Drop the standalone preflight; treat count==0 + not-found as 404.

### M-6. Whole XLSX workbook built in memory before streaming
- **Location:** `internal/app/crawl_score_breakdown_exports.go:85-95`, `crawl_score_breakdown_workbook.go:277-282`
- **Issue:** `WriteToBuffer()` + `w.Write` holds the row slice and the serialized workbook simultaneously; combined with H-14, peak memory per export is large.
- **Fix:** Use excelize `StreamWriter`, or cap rows before building.

### M-7. Admin list endpoints have no pagination
- **Location:** `internal/app/admin_users.go:29`, `admin_organizations.go:31`
- **Issue:** `ListAllUsers` / `ListAllOrganizations` load every row with no `LIMIT`.
- **Fix:** Reuse `parsePaginationParams` + paginated queries.

### M-8. Trend endpoint unmarshals full breakdown JSON for every row, no pagination
- **Location:** `internal/app/crawl_score_breakdown_trends.go:74-92`
- **Issue:** Hard-coded limit of 20, no pagination metadata, and each row's full `BreakdownJson` is unmarshaled in a loop.
- **Fix:** Expose `?limit/offset` (capped), return only score fields unless full breakdown is requested.

### M-9. `Content-Disposition` filename built by string concatenation
- **Location:** `internal/app/crawl_score_breakdown_exports.go:29-31,91-92`
- **Issue:** Safe today (UUID only) but fragile — substituting a user-controlled crawl name later enables header injection; also not RFC 5987-encoded.
- **Fix:** `filename*=UTF-8''` + `url.PathEscape`.

### M-10. Public `handleGetInvite` leaks org metadata for invalid invites
- **Location:** `internal/app/invites.go:241-264`
- **Issue:** The unauthenticated endpoint returns `organization_name`, status, and usage counts even for revoked/expired/exhausted tokens — enables org-name harvesting via token enumeration.
- **Fix:** Return 404 (or minimal payload) for non-`active` invites.

### M-11. Active-org update inside the membership transaction
- **Location:** `internal/app/invites.go:336-343`, `internal/app/me.go:224-247,269-318`
- **Issue:** `UpdateActiveOrganization` runs inside `withTx`. If it writes to a different store than the membership tx (or fails after a partial write), commit/rollback semantics are incoherent — a user can be a member but error out, or vice versa.
- **Fix:** Move the active-org update to a best-effort call *after* `tx.Commit()`.

### M-12. `handleDeleteProject` authorization may not require ownership
- **Location:** `internal/app/projects.go:178-208`
- **Issue:** Deletion relies on the implicit `UserID` filter in `DeleteProjectByIDForUser`. Unlike `handleUpsertProjectBusinessProfile` (which calls `requireOrganizationOwner`), there's no explicit role check — any org *member* may be able to delete a project. Inconsistent and likely unintended.
- **Fix:** Add an explicit `requireOrganizationOwner`/role assertion before delete.

### M-13. Zero-UUID sentinel skips crawl-ownership check (defense-in-depth)
- **Location:** `internal/app/scoring_config.go:120-176`
- **Issue:** `handleAdminPreviewScoringConfig` passes `pgtype.UUID{}`, and `previewScoringConfig` skips the ownership check when `!userID.Valid`. This is *not* an open IDOR — the route is behind `requirePlatformAdmin` (`routes.go:106`) — but using an invalid UUID as a "bypass auth" sentinel is fragile, and `ensureInternalScoringUser` does no role check of its own (it leans entirely on `platformAdminOnly` at `routes.go:41-43`).
- **Fix:** Don't encode "admin" as a zero UUID; pass an explicit `isAdmin bool`. Keep the role check defensively inside the handler too.

### M-14. `resolveUser` swallows the DB error on `CreateUser` failure
- **Location:** `internal/app/me.go:134-144`
- **Issue:** Returns `User{}, false` (the bool means "needs org", not "ok"); the caller surfaces a generic error and the real DB error is never logged or propagated.
- **Fix:** Return `(User, bool, error)` and propagate, or at least log the error.

### M-15. PSI run/skip is silent — no observability
- **Location:** `internal/worker/worker.go:135-137`
- **Issue:** PSI start is logged only when a key is present; the no-key (skip) path is completely silent, so operators can't tell whether PSI ran, was skipped, or failed.
- **Fix:** Add an `else` log line for the skip case.

### M-16. O(n²) hand-rolled sort on GSC overview rows
- **Location:** `internal/gsc/overview.go:273-280`
- **Issue:** Custom selection sort used on row slices; negligible at current sizes but unnecessary and a correctness hazard if the comparator isn't total.
- **Fix:** `sort.Slice`.

### M-17. AEO site-issue uses an inconsistent coverage denominator
- **Location:** `internal/issues/aeo/derive.go:102`
- **Issue:** `weak_open_graph_coverage` divides by `len(pageFacts)`, while other coverage metrics use `totalScoredPages`. Mismatched denominators make this site issue fire at a different rate than the scoring engine expects.
- **Fix:** Pass and use `totalScoredPages` consistently.

---

## Low

### L-1. `selectSiteIssuePageFact` missing `else` lets a non-homepage override the homepage
- **Location:** `internal/issues/aeo/page_helpers.go:15-24` — homepage promotion and depth promotion are independent `if`s; add `else if` so a shallower non-homepage can't replace a chosen homepage.

### L-2. Trigram builder slices bytes, not runes
- **Location:** `internal/issues/seo/duplicates.go:287` — byte-slicing mid-codepoint corrupts trigrams for non-ASCII content, degrading near-duplicate detection. Convert to `[]rune` first.

### L-3. `limitURLsForIssueDetails` aliases & mutates the caller's slice
- **Location:** `internal/issues/seo/derive_helpers.go:184` — `append(urls[:3], …)` overwrites the 4th backing-array element. Copy into a fresh `make([]string, 3, 4)` first.

### L-4. `EnrichPageFactsWithContentFingerprints` runs twice per derive
- **Location:** `internal/issues/store.go:42` + `internal/issues/seo/derive.go:141` — redundant hashing of every page's text. Remove the inner call (store layer already enriches). Also note `aeo`/`pagespeed` get the un-cloned slice while `seo` gets a clone — make cloning consistent.

### L-5. `OverallWeights` not validated to sum to 1.0
- **Location:** `internal/issues/score.go:83` + `scoring_config.go` validator — weights are individually `>=0`-checked but their sum isn't, so scores can blow past 100 (clamped, losing differentiation) or top out low. Add a `|sum-1.0| < 1e-3` check.

### L-6. Hardcoded `minimumAEOIssueCoverage` shadows the configurable value
- **Location:** `internal/issues/aeo/score.go:9` vs `scoring_config.go:33` — the standalone `aeo.Score` uses a hardcoded 0.75 while the configurable path reads from config; they diverge if config changes. Route all callers through `BuildScoreBreakdownWithConfig`.

### L-7. `MaximumVolumePressure`/`VolumePressureScale` of 0 silently replaced with defaults
- **Location:** `internal/issues/shared/scoring_helpers.go:196-199` — a deliberately-configured 0 (allowed by the validator) is overridden by the `<= 0` guard. Use a pointer/sentinel to distinguish "unset" from "zero".

### L-8. `newIssue` duplicated across three packages
- **Location:** `seo/derive_helpers.go:13`, `aeo/derive.go:129`, `page_speed/derive.go:25` — identical constructors. Move to `shared.NewIssue`.

### L-9. Model-name defaults duplicated as bare string literals
- **Location:** `internal/app/ai_provider.go:47` vs `internal/ai/provider.go:38` (`"deepseek-v4-flash"`, `"gemini-2.5-flash"`) — drift corrupts `ai_messages.model` attribution. Export shared constants.

### L-10. `cmd/` entrypoints duplicate boot boilerplate; migrate tool skips a ping
- **Location:** `cmd/api/main.go`, `cmd/worker/main.go`, `cmd/ai-audit-worker/main.go` (signal/config/connect copy-paste); `cmd/migrate/main.go:34` (`sql.Open` without `PingContext`). Extract an `internal/bootstrap` helper; add a ping to fail fast on bad credentials.

### L-11. Misc cleanups
- `internal/auth/middleware.go:24-30` — the `pgx.ErrNoRows` branch and the `else` emit the identical 401; drop the dead branch and the `pgx` import.
- `internal/app/helpers.go:17-26` — oversized bodies surface as generic "invalid json" (400) instead of 413; detect "request body too large" and return 413.
- `internal/app/ai_fix_issues.go:24-85` — the only raw `sqlBuilder` query (correctly parameterized, but bypasses sqlc's compile-time checks and manual tenant-join maintenance). Migrate to sqlc or comment why not.
- `internal/app/scoring_config.go:60-65` — serves stored `BreakdownJson` bytes without `json.Valid` validation; a corrupt blob is returned with 200.

---

## Cross-cutting recommendations (in priority order)

1. **Secrets hygiene:** move all API keys to headers; encrypt Supabase tokens at rest; harden the KDF; never put `err.Error()` from upstream services into client responses.
2. **SSRF + ingress limits:** one shared, hardened `http.Transport` with an IP-blocking dialer and redirect cap, plus a body-size limiter, reused by the crawler, renderer, LLM, and GSC/PSI clients.
3. **Resource governance:** per-crawl timeout, worker panic recovery + errgroup, DB pool limits, graceful drain, LLM rate/cost caps, and `LIMIT`s on every list/export/preview query.
4. **Authorization consistency:** make `isPlatformAdmin` DB-only; ensure `requireActiveUser` fails closed and wraps every authenticated route; add explicit role checks (don't lean solely on route middleware); revoke sessions on suspend/delete.
5. **Scoring determinism:** sort before every map iteration that feeds penalties, scores, or emitted rows; fix the integer-division ratio; validate weight sums.
6. **De-duplication:** shared `NewIssue`, shared model-name constants, shared `cmd` bootstrap, single fingerprint-enrichment call.

> Severity reflects exploitability and blast radius given the current routing. Before acting on the "missing auth" class of findings, confirm against `internal/app/routes.go` — several handlers are protected by middleware the per-file reviews didn't see (as corrected in M-13).
