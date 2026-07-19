package ai

import (
	"context"
	"encoding/json"
)

// Role identifies who authored a chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one turn in a chat conversation. Reasoning content is
// intentionally not representable here: it must never be sent back to the
// model on subsequent turns.
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall // set on assistant messages that invoked tools
	ToolCallID string     // set on tool messages, references the originating ToolCall.ID
	Name       string     // tool name, set on tool messages
}

// ToolCall is a single invocation of a tool requested by the model.
type ToolCall struct {
	ID   string
	Name string
	Args string // raw JSON arguments
}

// ToolDef describes a tool the model may call.
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON Schema for the tool's parameters
}

// EventType identifies the kind of a StreamEvent.
type EventType string

const (
	EventReasoning EventType = "reasoning"
	EventText      EventType = "text"
	EventToolCall  EventType = "tool_call"
	EventUsage     EventType = "usage"
	EventDone      EventType = "done"
	EventError     EventType = "error"
)

// Usage reports token accounting for a completed turn.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// StreamEvent is one unit emitted while streaming a turn. Reasoning and Text
// events carry incremental deltas; ToolCall events carry a fully reassembled
// call (emitted once, when complete).
type StreamEvent struct {
	Type     EventType
	Delta    string // reasoning/text delta, for EventReasoning/EventText
	ToolCall *ToolCall
	Usage    *Usage // set on EventDone when available
	Err      error  // set on EventError
}

// TurnRequest is a single request to run one turn of a conversation,
// optionally with tools available for the model to call.
type TurnRequest struct {
	Model     string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
}

// Client is an LLM backend capable of both a streaming agentic turn (with
// tool calling) and a simple one-shot text generation.
type Client interface {
	StreamTurn(ctx context.Context, req TurnRequest) (<-chan StreamEvent, error)
	GenerateText(ctx context.Context, prompt string) (string, error)
}
