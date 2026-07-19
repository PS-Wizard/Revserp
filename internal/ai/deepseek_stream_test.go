package ai

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	openai "github.com/openai/openai-go"
)

// drainEvents collects all events sent on the channel by a synchronous
// producer using a buffered channel large enough not to block.
func drainEvents(t *testing.T, produce func(events chan<- StreamEvent)) []StreamEvent {
	t.Helper()
	events := make(chan StreamEvent, 64)
	produce(events)
	close(events)

	var collected []StreamEvent
	for event := range events {
		collected = append(collected, event)
	}
	return collected
}

// TestProcessStreamChunk_InterleavedReasoningTextAndParallelToolCalls feeds a
// synthetic sequence of chunks that mimics a real DeepSeek stream: reasoning
// deltas, then text, then two parallel tool calls fragmented across several
// chunks, finishing with tool_calls.
func TestProcessStreamChunk_InterleavedReasoningTextAndParallelToolCalls(t *testing.T) {
	chunks := []streamChunk{
		{Reasoning: "Let me check "},
		{Reasoning: "the weather and time."},
		{Text: "Sure, "},
		{Text: "checking now."},
		{ToolCalls: []toolCallFragment{{Index: 0, ID: "call_1", Name: "get_weather", ArgsDelta: `{"city":`}}},
		{ToolCalls: []toolCallFragment{{Index: 1, ID: "call_2", Name: "get_time", ArgsDelta: `{"tz":`}}},
		{ToolCalls: []toolCallFragment{{Index: 0, ArgsDelta: `"SF"}`}}},
		{ToolCalls: []toolCallFragment{{Index: 1, ArgsDelta: `"UTC"}`}}},
		{FinishReason: "tool_calls", Usage: &Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	}

	accumulator := newToolCallAccumulator()
	var usage *Usage
	events := drainEvents(t, func(out chan<- StreamEvent) {
		for _, chunk := range chunks {
			processStreamChunk(accumulator, chunk, out, &usage)
		}
	})

	var reasoning, text string
	var toolCalls []ToolCall
	for _, event := range events {
		switch event.Type {
		case EventReasoning:
			reasoning += event.Delta
		case EventText:
			text += event.Delta
		case EventToolCall:
			toolCalls = append(toolCalls, *event.ToolCall)
		default:
			t.Fatalf("unexpected event type %q in this synthetic feed", event.Type)
		}
	}

	if reasoning != "Let me check the weather and time." {
		t.Fatalf("reassembled reasoning = %q", reasoning)
	}
	if text != "Sure, checking now." {
		t.Fatalf("reassembled text = %q", text)
	}
	want := []ToolCall{
		{ID: "call_1", Name: "get_weather", Args: `{"city":"SF"}`},
		{ID: "call_2", Name: "get_time", Args: `{"tz":"UTC"}`},
	}
	if !reflect.DeepEqual(toolCalls, want) {
		t.Fatalf("tool calls = %+v, want %+v", toolCalls, want)
	}
	if usage == nil || usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v, want TotalTokens=15", usage)
	}
	if !accumulator.empty() {
		t.Fatalf("expected accumulator to be drained after finish reason")
	}
}

// TestProcessStreamChunk_TextOnlyFinish verifies a plain "stop" finish with
// no tool calls emits no spurious ToolCall event.
func TestProcessStreamChunk_TextOnlyFinish(t *testing.T) {
	chunks := []streamChunk{
		{Text: "Hello"},
		{Text: " world."},
		{FinishReason: "stop"},
	}

	accumulator := newToolCallAccumulator()
	var usage *Usage
	events := drainEvents(t, func(out chan<- StreamEvent) {
		for _, chunk := range chunks {
			processStreamChunk(accumulator, chunk, out, &usage)
		}
	})

	for _, event := range events {
		if event.Type == EventToolCall {
			t.Fatalf("unexpected tool call event for a text-only finish: %+v", event)
		}
	}
}

// TestToStreamChunk_CapturesDeepSeekReasoningContent verifies that DeepSeek's
// non-standard reasoning_content delta field, surfaced by openai-go as an
// unknown extra field, is extracted into the reasoning stream.
func TestToStreamChunk_CapturesDeepSeekReasoningContent(t *testing.T) {
	raw := `{"choices":[{"index":0,"delta":{"reasoning_content":"the model is thinking","content":"answer"},"finish_reason":""}]}`
	var chunk openai.ChatCompletionChunk
	if err := json.Unmarshal([]byte(raw), &chunk); err != nil {
		t.Fatalf("unmarshal chunk: %v", err)
	}
	converted := toStreamChunk(chunk)
	if converted.Reasoning != "the model is thinking" {
		t.Fatalf("reasoning = %q, want %q", converted.Reasoning, "the model is thinking")
	}
	if converted.Text != "answer" {
		t.Fatalf("text = %q, want %q", converted.Text, "answer")
	}
}

// fakeStream is a minimal deepSeekStream driven by a fixed chunk list, used to
// assert pumpDeepSeekStream unwinds on context cancellation rather than
// blocking on a channel send nobody is draining.
type fakeStream struct {
	chunks []openai.ChatCompletionChunk
	index  int
	closed bool
}

func (s *fakeStream) Next() bool {
	if s.index >= len(s.chunks) {
		return false
	}
	s.index++
	return true
}

func (s *fakeStream) Current() openai.ChatCompletionChunk { return s.chunks[s.index-1] }
func (s *fakeStream) Err() error                          { return nil }
func (s *fakeStream) Close() error                        { s.closed = true; return nil }

func TestPumpDeepSeekStreamCanceledConsumer(t *testing.T) {
	const buffer = 8
	chunks := make([]openai.ChatCompletionChunk, buffer+64)
	for i := range chunks {
		if err := json.Unmarshal([]byte(`{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":""}]}`), &chunks[i]); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	}
	stream := &fakeStream{chunks: chunks}
	events := make(chan StreamEvent, buffer)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pumpDeepSeekStream(ctx, stream, events)
		close(done)
	}()

	// Read exactly one event, then cancel and stop draining. The pump must
	// unwind on ctx.Done rather than block forever on a full channel.
	<-events
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pumpDeepSeekStream did not unwind after ctx cancellation")
	}
	if !stream.closed {
		t.Fatal("expected the stream to be closed by the pump")
	}
}
