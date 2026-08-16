# AI Chat Rewrite Plan

## Status

- Legacy chat cleanup, durable schema, and workspace controls are implemented through migration `000045`.
- Project-scoped conversation CRUD and durable turn submission are implemented.
- AI chat worker claiming, concurrency, leases, retries, recovery, cancellation handling, and DeepSeek streaming are implemented.
- Turn delivery, conversation history, and chat switching are implemented; quota display and UI polish remain.
- AI Config system prompts are selected per workspace; external is the default.
- Production reuse of migration `000041` still requires confirmation that no shared environment ran the old migration.
- Visibility/audit data and non-chat AI behavior remain preserved.
- The first working version has no tools, artifacts, charts, navigation actions, exports, or crawl actions.

## Product decisions

- Every conversation belongs to exactly one project.
- A conversation can never switch projects.
- The server derives the organization from the project.
- The server validates all project and crawl references. The model never supplies tenant, organization, project, or crawl IDs.
- AI generation continues when the browser closes or loses its connection.
- One user can run turns in multiple conversations at the same time.
- Only one turn can be active in a given conversation.
- The workspace has a shared monthly user-message allowance.
- Only an accepted, explicit user message consumes the allowance. Provider retries and future internal actions do not consume it.
- The user can request `none`, `low`, `high`, or `max` reasoning.
- The workspace controls which reasoning efforts are available.
- SSE is a resumable view of durable work. SSE does not own or execute the work.
- Raw chain-of-thought is not persisted, replayed, or exposed. The UI receives only phase changes such as `thinking` and `writing`.

## Destruction boundary

### Remove from the backend

- The request-bound AI agent loop in `internal/app/ai_agent.go` and its tests.
- Current AI conversation handlers and response mapping in `internal/app/ai_conversations.go` and `internal/app/ai_conversation_responses.go`.
- Current AI conversation routes.
- The complete model-facing tool registry and tool execution path in `internal/app/aitools`.
- Tool-call stream reassembly and tool-specific provider types.
- Tool replay, tool message roles, tool round limits, tool result limits, and forced tool synthesis.
- Chat navigation, project switching, comparison, chart, export, crawl, auto-crawl, and file-generation actions.
- AI chat artifact creation, persistence, download routes, rendering code, embedded artifact fonts, SQL, and tests.
- AI tool groups and `disabled_ai_tools` feature-gate behavior.
- Current chat-specific prompt loading and contextual agent prompt behavior.
- Old chat SQL queries and generated SQLC code after replacement queries exist.

### Remove from the frontend

- The complete `app/components/ai-dock` chat implementation.
- `app/components/command-dock/ai-panel.tsx`.
- AI chat mode wiring in the command dock, mode rail, constants, and related navigation state.
- `use-ai-chat.ts` and all optimistic stream, tool, artifact, chart, navigation, and stop state.
- AI conversation mapping in `app/lib/ai-conversation.ts`.
- Old AI chat API types in `app/lib/api.types.ts`.
- AI artifact cards and chat chart rendering.
- Old tool controls in the admin feature UI.
- Any styles used only by the removed chat UI.

### Preserve by default

- Projects, organizations, memberships, business profiles, crawls, crawl workers, GSC data, and scoring.
- Authentication, active-organization validation, and platform-admin authorization.
- The non-chat AI visibility/audit system, its OpenRouter integration, its jobs, and its tables.
- Non-chat question generation and visibility runs.
- AI-generated fix/commentary features only if dependency review confirms that they remain active products.
- Shared prompt configuration only for fields still used by preserved non-chat systems.

### Required scope check before deletion

- Inventory every reference to `ai_prompt_configs`, `ai_worker_jobs`, `ai_audits`, `ai_audit_prompts`, `ai_audit_runs`, and `project_ai_questions`.
- Mark each reference as chat or non-chat.
- Preserve visibility checks, their jobs, and their existing data.
- Do not delete a shared table because its name starts with `ai_`.
- Confirm only whether AI-generated fixes and crawl commentary remain product features.
- Existing chat history and generated artifacts are confirmed for permanent deletion.

