package projectkeywords

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// NormalizeProjectKeywordDisplay trims a phrase and collapses inner whitespace
// runs to single spaces, preserving the readable original casing.
func NormalizeProjectKeywordDisplay(phrase string) string {
	return strings.Join(strings.Fields(phrase), " ")
}

// NormalizeProjectKeywordKey folds a phrase to its dedupe key: display-normalized, lowercased.
func NormalizeProjectKeywordKey(phrase string) string {
	return strings.ToLower(NormalizeProjectKeywordDisplay(phrase))
}

// ValidateProjectKeywordKind rejects anything but brand and non_brand.
func ValidateProjectKeywordKind(kind string) error {
	if kind != ProjectKeywordKindBrand && kind != ProjectKeywordKindNonBrand {
		return fmt.Errorf("%w: kind must be %q or %q", ErrProjectKeywordInvalid, ProjectKeywordKindBrand, ProjectKeywordKindNonBrand)
	}
	return nil
}

// ValidateProjectKeywordPhrase normalizes one phrase and rejects blanks and
// phrases over the rune limit, returning the display form.
func ValidateProjectKeywordPhrase(phrase string) (string, error) {
	display := NormalizeProjectKeywordDisplay(phrase)
	if display == "" {
		return "", fmt.Errorf("%w: phrase must not be blank", ErrProjectKeywordInvalid)
	}
	if utf8.RuneCountInString(display) > MaxProjectKeywordRunes {
		return "", fmt.Errorf("%w: phrase over %d characters", ErrProjectKeywordInvalid, MaxProjectKeywordRunes)
	}
	if !utf8.ValidString(display) || strings.ContainsRune(display, '\x00') {
		return "", fmt.Errorf("%w: phrase contains invalid text", ErrProjectKeywordInvalid)
	}
	return display, nil
}

// NormalizeSuggestedProjectKeywords requires both lists and rejects invalid or oversized input.
func NormalizeSuggestedProjectKeywords(brandKeywords, nonBrandKeywords []string) (brand, nonBrand []string, err error) {
	nonBrand, err = normalizeProjectKeywordEntries(nonBrandKeywords)
	if err != nil {
		return nil, nil, err
	}
	if len(nonBrand) == 0 {
		return nil, nil, fmt.Errorf("%w: non_brand list must not be empty", ErrProjectKeywordInvalid)
	}
	brand, err = normalizeProjectKeywordEntries(brandKeywords)
	if err != nil {
		return nil, nil, err
	}
	if len(brand) == 0 {
		return nil, nil, fmt.Errorf("%w: brand list must not be empty", ErrProjectKeywordInvalid)
	}
	inNonBrand := make(map[string]struct{}, len(nonBrand))
	for _, kw := range nonBrand {
		inNonBrand[NormalizeProjectKeywordKey(kw)] = struct{}{}
	}
	kept := brand[:0]
	for _, kw := range brand {
		if _, dup := inNonBrand[NormalizeProjectKeywordKey(kw)]; dup {
			continue
		}
		kept = append(kept, kw)
	}
	brand = kept
	if len(brand) == 0 {
		return nil, nil, fmt.Errorf("%w: brand list empty after overlap with non_brand", ErrProjectKeywordInvalid)
	}
	return brand, nonBrand, nil
}

func normalizeProjectKeywordEntries(phrases []string) ([]string, error) {
	out := make([]string, 0, len(phrases))
	seen := make(map[string]struct{}, len(phrases))
	for _, phrase := range phrases {
		if NormalizeProjectKeywordDisplay(phrase) == "" {
			continue
		}
		display, err := ValidateProjectKeywordPhrase(phrase)
		if err != nil {
			return nil, err
		}
		key := NormalizeProjectKeywordKey(display)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, display)
		if len(out) > MaxProjectKeywordsPerKindPerSource {
			return nil, fmt.Errorf("%w: list exceeds %d phrases", ErrProjectKeywordLimit, MaxProjectKeywordsPerKindPerSource)
		}
	}
	return out, nil
}
