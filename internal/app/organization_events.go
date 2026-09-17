package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	organizationEventChannel = "organization_events"

	// SSE delivery bounds.
	organizationEventBatchSize         = 200
	organizationEventHeartbeatInterval = 15 * time.Second
	organizationEventMaxConnectionAge  = 5 * time.Minute

	// Listener reconnect bounds.
	organizationEventListenerMinBackoff = time.Second
	organizationEventListenerMaxBackoff = 30 * time.Second

	// Retention/cleanup bounds.
	organizationEventRetention       = 24 * time.Hour
	organizationEventCleanupInterval = time.Hour
	organizationEventCleanupBatch    = 1000
)

// organizationEventHub fans LISTEN notifications out to SSE handlers. Channels
// are buffered and sends are non-blocking, so a slow subscriber can never stall
// the listener; handlers always re-read durable rows after their cursor.
type organizationEventHub struct {
	mu   sync.Mutex
	subs map[pgtype.UUID]map[chan struct{}]struct{}
}

func newOrganizationEventHub() *organizationEventHub {
	return &organizationEventHub{subs: make(map[pgtype.UUID]map[chan struct{}]struct{})}
}

func (h *organizationEventHub) subscribe(organizationID pgtype.UUID) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs[organizationID] == nil {
		h.subs[organizationID] = make(map[chan struct{}]struct{})
	}
	h.subs[organizationID][ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			h.mu.Lock()
			if set, ok := h.subs[organizationID]; ok {
				delete(set, ch)
				if len(set) == 0 {
					delete(h.subs, organizationID)
				}
			}
			h.mu.Unlock()
		})
	}
	return ch, unsubscribe
}

func (h *organizationEventHub) wake(organizationID pgtype.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[organizationID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// wakeAll nudges every subscriber, used after a listener reconnect so handlers
// drain rows written while the notification stream was down.
func (h *organizationEventHub) wakeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.subs {
		for ch := range set {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

// StartOrganizationEventHub starts the single process-level LISTEN loop. It is
// bound to the root context and returns immediately.
func (a *App) StartOrganizationEventHub(ctx context.Context) {
	if a.OrgEvents == nil {
		return
	}
	go a.runOrganizationEventListener(ctx)
}

func (a *App) runOrganizationEventListener(ctx context.Context) {
	backoff := organizationEventListenerMinBackoff
	for ctx.Err() == nil {
		err := a.listenForOrganizationEvents(ctx, &backoff)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("organization event listener: %v", err)
		}
		// Close the missed-notify gap before reconnecting.
		a.OrgEvents.wakeAll()

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > organizationEventListenerMaxBackoff {
			backoff = organizationEventListenerMaxBackoff
		}
	}
}

// listenForOrganizationEvents holds one dedicated connection and blocks until
// it drops. A successful LISTEN resets the backoff.
func (a *App) listenForOrganizationEvents(ctx context.Context, backoff *time.Duration) error {
	conn, err := a.DB.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire listen connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+organizationEventChannel); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	*backoff = organizationEventListenerMinBackoff
	// Drain anything written while reconnecting.
	a.OrgEvents.wakeAll()

	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		organizationID, err := parseUUIDParam(notification.Payload)
		if err != nil {
			continue
		}
		a.OrgEvents.wake(organizationID)
	}
}

// StartOrganizationEventsCleanup starts the hourly bounded retention sweep.
func (a *App) StartOrganizationEventsCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(organizationEventCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := a.cleanupOrganizationEvents(ctx); err != nil && ctx.Err() == nil {
					log.Printf("organization events cleanup: %v", err)
				}
			}
		}
	}()
}

// cleanupOrganizationEvents deletes rows older than the retention window in
// bounded batches. The advisory transaction lock makes concurrent API replicas
// safe: whoever loses the lock skips this round.
func (a *App) cleanupOrganizationEvents(ctx context.Context) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := a.Queries.WithTx(tx)

	locked, err := queries.TryOrganizationEventsCleanupLock(ctx)
	if err != nil {
		return err
	}
	if !locked {
		return nil
	}

	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-organizationEventRetention), Valid: true}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		deleted, err := queries.DeleteOrganizationEventsBefore(ctx, sqlc.DeleteOrganizationEventsBeforeParams{
			Cutoff:    cutoff,
			BatchSize: organizationEventCleanupBatch,
		})
		if err != nil {
			return err
		}
		if deleted < organizationEventCleanupBatch {
			break
		}
	}
	return tx.Commit(ctx)
}