## Migration strategy

### Rules

- Never edit or remove a migration that has run in any shared environment.
- Check the Goose migration state in local, staging, and production databases before choosing the migration path.
- Take a database backup before the destructive cleanup migration.
- Treat the cleanup as irreversible for data. A Goose down migration can restore schema shape, but it cannot restore deleted conversations or artifacts.

### Confirmed local `000041` handling

`000041_ai_artifacts.sql` belongs to the unshipped tool-based chat work. Goose status confirms that it is applied to the local database. The migration file is not tracked and has not been pushed.

Before deleting or replacing the file:

1. Keep the current `000041_ai_artifacts.sql` file long enough to roll the local database down from version 41 to version 40.
2. Verify that `ai_artifacts` and the artifact-only conversation constraint were removed by its down migration.
3. Verify that Goose reports version 40.
4. Delete the untracked old artifact migration and artifact implementation.
5. Reuse migration number 41 for the real cleanup migration.

If any shared database is later found at migration 41, stop and reassess. Do not run a different migration with the same version against that database.

### Proposed migration sequence

#### `000041_clean_old_ai_chat.sql`

- Drop old `ai_messages`.
- Drop old `ai_conversations`.
- Remove `organization_features.disabled_ai_tools`.
- Remove old chat-only indexes and constraints that are not dropped with their tables.
- Do not drop `organization_features.ai_chat`; it remains the top-level workspace switch.
- Do not drop visibility/audit, question-generation, or shared prompt tables.
- Document the destructive chat-data loss in the migration header.

The old `ai_artifacts` table is not part of this migration because the local-only artifact migration is rolled back and removed first.

#### `000042_ai_chat_v2.sql`

- Create the new project-owned conversation, turn, message, and event tables.
- Add workspace reasoning and monthly-message settings.
- Create the monthly usage table.
- Add active-turn, worker-claim, event-tail, history, and quota indexes.
- Add check constraints for every status, role, and reasoning value.

Keeping cleanup and creation separate makes the boundary clear. Both migrations can still deploy in one release.

## Target architecture

### Components

- API: authenticates, authorizes, reserves quota, creates durable turns, serves status, serves history, accepts cancellation, and tails events.
- AI chat worker: claims queued turns and calls DeepSeek independently of any browser request.
- PostgreSQL: authoritative queue, state, messages, usage, leases, cancellation requests, and stream events.
- DeepSeek client: a narrow transport that accepts messages and reasoning settings and emits reasoning phase, text deltas, usage, and errors.
- Frontend: created later against the stable API. It does not own generation state.

### Process flow

1. The client creates a conversation for a project.
2. The client submits a user message with an idempotency key and requested reasoning effort.
3. The API validates membership, project ownership, feature access, reasoning entitlement, active-turn rules, message size, and quota.
4. One transaction reserves one monthly message, inserts the user message, creates an empty assistant message, and creates a queued turn.
5. The API returns `202 Accepted` with conversation, turn, and message IDs.
6. An independent worker claims the turn with `FOR UPDATE SKIP LOCKED`.
7. The worker builds bounded context from completed messages in that project conversation.
8. The worker calls DeepSeek and writes batched durable events.
9. The worker writes the final or partial assistant message and terminal turn status.
10. A connected client observes events through SSE. A disconnected client can return later and load the authoritative turn and message.

## Database model

### `ai_conversations`

- `id UUID PRIMARY KEY`
- `project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE`
- `created_by_user_id UUID NOT NULL REFERENCES users(id)`
- `title TEXT NOT NULL`
- `created_at TIMESTAMPTZ NOT NULL`
- `updated_at TIMESTAMPTZ NOT NULL`

Rules:

