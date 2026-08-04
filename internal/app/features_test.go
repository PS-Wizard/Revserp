package app

import (
	"context"
	"testing"

	"github.com/ps-wizard/revserp/internal/ai"

	"github.com/ps-wizard/revserp/internal/app/aitools"
)

// Every registered tool must belong to exactly one admin group. Without this,
// adding tool #18 would silently make it ungateable: it would never appear in
// the admin matrix, so no admin could ever turn it off.
func TestAIToolGroupsCoverRegistryExactlyOnce(t *testing.T) {
	registry := aitools.NewRegistry(aitools.Deps{})

	groupByTool := map[string]AIToolGroup{}
	for group, tools := range aiToolGroups {
		for _, tool := range tools {
			if existing, duplicate := groupByTool[tool]; duplicate {
				t.Errorf("tool %q is in both group %q and group %q", tool, existing, group)
			}
			groupByTool[tool] = group
		}
	}

	registered := map[string]struct{}{}
	for _, def := range registry.Defs() {
		registered[def.Name] = struct{}{}
		if _, grouped := groupByTool[def.Name]; !grouped {
			t.Errorf("registered tool %q is in no admin group, so it can never be gated", def.Name)
		}
	}

	for tool := range groupByTool {
		if _, exists := registered[tool]; !exists {
			t.Errorf("group references tool %q, which is not registered", tool)
		}
	}
}

func TestAIToolGroupOrderCoversEveryGroup(t *testing.T) {
	if len(AIToolGroupOrder) != len(aiToolGroups) {
		t.Fatalf("AIToolGroupOrder has %d entries, aiToolGroups has %d", len(AIToolGroupOrder), len(aiToolGroups))
	}
	for _, group := range AIToolGroupOrder {
		if len(ToolsInGroup(group)) == 0 {
			t.Errorf("group %q in the column order has no tools", group)
		}
	}
}

// A workspace nobody has restricted gets everything. This is the denylist
// default that keeps new signups working without admin intervention.
func TestDefaultIsEverythingEnabled(t *testing.T) {
	features := allFeaturesEnabled()

	for _, feature := range []Feature{FeatureAutoCrawl, FeatureGSCConnector, FeatureAIChat} {
		if !features.Enabled(feature) {
			t.Errorf("feature %q defaulted to disabled", feature)
		}
	}
	for _, group := range AIToolGroupOrder {
		for _, tool := range ToolsInGroup(group) {
			if !features.AIToolEnabled(tool) {
				t.Errorf("tool %q defaulted to disabled", tool)
			}
		}
	}
}

func TestFeaturesFromRowAppliesToolDenylist(t *testing.T) {
	features := featuresFromRow(true, true, true, []string{"start_crawl", "export_crawl"})

	if features.AIToolEnabled("start_crawl") {
		t.Error("start_crawl is in the denylist but resolved enabled")
	}
	if features.AIToolEnabled("export_crawl") {
		t.Error("export_crawl is in the denylist but resolved enabled")
	}
	if !features.AIToolEnabled("list_issues") {
		t.Error("list_issues is not in the denylist but resolved disabled")
	}
}

// Turning off AI chat must take its tools with it. Otherwise a caller reaching
// the agent by another path would still get a working tool set.
func TestDisablingAIChatDisablesEveryTool(t *testing.T) {
	features := featuresFromRow(true, true, false, nil)

	for _, group := range AIToolGroupOrder {
		for _, tool := range ToolsInGroup(group) {
			if features.AIToolEnabled(tool) {
				t.Errorf("tool %q is still enabled with ai_chat off", tool)
			}
		}
	}
}

func TestEnabledReadsEachFeatureIndependently(t *testing.T) {
	tests := []struct {
		name        string
		features    OrgFeatures
		feature     Feature
		wantEnabled bool
	}{
		{"autocrawl off", featuresFromRow(false, true, true, nil), FeatureAutoCrawl, false},
		{"autocrawl off leaves gsc on", featuresFromRow(false, true, true, nil), FeatureGSCConnector, true},
		{"gsc off", featuresFromRow(true, false, true, nil), FeatureGSCConnector, false},
		{"gsc off leaves autocrawl on", featuresFromRow(true, false, true, nil), FeatureAutoCrawl, true},
		{"ai chat off", featuresFromRow(true, true, false, nil), FeatureAIChat, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.features.Enabled(test.feature); got != test.wantEnabled {
				t.Errorf("Enabled(%q) = %v, want %v", test.feature, got, test.wantEnabled)
			}
		})
	}
}

// An unknown feature key is not something an admin could have disabled, so it
// must not block a surface by accident.
func TestUnknownFeatureFailsOpen(t *testing.T) {
	if !featuresFromRow(false, false, false, nil).Enabled(Feature("not_a_real_feature")) {
		t.Error("an unrecognized feature resolved to disabled")
	}
}

