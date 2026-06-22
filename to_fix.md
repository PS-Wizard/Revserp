# revserp backend audit — to fix

Scope: `/home/wizard/Projects/revketer/revserp`
Current state: **partially completed**
Verification: `go test ./... && go build ./... && go vet ./...` ✅

## Note
- `.env` exists locally but is **not tracked in git**.
- `.env.example` is tracked.
- So “committed secrets” is **not confirmed** from git state.

---

## Status legend
- [x] done
- [ ] remaining
- [~] partially addressed

---

## Completed

### Platform Admin System (NEW)
- [x] Migration 000024: users.is_platform_admin, users.status, users.suspended_at, users.suspension_reason, organization_scoring_configs table, ai_prompt_configs table
- [x] SQLC queries: ListAllUsers, GetUserByID, UpdateUserPlatformAdmin, UpdateUserStatus, GetOrgScoringConfig, UpsertOrgScoringConfig, DeleteOrgScoringConfig, GetAIPromptConfig, UpsertAIPromptConfig, ResetAIPromptConfig, ListAllOrganizations, GetCrawlOrgID
- [x] Admin auth helpers: `isPlatformAdmin`, `requirePlatformAdmin` middleware, `requireActiveUser` middleware
- [x] `/me` response extended with `is_platform_admin` and `status`
- [x] Non-active user enforcement in middleware; delete action soft-disables users with status `deleted`
- [x] Scoring config endpoints gated behind platform admin
- [x] Admin CRUD APIs: users (list/make-admin/remove-admin/suspend/unsuspend/delete), organizations (list, org scoring config CRUD), AI config (get/put/reset)
- [x] Runtime scoring org override: `LoadEffectiveScoringConfig` checks org override first, falls back to global
- [x] Runtime AI prompt: `loadEffectiveAISystemPrompt` uses DB config with defaults fallback in `buildAIFixPrompt`
- [x] Frontend: `app/routes/app/admin.tsx` with Scoring / AI Config / Accounts tabs
- [x] Frontend: profile menu shows "Admin Settings" only for platform admins
- [x] `grill.md` written with agreed scope/decisions
- [x] Validation: `go test ./... && go build ./... && go vet ./...` passes; `pnpm typecheck && pnpm build` passes

### Foundation / startup / workers
- [x] GSC response handling now checks HTTP status before success-payload JSON unmarshal in `internal/gsc/service.go`
- [x] Worker transient claim errors no longer tear down the crawl worker pool in `internal/worker/worker.go`
- [x] `sleepOrCancel` timer leak fixed in `internal/worker/worker.go`
- [x] Graceful shutdown added to:
  - `cmd/api/main.go`
  - `cmd/worker/main.go`
  - `cmd/ai-audit-worker/main.go`
  - `cmd/migrate/main.go`
- [x] Added conservative `Config.Validate()` and wired it into binaries

### App/API layer
- [x] Added request body size limit to `readJSON`
- [x] Added shared app `serverError(...)` helper with server-side logging
- [x] Added shared app transaction helper and refactored targeted handlers in:
  - `internal/app/me.go`
  - `internal/app/projects.go`
- [x] Decomposed `internal/app/gsc.go` into smaller responsibility-based files
- [x] Refactored `internal/app/ai_conversations.go`
- [x] Eliminated the dual-transaction flow in `handleCreateAIConversationMessage`
- [x] Added `FOR UPDATE` conversation lookup for serialized message creation

### Auth / sessions
- [x] Successful session reads now update `last_used_at`
- [x] Session refresh no longer revokes backend sessions on transient refresh failures
- [x] `RequireSession` now logs authentication failures server-side so infra/auth issues are distinguishable operationally

### AI / issues
- [x] Added AI provider interface/factory in `internal/ai`
- [x] Removed per-call default AI HTTP client allocation
- [x] Replaced `RecommendedFix` giant switch with a data-driven map while preserving fallback behavior
- [x] Restored XLSX export tab structure (Overview, All Issues, SEO/AEO/PageSpeed Issues, Duplicate Peers) — previous refactor had collapsed them into a single "Detailed" sheet
- [x] Removed ISSUE_LABEL, RECOMMENDED_FIX, SUGGESTION columns from XLSX headers; CSV export unchanged

