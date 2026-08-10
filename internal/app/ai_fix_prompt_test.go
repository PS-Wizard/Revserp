package app

import "testing"

func TestIsInternalPromptUser(t *testing.T) {
	tests := []struct {
		name  string
		email string
		want  bool
	}{
		{"internal", "person@revketer.ai", true},
		{"case insensitive domain", "person@RevKeter.AI", true},
		{"subdomain", "person@team.revketer.ai", false},
		{"suffix", "person@revketer.ai.example", false},
		{"missing local part", "@revketer.ai", false},
		{"multiple at signs", "person@revketer.ai@evil.example", false},
		{"whitespace", "person@revketer.ai ", false},
		{"missing domain", "person@", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInternalPromptUser(tt.email); got != tt.want {
				t.Errorf("isInternalPromptUser(%q) = %t, want %t", tt.email, got, tt.want)
			}
		})
	}
}

func TestSelectSystemPrompt(t *testing.T) {
	tests := []struct {
		name     string
		email    string
		internal string
		external string
		fallback string
		want     string
	}{
		{"internal user", "person@revketer.ai", "internal", "external", "fallback", "internal"},
		{"external user", "person@example.com", "internal", "external", "fallback", "external"},
		{"empty internal falls back", "person@revketer.ai", "", "external", "fallback", "fallback"},
		{"empty external falls back", "person@example.com", "internal", "", "fallback", "fallback"},
		{"malformed internal-looking address is external", "person@revketer.ai@evil.example", "internal", "external", "fallback", "external"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectSystemPrompt(tt.email, tt.internal, tt.external, tt.fallback); got != tt.want {
				t.Errorf("selectSystemPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}
