package aichattools

import (
	"context"
	"errors"
	"sync"

	"github.com/ps-wizard/revserp/internal/tinyfish"
)

// WebClient is the web search and fetch path the worker provides. It is a small
// interface so tool tests can substitute fakes. The worker leaves Scope.Web nil
// when no TinyFish key is configured; tools report that as an ordinary
// unavailable state, not an error.
//
// Search and Fetch are free but rate limited across the whole deployment (30
// queries and 150 URLs per minute), which is what WebBudget exists to protect.
type WebClient interface {
	Search(ctx context.Context, query string, limit int) ([]tinyfish.SearchResult, error)
	Fetch(ctx context.Context, rawURL string) (tinyfish.FetchResult, error)
}

// WebBudget is a thread-safe count of the web calls one turn may still spend.
// A nil budget means no cap (raw tool-call mode), matching the other budgets.
type WebBudget struct {
	mu           sync.Mutex
	searchesLeft int
	fetchesLeft  int
}

// ErrWebBudgetExhausted reports that the turn spent its web allowance.
var ErrWebBudgetExhausted = errors.New("web budget exhausted for this turn")

// NewWebBudget returns a budget with the given search and fetch allowances.
// Negative limits become zero.
func NewWebBudget(searches, fetches int) *WebBudget {
	if searches < 0 {
		searches = 0
	}
	if fetches < 0 {
		fetches = 0
	}
	return &WebBudget{searchesLeft: searches, fetchesLeft: fetches}
}

// SpendSearch reserves one search. A nil budget allows the call.
func (b *WebBudget) SpendSearch() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.searchesLeft <= 0 {
		return ErrWebBudgetExhausted
	}
	b.searchesLeft--
	return nil
}

// SpendFetch reserves one fetch. A nil budget allows the call.
func (b *WebBudget) SpendFetch() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fetchesLeft <= 0 {
		return ErrWebBudgetExhausted
	}
	b.fetchesLeft--
	return nil
}
