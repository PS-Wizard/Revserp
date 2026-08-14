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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/ai"
)

const (
	defaultLease         = 45 * time.Second
	defaultHeartbeat     = 10 * time.Second
	defaultFlush         = 500 * time.Millisecond
	defaultRecovery      = time.Minute
	defaultShutdownGrace = 10 * time.Second
	defaultAttempts      = 2
	maxWorkerSlots       = 20
	contextBudgetBytes   = 64 << 10
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

	lease         time.Duration
	heartbeat     time.Duration
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
		cfg.TurnTimeout = 5 * time.Minute
	}
	return &Worker{
		pool:          pool,
		provider:      provider,
		cfg:           cfg,
		lease:         defaultLease,
		heartbeat:     defaultHeartbeat,
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
SELECT t.id, t.conversation_id, t.effective_effort, t.model, t.attempt_count + 1
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
LIMIT 1`).Scan(&claimed.ID, &claimed.ConversationID, &claimed.Effort, &claimed.Model, &claimed.AttemptCount)
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

	messages, err := w.loadContext(ctx, claimed)
	if err != nil {
		log.Printf("ai chat context load failed: worker_id=%s turn_id=%s error=%v", w.cfg.ID, claimed.ID.String(), err)
		w.finalizeAndLog(claimed, "failed", "worker_interrupted", "failed", ai.Usage{})
		return
	}

	events := make(chan ai.Event, 32)
	result := make(chan error, 1)
	go func() {
		err := w.provider.Stream(ctx, ai.Request{
			Model:    claimed.Model,
			Effort:   claimed.Effort,
			Messages: messages,
		}, func(event ai.Event) error {
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

	flushTicker := time.NewTicker(w.flushInterval)
	heartbeatTicker := time.NewTicker(w.heartbeat)
	defer flushTicker.Stop()
	defer heartbeatTicker.Stop()

	var buffer strings.Builder
	var usage ai.Usage
	var providerErr error
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

	for events != nil || result != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event.Usage != nil {
				usage = *event.Usage
			}
			if event.Thinking && !thinking {
				if !cancelRequested {
					if err := w.event(ctx, claimed, "phase", map[string]string{"phase": "thinking"}); err != nil {
						cancel()
						return
					}
				}
				thinking = true
			}
			if event.Text != "" {
				if !writing {
					if !cancelRequested {
						if err := w.event(ctx, claimed, "phase", map[string]string{"phase": "writing"}); err != nil {
							cancel()
							return
						}
					}
					writing = true
				}
				buffer.WriteString(event.Text)
				if buffer.Len() >= 4096 {
					if err := flush(cancelRequested); err != nil {
						cancel()
						return
					}
				}
			}
		case err := <-result:
			providerErr = err
			result = nil
		case <-flushTicker.C:
			if err := flush(cancelRequested); err != nil {
				cancel()
				return
			}
		case <-heartbeatTicker.C:
			if cancelRequested {
				continue
			}
			requested, err := w.refreshLease(ctx, claimed)
			if err != nil {
				cancel()
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

	if err := flush(cancelRequested || timedOut); err != nil {
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
		w.finalizeAndLog(claimed, "failed", classified.Code, messageStatus, usage)
		return
	}
	w.finalizeAndLog(claimed, "completed", "", "complete", usage)
}

func (w *Worker) loadContext(ctx context.Context, claimed turn) ([]ai.Message, error) {
	var projectName string
	var baseURL string
	var crawlID pgtype.UUID
	if err := w.pool.QueryRow(ctx, `
SELECT p.name, p.base_url, t.crawl_id
FROM ai_turns AS t
JOIN ai_conversations AS c ON c.id = t.conversation_id
JOIN projects AS p ON p.id = c.project_id
WHERE t.id = $1 AND t.conversation_id = $2`, claimed.ID, claimed.ConversationID).Scan(&projectName, &baseURL, &crawlID); err != nil {
		return nil, err
	}

	system := "You are RevSERP chat v1. Give concise, useful, text-only SEO guidance. Project: " + projectName + " (" + baseURL + ")."
	if crawlID.Valid {
		var completedAt pgtype.Timestamptz
		if err := w.pool.QueryRow(ctx, `
SELECT crawl.completed_at
FROM crawls AS crawl
JOIN ai_conversations AS conversation ON conversation.id = $2
WHERE crawl.id = $1
  AND crawl.project_id = conversation.project_id
  AND crawl.status = 'completed'`, crawlID, claimed.ConversationID).Scan(&completedAt); err != nil {
			return nil, fmt.Errorf("validate turn crawl: %w", err)
		}
		if completedAt.Valid {
			system += " The selected crawl completed at " + completedAt.Time.UTC().Format(time.RFC3339) + "."
		}
	}

	var currentUser string
	if err := w.pool.QueryRow(ctx, `
SELECT content
FROM ai_messages
WHERE turn_id = $1 AND role = 'user' AND status = 'complete'`, claimed.ID).Scan(&currentUser); err != nil {
		return nil, err
	}

	rows, err := w.pool.Query(ctx, `
SELECT user_message.content, assistant_message.content
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
		return nil, err
	}
	defer rows.Close()

	type pair struct{ user, assistant string }
	remaining := contextBudgetBytes - len(system) - len(currentUser)
	pairs := make([]pair, 0, 16)
	for rows.Next() {
		var historical pair
		if err := rows.Scan(&historical.user, &historical.assistant); err != nil {
			return nil, err
		}
		pairBytes := len(historical.user) + len(historical.assistant)
		if pairBytes > remaining {
			continue
		}
		remaining -= pairBytes
		pairs = append(pairs, historical)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	messages := make([]ai.Message, 0, 2+len(pairs)*2)
	messages = append(messages, ai.Message{Role: "system", Content: system})
	for i := len(pairs) - 1; i >= 0; i-- {
		messages = append(messages,
			ai.Message{Role: "user", Content: pairs[i].user},
			ai.Message{Role: "assistant", Content: pairs[i].assistant},
		)
	}
	messages = append(messages, ai.Message{Role: "user", Content: currentUser})
	return messages, nil
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
	}
	return tx.Commit(ctx)
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
