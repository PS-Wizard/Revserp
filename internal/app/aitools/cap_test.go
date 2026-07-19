package aitools

import (
	"strings"
	"testing"
)

func TestCapText(t *testing.T) {
	if got := capText("short", 100); got != "short" {
		t.Errorf("capText should not touch text under the cap, got %q", got)
	}

	long := strings.Repeat("a", 50)
	got := capText(long, 10)
	if strings.HasPrefix(got, long) {
		t.Errorf("capText did not truncate: %q", got)
	}
	if !strings.Contains(got, "[truncated, 50 total chars]") {
		t.Errorf("capText missing truncation marker: %q", got)
	}
}

func TestClampLimit(t *testing.T) {
	cases := []struct {
		requested, def, max int
		want                int32
	}{
		{0, 25, 50, 25},
		{-5, 25, 50, 25},
		{10, 25, 50, 10},
		{1000, 25, 50, 50},
		{50, 25, 50, 50},
	}
	for _, c := range cases {
		if got := clampLimit(c.requested, c.def, c.max); got != c.want {
			t.Errorf("clampLimit(%d, %d, %d) = %d, want %d", c.requested, c.def, c.max, got, c.want)
		}
	}
}
