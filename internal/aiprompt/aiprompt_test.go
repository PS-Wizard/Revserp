package aiprompt

import "testing"

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