- `project_id` is immutable.
- Organization scope is derived through `projects.organization_id`; do not accept it from the model or client as conversation authority.
- The title is deterministic from the first user message in version one. Do not make a second model request to create it.

### `ai_turns`

- `id UUID PRIMARY KEY`
- `conversation_id UUID NOT NULL REFERENCES ai_conversations(id) ON DELETE CASCADE`
- `created_by_user_id UUID NOT NULL REFERENCES users(id)`
- `status TEXT NOT NULL`: `queued`, `running`, `completed`, `stopped`, `failed`
- `requested_effort TEXT NOT NULL`: `none`, `low`, `high`, `max`
- `effective_effort TEXT NOT NULL`: `none`, `low`, `high`, `max`
- `model TEXT NOT NULL`
- `prompt_version TEXT NOT NULL`
- `crawl_id UUID NULL REFERENCES crawls(id) ON DELETE SET NULL`
- `client_request_id TEXT NOT NULL`
- `request_hash BYTEA NOT NULL`
- `attempt_count INTEGER NOT NULL DEFAULT 0`
- `claimed_by TEXT NULL`
- `lease_expires_at TIMESTAMPTZ NULL`
- `heartbeat_at TIMESTAMPTZ NULL`
- `cancel_requested_at TIMESTAMPTZ NULL`
- `output_started_at TIMESTAMPTZ NULL`
- `prompt_tokens INTEGER NULL`
- `reasoning_tokens INTEGER NULL`
- `completion_tokens INTEGER NULL`
- `total_tokens INTEGER NULL`
- `error_code TEXT NULL`
- `error_message TEXT NULL`
- `queued_at`, `started_at`, `completed_at`, `created_at`, `updated_at`

Rules:

- A partial unique index permits only one `queued` or `running` turn per conversation.
- A unique idempotency constraint prevents the same client request from creating or charging two turns.
- `request_hash` is the SHA-256 hash of the accepted request fields. It detects reuse of an idempotency key with different input.
- `crawl_id`, when present, must belong to the conversation project. Validate this before insertion and again when the worker loads context.
- Only the current lease owner can write worker progress or terminal state.

### `ai_messages`

- `id UUID PRIMARY KEY`
- `turn_id UUID NOT NULL REFERENCES ai_turns(id) ON DELETE CASCADE`
- `role TEXT NOT NULL`: `user`, `assistant`
- `status TEXT NOT NULL`: `pending`, `complete`, `partial`, `failed`
- `content TEXT NOT NULL DEFAULT ''`
- `created_at TIMESTAMPTZ NOT NULL`
- `updated_at TIMESTAMPTZ NOT NULL`

Rules:

- Each turn has at most one user message and one assistant message.
- No `tool` role.
- No raw reasoning column.
- Only complete user and assistant messages enter later model context.
- A stopped or interrupted assistant message remains visible as partial content but does not become authoritative model history automatically.

### `ai_turn_events`

- `id BIGSERIAL PRIMARY KEY`
- `turn_id UUID NOT NULL REFERENCES ai_turns(id) ON DELETE CASCADE`
- `event_type TEXT NOT NULL`: `phase`, `text_delta`, `completed`, `stopped`, `failed`
- `payload JSONB NOT NULL`
- `created_at TIMESTAMPTZ NOT NULL`

Rules:

- The event ID is the reconnect cursor.
- Batch text before insertion. Do not insert one row per token.
- Do not store raw DeepSeek chain-of-thought.
- Keep events for a short retention period. Keep final messages and turn metadata for normal conversation retention.

### `ai_workspace_monthly_usage`

- `organization_id UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE`
- `period_start DATE NOT NULL`
- `used_messages INTEGER NOT NULL DEFAULT 0`
- `updated_at TIMESTAMPTZ NOT NULL`
- Primary key: `(organization_id, period_start)`

Rules:

