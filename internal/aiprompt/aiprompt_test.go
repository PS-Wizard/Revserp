package aiprompt

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/aichattools"
)

func TestComposeSystemPromptBlankDeltaReturnsBaseExactly(t *testing.T) {
	tests := []struct {
		name           string
		useInternal    bool
		internalPrompt string
		externalPrompt string
	}{
		{"both empty", false, "", ""},
		{"whitespace external", false, "", " \n\t "},
		{"whitespace internal", true, "  \n ", ""},
		{"whitespace both", true, "\t", "  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComposeSystemPrompt(tt.useInternal, tt.internalPrompt, tt.externalPrompt); got != DefaultSystemPrompt {
				t.Errorf("ComposeSystemPrompt() = %q, want exactly the base prompt", got)
			}
		})
	}
}

// The delta must never be able to replace the code-owned base. If this fails,
// an admin value can silently delete the accuracy, safety, and citation rules.
func TestComposeSystemPromptAppendsDeltaAndKeepsBase(t *testing.T) {
	tests := []struct {
		name           string
		useInternal    bool
		internalPrompt string
		externalPrompt string
		wantDelta      string
	}{
		{"external appended", false, "internal delta", "external delta", "external delta"},
		{"internal appended", true, "internal delta", "external delta", "internal delta"},
		{"internal wins when selected", true, "internal delta", "", "internal delta"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComposeSystemPrompt(tt.useInternal, tt.internalPrompt, tt.externalPrompt)
			if !strings.HasPrefix(got, DefaultSystemPrompt) {
				t.Fatal("composed prompt does not start with the base prompt")
			}
			if !strings.Contains(got, AudienceDeltaHeader) {
				t.Fatalf("composed prompt missing the audience header: %q", got)
			}
			if !strings.HasSuffix(got, tt.wantDelta) {
				t.Errorf("composed prompt delta = %q, want %q", got, tt.wantDelta)
			}
			if strings.Contains(got, "internal delta") && tt.wantDelta == "external delta" {
				t.Error("the unselected audience delta leaked into the prompt")
			}
		})
	}
}

// Per-tool behaviour belongs in the tool's own description, which the provider
// filters to the tools a workspace may call. A tool named here would be
// documented even for a workspace that has that tool disabled.
func TestDefaultSystemPromptNamesNoTool(t *testing.T) {
	for _, def := range aichattools.CatalogDefs() {
		if strings.Contains(DefaultSystemPrompt, def.Name) {
			t.Errorf("base prompt names the tool %q; move that text into its Def.Description", def.Name)
		}
	}
}

// A hand-maintained count is the drift this architecture exists to prevent.
func TestDefaultSystemPromptStatesNoToolCount(t *testing.T) {
	countWord := regexp.MustCompile(`(?i)\b(one|two|three|four|five|six|seven|eight|nine|ten|\d+)\s+(data\s+)?tools?\b`)
	if match := countWord.FindString(DefaultSystemPrompt); match != "" {
		t.Errorf("base prompt states a tool count (%q); the enabled set is per workspace and changes at runtime", match)
	}
}
