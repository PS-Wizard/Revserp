package aichattools

import "testing"

// TestBudgetRemainingReportsSpentState covers the durable-snapshot contract:
// every capped budget reports what a turn may still spend, and a nil budget
// (raw mode) reports zero so a restored turn is never handed fresh allowance.
func TestBudgetRemainingReportsSpentState(t *testing.T) {
	web := NewWebBudget(3, 2)
	if err := web.SpendSearch(); err != nil {
		t.Fatalf("spend search: %v", err)
	}
	if err := web.SpendFetch(); err != nil {
		t.Fatalf("spend fetch: %v", err)
	}
	if searches, fetches := web.Remaining(); searches != 2 || fetches != 1 {
		t.Fatalf("web remaining = %d/%d, want 2/1", searches, fetches)
	}
	suggest := NewSuggestBudget(4, 40)
	if err := suggest.SpendRequests(30); err != nil {
		t.Fatalf("spend requests: %v", err)
	}
	if err := suggest.SpendCall(); err != nil {
		t.Fatalf("spend call: %v", err)
	}
	if calls, requests := suggest.Remaining(); calls != 3 || requests != 10 {
		t.Fatalf("suggest remaining = %d/%d, want 3/10", calls, requests)
	}
	var absentWeb *WebBudget
	var absentSuggest *SuggestBudget
	var absentPage *PageContentBudget
	if searches, fetches := absentWeb.Remaining(); searches != 0 || fetches != 0 {
		t.Errorf("nil web budget = %d/%d, want spent", searches, fetches)
	}
	if calls, requests := absentSuggest.Remaining(); calls != 0 || requests != 0 {
		t.Errorf("nil suggest budget = %d/%d, want spent", calls, requests)
	}
	if state := absentPage.State(); state.BytesLeft != 0 || state.UniqueLimit != 0 || len(state.SeenKeys) != 0 {
		t.Errorf("nil page budget state = %+v, want spent", state)
	}
}

// TestPageContentBudgetStateRestoreKeepsSeenKeys requires a restored page
// budget to keep the keys it already paid for: a re-read of a known key stays
// free, but the unique-page slots it consumed are not handed out again.
func TestPageContentBudgetStateRestoreKeepsSeenKeys(t *testing.T) {
	budget := NewPageContentBudget(1000, 2)
	if !budget.TryRegisterPage("page-1") {
		t.Fatal("first unique page refused")
	}
	budget.SpendBytes(400)

	restored := RestorePageContentBudget(budget.State())
	if got := restored.RemainingBytes(); got != 600 {
		t.Fatalf("restored bytes = %d, want 600", got)
	}
	if !restored.TryRegisterPage("page-1") {
		t.Fatal("an already registered key must stay readable")
	}
	if !restored.TryRegisterPage("page-2") {
		t.Fatal("the one remaining unique slot must survive the restore")
	}
	if restored.TryRegisterPage("page-3") {
		t.Fatal("restored budget handed out a fresh unique slot")
	}
}

// TestRestorePageContentBudgetWithoutSeenKeysFailsSafe covers a checkpoint
// that carries no page registry: the unique-page allowance reads as spent, so
// a restored turn cannot read a page the turn never paid for.
func TestRestorePageContentBudgetWithoutSeenKeysFailsSafe(t *testing.T) {
	restored := RestorePageContentBudget(PageContentState{BytesLeft: 500})
	if restored.TryRegisterPage("page-1") {
		t.Fatal("a snapshot without the page registry must not grant unique pages")
	}
	if got := restored.RemainingBytes(); got != 500 {
		t.Fatalf("restored bytes = %d, want the checkpointed 500", got)
	}
	if got := RestorePageContentBudget(PageContentState{BytesLeft: 5, UniqueLimit: 1, SeenKeys: []string{"a", "b"}}).TryRegisterPage("c"); got {
		t.Fatal("the restored unique limit must stay authoritative")
	}
}