- `period_start` is the first UTC day of the calendar month.
- Usage is shared by all users in the workspace.
- Reserve usage in the same transaction that creates the turn.
- An idempotent repeat returns the existing turn and does not increment usage.
- An accepted user message remains charged if DeepSeek later fails. Provider retries do not add charges.
- Admin adjustments can be added later only if required.

## Workspace settings and admin gates

Extend `organization_features` with:

- `ai_chat BOOLEAN NOT NULL DEFAULT TRUE` — preserve the existing top-level gate.
- `ai_monthly_message_limit INTEGER NOT NULL DEFAULT 50 CHECK (ai_monthly_message_limit BETWEEN 0 AND 1000000)`.
- `ai_allowed_reasoning_efforts TEXT[] NOT NULL DEFAULT ARRAY['none', 'low', 'high', 'max']` with only the non-empty canonical subset allowed.

Behavior:

- No organization feature row uses the documented application defaults.
- `0` monthly messages disables new messages without changing access to old conversation history.
- The API validates the requested effort before reserving quota.
- The requested effort must be a member of the allowed effort list. Return `reasoning_not_allowed` when it is not.
- Admin APIs return the configured limit and allowed reasoning efforts.
- The admin UI replaces old tool checkboxes with the chat switch, monthly message limit, and allowed reasoning efforts.

If individual member overrides become a firm requirement, add a later table keyed by `(organization_id, user_id)`. Do not add override precedence in version one.

## Reasoning contract

Application values map to DeepSeek as follows:

- `none` -> thinking disabled.
- `low` -> thinking enabled and `reasoning_effort: low`.
- `high` -> thinking enabled and `reasoning_effort: high`.
- `max` -> thinking enabled and `reasoning_effort: max`.

Additional rules:

- Do not expose `medium` or `xhigh`; DeepSeek maps them to `high`.
- Keep output-token limits separate from reasoning entitlement.
- Store requested and effective effort, model, prompt version, and usage on every turn.
- Raw `reasoning_content` is consumed only to detect the `thinking` phase and token usage, then discarded.
- Temperature and related sampling settings are not part of version one because DeepSeek thinking mode ignores them.

## Minimal DeepSeek module

Keep one concrete DeepSeek streaming client behind one narrow test interface.

Input:

- Model.
- Ordered system, user, and assistant messages.
- Thinking enabled or disabled.
- Reasoning effort when thinking is enabled.
- Maximum generated tokens.

Output events:

- Reasoning started.
- Text delta.
- Final usage.
- Provider error.

Remove from the chat provider contract:

- Tool definitions.
- Tool calls.
- Tool argument fragment assembly.
- Tool roles.
- Provider-specific action payloads.

Keep one-shot generation only where a preserved non-chat feature still uses it. Do not keep a generic abstraction without an active caller.

## Project and business scope

- The conversation project is the permanent tenant-data boundary for the chat.
- At turn creation, validate that the user is a current member of the project organization.
- Store an optional validated crawl snapshot on the turn. If none is supplied, the server can resolve the latest completed crawl and store that choice.
- The worker may inject bounded ambient context such as project name, base URL, business profile, crawl timestamp, and scores.
- The worker must not read another project's business profile, crawl, GSC property, pages, issues, or scores.
- Future data capabilities must receive a server-created project scope. Their model schema must not contain IDs.
- No data tools are included in this rewrite. The project boundary is established now so later capabilities cannot escape it.

## Worker design

### Deployment

- Add a separate AI chat worker process so AI concurrency and timeouts can scale independently from crawl and visibility workers.
- The API only enqueues and observes turns.
- API deployments and browser disconnects do not cancel worker jobs.

### Claiming

- Claim queued turns with a short transaction and `FOR UPDATE SKIP LOCKED`.
- Set `claimed_by`, increment `attempt_count`, set `running`, and establish a lease atomically.
- Use configurable worker concurrency.
- Add a global worker capacity and a per-user active-turn limit. Start with a small explicit value, such as three concurrent turns per user, and make it configuration rather than product entitlement.

