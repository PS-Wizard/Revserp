package localvisibility

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ps-wizard/revserp/internal/textnormalization"
)

func GenerateMapQueryCandidates(services, localities []string) ([]string, error) {
	serviceNames, err := normalizeMapQueryInputs("service", services)
	if err != nil {
		return nil, err
	}
	if len(serviceNames) == 0 {
		return nil, errors.New("generate map query candidates: at least one service must be non-empty")
	}
	localityNames, err := normalizeMapQueryInputs("locality", localities)
	if err != nil {
		return nil, err
	}

	raw := make([]string, 0, len(serviceNames)*(2+len(localityNames)))
	for _, service := range serviceNames {
		raw = append(raw, service)
	}
	for _, service := range serviceNames {
		raw = append(raw, service+" near me")
	}
	for _, locality := range localityNames {
		for _, service := range serviceNames {
			raw = append(raw, service+" in "+locality)
		}
	}

	candidates := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, query := range raw {
		if err := ValidateMapQueryCandidate(query); err != nil {
			return nil, err
		}
		key := textnormalization.NormalizeTextKey(query)
		if seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, query)
	}
	return candidates, nil
}

// Reject invalid UTF-8 before normalization can replace the invalid bytes.
func normalizeMapQueryInputs(kind string, values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for i, value := range values {
		if !utf8.ValidString(value) {
			return nil, fmt.Errorf("generate map query candidates: %s %d is not valid UTF-8", kind, i+1)
		}
		if strings.IndexByte(value, 0) >= 0 {
			return nil, fmt.Errorf("generate map query candidates: %s %d contains a NUL byte", kind, i+1)
		}
		display := textnormalization.NormalizeTextDisplay(value)
		if display == "" {
			continue
		}
		if len(display) > MaxMapQueryBytes {
			return nil, fmt.Errorf("generate map query candidates: %s %d exceeds %d bytes", kind, i+1, MaxMapQueryBytes)
		}
		key := textnormalization.NormalizeTextKey(display)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, display)
	}
	return out, nil
}

// ValidateMapQueryCandidate checks query encoding, content and the shared byte limit.
func ValidateMapQueryCandidate(query string) error {
	switch {
	case !utf8.ValidString(query):
		return errors.New("generate map query candidates: query is not valid UTF-8")
	case strings.IndexByte(query, 0) >= 0:
		return errors.New("generate map query candidates: query contains a NUL byte")
	case textnormalization.NormalizeTextDisplay(query) == "":
		return errors.New("generate map query candidates: query must not be blank")
	case len(query) > MaxMapQueryBytes:
		return fmt.Errorf("generate map query candidates: query exceeds %d bytes", MaxMapQueryBytes)
	default:
		return nil
	}
}
