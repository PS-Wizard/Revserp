package businessprofile

import (
	"encoding/json"
	"errors"
	"strings"
)

const (
	// MaxTargetKeywords caps every keyword list (target, branded, non-branded).
	MaxTargetKeywords = 50
	// MaxBusinessCompetitors caps the business_competitors list.
	MaxBusinessCompetitors = 20
)

// NormalizeSeedPrompts trims and validates seed prompts. Shared by HTTP and tool paths.
func NormalizeSeedPrompts(prompts []string) ([]string, error) {
	if len(prompts) > 5 {
		return nil, errors.New("seed_prompts cannot contain more than 5 prompts")
	}
	normalized := make([]string, 0, len(prompts))
	for _, prompt := range prompts {
		trimmed := strings.TrimSpace(prompt)
		if trimmed == "" {
			return nil, errors.New("seed_prompts cannot contain empty prompts")
		}
		normalized = append(normalized, trimmed)
	}
	return normalized, nil
}

// NormalizeStringList trims, drops empty, and case-insensitive dedupes
// preserving first spelling and order, capped at max entries.
func NormalizeStringList(values []string, max int) []string {
	if len(values) == 0 {
		return []string{}
	}
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		normalized = append(normalized, trimmed)
		if len(normalized) >= max {
			break
		}
	}
	return normalized
}

// NormalizeTargetKeywords trims, drops empty, and case-insensitive dedupes preserving first spelling/order.
func NormalizeTargetKeywords(keywords []string) []string {
	return NormalizeStringList(keywords, MaxTargetKeywords)
}

// NormalizeBusinessCompetitors normalizes the competitor-name list.
func NormalizeBusinessCompetitors(competitors []string) []string {
	return NormalizeStringList(competitors, MaxBusinessCompetitors)
}

// NormalizeKeywordLists normalizes branded and non-branded keyword lists with
// MaxTargetKeywords, then drops from branded any entry whose lowercase form
// also appears in nonBranded. The two lists come out disjoint; non-branded wins.
func NormalizeKeywordLists(branded, nonBranded []string) ([]string, []string) {
	nb := NormalizeStringList(nonBranded, MaxTargetKeywords)
	inNonBranded := make(map[string]struct{}, len(nb))
	for _, kw := range nb {
		inNonBranded[strings.ToLower(kw)] = struct{}{}
	}
	b := NormalizeStringList(branded, MaxTargetKeywords)
	out := make([]string, 0, len(b))
	for _, kw := range b {
		if _, dup := inNonBranded[strings.ToLower(kw)]; dup {
			continue
		}
		out = append(out, kw)
	}
	return out, nb
}

// DecodeStringSlice decodes a JSON string slice; nil/empty returns [].
func DecodeStringSlice(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if values == nil {
		return []string{}, nil
	}
	return values, nil
}

// DecodeSeedPrompts decodes seed_prompts JSON.
func DecodeSeedPrompts(raw []byte) ([]string, error) {
	return DecodeStringSlice(raw)
}

// DecodeTargetKeywords decodes target_keywords JSON.
func DecodeTargetKeywords(raw []byte) ([]string, error) {
	return DecodeStringSlice(raw)
}

// DecodeBrandedKeywords decodes branded_keywords JSON.
func DecodeBrandedKeywords(raw []byte) ([]string, error) {
	return DecodeStringSlice(raw)
}

// DecodeNonBrandedKeywords decodes non_branded_keywords JSON.
func DecodeNonBrandedKeywords(raw []byte) ([]string, error) {
	return DecodeStringSlice(raw)
}

// DecodeBusinessCompetitors decodes business_competitors JSON.
func DecodeBusinessCompetitors(raw []byte) ([]string, error) {
	return DecodeStringSlice(raw)
}