### Lease and heartbeat

- Refresh the lease while the provider stream is active.
- Reject updates from a stale or different lease owner.
- Reclaim an expired turn with no output when retry policy permits.
- Mark an expired turn with partial output as interrupted/failed and preserve its partial response.

### Retry policy

- Retry only classified temporary provider or network errors.
- Retry automatically only before answer output starts.
- Use a small attempt limit.
- Never append a new generated answer to output from a failed attempt.
- Persist stable error codes rather than raw provider errors.

### Cancellation

- `POST /ai/turns/{turnID}/cancel` sets `cancel_requested_at` durably.
- The worker checks cancellation while consuming the provider stream and during heartbeats.
- Cancellation closes the provider request, persists partial output, and marks the turn `stopped`.
- Closing the page does not request cancellation.

### Shutdown

- Stop claiming new turns first.
- Give active turns a bounded grace period.
- Leave unfinished turns reclaimable through lease expiry.

## API contract

### Conversations

- `POST /projects/{projectID}/ai/conversations`
- `GET /projects/{projectID}/ai/conversations`
- `GET /ai/conversations/{conversationID}`
- `DELETE /ai/conversations/{conversationID}`

### Turns

- `POST /ai/conversations/{conversationID}/turns`
  - Body: `content`, `reasoning_effort`, optional validated `crawl_id`, and `client_request_id`.
  - Returns `202 Accepted` with the durable turn and message IDs.
- `GET /ai/turns/{turnID}`
- `POST /ai/turns/{turnID}/cancel`

### Events

- `GET /ai/turns/{turnID}/events?after={eventID}`
- Accept `Last-Event-ID` as an alternative cursor.
- Authenticate and authorize every connection through the turn's conversation project.
- Emit heartbeats so intermediaries do not close an otherwise active stream.
- Close after a terminal event.

### Stable errors

- `ai_chat_disabled`
- `monthly_message_limit_reached`
- `reasoning_not_allowed`
- `conversation_busy`
- `rate_limited`
- `context_too_large`
- `provider_unavailable`
- `provider_timeout`
- `worker_interrupted`
- `cancelled`

Do not return raw DeepSeek or database errors.

## SSE implementation direction

- Keep SSE because output is one-way from server to client.
- Do not use WebSocket in version one.
- The SSE handler tails durable event rows after the cursor.
- Start with short database polling and batched event reads. This requires no Redis or new broker.
- Do not hold a dedicated PostgreSQL advisory-lock connection for the SSE lifetime.
- Add PostgreSQL `LISTEN/NOTIFY` only if measured polling load requires it; durable rows remain the source of truth.
- The frontend can always recover with `GET /ai/turns/{turnID}` and conversation history, even if event retention has expired.

## Context policy

- Include the versioned system prompt.
- Include bounded project and business context.
- Include the newest complete user/assistant message pairs that fit the model input budget.
- Exclude raw reasoning, events, partial assistant messages, failed turns, and stopped turns from authoritative replay.
- Calculate a conservative token budget, not only byte counts.
- Leave space for the selected reasoning effort and answer output.
- Do not add automatic conversation summarization in version one.

## Quota and abuse controls

- Workspace monthly allowance is the product gate.
- Count one accepted explicit user message across all workspace users.
- Do not count worker retries, provider retries, assistant messages, stream reconnects, cancellation requests, or future tool calls.
- Use the client request ID to make retries idempotent.
- Limit user-message size.
- Limit queued and active turns per user and workspace.
- Rate-limit turn creation separately from the monthly allowance.
- Return remaining workspace messages in an authorized usage response for future UI display.

## Observability

Record per turn:

- Conversation, project, organization, turn, and worker IDs.
- Queue wait time.
- Time to reasoning start.
- Time to first answer text.
- Total duration.
- Model and prompt version.
- Requested and effective effort.
- Prompt, reasoning, completion, and total tokens when provided.
- Attempt count.
- Final status and stable error code.

