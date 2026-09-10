package app

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestAcceptAITurnRequest(t *testing.T) {
	crawlID := "018f39f7-0e1b-7e9c-9f3b-1e1d2c3b4a5f"
	jpeg := tinyJPEGImage()
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
		{name: "oversize content with image", body: aiTurnRequest{Content: strings.Repeat("x", 32769), Images: []aiTurnImage{jpeg}, ReasoningEffort: "low", ClientRequestID: "request"}, wantErr: errInvalidTurnRequest},
		{name: "image only", body: aiTurnRequest{Images: []aiTurnImage{jpeg}, ReasoningEffort: "low", ClientRequestID: "request"}, wantEffort: "low"},
		{name: "whitespace content with image", body: aiTurnRequest{Content: " \t", Images: []aiTurnImage{jpeg}, ReasoningEffort: "low", ClientRequestID: "request"}, wantEffort: "low"},
		{name: "too many images", body: aiTurnRequest{Images: nTinyJPEGImages(5), ReasoningEffort: "low", ClientRequestID: "request"}, wantErr: errInvalidTurnRequest},
		{name: "bad base64", body: aiTurnRequest{Images: []aiTurnImage{{MediaType: "image/jpeg", Data: "not-base64!!!"}}, ReasoningEffort: "low", ClientRequestID: "request"}, wantErr: errInvalidTurnRequest},
		{name: "https url", body: aiTurnRequest{Images: []aiTurnImage{{MediaType: "image/jpeg", Data: "https://example.com/a.jpg"}}, ReasoningEffort: "low", ClientRequestID: "request"}, wantErr: errInvalidTurnRequest},
		{name: "http url", body: aiTurnRequest{Images: []aiTurnImage{{MediaType: "image/jpeg", Data: "http://example.com/a.jpg"}}, ReasoningEffort: "low", ClientRequestID: "request"}, wantErr: errInvalidTurnRequest},
		{name: "data url", body: aiTurnRequest{Images: []aiTurnImage{{MediaType: "image/jpeg", Data: "data:image/jpeg;base64," + jpeg.Data}}, ReasoningEffort: "low", ClientRequestID: "request"}, wantErr: errInvalidTurnRequest},
		{name: "empty image data", body: aiTurnRequest{Images: []aiTurnImage{{MediaType: "image/jpeg", Data: ""}}, ReasoningEffort: "low", ClientRequestID: "request"}, wantErr: errInvalidTurnRequest},
		{name: "magic mismatch", body: aiTurnRequest{Images: []aiTurnImage{{MediaType: "image/png", Data: jpeg.Data}}, ReasoningEffort: "low", ClientRequestID: "request"}, wantErr: errInvalidTurnRequest},
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

	jpeg := tinyJPEGImage()
	withImage, err := acceptAITurnRequest(aiTurnRequest{Content: "hello", Images: []aiTurnImage{jpeg}, ReasoningEffort: "high", ClientRequestID: "img-one"})
	if err != nil {
		t.Fatal(err)
	}
	sameImage, err := acceptAITurnRequest(aiTurnRequest{Content: "hello", Images: []aiTurnImage{jpeg}, ReasoningEffort: "high", ClientRequestID: "img-two"})
	if err != nil {
		t.Fatal(err)
	}
	otherJPEG := aiTurnImage{MediaType: "image/jpeg", Data: base64.StdEncoding.EncodeToString([]byte{0xFF, 0xD8, 0xFF, 0x00})}
	changedImage, err := acceptAITurnRequest(aiTurnRequest{Content: "hello", Images: []aiTurnImage{otherJPEG}, ReasoningEffort: "high", ClientRequestID: "img-three"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(base.requestHash, withImage.requestHash) {
		t.Fatal("adding an image did not change hash")
	}
	if !bytes.Equal(withImage.requestHash, sameImage.requestHash) {
		t.Fatal("same images were not hash-stable")
	}
	if bytes.Equal(withImage.requestHash, changedImage.requestHash) {
		t.Fatal("different image bytes did not change hash")
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

func tinyJPEGImage() aiTurnImage {
	return aiTurnImage{MediaType: "image/jpeg", Data: base64.StdEncoding.EncodeToString([]byte{0xFF, 0xD8, 0xFF, 0xD9})}
}

func nTinyJPEGImages(n int) []aiTurnImage {
	images := make([]aiTurnImage, n)
	for i := range images {
		images[i] = tinyJPEGImage()
	}
	return images
}

func stringPtr(value string) *string { return &value }