func TestDisabledAIToolsIsSorted(t *testing.T) {
	features := featuresFromRow(true, true, true, []string{"navigate", "export_crawl", "start_crawl"})

	got := features.DisabledAITools()
	want := []string{"export_crawl", "navigate", "start_crawl"}
	if len(got) != len(want) {
		t.Fatalf("DisabledAITools() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DisabledAITools() = %v, want %v", got, want)
		}
	}
}

// stubRegistry is a minimal agentToolRegistry for exercising the gating wrapper.
type stubRegistry struct{ names []string }

func (s stubRegistry) Defs() []ai.ToolDef {
	defs := make([]ai.ToolDef, 0, len(s.names))
	for _, name := range s.names {
		defs = append(defs, ai.ToolDef{Name: name})
	}
	return defs
}

func (s stubRegistry) Get(name string) (aitools.Tool, bool) {
	for _, candidate := range s.names {
		if candidate == name {
			return aitools.Tool{Def: ai.ToolDef{Name: name}}, true
		}
	}
	return aitools.Tool{}, false
}

func defNames(defs []ai.ToolDef) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	return names
}

// The model must not be told a disabled tool exists.
func TestGatedRegistryHidesDisabledToolsFromModel(t *testing.T) {
	inner := stubRegistry{names: []string{"list_issues", "start_crawl", "export_crawl"}}
	gated := featureGatedRegistry{inner: inner, features: featuresFromRow(true, true, true, []string{"start_crawl"})}

	got := defNames(gated.Defs())
	if len(got) != 2 || got[0] != "list_issues" || got[1] != "export_crawl" {
		t.Errorf("Defs() = %v, want [list_issues export_crawl]", got)
	}
}

// Hiding it from the list is not enough: a model that names the tool anyway
// must not be able to run it.
func TestGatedRegistryRefusesToExecuteDisabledTool(t *testing.T) {
	inner := stubRegistry{names: []string{"list_issues", "start_crawl"}}
	gated := featureGatedRegistry{inner: inner, features: featuresFromRow(true, true, true, []string{"start_crawl"})}

	if _, ok := gated.Get("start_crawl"); ok {
		t.Error("Get() returned a disabled tool")
	}
	if _, ok := gated.Get("list_issues"); !ok {
		t.Error("Get() refused an enabled tool")
	}
}

func TestGatedRegistryWithAIChatOffExposesNoTools(t *testing.T) {
	inner := stubRegistry{names: []string{"list_issues", "start_crawl"}}
	gated := featureGatedRegistry{inner: inner, features: featuresFromRow(true, true, false, nil)}

	if defs := gated.Defs(); len(defs) != 0 {
		t.Errorf("Defs() = %v, want empty with ai_chat off", defNames(defs))
	}
	if _, ok := gated.Get("list_issues"); ok {
		t.Error("Get() returned a tool with ai_chat off")
	}
}

// Filtering must not scribble into the shared registry's backing array; the
// registry is a process-wide singleton serving every workspace concurrently.
func TestGatedRegistryDoesNotMutateSharedRegistry(t *testing.T) {
	inner := stubRegistry{names: []string{"list_issues", "start_crawl", "export_crawl"}}
	before := defNames(inner.Defs())

	gated := featureGatedRegistry{inner: inner, features: featuresFromRow(true, true, true, []string{"start_crawl"})}
	_ = gated.Defs()

	after := defNames(inner.Defs())
	if len(before) != len(after) {
		t.Fatalf("shared registry changed from %v to %v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("shared registry changed from %v to %v", before, after)
		}
	}
}

// A request that never hit a gating middleware keeps every tool, matching the
// enabled-by-default rule.
func TestUngatedRequestKeepsFullToolSet(t *testing.T) {
	inner := stubRegistry{names: []string{"list_issues", "start_crawl"}}
	if got := gateRegistryForRequest(context.Background(), inner); len(got.Defs()) != 2 {
		t.Errorf("ungated request got %v tools, want all of them", defNames(got.Defs()))
	}
}

func TestGateRegistryForRequestAppliesContextFeatures(t *testing.T) {
	inner := stubRegistry{names: []string{"list_issues", "start_crawl"}}
	ctx := context.WithValue(context.Background(), orgFeaturesContextKey{}, featuresFromRow(true, true, true, []string{"start_crawl"}))

	got := defNames(gateRegistryForRequest(ctx, inner).Defs())
	if len(got) != 1 || got[0] != "list_issues" {
		t.Errorf("Defs() = %v, want [list_issues]", got)
	}
}