Do not log user content, raw reasoning, access tokens, or raw provider payloads by default.

## Frontend direction after the backend foundation

- Build the new chat UI only after worker, quota, reasoning, status, cancellation, history, and SSE contracts are stable.
- Treat the database response as authoritative and streamed events as temporary acceleration.
- On load, discover active turns for visible conversations and reconnect by cursor.
- Allow several conversation tabs to show independent running states.
- Stopping a turn calls the cancellation endpoint; aborting the local event stream only disconnects the view.
- Show reasoning choices only up to the workspace entitlement.
- Show remaining monthly messages.
- Keep the first UI version text-only. Do not add tools, files, charts, navigation, or hidden action protocols.

## Implementation phases

### Phase 0: Prepare the local migration state

- Existing chat history and chat artifacts are confirmed for deletion.
- Visibility checks, jobs, and data remain.
- Roll the local-only artifact migration down from 41 to 40 while its original file still exists.
- Verify the schema and Goose version after rollback.
- Remove the old untracked migration and artifact implementation.
- Revert the unpushed light-mode work together with the old AI chat frontend work.

### Phase 1: Remove the old product surface

- Remove old frontend chat UI and command-dock integration.
- Remove old chat routes, handlers, agent loop, tools, artifacts, gates, and tests.
- Remove dead dependencies after code deletion.
- Keep the repository compiling by temporarily exposing no AI chat route.

### Phase 2: Clean the database

- Add and run `000041_clean_old_ai_chat.sql`.
- Regenerate SQLC after old queries are removed.
- Verify that only intentional non-chat AI tables remain.

### Phase 3: Add the durable core

- Add `000042_ai_chat_v2.sql`.
- Add new SQL queries and generated SQLC.
- Add workspace quota and reasoning entitlement resolution.
- Add project-scoped conversation and idempotent turn-creation APIs.

### Phase 4: Add the worker and DeepSeek stream

- Simplify the DeepSeek chat interface.
- Add claim, lease, heartbeat, cancellation, retry, output batching, finalization, and stale recovery.
- Add the separate worker command and deployment configuration.

### Phase 5: Add status and resumable SSE

Status: complete

- Add turn status, cancellation, and event-tail endpoints.
- Verify disconnect and reconnect behavior across API and worker processes.

### Phase 6: Add the new frontend

Status: minimal pipeline complete

- Build the text-only conversation UI against the stable contract.
- Add reasoning selection, quota display, concurrent conversation state, reconnect, and explicit stop.

### Phase 7: Harden and deploy

- Run migrations against a production-shaped copy.
- Add worker dashboards and alerts.
- Load-test concurrent turns and SSE observers.
- Deploy backend and worker before enabling the new frontend.
- Enable workspaces gradually through `ai_chat`.

## Required tests

### Database and quota

- Conversation project is mandatory and immutable.
- Cross-project and cross-organization reads are rejected.
- Monthly quota is shared across workspace users.
- Concurrent requests cannot exceed the quota.
- An idempotent retry does not create a second turn or charge twice.
- Provider and worker retries do not charge again.

### Worker

- Two workers cannot claim the same turn.
- One user can run different conversations concurrently.
- One conversation cannot run two turns concurrently.
- The worker continues after the initiating client disconnects.
- Explicit cancellation stops generation and preserves partial output.
- Lease expiry recovers a turn with no output.
- Lease expiry does not duplicate a partial answer.
- Retry classification and attempt limits are enforced.

### Reasoning

- `none`, `low`, `high`, and `max` map to the correct DeepSeek request.
- Requests above the workspace maximum are rejected before quota is charged.
- Requested and effective effort are persisted.
- Raw reasoning is not stored or returned.

### SSE and API

- Event IDs are ordered and resumable.
- Reconnect emits only events after the cursor.
- Terminal turns close the stream.
- Unauthorized users cannot read status, messages, or events.
- API restart does not stop worker generation.
- Browser disconnect does not mark a turn cancelled.

