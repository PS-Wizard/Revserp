package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestAIMessageActivitySnapshotsIntegration(t *testing.T) {
	fixture := newAITurnFixture(t, 10, canonicalAIReasoningEfforts)
	submission := submitObserverTurn(t, fixture, fixture.conversationID, "What changed?", "activity-snapshot")
	blocks := `[
		{"type":"activity","round":0,"text":"Read crawl issues.","status":"complete"},
		{"type":"activity","round":1,"text":"Checking the live page.","status":"running","reasoning_content":"private reasoning"},
		{"type":"provider_transcript","text":"private transcript"}
	]`
	if _, err := fixture.app.DB.Exec(fixture.ctx, `UPDATE ai_messages SET content=$2, content_blocks=$3::jsonb WHERE turn_id=$1 AND role='assistant'`, submission.TurnID, "The change is not verified yet.", blocks); err != nil {
		t.Fatal(err)
	}
	var cursor int64
	if err := fixture.app.DB.QueryRow(fixture.ctx, `INSERT INTO ai_turn_events(turn_id,event_type,payload) VALUES($1,'text_delta','{"round":1,"text":"Checking the live page."}'::jsonb) RETURNING id`, submission.TurnID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}

	turnRecorder := httptest.NewRecorder()
	fixture.app.handleGetAITurn(turnRecorder, observerRequest(fixture.userID, http.MethodGet, "/", submission.TurnID, nil))
	if turnRecorder.Code != http.StatusOK {
		t.Fatalf("turn status=%d body=%s", turnRecorder.Code, turnRecorder.Body.String())
	}
	turnBody := turnRecorder.Body.String()
	turn := decodeAITurnResponse(t, turnRecorder)
	if turn.EventCursor != cursor {
		t.Fatalf("cursor=%d, want %d", turn.EventCursor, cursor)
	}
	if len(turn.Messages) != 2 || len(turn.Messages[1].Activity) != 2 || turn.Messages[1].Activity[1].Status != "running" {
		t.Fatalf("turn messages=%#v", turn.Messages)
	}

	conversationRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("conversationID", fixture.conversationID.String())
	ctx := context.WithValue(conversationRequest.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: fixture.userID}})
	conversationRecorder := httptest.NewRecorder()
	fixture.app.handleGetAIConversation(conversationRecorder, conversationRequest.WithContext(ctx))
	if conversationRecorder.Code != http.StatusOK {
		t.Fatalf("conversation status=%d body=%s", conversationRecorder.Code, conversationRecorder.Body.String())
	}
	var conversation aiConversationDetailResponse
	if err := json.Unmarshal(conversationRecorder.Body.Bytes(), &conversation); err != nil {
		t.Fatal(err)
	}
	if len(conversation.Messages) != 2 || len(conversation.Messages[1].Activity) != 2 {
		t.Fatalf("conversation messages=%#v", conversation.Messages)
	}
	for _, body := range []string{turnBody, conversationRecorder.Body.String()} {
		for _, forbidden := range []string{"private", "reasoning_content", "provider_transcript"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("snapshot leaked %q: %s", forbidden, body)
			}
		}
		if !strings.Contains(body, "The change is not verified yet.") {
			t.Fatal("snapshot lost the final answer")
		}
	}
}
