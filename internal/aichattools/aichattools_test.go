package aichattools

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestRegistry(t *testing.T) {
	registry := NewRegistry()

	if names := registry.Names(); !slices.Equal(names, []string{"read_issues", "get_score_summary", "get_search_console_data", "get_business_profile", "read_issue_work", "read_page", "render_chart", "update_business_profile", "get_project_keywords", "update_project_keywords", "web_search", "get_search_suggestions", "fetch_url", "get_keyword_coverage", "get_location_landmarks"}) {
		t.Fatalf("Names() = %v, want the fifteen served tools", names)
	}
	defs := registry.Defs()
	if len(defs) != 15 {
		t.Fatalf("Defs() = %d defs, want 15", len(defs))
	}
	for _, def := range defs {
		if def.Name == "" || def.Label == "" || def.Description == "" || len(def.Schema) == 0 {
			t.Fatalf("Defs() = %+v, want fully populated defs", def)
		}
		var schema map[string]any
		if err := json.Unmarshal(def.Schema, &schema); err != nil {
			t.Fatalf("%s schema is not valid JSON: %v", def.Name, err)
		}
	}

	tool, ok := registry.Get("read_issues")
	if !ok || tool.Def.Name != "read_issues" {
		t.Fatalf("Get(read_issues) = %+v, %v; want registered tool", tool, ok)
	}
	if _, ok := registry.Get("missing"); ok {
		t.Fatal("Get(missing) = true, want false")
	}
}

func TestBudgetSpendDown(t *testing.T) {
	tests := []struct {
		name          string
		start         int
		spends        []int
		wantRemaining []int
	}{
		{name: "exact", start: 10, spends: []int{4, 6}, wantRemaining: []int{6, 0}},
		{name: "overspend clamps to zero", start: 5, spends: []int{7}, wantRemaining: []int{0}},
		{name: "multiple overspend stays zero", start: 3, spends: []int{5, 2}, wantRemaining: []int{0, 0}},
		{name: "spend zero", start: 9, spends: []int{0}, wantRemaining: []int{9}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			budget := NewBudget(test.start)
			for i, spend := range test.spends {
				if got := budget.Spend(spend); got != test.wantRemaining[i] {
					t.Fatalf("Spend(%d) = %d, want %d", spend, got, test.wantRemaining[i])
				}
				if got := budget.Remaining(); got != test.wantRemaining[i] {
					t.Fatalf("Remaining() = %d, want %d", got, test.wantRemaining[i])
				}
			}
		})
	}
}

func TestBudgetConcurrentSpend(t *testing.T) {
	budget := NewBudget(1000)
	var wait sync.WaitGroup
	for range 10 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			budget.Spend(1)
		}()
	}
	wait.Wait()
	if got := budget.Remaining(); got != 990 {
		t.Fatalf("Remaining() = %d after 10 concurrent spends of 1, want 990", got)
	}
}

func TestPageContentBudget(t *testing.T) {
	budget := NewPageContentBudget(100, 2)
	if !budget.TryRegisterPage("a") || !budget.TryRegisterPage("b") {
		t.Fatal("first two unique pages should fit")
	}
	if budget.TryRegisterPage("c") {
		t.Fatal("third unique page should exceed the limit")
	}
	if !budget.TryRegisterPage("a") {
		t.Fatal("an existing page should remain available")
	}
	if got := budget.SpendBytes(30); got != 70 {
		t.Fatalf("SpendBytes(30) = %d, want 70", got)
	}
	if got := budget.SpendBytes(100); got != 0 {
		t.Fatalf("overspend = %d, want 0", got)
	}
}

func TestPageContentBudgetZeroLimits(t *testing.T) {
	budget := NewPageContentBudget(-1, -1)
	if budget.RemainingBytes() != 0 || budget.TryRegisterPage("a") {
		t.Fatal("negative limits should become zero")
	}
}

