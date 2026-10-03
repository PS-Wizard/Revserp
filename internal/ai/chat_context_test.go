package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChatContextCapacityModelAliases(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  int
	}{
		{"deepseek-flash", 1_000_000},
		{"deepseek-v4-flash", 1_000_000},
		{"deepseek-v4-flash-vision-exp", 1_000_000},
		{"deepseek-v4-pro", 1_000_000},
		{"", 1_000_000},
		{" \t", 1_000_000},
		{" deepseek-flash ", 1_000_000},
		{"deepseek-chat", 128_000},
		{"some-other-model", 128_000},
		{"DeepSeek-Flash", 128_000},
	} {
		if got := ChatContextCapacity(tc.model); got != tc.want {
			t.Errorf("ChatContextCapacity(%q)=%d want=%d", tc.model, got, tc.want)
		}
	}
}

func TestChatReplaysReasoningRequiresToolsAndEffort(t *testing.T) {
	tools := []ToolDef{{Name: "read_issues"}}
	for _, tc := range []struct {
		name    string
		request Request
		want    bool
	}{
		{"tools with effort", Request{Tools: tools, Effort: "high"}, true},
		{"tools with empty effort", Request{Tools: tools}, true},
		{"no tools replays nothing", Request{Effort: "high"}, false},
		{"effort none", Request{Tools: tools, Effort: "none"}, false},
	} {
		if got := ChatReplaysReasoning(tc.request); got != tc.want {
			t.Errorf("%s: ChatReplaysReasoning=%v want=%v", tc.name, got, tc.want)
		}
	}
}

func TestEstimateChatInputTokensCountsReasoningOnce(t *testing.T) {
	base := Request{
		Effort:   "high",
		Messages: []Message{{Role: RoleAssistant, Content: "done", ReasoningContent: "step one step two step three"}},
	}
	withTools := base
	withTools.Tools = []ToolDef{{Name: "read_issues"}}
	noReasoning := withTools
	noReasoning.Messages = []Message{{Role: RoleAssistant, Content: "done"}}
	if delta := EstimateChatInputTokens(withTools) - EstimateChatInputTokens(noReasoning); delta <= 0 {
		t.Fatalf("replayed reasoning not counted: delta=%d", delta)
	}
	noTools := base
	noTools.Effort = "none"
	if got, want := EstimateChatInputTokens(noTools), EstimateChatInputTokens(base); got != want {
		t.Fatalf("reasoning counted without replay: got=%d want=%d", got, want)
	}
}

func TestEstimateChatInputTokensCountsToolPayloadsAndImages(t *testing.T) {
	request := Request{
		Effort: "high",
		Tools:  []ToolDef{{Name: "read_issues", Description: "Read issues", Schema: json.RawMessage(`{"type":"object"}`)}},
		Messages: []Message{
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call_1", Name: "read_issues", Args: `{"url":"https://example.com/a?b=c"}`}}},
			{Role: RoleTool, ToolCallID: "call_1", Content: "result"},
		},
	}
	without := request
	without.Tools = nil
	if EstimateChatInputTokens(request) <= EstimateChatInputTokens(without) {
		t.Fatal("tool definitions not counted")
	}
	noCalls := request
	noCalls.Messages = append([]Message{}, request.Messages...)
	noCalls.Messages[1].ToolCalls = nil
	if EstimateChatInputTokens(request) <= EstimateChatInputTokens(noCalls) {
		t.Fatal("tool call id/name/args not counted")
	}
	withImage := noCalls
	withImage.Messages = append(append([]Message{}, noCalls.Messages...),
		Message{Role: RoleUser, Images: []Image{{MediaType: "image/png", Data: strings.Repeat("A", 2000)}}})
	if EstimateChatInputTokens(withImage) <= EstimateChatInputTokens(noCalls) {
		t.Fatal("user image not counted")
	}
}

func TestEstimateChatInputTokensChargesUnicodeMoreThanAscii(t *testing.T) {
	ascii := EstimateChatInputTokens(Request{Messages: []Message{{Role: RoleUser, Content: "abcdefgh"}}})
	cyrillic := EstimateChatInputTokens(Request{Messages: []Message{{Role: RoleUser, Content: "привет"}}})
	if cyrillic <= ascii {
		t.Fatalf("unicode=%d ascii=%d: non-ASCII must cost more than an equal-length word run", cyrillic, ascii)
	}
	if cyrillic <= len("привет") {
		t.Fatalf("unicode=%d: byte length is not a token count", cyrillic)
	}
	// Approximation sanity: a plain English sentence must land in a believable band.
	sentence := EstimateChatInputTokens(Request{Messages: []Message{{Role: RoleUser, Content: "Please summarize the open issues for this repository."}}})
	if sentence < 5 || sentence > 20 {
		t.Fatalf("sentence estimate=%d out of plausible band", sentence)
	}
}

func TestChatInputTokensMatchSerializedRolesAndImageLimit(t *testing.T) {
	base := Request{Effort: "high", Tools: []ToolDef{{Name: "read_page"}}, Messages: []Message{{Role: RoleUser, Content: "hello"}}}
	ignored := base
	ignored.Messages = []Message{{Role: RoleUser, Content: "hello", ReasoningContent: strings.Repeat("private", 1000), ToolCalls: []ToolCall{{ID: "not-an-assistant-call", Args: strings.Repeat("args", 1000)}}}}
	if EstimateChatInputTokens(ignored) != EstimateChatInputTokens(base) {
		t.Fatal("user-only message fields absent from the wire must not count")
	}
	withImage := base
	withImage.Messages = []Message{{Role: RoleUser, Content: "hello", Images: []Image{{Data: strings.Repeat("A", 2000)}}}}
	if delta := EstimateChatInputTokens(withImage) - EstimateChatInputTokens(base); delta != 1024 {
		t.Fatalf("image token reserve=%d, want provider-documented maximum 1024", delta)
	}
	withImage.Messages[0].Images[0].Data = strings.Repeat("A", 200000)
	if delta := EstimateChatInputTokens(withImage) - EstimateChatInputTokens(base); delta != 1024 {
		t.Fatalf("base64 size must not change image token reserve: %d", delta)
	}
	assistant := Request{Effort: "high", Messages: []Message{{Role: RoleAssistant, Content: "answer"}}}
	ignoredImage := assistant
	ignoredImage.Messages = []Message{{Role: RoleAssistant, Content: "answer", Images: withImage.Messages[0].Images}}
	if EstimateChatInputTokens(ignoredImage) != EstimateChatInputTokens(assistant) {
		t.Fatal("assistant images are not sent by the provider client")
	}
}
