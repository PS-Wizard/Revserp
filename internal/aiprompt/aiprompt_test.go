package aiprompt

import (
	"strings"
	"testing"
)

func TestSelectSystemPrompt(t *testing.T) {
	tests := []struct {
		name           string
		useInternal    bool
		internalPrompt string
		externalPrompt string
		want           string
	}{
		{"external", false, "internal", "external", "external"},
		{"internal", true, "internal", "external", "internal"},
		{"blank internal falls back", true, " \n", "external", DefaultSystemPrompt},
		{"blank external falls back", false, "internal", "\t", DefaultSystemPrompt},
		{"preserves selected prompt", true, "  internal \n", "external", "  internal \n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SelectSystemPrompt(tt.useInternal, tt.internalPrompt, tt.externalPrompt); got != tt.want {
				t.Errorf("SelectSystemPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultSystemPromptIncludesCurrentTools(t *testing.T) {
	for _, name := range []string{"read_issues", "get_score_summary", "get_search_console_data", "get_business_profile", "read_issue_work", "render_chart"} {
		if !strings.Contains(DefaultSystemPrompt, name) {
			t.Errorf("default prompt missing %q", name)
		}
	}
	for _, guidance := range []string{"five tools that read real data", "render_chart does not retrieve facts", "preset ranking", "categories", "projected_points"} {
		if !strings.Contains(DefaultSystemPrompt, guidance) {
			t.Errorf("default prompt missing %q", guidance)
		}
	}
	if strings.Contains(DefaultSystemPrompt, "four tools") {
		t.Error("default prompt still claims four tools")
	}
}
