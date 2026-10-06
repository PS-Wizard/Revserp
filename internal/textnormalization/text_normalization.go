// Package textnormalization provides domain-neutral display and dedupe-key
// normalization for free-form text phrases.
package textnormalization

import "strings"

// NormalizeTextDisplay trims text and collapses inner Unicode whitespace runs
// (strings.Fields spans NBSP and other Unicode spaces) to single ASCII spaces,
// preserving the original casing.
func NormalizeTextDisplay(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// NormalizeTextKey folds text to its dedupe key: display-normalized, then lowercased.
func NormalizeTextKey(text string) string {
	return strings.ToLower(NormalizeTextDisplay(text))
}
