package app

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestParseAITurnEventCursor(t *testing.T) {
	for _, test := range []struct {
		name   string
		target string
		header string
		want   int64
		bad    bool
	}{
		{name: "default", target: "/"},
		{name: "header fallback", header: "9", want: 9},
		{name: "query wins", target: "/?after=7", header: "9", want: 7},
		{name: "negative", target: "/?after=-1", bad: true},
		{name: "not an integer", target: "/?after=nope", bad: true},
		{name: "empty query wins", target: "/?after=", header: "9", bad: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := test.target
			if target == "" {
				target = "/"
			}
			r := httptest.NewRequest("GET", target, nil)
			r.Header.Set("Last-Event-ID", test.header)
			got, err := parseAITurnEventCursor(r)
			if test.bad {
				if err == nil {
					t.Fatal("expected invalid cursor")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("cursor = %d, err = %v; want %d", got, err, test.want)
			}
		})
	}
}

func TestWriteAITurnSSEBatchIsOrderedAndResumable(t *testing.T) {
	response := httptest.NewRecorder()
	cursor := int64(1)
	events := []sqlc.ListAITurnEventsForUserRow{
		{ID: 2, EventType: "phase", Payload: []byte(`{"phase":"started"}`)},
		{ID: 4, EventType: "text_delta", Payload: []byte(`{"text":"hello"}`)},
	}
	if !writeAITurnSSEBatch(response, response, events, &cursor) {
		t.Fatal("write batch failed")
	}
	if cursor != 4 {
		t.Fatalf("cursor = %d, want 4", cursor)
	}
	want := "id: 2\nevent: phase\ndata: {\"phase\":\"started\"}\n\nid: 4\nevent: text_delta\ndata: {\"text\":\"hello\"}\n\n"
	if strings.TrimSpace(response.Body.String()) != strings.TrimSpace(want) {
		t.Fatalf("SSE body = %q, want %q", response.Body.String(), want)
	}
}
