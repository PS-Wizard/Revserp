package ai

import (
	"reflect"
	"testing"
)

func TestToolCallAccumulator_SingleCall(t *testing.T) {
	acc := newToolCallAccumulator()

	acc.add(toolCallFragment{Index: 0, ID: "call_1", Name: "get_weather", ArgsDelta: `{"loc`})
	acc.add(toolCallFragment{Index: 0, ArgsDelta: `ation":"S`})
	acc.add(toolCallFragment{Index: 0, ArgsDelta: `F"}`})

	got := acc.drain()
	want := []ToolCall{{ID: "call_1", Name: "get_weather", Args: `{"location":"SF"}`}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	// drain resets the accumulator.
	if !acc.empty() {
		t.Fatalf("expected accumulator to be empty after drain")
	}
}

func TestToolCallAccumulator_ParallelInterleavedCalls(t *testing.T) {
	acc := newToolCallAccumulator()

	// Two tool calls streamed with interleaved fragments, as a real
	// OpenAI-compatible API would emit them.
	acc.add(toolCallFragment{Index: 0, ID: "call_1", Name: "get_weather", ArgsDelta: `{"city":`})
	acc.add(toolCallFragment{Index: 1, ID: "call_2", Name: "get_time", ArgsDelta: `{"tz":`})
	acc.add(toolCallFragment{Index: 0, ArgsDelta: `"SF"}`})
	acc.add(toolCallFragment{Index: 1, ArgsDelta: `"UTC"}`})

	got := acc.drain()
	want := []ToolCall{
		{ID: "call_1", Name: "get_weather", Args: `{"city":"SF"}`},
		{ID: "call_2", Name: "get_time", Args: `{"tz":"UTC"}`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestToolCallAccumulator_EmptyFragmentsDoNotOverwrite(t *testing.T) {
	acc := newToolCallAccumulator()

	acc.add(toolCallFragment{Index: 0, ID: "call_1", Name: "get_weather", ArgsDelta: ""})
	acc.add(toolCallFragment{Index: 0, ID: "", Name: "", ArgsDelta: `{}`})

	got := acc.drain()
	want := []ToolCall{{ID: "call_1", Name: "get_weather", Args: `{}`}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestToolCallAccumulator_EmptyReturnsNoCalls(t *testing.T) {
	acc := newToolCallAccumulator()
	if !acc.empty() {
		t.Fatalf("expected new accumulator to be empty")
	}
	if got := acc.drain(); len(got) != 0 {
		t.Fatalf("expected no tool calls, got %+v", got)
	}
}
