package aichatworker

import (
	"strings"
	"testing"
)

func TestNeedParagraphBreak(t *testing.T) {
	cases := []struct {
		name  string
		prior string
		round string
		delta string
		want  bool
	}{
		{"first round never breaks", "", "", "Hello.", false},
		{"later round breaks once", "Before.", "", "Now another.", true},
		{"no break within round", "Before.", "Now", " again.", false},
		{"whitespace delta never breaks", "Before.", "", " ", false},
		{"whitespace prior never breaks", " ", "", "Hi.", false},
	}
	for _, tc := range cases {
		priorText := strings.TrimSpace(tc.prior) != ""
		if got := needParagraphBreak(priorText, tc.round, tc.delta); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	// End-to-end shape: Before. + later round => single blank line.
	var buffer strings.Builder
	buffer.WriteString("Before.")
	if needParagraphBreak(true, "", "Now another.") {
		buffer.WriteString("\n\n")
	}
	buffer.WriteString("Now another.")
	if buffer.String() != "Before.\n\nNow another." {
		t.Errorf("joined text = %q", buffer.String())
	}
}