### Cleanup

- No old chat tool route or tool schema remains.
- No old chat artifact route or table remains when artifacts are in deletion scope.
- No old AI dock or chat state code remains in the frontend.
- Preserved visibility/audit and crawl products still pass their tests.

## Acceptance criteria

- The initial product is a project-owned, text-only DeepSeek chat with no tools.
- Creating a turn returns quickly and does not hold the provider request open in the API handler.
- The worker completes the answer after the user closes the browser.
- A returning user can load the completed or partial answer from the database.
- Several conversations for one user can run concurrently within configured capacity.
- One conversation has deterministic turn ordering.
- Workspace monthly user-message limits are atomic and admin-controlled.
- Workspace reasoning limits are server-enforced and admin-controlled.
- SSE reconnects by durable event ID and is never the source of truth.
- Old chat tables, tools, artifacts, and frontend state do not linger.
- The remaining DeepSeek module is small, testable, and contains no tool protocol.

## Tool layer (Phase 8)

### Goal

- Project-scoped tool calling for the durable AI chat, added on top of the text product.
- Step 1 delivers the pipeline foundations and the first raw tool (`read_issues`) plus
  workspace gating. No worker wiring, no model access, no chat frontend yet.

### Foundations (inert until the worker loop wires)

- `ai_tool_calls`: the durable tool log. One row per tool call with per-turn `seq`,
  provider `call_id`, name, args, status, result content, and UI summary. The
  resumable agent loop will rebuild its in-flight context from this log.
- `ai_turns.status` gains `waiting`: a turn paused on user input. No worker holds a
  lease while waiting; the future respond endpoint flips it back to `queued`. The
  one-active-turn partial index includes it.
- `ai_turn_events.event_type` gains `tool_call` and `tool_result` as the live view.
- No question timeout: a waiting turn persists until answered or cancelled.

### Tool catalog and gating

- Package `internal/aichattools`: ordered catalog of `Def{Name, Label, Description,
  Schema}` + `Execute(ctx, args, Scope)`. `Scope` carries server-derived
  UserID/ProjectID/CrawlID and the sqlc queries; tool schemas never contain tenant IDs.
- Gating is a **denylist**: `organization_features.disabled_ai_tools TEXT[]` (default
  empty = all tools). New tools ship enabled everywhere; admins opt out.
- Admin API lists the full catalog and accepts a disabled list; unknown names are
  rejected. The admin workspace drawer gains a nested "AI tools" drawer with
  checkboxes.
- Enforcement (wired later): the resolved tool set is snapshotted onto the turn at
  creation and the worker sends only that set.

### `read_issues` contract

- Reads `crawl_issues` of the turn's crawl. Args (all optional): `pillar`
  (seo/aeo/pagespeed), `bucket` (id or label), `issue_type`, `severity`
  (high/medium/low), `urls` (<=25), `limit` (default 25, max 50), `offset` (>=0).
- Returns `total_matching`, `breakdown` (top buckets and issue types with labels and
  severity counts), `issues[]` rows with the deterministic recommended fix folded in,
  `next_offset`, and `has_more`. Stable order: severity desc, bucket, issue_type,
  url, id.
- Caps: message/details ~250 chars, breakdown top 20, per-turn row budget 200
  (enforced by the loop via the shared budget counter).
- Unknown bucket/issue_type in a filter returns an error listing valid values for
  that crawl. The breakdown teaches the model the crawl's vocabulary so it never
  guesses.

### Future (recorded, not built)

- `ask_user`: suspends the turn (`waiting`), frontend renders the question,
  `POST /ai/turns/{turnID}/respond` resumes it. Answers are tool results, never
  quota-charged user messages.
- Write tools require a server-enforced confirmation through `ask_user`; no direct
  writes.
- Charts and citations ride the tool log; citations are a prompt + rendering
  convention, not a tool.