---

## Remaining priority hit list

#### 1. [x] `internal/app/crawl_score_breakdowns.go` — 1382 lines
- File split into `crawl_score_breakdowns.go` (handlers), `crawl_score_breakdown_exports.go` (CSV/XLSX export), `crawl_score_breakdown_workbook.go` (XLSX workbook builder), `crawl_score_breakdown_trends.go` (trend building).
- XLSX workbook now has correct tabs (Overview, All Issues, SEO/AEO/PageSpeed Issues, Duplicate Peers).



#### 2. [x] `internal/app/gsc.go` — 791 lines
- Completed: split by responsibility into smaller files and moved shared helpers out.

#### 3. [x] `internal/app/ai_conversations.go` — 648 lines
- Completed: extracted helper responsibilities and collapsed create-message flow to one transaction.
- Note: file still exists and may still be large, but the main correctness/structure issue called out here is addressed.

#### 4. [~] API handlers swallow real errors
- Completed in the first shared slice for:
  - `internal/app/me.go`
  - `internal/app/projects.go`
- Remaining: broader rollout across the rest of `internal/app/*`.

#### 5. [~] Transaction boilerplate duplicated across many handlers
- Completed in the first shared slice for:
  - `internal/app/me.go`
  - `internal/app/projects.go`
- Remaining: broader rollout across crawls, issues, invites, AI audits, scoring, etc.

---

### HIGH

#### 6. [x] Per-request user/org bootstrap is too heavy
- Resolved user + organizations are now cached on the request context via `resolveUserContextKey`. `ensureCurrentUser` checks the cache first; bootstrap queries run at most once per request, even when called from nested helpers.
- Implemented in `internal/app/projects.go`.

#### 7. [x] Worker loop is fragile to transient failures
- Fixed in `internal/worker/worker.go`.

#### 8. [x] Crawler cancellation/error path is brittle
- `internal/crawler/runner.go`
- Fixed: removed redundant `close(jobs)` from both error paths. `cancelRun()` alone signals workers via context, preventing double-close and ensuring leak-free goroutine exit.

#### 9. [x] AI audit worker is effectively a stub
- `internal/aiaudit/worker.go`: removed the empty `runLoop` (which also had a `time.After` timer leak) and `sleepOrCancel`. `Run` now returns nil immediately.
- `cmd/ai-audit-worker/main.go`: log message updated to be honest. The binary still starts and exits cleanly with graceful-shutdown wiring intact for when queue processing is added.

#### 10. [x] Dual-transaction flow in AI conversation message creation
- Fixed.

#### 11. [x] Fragile raw SQL assembly in `ai_fix`
- `internal/app/ai_fix.go`
- Fixed: replaced raw `$1`-`$4` + `fmt.Sprintf("$%d", len(args))` pattern with a tiny `sqlBuilder` helper that auto-numbers positional parameters via `param()`. No more manual positional tracking.

#### 12. [x] No graceful shutdown in binaries
- Fixed for API, worker, AI audit worker, and migrate.

#### 13. [x] `handleCreateCrawlPage` style positional param overload
- `internal/app/crawl_pages.go`
- Fixed: introduced `crawlPageRowData` struct to replace the 33-parameter `buildCrawlPageResponse` signature. All three `new*Row` wrappers now use named struct literals instead of positional arguments.

---

### MEDIUM

#### 14. [x] `internal/crawler/store.go` — split into `store.go`, `store_extract.go`, `store_null.go`
- Persistence logic separated from extractor helpers and nullable/pgtype utilities. Extractor helpers moved to `store_extract.go`; nullable helpers moved to `store_null.go`.

#### 15. [~] Repeated nullable/pgtype helpers
- Some shared helper extraction completed (`textValue`, `timestamptzValue`, shared app helpers).
- Broader dedup across app/auth/issues is still incomplete.

#### 16. [ ] Read-only handlers still open write transactions
- Still broadly true.
- Not cleaned up yet.

