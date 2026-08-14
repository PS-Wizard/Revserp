package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestAITurnStatusAndCancellationIntegration(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)

	t.Run("status includes messages", func(t *testing.T) {
		submission := submitObserverTurn(t, fixture, fixture.conversationID, "status message", "status")
		response := httptest.NewRecorder()
		fixture.app.handleGetAITurn(response, observerRequest(fixture.userID, http.MethodGet, "/", submission.TurnID, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("status code = %d, body = %s", response.Code, response.Body.String())
		}
		turn := decodeAITurnResponse(t, response)
		if turn.ID != submission.TurnID.String() || turn.Status != "queued" || len(turn.Messages) != 2 {
			t.Fatalf("turn = %+v", turn)
		}
		if turn.Messages[0].Role != "user" || turn.Messages[0].Content != "status message" || turn.Messages[1].Role != "assistant" {
			t.Fatalf("messages = %+v", turn.Messages)
		}
	})

	t.Run("queued cancellation is immediate and idempotent", func(t *testing.T) {
		submission := submitObserverTurn(t, fixture, fixture.conversation(t), "queued message", "queued-cancel")
		usageBefore := fixture.usage(t)
		for attempt := 0; attempt < 2; attempt++ {
			response := httptest.NewRecorder()
			fixture.app.handleCancelAITurn(response, observerRequest(fixture.userID, http.MethodPost, "/", submission.TurnID, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("attempt %d status code = %d, body = %s", attempt, response.Code, response.Body.String())
			}
			turn := decodeAITurnResponse(t, response)
			if turn.Status != "stopped" || !turn.CancelRequested || turn.ErrorCode == nil || *turn.ErrorCode != "cancelled" || turn.Messages[1].Status != "partial" {
				t.Fatalf("attempt %d turn = %+v", attempt, turn)
			}
		}
		var events int
		if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'stopped'`, submission.TurnID).Scan(&events); err != nil {
			t.Fatalf("count stopped events: %v", err)
		}
		if events != 1 || fixture.usage(t) != usageBefore {
			t.Fatalf("events = %d, usage = %d; want 1, %d", events, fixture.usage(t), usageBefore)
		}
	})

	t.Run("running cancellation requests worker stop", func(t *testing.T) {
		submission := submitObserverTurn(t, fixture, fixture.conversation(t), "running message", "running-cancel")
		if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET status = 'running', started_at = now() WHERE id = $1`, submission.TurnID); err != nil {
			t.Fatalf("mark running: %v", err)
		}
		response := httptest.NewRecorder()
		fixture.app.handleCancelAITurn(response, observerRequest(fixture.userID, http.MethodPost, "/", submission.TurnID, nil))
		turn := decodeAITurnResponse(t, response)
		if response.Code != http.StatusOK || turn.Status != "running" || !turn.CancelRequested {
			t.Fatalf("status code = %d, turn = %+v", response.Code, turn)
		}
		var events int
		if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1`, submission.TurnID).Scan(&events); err != nil || events != 0 {
			t.Fatalf("events = %d, err = %v; want 0", events, err)
		}
	})

	t.Run("terminal cancellation is a no-op", func(t *testing.T) {
		submission := submitObserverTurn(t, fixture, fixture.conversation(t), "done message", "terminal-cancel")
		if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET status = 'completed', completed_at = now() WHERE id = $1`, submission.TurnID); err != nil {
			t.Fatalf("mark completed: %v", err)
		}
		response := httptest.NewRecorder()
		fixture.app.handleCancelAITurn(response, observerRequest(fixture.userID, http.MethodPost, "/", submission.TurnID, nil))
		turn := decodeAITurnResponse(t, response)
		if response.Code != http.StatusOK || turn.Status != "completed" || turn.CancelRequested {
			t.Fatalf("status code = %d, turn = %+v", response.Code, turn)
		}
	})
}

