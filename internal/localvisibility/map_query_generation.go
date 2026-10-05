package localvisibility

import (
	"errors"
	"fmt"
	"strings"
)

// MaxMapQueryBytes is the maximum length in UTF-8 bytes of one editable Maps
// query. Longer queries are rejected, never truncated.
const MaxMapQueryBytes = 500

// GenerateMapQueries returns the deterministic Maps prefill for services and
// ordered locality levels, smallest first. It round-robins by query type so
// every service appears before going deep on any one: bare names first, then
// proximity, then one area query per distinct level per service. Each emitted
// query is a genuinely different search, never two word orders of the same
// one. A thin geography yields fewer queries rather than padded duplicates,
// so a run costs only what it actually searches. At least one service must be
// non-empty; locality levels may be empty. Output is capped at MapQueryCount.
func GenerateMapQueries(services []string, localities []string) ([]string, error) {
	services = distinctQueryLevels(services)
	if len(services) == 0 {
		return nil, errors.New("generate map queries: at least one service must be non-empty")
	}
	levels := distinctQueryLevels(localities)
	seen := make(map[string]bool, MapQueryCount)
	candidates := make([]string, 0, MapQueryCount)
	add := func(query string) {
		if len(candidates) >= MapQueryCount {
			return
		}
		if key := strings.ToLower(strings.TrimSpace(query)); key != "" && !seen[key] {
			seen[key] = true
			candidates = append(candidates, strings.TrimSpace(query))
		}
	}
	for _, service := range services {
		add(service)
	}
	for _, service := range services {
		add(service + " near me")
	}
	for _, level := range levels {
		for _, service := range services {
			add(service + " in " + level)
		}
	}
	queries, err := ValidateEditableMapQueries(candidates)
	if err != nil {
		return nil, fmt.Errorf("generate map queries: %w", err)
	}
	return queries, nil
}

// distinctQueryLevels trims, drops blanks and case-insensitive repeats, and
// keeps the caller's smallest-first order so each level is a different scope.
func distinctQueryLevels(levels []string) []string {
	seen := make(map[string]bool, len(levels))
	out := make([]string, 0, len(levels))
	for _, level := range levels {
		level = strings.TrimSpace(level)
		if level == "" {
			continue
		}
		if key := strings.ToLower(level); !seen[key] {
			seen[key] = true
			out = append(out, level)
		}
	}
	return out
}

// ValidateEditableMapQueries normalises and validates an editable query list of
// zero to MapQueryCount (five) queries. It strips outer whitespace, then rejects
// blank entries, queries longer than MaxMapQueryBytes, and duplicates compared
// case-insensitively. It returns a non-nil slice, so an empty list round-trips
// as JSON [] rather than null, and it never pads a short list.
func ValidateEditableMapQueries(queries []string) ([]string, error) {
	if len(queries) > MapQueryCount {
		return nil, fmt.Errorf("local visibility editable queries: %d exceeds maximum %d", len(queries), MapQueryCount)
	}
	normalized := make([]string, 0, len(queries))
	seen := make(map[string]bool, len(queries))
	for i, query := range queries {
		query = strings.TrimSpace(query)
		if query == "" {
			return nil, fmt.Errorf("local visibility editable query %d must not be blank", i+1)
		}
		if len(query) > MaxMapQueryBytes {
			return nil, fmt.Errorf("local visibility editable query %d exceeds %d bytes", i+1, MaxMapQueryBytes)
		}
		key := strings.ToLower(query)
		if seen[key] {
			return nil, fmt.Errorf("local visibility editable query %d duplicates an earlier query", i+1)
		}
		seen[key] = true
		normalized = append(normalized, query)
	}
	return normalized, nil
}