#### 17. [x] GSC response handling checks status too late
- Fixed.

#### 18. [x] No common AI provider interface
- Fixed.

#### 19. [x] Hardcoded recommendation switch in issues
- Fixed with a package-level map.

#### 20. [x] `readJSON` has no body size limit
- Fixed.

#### 21. [x] Session refresh failure revokes sessions too aggressively
- Fixed.

#### 22. [ ] `handleMe` / auth path uses transactions for mostly-read flow
- Shared helper cleanup done, but the deeper read-path simplification is not done yet.

#### 23. [x] Config validation is weak
- Conservative startup validation added.

#### 24. [ ] Supabase config field naming is overloaded
- Not addressed yet.

#### 25. [x] `last_used_at` not updated on normal session reads
- Fixed.

#### 26. [x] `RequireSession` conflates infra failures with auth failures
- Still returns 401 to clients by design for now, but server-side logging now distinguishes causes operationally.

---

### LOW

#### 27. [ ] Bubble sort in GSC overview
- `internal/gsc/overview.go`
- Not fixed yet.

#### 28. [ ] Parser/tests are getting monolithic
- Not fixed yet.

#### 29. [ ] `go 1.26.2` in `go.mod` is unusual
- Not addressed yet.

#### 30. [ ] Excel dependency footprint should be justified
- Not addressed yet.

#### 31. [ ] Some tiny helper duplications remain
- Not fully addressed yet.

---

## Remaining subsystem notes

### App/API layer
- [ ] `internal/app/crawl_score_breakdown_compare.go` — 537 lines
- [ ] `internal/app/ai_fix.go` — 521 lines
- [ ] `internal/app/crawl_pages.go` — 471 lines
- [ ] `internal/app/invites.go` — 432 lines
- [ ] `internal/app/crawls.go` — 430 lines

### Crawler/worker
- [x] `internal/crawler/store.go` — split into `store.go`, `store_extract.go`, `store_null.go`
- [ ] `internal/crawler/parser.go` — 422 lines
- [x] `internal/crawler/runner.go` cancellation/error-path cleanup — removed redundant `close(jobs)` from error paths
- [x] `internal/aiaudit/worker.go` — stub loop removed; `Run` returns nil immediately

### AI/GSC/issues
- [ ] `internal/issues/aeo/derive.go` — 614 lines
- [ ] `internal/issues/shared/helpers.go` — 391 lines
- [x] AI provider construction cleaned up
- [x] GSC app handler file decomposition completed

### Foundation/auth/config
- [x] graceful shutdown added
- [x] baseline config validation added
- [x] session activity tracking improved
- [ ] env trimming / config semantics cleanup still open

---

## Recommended next cleanup order

1. `internal/app/crawl_score_breakdowns.go`
2. shared handler error/transaction rollout across the rest of `internal/app/*`
3. [x] per-request user/org resolution cleanup
4. [x] `internal/crawler/store.go`
5. [x] `internal/crawler/runner.go` error/cancel path
6. `internal/app/ai_fix.go`
7. `internal/app/crawl_pages.go`
8. [x] AI audit worker implementation or removal
9. shared pgtype/null helper cleanup across remaining packages
10. smaller remaining cleanup items

---

## Summary

Main work completed so far:
- worker resilience improved
- startup/shutdown behavior improved
- GSC app layer decomposed
- AI conversation transaction correctness improved
- auth/session behavior hardened
- AI provider abstraction cleaned up
- issue recommendation logic simplified
- targeted app-layer error/transaction foundation added

Main work still remaining:
- biggest export/reporting god file
- broader app-layer handler cleanup rollout
- [x] crawler store/runner cleanup — store.go split by responsibility (extractors → store_extract.go, nullable helpers → store_null.go), runner.go `close(jobs)` redundancy removed from error paths
- [x] user/org bootstrap optimization — resolved user+orgs cached on request context; bootstrap runs at most once per request
- [x] AI audit worker — empty loop removed; `Run` returns nil immediately, binary exits cleanly with honest log message