func TestAITurnObserverAuthorizationIntegration(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	submission := submitObserverTurn(t, fixture, fixture.conversationID, "private", "private")
	otherUserID := createObserverUser(t, fixture, "other")

	for _, test := range []struct {
		name    string
		method  string
		handler http.HandlerFunc
	}{
		{name: "status", method: http.MethodGet, handler: fixture.app.handleGetAITurn},
		{name: "cancel", method: http.MethodPost, handler: fixture.app.handleCancelAITurn},
		{name: "events", method: http.MethodGet, handler: fixture.app.handleGetAITurnEvents},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			test.handler(response, observerRequest(otherUserID, test.method, "/", submission.TurnID, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("status code = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
	var cancelRequested pgtype.Timestamptz
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT cancel_requested_at FROM ai_turns WHERE id = $1`, submission.TurnID).Scan(&cancelRequested); err != nil || cancelRequested.Valid {
		t.Fatalf("cancel requested = %v, err = %v; want null", cancelRequested, err)
	}
}

func TestAITurnEventsResumeAndTerminalCloseIntegration(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	submission := submitObserverTurn(t, fixture, fixture.conversationID, "stream", "stream")
	var firstID, secondID, terminalID int64
	for _, event := range []struct {
		typeName string
		payload  string
		id       *int64
	}{
		{typeName: "phase", payload: `{"phase":"thinking"}`, id: &firstID},
		{typeName: "text_delta", payload: `{"text":"answer"}`, id: &secondID},
		{typeName: "completed", payload: `{"error_code":""}`, id: &terminalID},
	} {
		if err := fixture.app.DB.QueryRow(fixture.ctx, `INSERT INTO ai_turn_events (turn_id, event_type, payload) VALUES ($1, $2, $3::jsonb) RETURNING id`, submission.TurnID, event.typeName, event.payload).Scan(event.id); err != nil {
			t.Fatalf("insert %s event: %v", event.typeName, err)
		}
	}
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET status = 'completed', completed_at = now() WHERE id = $1`, submission.TurnID); err != nil {
		t.Fatalf("complete turn: %v", err)
	}

	response := httptest.NewRecorder()
	headers := make(http.Header)
	headers.Set("Last-Event-ID", fmt.Sprint(firstID))
	fixture.app.handleGetAITurnEvents(response, observerRequest(fixture.userID, http.MethodGet, "/", submission.TurnID, headers))
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, fmt.Sprintf("id: %d\n", firstID)) {
		t.Fatalf("status = %d, body = %q", response.Code, body)
	}
	secondPosition := strings.Index(body, fmt.Sprintf("id: %d\n", secondID))
	terminalPosition := strings.Index(body, fmt.Sprintf("id: %d\n", terminalID))
	if secondPosition < 0 || terminalPosition <= secondPosition {
		t.Fatalf("events are missing or unordered: %q", body)
	}
}

func TestAITurnEventDisconnectDoesNotCancelIntegration(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	submission := submitObserverTurn(t, fixture, fixture.conversationID, "disconnect", "disconnect")
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_turns SET status = 'running', started_at = now() WHERE id = $1`, submission.TurnID); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	request := observerRequest(fixture.userID, http.MethodGet, "/", submission.TurnID, nil)
	ctx, cancel := context.WithCancel(request.Context())
	writer := newFlushNotifyWriter()
	done := make(chan struct{})
	go func() {
		fixture.app.handleGetAITurnEvents(writer, request.WithContext(ctx))
		close(done)
	}()
	select {
	case <-writer.flushed:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE did not stop after disconnect")
	}
	var status string
	var cancelRequested pgtype.Timestamptz
	if err := fixture.app.DB.QueryRow(fixture.ctx, `SELECT status, cancel_requested_at FROM ai_turns WHERE id = $1`, submission.TurnID).Scan(&status, &cancelRequested); err != nil {
		t.Fatalf("read turn: %v", err)
	}
	if status != "running" || cancelRequested.Valid {
		t.Fatalf("status = %q, cancel requested = %v", status, cancelRequested)
	}
}

func submitObserverTurn(t *testing.T, fixture aiTurnFixture, conversationID pgtype.UUID, content, requestID string) aiTurnSubmission {
	t.Helper()
	submission, err := fixture.submit(t, conversationID, content, "low", requestID, nil)
	if err != nil {
		t.Fatalf("submit turn: %v", err)
	}
	return submission
}

func observerRequest(userID pgtype.UUID, method, target string, turnID pgtype.UUID, headers http.Header) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.Header = headers.Clone()
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("turnID", turnID.String())
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, resolvedUserContextKey{}, resolvedUserEntry{user: sqlc.User{ID: userID}})
	return request.WithContext(ctx)
}

func decodeAITurnResponse(t *testing.T, response *httptest.ResponseRecorder) aiTurnResponse {
	t.Helper()
	var turn aiTurnResponse
	if err := json.NewDecoder(response.Body).Decode(&turn); err != nil {
		t.Fatalf("decode turn response: %v; body = %s", err, response.Body.String())
	}
	return turn
}

func createObserverUser(t *testing.T, fixture aiTurnFixture, suffix string) pgtype.UUID {
	t.Helper()
	var userID pgtype.UUID
	name := fmt.Sprintf("ai-observer-%s-%d", suffix, time.Now().UnixNano())
	if err := fixture.app.DB.QueryRow(fixture.ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', $1, $2) RETURNING id`, name, name+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create observer user: %v", err)
	}
	t.Cleanup(func() { _, _ = fixture.app.DB.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
	return userID
}

type flushNotifyWriter struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
	once    sync.Once
}

func newFlushNotifyWriter() *flushNotifyWriter {
	return &flushNotifyWriter{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{})}
}

func (writer *flushNotifyWriter) Flush() {
	writer.once.Do(func() { close(writer.flushed) })
	writer.ResponseRecorder.Flush()
}