// parseOrganizationEventCursor reads ?after with a Last-Event-ID fallback. The
// second return value reports whether the client supplied a cursor at all.
func parseOrganizationEventCursor(r *http.Request) (int64, bool, error) {
	values, ok := r.URL.Query()["after"]
	if !ok {
		value := r.Header.Get("Last-Event-ID")
		if value == "" {
			return 0, false, nil
		}
		cursor, err := strconv.ParseInt(value, 10, 64)
		if err != nil || cursor < 0 {
			return 0, false, errors.New("invalid cursor")
		}
		return cursor, true, nil
	}
	if len(values) == 0 || values[0] == "" {
		return 0, false, errors.New("empty cursor")
	}
	cursor, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || cursor < 0 {
		return 0, false, errors.New("invalid cursor")
	}
	return cursor, true, nil
}

type organizationEventEnvelope struct {
	OrganizationID string          `json:"organization_id"`
	ProjectID      *string         `json:"project_id"`
	ResourceID     *string         `json:"resource_id"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      string          `json:"created_at"`
}

func nullableUUIDString(value pgtype.UUID) *string {
	if !value.Valid {
		return nil
	}
	s := value.String()
	return &s
}

// handleListOrganizationEvents streams organization activity over SSE. Access
// is membership-scoped: a non-member gets 404. A fresh request starts at the
// current head (no replay); a reconnect drains rows after its cursor.
func (a *App) handleListOrganizationEvents(w http.ResponseWriter, r *http.Request) {
	organizationID, err := parseUUIDParam(chi.URLParam(r, "organizationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}
	cursor, hasCursor, err := parseOrganizationEventCursor(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid event cursor")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User

	if _, err := a.Queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{
		OrgID:  organizationID,
		UserID: user.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "organization not found")
			return
		}
		serverError(w, r, fmt.Errorf("authorize organization events: %w", err))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		serverError(w, r, fmt.Errorf("clear sse write deadline: %w", err))
		return
	}

	// Subscribe before reading the head so events committed in between are
	// delivered by the first drain instead of being lost.
	wake, unsubscribe := a.OrgEvents.subscribe(organizationID)
	defer unsubscribe()

	if !hasCursor {
		head, err := a.Queries.GetOrganizationEventHeadForUser(r.Context(), sqlc.GetOrganizationEventHeadForUserParams{
			UserID:         user.ID,
			OrganizationID: organizationID,
		})
		if err != nil {
			serverError(w, r, fmt.Errorf("read organization event head: %w", err))
			return
		}
		cursor = head
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	// The cursor is a decimal string: frontends treat it as opaque so BIGSERIAL
	// values beyond JS safe-integer range survive.
	if _, err := fmt.Fprint(w, organizationEventReadyFrame(cursor)); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(organizationEventHeartbeatInterval)
	defer heartbeat.Stop()
	lifetime := time.NewTimer(organizationEventMaxConnectionAge)
	defer lifetime.Stop()

	for {
		if !a.drainOrganizationEvents(w, flusher, r.Context(), user.ID, organizationID, &cursor) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-lifetime.C:
			// Force reconnect so auth is rechecked.
			return
		}
	}
}

func organizationEventReadyFrame(cursor int64) string {
	return fmt.Sprintf("event: ready\ndata: {\"cursor\":\"%d\"}\n\n", cursor)
}

// drainOrganizationEvents writes every committed row after the cursor in
// ordered batches until a batch comes back short.
func (a *App) drainOrganizationEvents(w http.ResponseWriter, flusher http.Flusher, ctx context.Context, userID, organizationID pgtype.UUID, cursor *int64) bool {
	for {
		rows, err := a.Queries.ListOrganizationEventsForUser(ctx, sqlc.ListOrganizationEventsForUserParams{
			UserID:         userID,
			OrganizationID: organizationID,
			AfterID:        *cursor,
			MaxRows:        organizationEventBatchSize,
		})
		if err != nil {
			return false
		}
		if len(rows) == 0 {
			return true
		}
		for _, event := range rows {
			payload := event.Payload
			if len(payload) == 0 {
				payload = []byte("{}")
			}
			data, err := json.Marshal(organizationEventEnvelope{
				OrganizationID: event.OrganizationID.String(),
				ProjectID:      nullableUUIDString(event.ProjectID),
				ResourceID:     nullableUUIDString(event.ResourceID),
				Payload:        json.RawMessage(payload),
				CreatedAt:      formatTimestamp(event.CreatedAt),
			})
			if err != nil {
				return false
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.EventType, data); err != nil {
				return false
			}
			*cursor = event.ID
		}
		flusher.Flush()
		if len(rows) < organizationEventBatchSize {
			return true
		}
	}
}
