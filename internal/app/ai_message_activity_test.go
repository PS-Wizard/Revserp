package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestActivityFromContentBlocks(t *testing.T) {
	blocks := []byte(`[
		{"type":"activity","round":0,"text":"Checking the page.","status":"complete","reasoning_content":"private reasoning"},
		{"type":"activity","round":1,"text":"Reading live output.","status":"running"},
		{"type":"provider_transcript","round":2,"text":"private transcript","status":"complete"},
		{"type":"activity","round":-1,"text":"invalid round","status":"complete"},
		{"type":"activity","round":2,"text":"invalid status","status":"unknown"}
	]`)
	want := []aiMessageActivity{
		{Round: 0, Text: "Checking the page.", Status: "complete"},
		{Round: 1, Text: "Reading live output.", Status: "running"},
	}
	if got := activityFromContentBlocks("assistant", blocks); !reflect.DeepEqual(got, want) {
		t.Fatalf("activity = %#v, want %#v", got, want)
	}
	for _, tc := range []struct {
		role   string
		blocks []byte
	}{
		{role: "user", blocks: blocks},
		{role: "assistant"},
		{role: "assistant", blocks: []byte(`{"type":"activity"}`)},
		{role: "assistant", blocks: []byte(`invalid`)},
	} {
		if got := activityFromContentBlocks(tc.role, tc.blocks); len(got) != 0 {
			t.Fatalf("unexpected activity for %s: %#v", tc.role, got)
		}
	}
}

func TestAITurnResponseProjectsOnlyPublicActivity(t *testing.T) {
	response := newAITurnResponse(aiTurnSnapshot{}, []sqlc.ListAIMessagesForUserRow{
		{
			Role: "assistant", Content: "The change is not verified yet.",
			ContentBlocks: []byte(`[
				{"type":"activity","round":0,"text":"Read schema settings.","status":"complete","reasoning_content":"secret reasoning"},
				{"type":"provider_transcript","text":"secret transcript"}
			]`),
		},
		{Role: "user", Content: "What changed?", ContentBlocks: []byte(`[{"type":"activity","round":0,"text":"not assistant activity","status":"complete"}]`)},
	})
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret", "reasoning_content", "provider_transcript", "not assistant activity"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("public response contains %q: %s", forbidden, encoded)
		}
	}
	if len(response.Messages[0].Activity) != 1 || len(response.Messages[1].Activity) != 0 {
		t.Fatalf("unexpected projected messages: %#v", response.Messages)
	}
	if response.Messages[0].Content != "The change is not verified yet." {
		t.Fatal("activity changed the final answer")
	}
}
