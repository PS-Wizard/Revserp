package textnormalization

import "testing"

func TestNormalizeTextDisplay(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace-only", " \t\n\r ", ""},
		{"trims", "  plumber ", "plumber"},
		{"double spaces", "emergency   plumber", "emergency plumber"},
		{"tabs", "a\tb", "a b"},
		{"newlines", "a\nb", "a b"},
		{"crlf", "a\r\nb", "a b"},
		{"nbsp", "a\u00a0b", "a b"},
		{"em space", "a\u2003b", "a b"},
		{"preserves casing", "ACME Shoes", "ACME Shoes"},
		{"preserves slashes and commas", "shoes/sandals, boots", "shoes/sandals, boots"},
		{"collapses around punctuation", "a,   b / c", "a, b / c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeTextDisplay(tc.in); got != tc.want {
				t.Fatalf("NormalizeTextDisplay(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeTextKey(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace-only", "\t\n", ""},
		{"mixed case and padding", "  ACME   Shoes ", "acme shoes"},
		{"nbsp", "ACME\u00a0SHOES", "acme shoes"},
		{"preserves slashes and commas", " Hessian/Trail, Boots ", "hessian/trail, boots"},
		{"unicode casing", "café  MÜNCHEN", "café münchen"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeTextKey(tc.in); got != tc.want {
				t.Fatalf("NormalizeTextKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
