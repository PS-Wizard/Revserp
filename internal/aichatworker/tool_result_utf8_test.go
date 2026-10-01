package aichatworker

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCapToolResultContentPreservesUTF8(t *testing.T) {
	for _, text := range []string{"é", "ने", "🚀"} {
		content := strings.Repeat("x", toolResultContentCap-1) + strings.Repeat(text, 100)
		got := capToolResultContent(content)
		if !utf8.ValidString(got) {
			t.Fatal("truncated tool result contains invalid UTF-8")
		}
		if !strings.HasSuffix(got, "…[truncated]") {
			t.Fatal("truncated tool result lacks an explicit marker")
		}
	}
}
