package aichatworker

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestComposeSystemContext(t *testing.T) {
	completedAt := pgtype.Timestamptz{Valid: true}
	context := composeSystemContext("selected prompt", "Example", "https://example.com", completedAt)
	for _, want := range []string{"selected prompt", "--- Project context ---", "Name: Example", "URL: https://example.com", "--- Crawl context ---"} {
		if !strings.Contains(context, want) {
			t.Errorf("system context missing %q: %q", want, context)
		}
	}
	if strings.Count(context, "selected prompt") != 1 {
		t.Errorf("system prompt appears more than once: %q", context)
	}
}
