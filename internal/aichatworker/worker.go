// Package aichatworker executes durable AI chat turns.
package aichatworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/aiprompt"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	defaultLease           = 45 * time.Second
	defaultHeartbeat       = 10 * time.Second
	defaultFlush           = 100 * time.Millisecond
	defaultRecovery        = time.Minute
	defaultShutdownGrace   = 10 * time.Second
	defaultAttempts        = 2
	maxWorkerSlots         = 20
	maxAgentRounds         = 20
	toolRowBudget          = 200
	pageContentBudgetBytes = 96 << 10
	pageContentBudgetPages = 5
	// Web search and fetch are free but rate limited across the whole deployment
	// (30 queries and 150 URLs per minute), so one turn gets a small allowance.
	webSearchBudgetPerTurn = 3
	webFetchBudgetPerTurn  = 3
	// Autocomplete is free but a turn can chain expands (about 27 requests each),
	// so the tool gets both a call cap and a request cap.
	suggestCallsPerTurn    = 4
	suggestRequestsPerTurn = 40
	toolResultContentCap   = 32 << 10
)

// Config contains the AI chat worker settings.
type Config struct {
	ID           string
	Concurrency  int
	PollInterval time.Duration
	TurnTimeout  time.Duration
}

// Worker claims and executes durable AI chat turns.
type Worker struct {
	pool     *pgxpool.Pool
	provider ai.Streamer
	cfg      Config

	// GSC is the search console data fetcher for tool calls; nil when the
	// worker has no search console access configured (tools report it as an
	// ordinary unavailable state).
	GSC aichattools.GSCFetcher

	// Web is the TinyFish-backed web search and fetch client for tool calls;
	// nil when no tinyfish key is configured (tools report it as an ordinary
	// unavailable state).
	Web aichattools.WebClient

	// Suggest is the Google autocomplete reader for tool calls; nil when the
	// worker could not be wired to the endpoint (tools report it as an ordinary
	// unavailable state).
	Suggest aichattools.SuggestClient

	// MCPDial connects one generic MCP session per connection per turn; nil
	// when no connector is wired (tests inject a fake). A nil connector
	// leaves MCP tools disconnected and native chat working. Wired in
	// cmd/ai-chat-worker.
	MCPDial MCPConnector

	lease     time.Duration
	heartbeat time.Duration
	// approvalWait bounds how long one tool call waits in this process for a
	// user decision; past it the call is denied.
	approvalWait  time.Duration
	flushInterval time.Duration
	recovery      time.Duration
	shutdownGrace time.Duration
}

type turn struct {
	ID             pgtype.UUID
	ConversationID pgtype.UUID
	Effort         string
	Model          string
	AttemptCount   int32
	DisabledTools  []string
}

// New creates an AI chat worker. One process has at most 20 shared slots.
func New(pool *pgxpool.Pool, provider ai.Streamer, cfg Config) *Worker {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.Concurrency > maxWorkerSlots {
		cfg.Concurrency = maxWorkerSlots
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.TurnTimeout <= 0 {
		cfg.TurnTimeout = 20 * time.Minute
	}
	return &Worker{
		pool:          pool,
		provider:      provider,
		cfg:           cfg,
		lease:         defaultLease,
		heartbeat:     defaultHeartbeat,
		approvalWait:  defaultApprovalWait,
		flushInterval: defaultFlush,
		recovery:      defaultRecovery,
		shutdownGrace: defaultShutdownGrace,
	}
}

// Run stops claims when ctx ends, then gives active streams a bounded grace.
func (w *Worker) Run(ctx context.Context) error {
	persistCtx, cancelPersist := w.persistenceContext()
	if err := w.recover(persistCtx); err != nil {
		cancelPersist()
		return fmt.Errorf("recover turns: %w", err)
	}
	cancelPersist()

	claimCtx, stopClaims := context.WithCancel(ctx)
	activeCtx, stopActive := context.WithCancel(context.Background())
	defer stopClaims()
	defer stopActive()

	var wg sync.WaitGroup
	wg.Add(w.cfg.Concurrency + 1)
	go func() {
		defer wg.Done()
		w.recoveryLoop(claimCtx)
	}()
	for range w.cfg.Concurrency {
		go func() {
			defer wg.Done()
			w.loop(claimCtx, activeCtx)
		}()
	}

	<-ctx.Done()
	stopClaims()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(w.shutdownGrace):
		stopActive()
		<-done
	}
	return nil
}

func (w *Worker) recoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(w.recovery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			persistCtx, cancel := w.persistenceContext()
			if err := w.recover(persistCtx); err != nil {
				log.Printf("ai chat recovery failed: worker_id=%s error=%v", w.cfg.ID, err)
			}
			cancel()
		}
	}
}

func (w *Worker) loop(claimCtx, activeCtx context.Context) {
	for claimCtx.Err() == nil {
		claimed, err := w.claim(claimCtx)
		if err == nil {
			log.Printf("ai chat turn claimed: worker_id=%s turn_id=%s conversation_id=%s attempt=%d effort=%s model=%s", w.cfg.ID, claimed.ID.String(), claimed.ConversationID.String(), claimed.AttemptCount, claimed.Effort, claimed.Model)
			w.run(activeCtx, claimed)
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, context.Canceled) {
			log.Printf("ai chat claim failed: worker_id=%s error=%v", w.cfg.ID, err)
		}
		if sleep(claimCtx, w.cfg.PollInterval) != nil {
			return
		}
	}
}

