package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOnboardingMalformedAndOversizedJSON(t *testing.T) {
	fixture := newSessionFixture(t)

	empty := httptest.NewRecorder()
	emptyRequest := httptest.NewRequest(http.MethodPut, "/me/onboarding", nil)
	emptyRequest.AddCookie(&http.Cookie{Name: "revserp_session", Value: fixture.rawCookie})
	fixture.app.Router().ServeHTTP(empty, emptyRequest)
	if empty.Code != http.StatusBadRequest {
		t.Errorf("empty body PUT = %d, want 400 (body %s)", empty.Code, empty.Body.String())
	}

	for name, body := range map[string]string{
		"truncated":    `{"version":`,
		"version type": `{"version":"1","status":"in_progress","checkpoint":"overview"}`,
		"status type":  `{"version":1,"status":7,"checkpoint":"overview"}`,
		"array body":   `[1,2,3]`,
		"scalar body":  `"overview"`,
	} {
		if response := putOnboarding(t, fixture, body); response.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", name, response.Code, response.Body.String())
		}
	}

	oversized := `{"version":1,"status":"in_progress","checkpoint":"` + strings.Repeat("a", 2<<20) + `"}`
	if response := putOnboarding(t, fixture, oversized); response.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized PUT = %d, want 413 (body %s)", response.Code, response.Body.String())
	}

	unchanged := decodeOnboarding(t, getOnboarding(t, fixture))
	if unchanged.Status != "not_started" || unchanged.Checkpoint != "new_project" {
		t.Fatalf("rejected bodies mutated state: %+v", unchanged)
	}

	tolerated := putOnboarding(t, fixture, `{"version":1,"status":"in_progress","checkpoint":"overview","fluffer":"nutter"}`)
	if tolerated.Code != http.StatusOK {
		t.Fatalf("unknown field PUT = %d, want 200 (unknown fields are ignored; body %s)", tolerated.Code, tolerated.Body.String())
	}
	if stored := decodeOnboarding(t, tolerated); stored.Status != "in_progress" || stored.Checkpoint != "overview" {
		t.Fatalf("unknown field PUT stored %+v", stored)
	}
}

func TestOnboardingCorruptStoredState(t *testing.T) {
	fixture := newSessionFixture(t)
	if _, err := fixture.pool.Exec(fixture.ctx,
		`UPDATE users SET onboarding_state = '"oops"'::jsonb WHERE id = $1`, fixture.userID); err != nil {
		t.Fatalf("corrupt stored state: %v", err)
	}

	if response := getOnboarding(t, fixture); response.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt GET = %d, want 500 (body %s)", response.Code, response.Body.String())
	}
	if response := putOnboarding(t, fixture, `{"version":1,"status":"completed","checkpoint":"finish"}`); response.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt completing PUT = %d, want 500 (body %s)", response.Code, response.Body.String())
	}
	if response := putOnboarding(t, fixture, `{"version":1,"status":"in_progress","checkpoint":"overview"}`); response.Code != http.StatusOK {
		t.Fatalf("healing PUT = %d, want 200 (body %s)", response.Code, response.Body.String())
	}
	healed := decodeOnboarding(t, getOnboarding(t, fixture))
	if healed.Status != "in_progress" || healed.Checkpoint != "overview" {
		t.Fatalf("healed state = %+v", healed)
	}
}

func TestOnboardingNotInMeResponse(t *testing.T) {
	fixture := newSessionFixture(t)
	response := get(t, fixture.app.Router(), "/me", fixture.rawCookie)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /me = %d (%s)", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "onboarding") {
		t.Fatalf("/me leaks onboarding state: %s", response.Body.String())
	}
}
