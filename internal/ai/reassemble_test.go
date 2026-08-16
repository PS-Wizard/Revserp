package ai

import "testing"

func TestToolCallAccumulator(t *testing.T) {
	accumulator := newToolCallAccumulator()
	if !accumulator.empty() {
		t.Fatal("new accumulator must be empty")
	}
	accumulator.add(toolCallFragment{Index: 0, ID: "call_1", Name: "read_issues", ArgsDelta: `{"ids":`})
	accumulator.add(toolCallFragment{Index: 1, ID: "call_2", Name: "read_issues", ArgsDelta: `{}`})
	accumulator.add(toolCallFragment{Index: 0, ArgsDelta: `[1,2]}`})
	if accumulator.empty() {
		t.Fatal("accumulator must not be empty after adds")
	}
	calls := accumulator.drain()
	want := []ToolCall{
		{ID: "call_1", Name: "read_issues", Args: `{"ids":[1,2]}`},
		{ID: "call_2", Name: "read_issues", Args: `{}`},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls[%d] = %#v, want %#v", i, calls[i], want[i])
		}
	}
	if !accumulator.empty() {
		t.Fatal("drain must reset the accumulator")
	}

	// The accumulator is reusable after a drain.
	accumulator.add(toolCallFragment{Index: 0, ID: "call_3", Name: "read_issues", ArgsDelta: `[]`})
	calls = accumulator.drain()
	want = []ToolCall{{ID: "call_3", Name: "read_issues", Args: `[]`}}
	if len(calls) != 1 || calls[0] != want[0] {
		t.Fatalf("second drain = %#v, want %#v", calls, want)
	}
}