// claim serializes the short eligibility check per workspace and user. The
// common worker pool has no reserved user or workspace slots.
func (w *Worker) claim(ctx context.Context) (turn, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return turn{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var claimed turn
	err = tx.QueryRow(ctx, `
SELECT t.id, t.conversation_id, t.effective_effort, t.model, t.attempt_count + 1, t.disabled_ai_tools
FROM ai_turns AS t
JOIN ai_conversations AS c ON c.id = t.conversation_id
JOIN projects AS p ON p.id = c.project_id
LEFT JOIN organization_features AS f ON f.org_id = p.organization_id
WHERE t.status = 'queued'
  AND pg_try_advisory_xact_lock(hashtextextended(p.organization_id::text || ':' || t.created_by_user_id::text, 0))
  AND (
      SELECT count(*)
      FROM ai_turns AS running
      JOIN ai_conversations AS running_conversation ON running_conversation.id = running.conversation_id
      JOIN projects AS running_project ON running_project.id = running_conversation.project_id
      WHERE running.status = 'running'
        AND running.created_by_user_id = t.created_by_user_id
        AND running_project.organization_id = p.organization_id
  ) < COALESCE(f.ai_concurrent_turn_limit_per_user, 2)
ORDER BY t.queued_at, t.id
FOR UPDATE OF t SKIP LOCKED
LIMIT 1`).Scan(&claimed.ID, &claimed.ConversationID, &claimed.Effort, &claimed.Model, &claimed.AttemptCount, &claimed.DisabledTools)
	if err != nil {
		return turn{}, err
	}

	tag, err := tx.Exec(ctx, `
UPDATE ai_turns
SET status = 'running',
    claimed_by = $2,
    attempt_count = attempt_count + 1,
    started_at = COALESCE(started_at, now()),
    heartbeat_at = now(),
    lease_expires_at = now() + $3::interval,
    updated_at = now()
WHERE id = $1 AND status = 'queued'`, claimed.ID, w.cfg.ID, w.lease.String())
	if err != nil {
		return turn{}, err
	}
	if tag.RowsAffected() != 1 {
		return turn{}, pgx.ErrNoRows
	}
	if err := tx.Commit(ctx); err != nil {
		return turn{}, err
	}
	return claimed, nil
}

func (w *Worker) run(parent context.Context, claimed turn) {
	ctx, cancel := context.WithTimeout(parent, w.cfg.TurnTimeout)
	defer cancel()

	messages, scope, err := w.loadContext(ctx, claimed)
	if err != nil {
		log.Printf("ai chat context load failed: worker_id=%s turn_id=%s error=%v", w.cfg.ID, claimed.ID.String(), err)
		w.finalizeAndLog(claimed, "failed", "worker_interrupted", "failed", ai.Usage{})
		return
	}

	queries := sqlc.New(w.pool)
	// One registry per turn drives both the provider-facing defs and the
	// executor, so the model cannot run a disabled or unexposed tool by
	// guessing its name. MCP tools join the same registry for this turn
	// only; nothing is shared across turns.
	var registry *aichattools.Registry
	var mcpStatus string
	var mcpHandles *mcpHandleSet
	closeMCP := func() {}
	if scope.LocationID.Valid {
		registry = aichattools.NewLocationScopedRegistry(claimed.DisabledTools)
	} else {
		registry = aichattools.NewFilteredRegistry(claimed.DisabledTools)
		mcpStatus, mcpHandles, closeMCP = w.setupMCP(ctx, scope, claimed.DisabledTools, registry)
	}
	if closeMCP != nil {
		defer closeMCP()
	}
	if mcpStatus != "" && len(messages) > 0 {
		messages[0].Content += "\n\n--- MCP context ---\n" + mcpStatus
	}
	allowed := allowedToolsFromRegistry(registry)
	messages = fitHistoryToTools(messages, claimed.Model, claimed.Effort, allowed)
	toolScope := aichattools.Scope{
		UserID:            scope.UserID,
		ProjectID:         scope.ProjectID,
		CrawlID:           scope.CrawlID,
		LocationID:        scope.LocationID,
		Queries:           queries,
		DB:                w.pool,
		GSC:               w.GSC,
		Web:               w.Web,
		Suggest:           w.Suggest,
		RowBudget:         aichattools.NewBudget(toolRowBudget),
		PageContentBudget: aichattools.NewPageContentBudget(pageContentBudgetBytes, pageContentBudgetPages),
		WebBudget:         aichattools.NewWebBudget(webSearchBudgetPerTurn, webFetchBudgetPerTurn),
		SuggestBudget:     aichattools.NewSuggestBudget(suggestCallsPerTurn, suggestRequestsPerTurn),
	}

	flushTicker := time.NewTicker(w.flushInterval)
	heartbeatTicker := time.NewTicker(w.heartbeat)
	defer flushTicker.Stop()
	defer heartbeatTicker.Stop()

	var buffer strings.Builder
	var usage ai.Usage
	thinking := false
	writing := false
	output := false
	cancelRequested := false
	timedOut := false
	ctxDone := ctx.Done()

	flush := func(freshContext bool) error {
		if buffer.Len() == 0 {
			return nil
		}
		text := buffer.String()
		var flushCtx context.Context = ctx
		var cancelFlush context.CancelFunc
		if freshContext {
			flushCtx, cancelFlush = w.persistenceContext()
			defer cancelFlush()
		}
		if err := w.flush(flushCtx, claimed, text); err != nil {
			return err
		}
		buffer.Reset()
		output = true
		return nil
	}

	// fail maps a tool-phase error to the round-loop control flow: whether
	// rounds must stop and whether run must abandon the turn entirely (lost
	// lease or database failure; recovery owns the turn afterwards).
	fail := func(err error) (stop bool, abandon bool) {
		if err == nil {
			return false, false
		}
		if parent.Err() != nil {
			if flushErr := flush(true); flushErr != nil {
				log.Printf("ai chat shutdown flush failed: worker_id=%s turn_id=%s error=%v", w.cfg.ID, claimed.ID.String(), flushErr)
			}
			log.Printf("ai chat turn abandoned: worker_id=%s turn_id=%s reason=parent_gone", w.cfg.ID, claimed.ID.String())
			return true, true
		}
		timed := ctx.Err() != nil
		if timed {
			timedOut = true
		}
		cancel()
		return true, !timed
	}

	failTurn := func(reason string, err error) {
		log.Printf("ai chat turn failed: worker_id=%s turn_id=%s reason=%s error=%v", w.cfg.ID, claimed.ID.String(), reason, err)
		w.finalizeAndLog(claimed, "failed", "worker_interrupted", messageStatusForOutput(output), usage)
		cancel()
	}

	live := messages
	var emittedText bool
	var providerErr error
	var currentRequest ai.Request
	var currentRequestInputTokens, currentRound int
	tracker := &chatContextTracker{}
	rundown := map[string]string{}
	modelBudget := chatInputBudgetTokens(claimed.Model)

	for round := 0; ; round++ {
		reqMessages := live
		reqTools := allowed
		final := round >= maxAgentRounds
		currentRound = round
		currentRequest = ai.Request{Model: claimed.Model, Effort: claimed.Effort, Messages: reqMessages, Tools: reqTools}
		currentRequestInputTokens = tracker.inputTokens(currentRequest, live)
		if !final && currentRequestInputTokens > modelBudget {
			final = true
			logChatContextDiagnostic("terminal_switch", w.cfg.ID, claimed.ID.String(), claimed.Model, round, currentRequest, currentRequestInputTokens, modelBudget)
		}
		if final {
			reqTools = nil
			terminalMessages, terminalTokens, ok := terminalNoToolsMessages(live, rundown, claimed.Model)
			currentRequest = ai.Request{Model: claimed.Model, Effort: claimed.Effort, Messages: terminalMessages}
			currentRequestInputTokens = terminalTokens
			if !ok {
				logChatContextDiagnostic("local_guard_exhausted", w.cfg.ID, claimed.ID.String(), claimed.Model, round, currentRequest, terminalTokens, modelBudget)
				w.finalizeAndLog(claimed, "failed", "context_too_large", messageStatusForOutput(output), usage)
				return
			}
			reqMessages = terminalMessages
		}
		tracker.noteSent(len(reqTools) > 0, claimed.Effort, len(live))

		events := make(chan ai.Event, 32)
		result := make(chan error, 1)
		var roundCalls []ai.ToolCall
		var roundText strings.Builder
		var roundReasoning strings.Builder
		var roundUsage ai.Usage
		haveRoundUsage := false
		go func() {
			requestStart := time.Now()
			err := w.provider.Stream(ctx, ai.Request{
				Model:    claimed.Model,
				Effort:   claimed.Effort,
				Messages: reqMessages,
				Tools:    reqTools,
				OnReasoningDelta: func(delta string) {
					roundReasoning.WriteString(delta)
				},
				OnStreamDiagnostic: func(diag ai.ChatStreamDiagnostic) {
					logChatProviderRequestDiagnostic(w.cfg.ID, claimed.ID.String(), round, claimed.Model, claimed.Effort, time.Since(requestStart), diag)
				},
			}, func(event ai.Event) error {
				if event.ToolCall != nil {
					roundCalls = append(roundCalls, *event.ToolCall)
				}
				select {
				case events <- event:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			close(events)
			result <- err
		}()

		for events != nil || result != nil {
			select {
			case event, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if event.ToolCall != nil {
					output = true
					writing = false
					continue
				}
				if event.Usage != nil {
					usage.Prompt += event.Usage.Prompt
					usage.Reasoning += event.Usage.Reasoning
					usage.Completion += event.Usage.Completion
					usage.Total += event.Usage.Total
					roundUsage = *event.Usage
					haveRoundUsage = true
				}
				if event.Thinking && !thinking {
					if !cancelRequested {
						if err := w.event(ctx, claimed, "phase", map[string]string{"phase": "thinking"}); err != nil {
							failTurn("phase_event", err)
							return
						}
					}
					thinking = true
				}
				if event.Text != "" {
					if !writing {
						if !cancelRequested {
							if err := w.event(ctx, claimed, "phase", map[string]string{"phase": "writing"}); err != nil {
								failTurn("phase_event", err)
								return
							}
						}
						writing = true
					}
					if needParagraphBreak(emittedText, roundText.String(), event.Text) {
						buffer.WriteString("\n\n")
					}
					roundText.WriteString(event.Text)
					buffer.WriteString(event.Text)
					if strings.TrimSpace(event.Text) != "" {
						emittedText = true
					}
					if buffer.Len() >= 4096 {
						if err := flush(cancelRequested); err != nil {
							failTurn("flush", err)
							return
						}
					}
				}
			case err := <-result:
				providerErr = err
				result = nil
			case <-flushTicker.C:
				if err := flush(cancelRequested); err != nil {
					failTurn("flush", err)
					return
				}
			case <-heartbeatTicker.C:
				if cancelRequested {
					continue
				}
				requested, err := w.refreshLease(ctx, claimed)
				if err != nil {
					failTurn("refresh_lease", err)
					return
				}
				if requested {
					cancelRequested = true
					cancel()
					ctxDone = nil
				}
			case <-ctxDone:
				ctxDone = nil
				switch {
				case cancelRequested:
				case parent.Err() != nil:
					if err := flush(true); err != nil {
						log.Printf("ai chat shutdown flush failed: worker_id=%s turn_id=%s error=%v", w.cfg.ID, claimed.ID.String(), err)
					}
					return
				default:
					timedOut = true
					cancel()
				}
			}
		}

		if haveRoundUsage {
			tracker.noteUsage(roundUsage)
		}
		if err := flush(cancelRequested || timedOut); err != nil {
			failTurn("flush", err)
			return
		}
		if cancelRequested || timedOut || providerErr != nil || final || len(roundCalls) == 0 {
			break
		}

		// Tool round: persist and execute every call, then loop for the next
		// provider round. Tool activity counts as output, so a later provider
		// failure never auto-retries the turn.
		output = true
		writing = false

		var nextSeq int32
		if err := w.pool.QueryRow(ctx, `SELECT COALESCE(MAX(seq), -1) + 1 FROM ai_tool_calls WHERE turn_id = $1`, claimed.ID).Scan(&nextSeq); err != nil {
			failTurn("next_seq", err)
			return
		}

		live = append(live, ai.Message{Role: ai.RoleAssistant, Content: roundText.String(), ToolCalls: roundCalls, ReasoningContent: roundReasoning.String()})
		toolStop := false
		first := true
		for _, call := range roundCalls {
			if ctx.Err() != nil {
				timedOut = true
				cancel()
				toolStop = true
				break
			}
			cancelled, err := w.refreshLease(ctx, claimed)
			if stop, abandon := fail(err); stop {
				if abandon {
					return
				}
				toolStop = true
				break
			}
			if cancelled {
				cancelRequested = true
				cancel()
				ctxDone = nil
				toolStop = true
				break
			}
			if first {
				first = false
				if err := w.event(ctx, claimed, "phase", map[string]string{"phase": "working"}); err != nil {
					failTurn("phase_event", err)
					return
				}
				if _, err := w.pool.Exec(ctx, `
UPDATE ai_turns
SET output_started_at = COALESCE(output_started_at, now()), updated_at = now()
WHERE id = $1 AND status = 'running' AND claimed_by = $2 AND lease_expires_at > now()`, claimed.ID, w.cfg.ID); err != nil {
					failTurn("output_started", err)
					return
				}
			}
			if !json.Valid([]byte(call.Args)) {
				// Truncated streams (output budget cut a large payload
				// mid-string) are not valid JSON, and the jsonb cast
				// below would fail the turn. Skip the row and hand the
				// model a failed result it can recover from instead.
				nextSeq++
				toolStart := time.Now()
				status, result := truncatedToolArgsResult(call)
				recordToolRundown(rundown, call.ID, status, result.Summary)
				result.Content = capToolResultContent(result.Content)
				if err := w.event(ctx, claimed, "tool_result", map[string]string{"id": call.ID, "name": call.Name, "summary": result.Summary, "status": status}); err != nil {
					failTurn("tool_result_event", err)
					return
				}
				log.Printf("ai chat tool call finished: worker_id=%s turn_id=%s call_id=%s name=%s status=%s duration=%s", w.cfg.ID, claimed.ID.String(), call.ID, call.Name, status, time.Since(toolStart))
				live = append(live, ai.Message{Role: ai.RoleTool, Content: result.Content, ToolCallID: call.ID, Name: call.Name})
				continue
			}
			// MCP calls consult the saved user policy before anything runs. The
			// gate only covers a tool this turn's active registry still serves
			// under a known connection alias: a disabled or unknown tool falls
			// through to the unknown-tool result instead of an approval. Deny
			// never dispatches, even for a call created before the Deny. Allow
			// executes with guards and semantic safety checks but no prompt.
			// Ask parks this run in place: the approval row, bound to the exact
			// connection, remote tool, immutable args, and schema digest, and
			// its event are committed, then the same process waits for the
			// decision with the lease held and the transcript in memory.
			var approval pgtype.UUID
			_, registered := registry.Get(call.Name)
			// Call-scoped proof: a previous call's approval never authorizes
			// this one, even for the same alias. The current call earns its
			// own proof below only after its exact decision is marked
			// executing, and the proof is dropped when its dispatch ends.
			mcpHandles.clearApproved()
			if registered {
				if handle, remote, isMCP := mcpHandles.toolHandle(call.Name); isMCP {
					// Current-row verification: membership, feature, revision,
					// exact tool presence, current schema digest, and rule.
					permission, currentDigest, checkErr := w.verifyMCPCallCurrent(ctx, scope, handle.connectionID, handle.revision, remote)
					if checkErr != nil || permission == "deny" {
						if err := w.denyToolCall(ctx, claimed, call, &live); err != nil {
							failTurn("deny_mcp_call", err)
							return
						}
						recordToolRundown(rundown, call.ID, "failed", "not approved")
						continue
					}
					// Semantic safety preparation reads only and never
					// dispatches the mutation. Its preflight reads run through
					// the policy-enforcing session, so they need the exact
					// read tool on Allow; a denied or unavailable read only
					// costs the preview, and the exact-arguments proposal
					// still goes through Ask or Allow for the write itself.
					proposal, proposalErr := aichattools.PrepareMCPApproval(ctx, w.preflightSessionFor(scope, handle), handle.service, remote, json.RawMessage(call.Args))
					if proposalErr != nil {
						nextSeq++
						recordToolRundown(rundown, call.ID, "failed", "mcp call blocked before execution")
						if err := w.failBlockedMCPCall(ctx, claimed, call, &live, proposalErr.Error()); err != nil {
							failTurn("tool_result_event", err)
							return
						}
						continue
					}
					// The session's live schema must match the current saved
					// discovery: a /check that removed or changed the tool
					// mid-turn blocks the stale execution. The digest
					// approved below is the current one, and dispatch
					// re-verifies it again.
					liveDigest, ok := liveMCPToolDigest(handle.session, remote)
					if !ok || liveDigest != currentDigest {
						nextSeq++
						recordToolRundown(rundown, call.ID, "failed", "mcp call blocked before execution")
						if err := w.failBlockedMCPCall(ctx, claimed, call, &live, "the tool changed on its connection"); err != nil {
							failTurn("tool_result_event", err)
							return
						}
						continue
					}
					// Allow executes below with no approval row, but the
					// execution-start guard re-verifies the rule at dispatch:
					// an Ask that landed after this gate blocks instead of
					// running.
					if permission != "allow" {
						if err := flush(false); err != nil {
							failTurn("flush", err)
							return
						}
						requested, err := w.requestMCPApproval(ctx, claimed, call, handle, remote, liveDigest, proposal, proposalSummary(proposal))
						if stop, abandon := fail(err); stop {
							if abandon {
								return
							}
							toolStop = true
							break
						}
						decision, err := w.waitForApprovalDecision(ctx, claimed, requested)
						if stop, abandon := fail(err); stop {
							if abandon {
								return
							}
							toolStop = true
							break
						}
						switch decision {
						case approvalCancelled:
							cancelRequested = true
							cancel()
							ctxDone = nil
							toolStop = true
						case approvalDenied:
							if err := w.denyToolCall(ctx, claimed, call, &live); err != nil {
								failTurn("deny_approved_call", err)
								return
							}
							recordToolRundown(rundown, call.ID, "failed", "not approved")
						case approvalTimedOut:
							w.closeApproval(ctx, claimed, requested, "rejected", approvalTimeoutSummary)
							if err := w.denyToolCall(ctx, claimed, call, &live); err != nil {
								failTurn("deny_approved_call", err)
								return
							}
							recordToolRundown(rundown, call.ID, "failed", "not approved")
						case approvalApproved:
							stored, storedErr := queries.GetMCPApprovalByID(ctx, requested)
							if storedErr != nil {
								failTurn("approval_read", storedErr)
								return
							}
							if !w.approvedCallUnchanged(ctx, scope, approvedMCPCall{handle: handle, remote: remote, proposal: proposal, schemaDigest: liveDigest, call: call}, stored.ProposedArgs) {
								w.closeApproval(ctx, claimed, requested, "invalidated", "connection changed while waiting for approval")
								w.finalizeAndLog(claimed, "failed", "cms_connection_changed", messageStatusForOutput(output), usage)
								return
							}
							if changed, err := queries.MarkMCPApprovalExecuting(ctx, requested); err != nil || changed != 1 {
								failTurn("approval_executing", err)
								return
							}
							mcpHandles.markApproved(call.Name)
							approval = requested
						}
						if toolStop {
							break
						}
						if !approval.Valid {
							continue
						}
					}
				}
			}
			rowID, err := queries.InsertAIToolCall(ctx, sqlc.InsertAIToolCallParams{
				TurnID: claimed.ID,
				Seq:    nextSeq,
				CallID: call.ID,
				Name:   call.Name,
				Args:   []byte(call.Args),
				Status: "running",
			})
			if stop, abandon := fail(err); stop {
				if abandon {
					return
				}
				toolStop = true
				break
			}
			nextSeq++
			if err := w.event(ctx, claimed, "tool_call", map[string]any{"id": call.ID, "name": call.Name, "args": json.RawMessage(call.Args)}); err != nil {
				failTurn("tool_call_event", err)
				return
			}
			toolStart := time.Now()
			log.Printf("ai chat tool call started: worker_id=%s turn_id=%s call_id=%s name=%s args=%s", w.cfg.ID, claimed.ID.String(), call.ID, call.Name, toolArgsForLog(call.Name, call.Args))
			status, result := executeToolCall(ctx, registry, call, toolScope)
			// The dispatch window is over: drop this call's approval proof
			// so it can never authorize a later call of the same alias.
			mcpHandles.unmarkApproved(call.Name)
			status, result = normalizeToolCallResult(call.Name, status, result)
			recordToolRundown(rundown, call.ID, status, result.Summary)
			result.Content = capToolResultContent(result.Content)
			if err := queries.CompleteAIToolCall(ctx, sqlc.CompleteAIToolCallParams{
				ID:            rowID,
				Status:        status,
				ResultContent: result.Content,
				Summary:       result.Summary,
			}); err != nil {
				if stop, abandon := fail(err); stop {
					if abandon {
						return
					}
					toolStop = true
					break
				}
			}
			if err := w.event(ctx, claimed, "tool_result", map[string]string{"id": call.ID, "name": call.Name, "summary": result.Summary, "status": status}); err != nil {
				failTurn("tool_result_event", err)
				return
			}
			log.Printf("ai chat tool call finished: worker_id=%s turn_id=%s call_id=%s name=%s status=%s duration=%s", w.cfg.ID, claimed.ID.String(), call.ID, call.Name, status, time.Since(toolStart))
			live = append(live, ai.Message{Role: ai.RoleTool, Content: result.Content, ToolCallID: call.ID, Name: call.Name})
			if approval.Valid {
				approvalStatus := "completed"
				if status == "failed" {
					approvalStatus = "failed"
				}
				if _, err := queries.CompleteMCPApproval(ctx, sqlc.CompleteMCPApprovalParams{
					ApprovalID: approval, Status: approvalStatus, Summary: result.Summary,
				}); err != nil {
					failTurn("approval_complete", err)
					return
				}
			}
		}
		if toolStop {
			break
		}
	}

	if err := flush(cancelRequested || timedOut); err != nil {
		failTurn("flush", err)
		return
	}
	if cancelRequested {
		w.finalizeAndLog(claimed, "stopped", "cancelled", "partial", usage)
		return
	}
	if timedOut {
		providerErr = &ai.ProviderError{Code: "provider_timeout", Temporary: true}
	}
	if providerErr != nil {
		classified := ai.ClassifyError(providerErr)
		if classified.Temporary && !output && claimed.AttemptCount < defaultAttempts {
			if err := w.requeue(claimed); err != nil {
				log.Printf("ai chat retry failed: worker_id=%s turn_id=%s error=%v", w.cfg.ID, claimed.ID.String(), err)
			} else {
				log.Printf("ai chat turn requeued: worker_id=%s turn_id=%s attempt=%d error_code=%s", w.cfg.ID, claimed.ID.String(), claimed.AttemptCount, classified.Code)
			}
			return
		}
		messageStatus := "failed"
		if output {
			messageStatus = "partial"
		}
		if classified.Code == "context_too_large" {
			logChatContextDiagnostic("provider_rejected", w.cfg.ID, claimed.ID.String(), claimed.Model, currentRound, currentRequest, currentRequestInputTokens, modelBudget)
		}
		w.finalizeAndLog(claimed, "failed", classified.Code, messageStatus, usage)
		return
	}
	w.finalizeAndLog(claimed, "completed", "", "complete", usage)
}

// turnScope carries the server-derived identity of one turn; tools never read
// tenant IDs from model arguments.
type turnScope struct {
	UserID     pgtype.UUID
	ProjectID  pgtype.UUID
	CrawlID    pgtype.UUID
	LocationID pgtype.UUID
}

func (w *Worker) loadContext(ctx context.Context, claimed turn) ([]ai.Message, turnScope, error) {
	var projectName string
	var baseURL string
	var scope turnScope
	var useInternalPrompt bool
	var locationName, locationLocality string
	if err := w.pool.QueryRow(ctx, `
SELECT p.name, p.base_url, t.crawl_id, COALESCE(f.ai_use_internal_prompt, FALSE)::boolean, p.id, t.created_by_user_id, c.location_id, COALESCE(l.name, ''), COALESCE(l.locality, '')
FROM ai_turns AS t
JOIN ai_conversations AS c ON c.id = t.conversation_id
JOIN projects AS p ON p.id = c.project_id
LEFT JOIN project_locations AS l ON l.id = c.location_id
LEFT JOIN organization_features AS f ON f.org_id = p.organization_id
WHERE t.id = $1 AND t.conversation_id = $2`, claimed.ID, claimed.ConversationID).Scan(&projectName, &baseURL, &scope.CrawlID, &useInternalPrompt, &scope.ProjectID, &scope.UserID, &scope.LocationID, &locationName, &locationLocality); err != nil {
		return nil, turnScope{}, err
	}

	var completedAt pgtype.Timestamptz
	if scope.CrawlID.Valid {
		if err := w.pool.QueryRow(ctx, `
SELECT crawl.completed_at
FROM crawls AS crawl
JOIN ai_conversations AS conversation ON conversation.id = $2
WHERE crawl.id = $1
  AND crawl.project_id = conversation.project_id
  AND crawl.status = 'completed'`, scope.CrawlID, claimed.ConversationID).Scan(&completedAt); err != nil {
			return nil, turnScope{}, fmt.Errorf("validate turn crawl: %w", err)
		}
	}

	configRow, err := sqlc.New(w.pool).GetAIPromptConfig(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, turnScope{}, fmt.Errorf("load AI prompt config: %w", err)
	}
	internalPrompt, externalPrompt := "", ""
	if err == nil {
		internalPrompt = configRow.InternalSystemPrompt
		externalPrompt = configRow.ExternalSystemPrompt
	}
	system := composeSystemContext(aiprompt.ComposeSystemPrompt(useInternalPrompt, internalPrompt, externalPrompt), projectName, baseURL, completedAt, locationName, locationLocality)

	var currentUser string
	var currentBlocks []byte
	if err := w.pool.QueryRow(ctx, `
SELECT content, content_blocks
FROM ai_messages
WHERE turn_id = $1 AND role = 'user' AND status = 'complete'`, claimed.ID).Scan(&currentUser, &currentBlocks); err != nil {
		return nil, turnScope{}, err
	}

	rows, err := w.pool.Query(ctx, `
SELECT user_message.content, user_message.content_blocks, assistant_message.content
FROM ai_turns AS historical_turn
JOIN ai_messages AS user_message ON user_message.turn_id = historical_turn.id
    AND user_message.role = 'user' AND user_message.status = 'complete'
JOIN ai_messages AS assistant_message ON assistant_message.turn_id = historical_turn.id
    AND assistant_message.role = 'assistant' AND assistant_message.status = 'complete'
WHERE historical_turn.conversation_id = $1
  AND historical_turn.status = 'completed'
  AND historical_turn.id <> $2
ORDER BY historical_turn.created_at DESC, historical_turn.id DESC`, claimed.ConversationID, claimed.ID)
	if err != nil {
		return nil, turnScope{}, err
	}
	defer rows.Close()

	newestFirst := make([]historyPair, 0, 16)
	for rows.Next() {
		var historical historyPair
		var blocks []byte
		if err := rows.Scan(&historical.user, &blocks, &historical.assistant); err != nil {
			return nil, turnScope{}, err
		}
		historical.images = parseUserImages(blocks)
		newestFirst = append(newestFirst, historical)
	}
	if err := rows.Err(); err != nil {
		return nil, turnScope{}, err
	}

	systemMsg := ai.Message{Role: "system", Content: system}
	currentMsg := ai.Message{Role: "user", Content: currentUser, Images: parseUserImages(currentBlocks)}
	return selectHistoryPairs(systemMsg, currentMsg, newestFirst, claimed.Model, claimed.Effort, nil), scope, nil
}

func parseUserImages(blocks []byte) []ai.Image {
	if len(blocks) == 0 {
		return nil
	}
	var stored []struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	}
	if json.Unmarshal(blocks, &stored) != nil {
		return nil
	}
	images := make([]ai.Image, 0, len(stored))
	for _, block := range stored {
		if block.Type != "image" || block.MediaType == "" || block.Data == "" {
			continue
		}
		images = append(images, ai.Image{MediaType: block.MediaType, Data: block.Data})
	}
	if len(images) == 0 {
		return nil
	}
	return images
}

func composeSystemContext(prompt, projectName, baseURL string, completedAt pgtype.Timestamptz, locationName, locationLocality string) string {
	var builder strings.Builder
	builder.WriteString(prompt)
	builder.WriteString("\n\n--- Editor links ---\n")
	builder.WriteString("When opening a page would help the user and its exact URL is known from the user or tool data, add an editor link after the answer. Put each editor link on its own final line. Use [Open in editor: Page name](https://example.com/page \"revserp-editor\") as the format, replacing the label and URL. Only use pages from the selected crawl. Never invent or alter a URL.\n")
	builder.WriteString("\n\n--- Project context ---\n")
	fmt.Fprintf(&builder, "Name: %s\nURL: %s\n", projectName, baseURL)
	if locationName != "" || locationLocality != "" {
		builder.WriteString("\n--- Location context ---\n")
		builder.WriteString("This conversation is scoped to ONE location of this project, not the whole project.\n")
		if locationName != "" {
			fmt.Fprintf(&builder, "Location: %s\n", locationName)
		}
		if locationLocality != "" {
			fmt.Fprintf(&builder, "Locality: %s\n", locationLocality)
		}
		builder.WriteString("Answer about this location only. Use the location tools for its own profile and selected keywords. Never present parent project totals, parent keyword lists, or parent Search Console data as this location's data.\n")
	}
	if completedAt.Valid {
		builder.WriteString("\n--- Crawl context ---\n")
		fmt.Fprintf(&builder, "Selected crawl completed at %s.\n", completedAt.Time.UTC().Format(time.RFC3339))
	}
	return builder.String()
}

func (w *Worker) refreshLease(ctx context.Context, claimed turn) (bool, error) {
	var cancelled bool
	err := w.pool.QueryRow(ctx, `
UPDATE ai_turns
SET heartbeat_at = now(), lease_expires_at = now() + $3::interval, updated_at = now()
WHERE id = $1 AND status = 'running' AND claimed_by = $2 AND lease_expires_at > now()
RETURNING cancel_requested_at IS NOT NULL`, claimed.ID, w.cfg.ID, w.lease.String()).Scan(&cancelled)
	return cancelled, err
}

func (w *Worker) flush(ctx context.Context, claimed turn, text string) error {
	if text == "" {
		return nil
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
UPDATE ai_messages AS message
SET content = message.content || $3, status = 'partial', updated_at = now()
FROM ai_turns AS turn
WHERE message.turn_id = turn.id
  AND message.role = 'assistant'
  AND turn.id = $1
  AND turn.status = 'running'
  AND turn.claimed_by = $2
  AND turn.lease_expires_at > now()`, claimed.ID, w.cfg.ID, text)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	tag, err = tx.Exec(ctx, `
UPDATE ai_turns
SET output_started_at = COALESCE(output_started_at, now()), updated_at = now()
WHERE id = $1 AND status = 'running' AND claimed_by = $2 AND lease_expires_at > now()`, claimed.ID, w.cfg.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if err := w.eventTx(ctx, tx, claimed, "text_delta", map[string]string{"text": text}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *Worker) event(ctx context.Context, claimed turn, eventType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	tag, err := w.pool.Exec(ctx, `
INSERT INTO ai_turn_events(turn_id, event_type, payload)
SELECT id, $2, $3
FROM ai_turns
WHERE id = $1 AND status = 'running' AND claimed_by = $4 AND lease_expires_at > now()`, claimed.ID, eventType, encoded, w.cfg.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (w *Worker) eventTx(ctx context.Context, tx pgx.Tx, claimed turn, eventType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
INSERT INTO ai_turn_events(turn_id, event_type, payload)
SELECT id, $2, $3
FROM ai_turns
WHERE id = $1 AND status = 'running' AND claimed_by = $4 AND lease_expires_at > now()`, claimed.ID, eventType, encoded, w.cfg.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (w *Worker) requeue(claimed turn) error {
	ctx, cancel := w.persistenceContext()
	defer cancel()
	tag, err := w.pool.Exec(ctx, `
UPDATE ai_turns
SET status = 'queued', claimed_by = NULL, lease_expires_at = NULL,
    heartbeat_at = NULL, queued_at = now(), updated_at = now()
WHERE id = $1
  AND status = 'running'
  AND claimed_by = $2
  AND lease_expires_at > now()
  AND output_started_at IS NULL
  AND attempt_count < $3`, claimed.ID, w.cfg.ID, defaultAttempts)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (w *Worker) finalizeAndLog(claimed turn, status, code, messageStatus string, usage ai.Usage) {
	ctx, cancel := w.persistenceContext()
	defer cancel()
	if err := w.finalize(ctx, claimed, status, code, messageStatus, usage); err != nil {
		log.Printf("ai chat finalize failed: worker_id=%s turn_id=%s status=%s error=%v", w.cfg.ID, claimed.ID.String(), status, err)
		return
	}
	log.Printf("ai chat turn finalized: worker_id=%s turn_id=%s conversation_id=%s status=%s error_code=%s attempt=%d prompt_tokens=%d reasoning_tokens=%d completion_tokens=%d total_tokens=%d", w.cfg.ID, claimed.ID.String(), claimed.ConversationID.String(), status, code, claimed.AttemptCount, usage.Prompt, usage.Reasoning, usage.Completion, usage.Total)
}

func (w *Worker) finalize(ctx context.Context, claimed turn, status, code, messageStatus string, usage ai.Usage) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := w.eventTx(ctx, tx, claimed, status, map[string]string{"error_code": code}); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
UPDATE ai_turns
SET status = $3,
    completed_at = now(),
    lease_expires_at = NULL,
    heartbeat_at = now(),
    error_code = NULLIF($4, ''),
    prompt_tokens = NULLIF($5, 0),
    reasoning_tokens = NULLIF($6, 0),
    completion_tokens = NULLIF($7, 0),
    total_tokens = NULLIF($8, 0),
    updated_at = now()
WHERE id = $1 AND claimed_by = $2 AND status = 'running' AND lease_expires_at > now()`,
		claimed.ID, w.cfg.ID, status, code, usage.Prompt, usage.Reasoning, usage.Completion, usage.Total)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	tag, err = tx.Exec(ctx, `
UPDATE ai_messages
SET status = $2, updated_at = now()
WHERE turn_id = $1 AND role = 'assistant'`, claimed.ID, messageStatus)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return tx.Commit(ctx)
}

func (w *Worker) recover(ctx context.Context) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
UPDATE ai_turns
SET status = CASE WHEN output_started_at IS NULL AND attempt_count < $1 THEN 'queued' ELSE 'failed' END,
    claimed_by = NULL,
    lease_expires_at = NULL,
    heartbeat_at = NULL,
    queued_at = CASE WHEN output_started_at IS NULL AND attempt_count < $1 THEN now() ELSE queued_at END,
    completed_at = CASE WHEN output_started_at IS NULL AND attempt_count < $1 THEN completed_at ELSE now() END,
    error_code = CASE WHEN output_started_at IS NULL AND attempt_count < $1 THEN NULL ELSE 'worker_interrupted' END,
    updated_at = now()
WHERE status = 'running' AND lease_expires_at < now()
RETURNING id, status, output_started_at IS NOT NULL`, defaultAttempts)
	if err != nil {
		return err
	}
	type recoveredTurn struct {
		id      pgtype.UUID
		status  string
		partial bool
	}
	recovered := make([]recoveredTurn, 0)
	for rows.Next() {
		var item recoveredTurn
		if err := rows.Scan(&item.id, &item.status, &item.partial); err != nil {
			rows.Close()
			return err
		}
		recovered = append(recovered, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, item := range recovered {
		if item.status != "failed" {
			continue
		}
		if !item.partial {
			tag, err := tx.Exec(ctx, `
UPDATE ai_messages
SET status = 'failed', updated_at = now()
WHERE turn_id = $1 AND role = 'assistant' AND status = 'pending'`, item.id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return pgx.ErrNoRows
			}
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO ai_turn_events(turn_id, event_type, payload)
VALUES ($1, 'failed', '{"error_code":"worker_interrupted"}'::jsonb)`, item.id); err != nil {
			return err
		}
		// An approved MCP call caught mid-execution by the crash may have
		// applied remotely while its result was never saved. Fail it as
		// unknown (paired with an unknown tool result) and never retry it.
		if err := w.failRecoveredApprovals(ctx, tx, item.id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// failRecoveredApprovals marks executing approvals of a recovered-failed
// turn as failed with an unknown outcome and pairs each with an unknown
// tool result. It executes nothing: the remote call may already have
// applied, so automatic retry is forbidden.
func (w *Worker) failRecoveredApprovals(ctx context.Context, tx pgx.Tx, turnID pgtype.UUID) error {
	queries := sqlc.New(tx)
	approvals, err := queries.FailExecutingMCPApprovalsForTurn(ctx, turnID)
	if err != nil {
		return err
	}
	for _, approval := range approvals {
		payload, err := json.Marshal(map[string]any{"approval": map[string]any{
			"id": approval.ID.String(), "turn_id": approval.TurnID.String(),
			"tool_call_id": approval.ToolCallID, "tool_name": approval.ToolName, "provider": approval.Provider,
			"status": approval.Status, "summary": "cms write outcome unknown, do not retry",
		}})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ai_turn_events(turn_id, event_type, payload) VALUES ($1, 'approval_decided', $2::jsonb)`, turnID, string(payload)); err != nil {
			return err
		}
	}
	calls, err := queries.FailUnknownAIToolCallsForTurn(ctx, turnID)
	if err != nil {
		return err
	}
	for _, call := range calls {
		payload, err := json.Marshal(map[string]string{"id": call.CallID, "name": call.Name, "summary": "cms write outcome unknown, do not retry", "status": "failed"})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ai_turn_events(turn_id, event_type, payload) VALUES ($1, 'tool_result', $2::jsonb)`, turnID, string(payload)); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) persistenceContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// allowedTools maps the registry catalog minus the turn's denylist snapshot to
// provider-facing tool definitions. An empty result keeps text-only behavior.
func allowedTools(disabled []string) []ai.ToolDef {
	return allowedToolsFromRegistry(aichattools.NewFilteredRegistry(disabled))
}

// allowedToolsFromRegistry maps one per-turn registry to provider-facing
// tool definitions. The executing registry and the offered defs always derive
// from the same per-turn registry.
func allowedToolsFromRegistry(registry *aichattools.Registry) []ai.ToolDef {
	defs := make([]ai.ToolDef, 0)
	for _, def := range registry.Defs() {
		defs = append(defs, ai.ToolDef{Name: def.Name, Description: def.Description, Schema: def.Schema})
	}
	return defs
}

// normalizeToolCallResult maps application-level tool errors to failed status so
// clients can render them without changing the model-facing tool content.
func normalizeToolCallResult(name, status string, result aichattools.Result) (string, aichattools.Result) {
	if status != "completed" {
		return status, result
	}
	prefix := name + " error:"
	if !strings.HasPrefix(result.Content, prefix) {
		return status, result
	}
	if result.Summary == "" {
		result.Summary = strings.TrimSpace(strings.TrimPrefix(result.Content, prefix))
	}
	return "failed", result
}

// capToolResultContent caps tool output at the bytes stored and replayed.
func capToolResultContent(content string) string {
	if len(content) <= toolResultContentCap {
		return content
	}
	cut := toolResultContentCap
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}
	return content[:cut] + "\u2026[truncated]"
}

// truncatedToolArgsResult maps streamed tool-call arguments that are not valid
// JSON (truncated mid-string when a large payload exhausted the output
// budget) to a failed result, so the model retries with a smaller payload
// instead of the turn dying on the jsonb cast. No tool-call row is stored.
// blockedMCPCallResult maps a call blocked before execution to a failed
// result, so the model sees the block and the turn continues. The bounded
// helper reason is included verbatim: it names what moved or what was
// malformed. No tool-call row is stored and no remote call ran.
func blockedMCPCallResult(call ai.ToolCall, reason string) (string, aichattools.Result) {
	reason = strings.TrimSpace(reason)
	if len(reason) > 500 {
		reason = reason[:500] + "…[truncated]"
	}
	if reason == "" {
		reason = "the call was blocked before execution"
	}
	content := fmt.Sprintf("tool %q call %q failed: %s, and was not performed. Check the current connection state and permissions before deciding whether to retry or what safer alternative to use instead.", call.Name, call.ID, reason)
	return "failed", aichattools.Result{Content: content, Summary: "mcp call blocked before execution"}
}

// failBlockedMCPCall records a call blocked before execution as a failed
// tool result event plus the in-memory result, without storing a tool-call
// row. A non-nil return means the turn itself failed while recording.
func (w *Worker) failBlockedMCPCall(ctx context.Context, claimed turn, call ai.ToolCall, live *[]ai.Message, reason string) error {
	toolStart := time.Now()
	status, result := blockedMCPCallResult(call, reason)
	result.Content = capToolResultContent(result.Content)
	if err := w.event(ctx, claimed, "tool_result", map[string]string{"id": call.ID, "name": call.Name, "summary": result.Summary, "status": status}); err != nil {
		return err
	}
	log.Printf("ai chat tool call finished: worker_id=%s turn_id=%s call_id=%s name=%s status=%s duration=%s", w.cfg.ID, claimed.ID.String(), call.ID, call.Name, status, time.Since(toolStart))
	*live = append(*live, ai.Message{Role: ai.RoleTool, Content: result.Content, ToolCallID: call.ID, Name: call.Name})
	return nil
}

func truncatedToolArgsResult(call ai.ToolCall) (string, aichattools.Result) {
	content := fmt.Sprintf("tool %q call %q failed: the arguments were truncated or malformed (not valid JSON), so the call was not executed. The payload was too large. Resend a SHORTER payload: for MCP calls, shorten the body or split the write across several smaller update calls.", call.Name, call.ID)
	return "failed", aichattools.Result{Content: content, Summary: "arguments truncated or malformed"}
}

// executeToolCall runs one call through the registry, mapping unknown tools and
// execution errors to failed results so a bad call never breaks the turn.
func executeToolCall(ctx context.Context, registry *aichattools.Registry, call ai.ToolCall, scope aichattools.Scope) (string, aichattools.Result) {
	tool, ok := registry.Get(call.Name)
	if !ok {
		return "failed", aichattools.Result{Content: fmt.Sprintf("unknown tool %q", call.Name), Summary: "unknown tool"}
	}
	result, err := tool.Execute(ctx, json.RawMessage(call.Args), scope)
	if err != nil {
		return "failed", aichattools.Result{Content: err.Error(), Summary: "tool execution failed"}
	}
	return "completed", result
}

// toolArgsForLog bounds a tool argument string for log output, redacting
// MCP call arguments entirely: remote payloads are user content, not
// diagnostics. Persisted tool-call rows and user activity still keep args.
func toolArgsForLog(name, args string) string {
	if aichattools.IsMCPModelToolName(name) || isHistoricalMCPToolName(name) {
		return "[redacted]"
	}
	return truncateToolLog(args)
}

// isHistoricalMCPToolName matches the retired cms__/wp__ spellings so old
// turns still redact their arguments in logs.
func isHistoricalMCPToolName(name string) bool {
	return strings.HasPrefix(name, "cms__") || strings.HasPrefix(name, "wp__")
}

// truncateToolLog bounds a tool argument string for log output.
func truncateToolLog(value string) string {
	const maxLogArgs = 200
	if len(value) <= maxLogArgs {
		return value
	}
	return value[:maxLogArgs] + "..."
}

func messageStatusForOutput(output bool) string {
	if output {
		return "partial"
	}
	return "failed"
}

func needParagraphBreak(priorText bool, roundText, delta string) bool {
	return priorText && strings.TrimSpace(roundText) == "" && strings.TrimSpace(delta) != ""
}
