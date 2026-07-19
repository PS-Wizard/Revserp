package ai

// toolCallFragment is one raw fragment of a streamed tool call, as delivered
// by an OpenAI-compatible streaming chat completions API. Providers split a
// single tool call across multiple stream chunks: the first fragment for a
// given Index carries ID and Name, and every fragment (including the first)
// may carry a piece of the JSON arguments to append.
type toolCallFragment struct {
	Index     int
	ID        string
	Name      string
	ArgsDelta string
}

// toolCallAccumulator reassembles fragmented streamed tool calls into
// complete ToolCall values, keyed by their stream index. Multiple tool calls
// streamed in parallel (interleaved fragments across different indices) are
// tracked independently.
type toolCallAccumulator struct {
	order []int
	calls map[int]*ToolCall
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{calls: make(map[int]*ToolCall)}
}

// add folds one fragment into the accumulator.
func (a *toolCallAccumulator) add(f toolCallFragment) {
	call, ok := a.calls[f.Index]
	if !ok {
		call = &ToolCall{}
		a.calls[f.Index] = call
		a.order = append(a.order, f.Index)
	}
	if f.ID != "" {
		call.ID = f.ID
	}
	if f.Name != "" {
		call.Name = f.Name
	}
	call.Args += f.ArgsDelta
}

// empty reports whether any fragments have been accumulated.
func (a *toolCallAccumulator) empty() bool {
	return len(a.order) == 0
}

// drain returns all accumulated tool calls in the order their first fragment
// arrived and resets the accumulator for reuse.
func (a *toolCallAccumulator) drain() []ToolCall {
	calls := make([]ToolCall, 0, len(a.order))
	for _, idx := range a.order {
		calls = append(calls, *a.calls[idx])
	}
	a.order = a.order[:0]
	a.calls = make(map[int]*ToolCall)
	return calls
}