func TestPageContentBudgetUnavailablePageSpendsNoBytes(t *testing.T) {
	budget := NewPageContentBudget(100, 1)
	if !budget.TryRegisterPage("unavailable") {
		t.Fatal("unavailable page should register")
	}
	if got := budget.SpendBytes(0); got != 100 {
		t.Fatalf("SpendBytes(0) = %d, want 100", got)
	}
}

func TestPageContentBudgetConcurrentAccess(t *testing.T) {
	budget := NewPageContentBudget(1000, 5)
	var wait sync.WaitGroup
	for i := range 10 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			budget.TryRegisterPage(string(rune('a' + i%5)))
			budget.SpendBytes(10)
		}()
	}
	wait.Wait()
	if got := budget.RemainingBytes(); got != 900 {
		t.Fatalf("RemainingBytes() = %d, want 900", got)
	}
	if budget.TryRegisterPage("sixth") {
		t.Fatal("sixth unique page should fail")
	}
}

// Tool definitions are the only place the model learns what a tool does. Their
// descriptions travel through the provider's tool-calling contract and are
// filtered to the enabled tools, so a tool with no description would be shipped
// to the model undocumented, with no other channel to describe it. The system
// prompt deliberately names no tool.
func TestEveryCatalogToolHasADescription(t *testing.T) {
	defs := CatalogDefs()
	if len(defs) == 0 {
		t.Fatal("catalog is empty")
	}
	for _, def := range defs {
		if strings.TrimSpace(def.Description) == "" {
			t.Errorf("tool %q has an empty description; the system prompt does not describe tools", def.Name)
		}
		if len(def.Schema) == 0 {
			t.Errorf("tool %q has an empty schema", def.Name)
		}
	}
}

func TestLocationScopedRegistryDeniesParentOnlyTools(t *testing.T) {
	catalog := make(map[string]bool, len(CatalogDefs()))
	for _, def := range CatalogDefs() {
		catalog[def.Name] = true
	}
	for _, name := range LocationUnsupportedNativeTools() {
		if !catalog[name] {
			t.Fatalf("location-unsupported tool %q is not in the native catalog", name)
		}
	}
	registry := NewLocationScopedRegistry(nil)
	for _, name := range LocationUnsupportedNativeTools() {
		if _, ok := registry.Get(name); ok {
			t.Fatalf("location-scoped registry still serves %q", name)
		}
	}
	for _, name := range []string{"web_search", "fetch_url", "get_search_suggestions", "render_chart", "read_page"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("location-scoped registry dropped safe tool %q", name)
		}
	}
	for _, name := range []string{"get_business_profile", "update_business_profile", "get_project_keywords", "update_project_keywords", "get_keyword_coverage", "get_location_landmarks"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("location-scoped registry dropped wired local tool %q", name)
		}
	}
	if _, ok := NewLocationScopedRegistry([]string{"read_issues"}).Get("get_score_summary"); ok {
		t.Fatal("combined registry still served a parent-only tool")
	}
}

func TestLocationKeywordToolSchemaHasNoListCap(t *testing.T) {
	parent, ok := NewFilteredRegistry(nil).Get(updateProjectKeywordsName)
	if !ok {
		t.Fatalf("%s not served", updateProjectKeywordsName)
	}
	if !strings.Contains(string(parent.Def.Schema), "maxItems") {
		t.Fatalf("parent keyword schema lost its cap: %s", parent.Def.Schema)
	}
	local, ok := NewLocationScopedRegistry(nil).Get(updateProjectKeywordsName)
	if !ok {
		t.Fatalf("%s not served in location mode", updateProjectKeywordsName)
	}
	if strings.Contains(string(local.Def.Schema), "maxItems") || strings.Contains(string(local.Def.Schema), "minItems") {
		t.Fatalf("location keyword schema still limits list size: %s", local.Def.Schema)
	}
	var schema map[string]any
	if err := json.Unmarshal(local.Def.Schema, &schema); err != nil {
		t.Fatalf("location keyword schema is not valid JSON: %v", err)
	}
}
