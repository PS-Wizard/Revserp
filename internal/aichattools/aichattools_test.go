package aichattools

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"
)

func TestRegistry(t *testing.T) {
	registry := NewRegistry()

	if names := registry.Names(); !slices.Equal(names, []string{"read_issues"}) {
		t.Fatalf("Names() = %v, want [read_issues]", names)
	}
	defs := registry.Defs()
	if len(defs) != 1 {
		t.Fatalf("Defs() = %d defs, want 1", len(defs))
	}
	if defs[0].Name != "read_issues" || defs[0].Label == "" || defs[0].Description == "" {
		t.Fatalf("Defs()[0] = %+v, want fully populated read_issues def", defs[0])
	}
	var schema map[string]any
	if err := json.Unmarshal(defs[0].Schema, &schema); err != nil {
		t.Fatalf("read_issues schema is not valid JSON: %v", err)
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
