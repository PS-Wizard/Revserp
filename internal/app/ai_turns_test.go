package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestAcceptAITurnRequest(t *testing.T) {
	crawlID := "018f39f7-0e1b-7e9c-9f3b-1e1d2c3b4a5f"
	for _, test := range []struct {
		name       string
		body       aiTurnRequest
		wantEffort string
		wantErr    turnSubmissionError
	}{
		{name: "canonical", body: aiTurnRequest{Content: " exact ", ReasoningEffort: " LOW ", ClientRequestID: " request "}, wantEffort: "low"},
		{name: "compatibility effort", body: aiTurnRequest{Content: "hello", ReasoningEffort: "xhigh", ClientRequestID: "request"}, wantEffort: "high"},
		{name: "blank content", body: aiTurnRequest{Content: " \t", ReasoningEffort: "low", ClientRequestID: "request"}, wantErr: errInvalidTurnRequest},
		{name: "oversize content", body: aiTurnRequest{Content: strings.Repeat("x", 32769), ReasoningEffort: "low", ClientRequestID: "request"}, wantErr: errInvalidTurnRequest},
		{name: "oversize client request ID", body: aiTurnRequest{Content: "hello", ReasoningEffort: "low", ClientRequestID: strings.Repeat("x", 129)}, wantErr: errInvalidTurnRequest},
		{name: "invalid effort", body: aiTurnRequest{Content: "hello", ReasoningEffort: "fast", ClientRequestID: "request"}, wantErr: errInvalidTurnRequest},
		{name: "invalid crawl", body: aiTurnRequest{Content: "hello", ReasoningEffort: "low", CrawlID: stringPtr("not-a-uuid"), ClientRequestID: "request"}, wantErr: errInvalidCrawl},
		{name: "crawl", body: aiTurnRequest{Content: "hello", ReasoningEffort: "medium", CrawlID: &crawlID, ClientRequestID: "request"}, wantEffort: "high"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := acceptAITurnRequest(test.body)
			if test.wantErr != "" {
				if err != test.wantErr {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("accept request: %v", err)
			}
			if got.content != test.body.Content || got.clientRequestID != "request" || got.effort != test.wantEffort {
				t.Fatalf("accepted request = %+v", got)
			}
		})
	}
}

func TestAITurnRequestHashUsesCanonicalAcceptedFields(t *testing.T) {
	base, err := acceptAITurnRequest(aiTurnRequest{Content: "hello", ReasoningEffort: "medium", ClientRequestID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	same, err := acceptAITurnRequest(aiTurnRequest{Content: "hello", ReasoningEffort: "high", ClientRequestID: "two"})
	if err != nil {
		t.Fatal(err)
	}
	different, err := acceptAITurnRequest(aiTurnRequest{Content: "hello ", ReasoningEffort: "high", ClientRequestID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	crawlChanged, err := acceptAITurnRequest(aiTurnRequest{Content: "hello", ReasoningEffort: "high", CrawlID: stringPtr("018f39f7-0e1b-7e9c-9f3b-1e1d2c3b4a5f"), ClientRequestID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(base.requestHash, same.requestHash) {
		t.Fatal("client request ID or compatibility effort changed hash")
	}
	if bytes.Equal(base.requestHash, different.requestHash) {
		t.Fatal("exact content did not change hash")
	}
	if bytes.Equal(base.requestHash, crawlChanged.requestHash) {
		t.Fatal("supplied crawl did not change hash")
	}
}

func TestAITurnUniqueErrorClassification(t *testing.T) {
	idempotencyConstraint := "ai_turns_conversation_id_created_by_user_id_client_request_id_key"[:63]
	if !isAITurnIdempotencyUniqueError(&pgconn.PgError{Code: "23505", ConstraintName: idempotencyConstraint}) {
		t.Fatal("truncated PostgreSQL idempotency constraint was not classified")
	}
	if isAITurnIdempotencyUniqueError(&pgconn.PgError{Code: "23505", ConstraintName: "idx_ai_turns_one_active_per_conversation"}) {
		t.Fatal("active-turn constraint was classified as idempotency")
	}
}

func TestNewAIToolCallsResponse(t *testing.T) {
	createdAt := time.Now()
	calls := newAIToolCallsResponse([]sqlc.ListAIToolCallsForTurnRow{
		{CallID: "call-1", Name: "read_issues", Args: []byte(`{"limit": 5}`), Status: "completed", Summary: "5 issues shown (7 matching total)", Seq: 0, CreatedAt: pgtype.Timestamptz{Time: createdAt, Valid: true}},
	})
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	got := calls[0]
	if got.CallID != "call-1" || got.Name != "read_issues" || string(got.Args) != `{"limit": 5}` || got.Status != "completed" || got.Summary != "5 issues shown (7 matching total)" || got.Seq != 0 || !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("call = %+v", got)
	}
	if empty := newAIToolCallsResponse(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("empty rows must marshal as []: %v", empty)
	}
}

func stringPtr(value string) *string { return &value }
