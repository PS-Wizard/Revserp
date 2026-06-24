package app

import (
	"net/http"
	"sync"
	"time"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"golang.org/x/time/rate"
)

// aiRateLimiter is an in-memory, per-identity rate limiter for AI endpoints.
// It is keyed by the auth identity Subject (the stable user/sub claim from the
// JWT), which matches how ensureCurrentUser resolves the caller. An entry is
// created on first use and cleaned up by a background goroutine.
//
// TODO(future): add per-org token/cost budget tracking so that aggregate spend
// across all members of an organization can be capped. Full budget tracking
// requires persistent storage (e.g. a credits table) and is out of scope here.
type aiRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*aiLimiterEntry

	perMinute int
	burst     int
}

type aiLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewAIRateLimiter returns a chi-compatible middleware that enforces a per-user
// rate limit on AI endpoints. perMinute is the sustained request rate; burst
// allows short bursts above that rate. On limit exceeded the middleware writes
// HTTP 429 with a JSON error body and stops the request chain.
//
// Example registration in routes.go (orchestrator must wire this):
//
//	aiLimiter := app.NewAIRateLimiter(20, 5)
//	r.Group(func(r chi.Router) {
//	    r.Use(aiLimiter)
//	    r.Post("/crawls/{crawlID}/ai-fix", a.handleAIFix)
//	    // conversation routes …
//	    r.Post("/conversations/{conversationID}/messages", a.handleCreateAIConversationMessage)
//	})
//
// Suggested values: perMinute=20, burst=5 for production; tune downward if
// model costs are high or upward if users require higher interactivity.
func NewAIRateLimiter(perMinute int, burst int) func(http.Handler) http.Handler {
	rl := &aiRateLimiter{
		entries:   make(map[string]*aiLimiterEntry),
		perMinute: perMinute,
		burst:     burst,
	}

	// Start a background cleanup goroutine that removes stale entries every
	// five minutes. An entry is stale when it has not been accessed for more
	// than ten minutes, meaning the limiter bucket has fully refilled.
	go rl.cleanupLoop()

	return rl.middleware
}

// middleware is the actual http.Handler wrapper.
func (rl *aiRateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := rl.identityKey(r)
		limiter := rl.limiterFor(key)
		if !limiter.Allow() {
			writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded — please slow down and try again")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// identityKey returns a stable string key for the caller. It uses the auth
// Subject from the request context — the same identity that ensureCurrentUser
// resolves — so the limit is per authenticated user. Falls back to the remote
// address for unauthenticated requests (which will be rejected by auth
// middleware before reaching AI handlers, but the fallback keeps the code safe).
func (rl *aiRateLimiter) identityKey(r *http.Request) string {
	if identity, ok := internalauth.IdentityFromContext(r.Context()); ok && identity.Subject != "" {
		return identity.Subject
	}
	return r.RemoteAddr
}

// limiterFor retrieves or creates the rate.Limiter for a given key.
func (rl *aiRateLimiter) limiterFor(key string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	entry, exists := rl.entries[key]
	if !exists {
		r := rate.Every(time.Minute / time.Duration(rl.perMinute))
		entry = &aiLimiterEntry{
			limiter: rate.NewLimiter(r, rl.burst),
		}
		rl.entries[key] = entry
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

// cleanupLoop removes entries that have not been accessed for more than ten
// minutes. It runs in a background goroutine for the lifetime of the process.
func (rl *aiRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.cleanup()
	}
}

func (rl *aiRateLimiter) cleanup() {
	cutoff := time.Now().Add(-10 * time.Minute)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for key, entry := range rl.entries {
		if entry.lastSeen.Before(cutoff) {
			delete(rl.entries, key)
		}
	}
}
