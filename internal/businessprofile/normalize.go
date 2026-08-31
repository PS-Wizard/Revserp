package businessprofile

import (
	"encoding/json"
	"errors"
	"strings"
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

// NormalizeTargetKeywords trims, drops empty, and case-insensitive dedupes preserving first spelling/order.
func NormalizeTargetKeywords(keywords []string) []string {
	if len(keywords) == 0 {
		return []string{}
	}
	normalized := make([]string, 0, len(keywords))
	seen := make(map[string]struct{}, len(keywords))
	for _, keyword := range keywords {
		trimmed := strings.TrimSpace(keyword)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	return normalized
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
