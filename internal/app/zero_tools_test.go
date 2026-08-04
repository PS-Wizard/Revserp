package app

import (
	"testing"

	"github.com/ps-wizard/revserp/internal/app/aitools"
)

// A workspace with AI Chat on but every tool disabled must still be able to
// hold a plain conversation: the agent should get an empty tool set, not fail.
func TestZeroEnabledToolsStillProducesAValidTurn(t *testing.T) {
	registry := aitools.NewRegistry(aitools.Deps{})
	all := make([]string, 0)
	for _, def := range registry.Defs() {
		all = append(all, def.Name)
	}

	gated := featureGatedRegistry{
		inner:    registry,
		features: featuresFromRow(true, true, true, all),
	}

	tools := gated.Defs()
	if len(tools) != 0 {
		t.Fatalf("expected every tool filtered out, got %d", len(tools))
	}

	// The message builder must accept an empty tool set without erroring; this
	// is what runs before the provider call on every round.
	messages, err := boundedAgentMessages("system prompt", nil, "hello", nil, tools)
	if err != nil {
		t.Fatalf("boundedAgentMessages with zero tools: %v", err)
	}
	if len(messages) == 0 {
		t.Fatal("no messages produced for a zero-tool turn")
	}
}
