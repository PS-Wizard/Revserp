package localvisibility

import (
	"strings"
	"testing"
)

func TestGenerateMapQueriesTemplates(t *testing.T) {
	// Each emitted query is a genuinely different search. The old templates
	// produced "in X", "near X" and "X service" as three slots for one idea;
	// those word-order duplicates each cost a full grid run for nothing.
	queries, err := GenerateMapQueries([]string{"coffee shop"}, []string{"Sano Gaucharan", "Kathmandu-05", "Kathmandu"})
	if err != nil {
		t.Fatalf("GenerateMapQueries returned error: %v", err)
	}
	want := []string{
		"coffee shop",
		"coffee shop near me",
		"coffee shop in Sano Gaucharan",
		"coffee shop in Kathmandu-05",
		"coffee shop in Kathmandu",
	}
	if len(queries) != len(want) {
		t.Fatalf("len(queries) = %d, want %d", len(queries), len(want))
	}
	for i, w := range want {
		if queries[i] != w {
			t.Errorf("queries[%d] = %q, want %q", i, queries[i], w)
		}
	}

	trimmed, err := GenerateMapQueries([]string{"  coffee shop "}, []string{" Paris "})
	if err != nil {
		t.Fatalf("GenerateMapQueries with outer spaces returned error: %v", err)
	}
	trimWant := []string{"coffee shop", "coffee shop near me", "coffee shop in Paris"}
	if len(trimmed) != len(trimWant) {
		t.Fatalf("len(trimmed) = %d, want %d", len(trimmed), len(trimWant))
	}
	for i, w := range trimWant {
		if trimmed[i] != w {
			t.Errorf("trimmed queries[%d] = %q, want %q", i, trimmed[i], w)
		}
	}
}

func TestGenerateMapQueriesThinGeographyYieldsFewer(t *testing.T) {
	// A single locality level produces three honest queries, not three honest
	// ones plus two padded word-order duplicates. A run with fewer queries
	// costs less because cost derives from the actual count.
	queries, err := GenerateMapQueries([]string{"car repair"}, []string{"Kalopul"})
	if err != nil {
		t.Fatalf("GenerateMapQueries returned error: %v", err)
	}
	want := []string{"car repair", "car repair near me", "car repair in Kalopul"}
	if len(queries) != len(want) {
		t.Fatalf("len(queries) = %d, want %d", len(queries), len(want))
	}
	for i, w := range want {
		if queries[i] != w {
			t.Errorf("queries[%d] = %q, want %q", i, queries[i], w)
		}
	}

	bare, err := GenerateMapQueries([]string{"car repair"}, nil)
	if err != nil {
		t.Fatalf("GenerateMapQueries with no levels returned error: %v", err)
	}
	if len(bare) != 2 || bare[0] != "car repair" || bare[1] != "car repair near me" {
		t.Fatalf("bare queries = %q, want service plus near me only", bare)
	}
}

func TestGenerateMapQueriesRoundRobinsServices(t *testing.T) {
	// Every service appears before going deep on any one: bare names first,
	// then proximity, then area per level. Two services and one level yield
	// both services bare and near, plus the first area query.
	queries, err := GenerateMapQueries([]string{"life insurance", "car repair"}, []string{"Gaucharan"})
	if err != nil {
		t.Fatalf("GenerateMapQueries returned error: %v", err)
	}
	want := []string{
		"life insurance",
		"car repair",
		"life insurance near me",
		"car repair near me",
		"life insurance in Gaucharan",
	}
	if len(queries) != len(want) {
		t.Fatalf("len(queries) = %d, want %d: %q", len(queries), len(want), queries)
	}
	for i, w := range want {
		if queries[i] != w {
			t.Errorf("queries[%d] = %q, want %q", i, queries[i], w)
		}
	}
}

func TestGenerateMapQueriesCapsAtFive(t *testing.T) {
	queries, err := GenerateMapQueries([]string{"plumber"}, []string{"A", "B", "C", "D", "E"})
	if err != nil {
		t.Fatalf("GenerateMapQueries returned error: %v", err)
	}
	if len(queries) != MapQueryCount {
		t.Fatalf("len(queries) = %d, want cap %d", len(queries), MapQueryCount)
	}
}

func TestGenerateMapQueriesRejections(t *testing.T) {
	for _, tc := range []struct {
		name       string
		service    string
		localities []string
	}{
		{"empty service", "", []string{"Paris"}},
		{"whitespace service", "   ", []string{"Paris"}},

		{"service oversized", strings.Repeat("a", MaxMapQueryBytes), []string{"Paris"}},
		{"level oversized", "coffee shop", []string{strings.Repeat("a", MaxMapQueryBytes+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := GenerateMapQueries([]string{tc.service}, tc.localities); err == nil {
				t.Fatalf("GenerateMapQueries(%q,%q) = %q, want error", tc.service, tc.localities, got)
			}
		})
	}
}

func TestValidateEditableMapQueriesEmptyReturnsNonNilSlice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []string
	}{
		{"nil input", nil},
		{"empty slice", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateEditableMapQueries(tc.input)
			if err != nil {
				t.Fatalf("ValidateEditableMapQueries returned error: %v", err)
			}
			if got == nil {
				t.Fatal("ValidateEditableMapQueries returned a nil slice, want non-nil for JSON []")
			}
			if len(got) != 0 {
				t.Fatalf("len(got) = %d, want 0", len(got))
			}
		})
	}
}

func TestValidateEditableMapQueriesAcceptsAndTrims(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []string
		want  []string
	}{
		{"partial", []string{"  coffee shop ", "Paris coffee shop"}, []string{"coffee shop", "Paris coffee shop"}},
		{"five", []string{"a", "b", "c", "d", "e"}, []string{"a", "b", "c", "d", "e"}},
		{"boundary 500 bytes", []string{strings.Repeat("a", MaxMapQueryBytes)}, []string{strings.Repeat("a", MaxMapQueryBytes)}},
		{"boundary 500 unicode bytes", []string{strings.Repeat("é", MaxMapQueryBytes/2)}, []string{strings.Repeat("é", MaxMapQueryBytes/2)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateEditableMapQueries(tc.input)
			if err != nil {
				t.Fatalf("ValidateEditableMapQueries returned error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len(got) = %d, want %d", len(got), len(tc.want))
			}
			for i, w := range tc.want {
				if got[i] != w {
					t.Errorf("got[%d] = %q, want %q", i, got[i], w)
				}
			}
		})
	}
}

func TestValidateEditableMapQueriesRejections(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []string
	}{
		{"too many", []string{"a", "b", "c", "d", "e", "f"}},
		{"blank middle", []string{"a", "   ", "b"}},
		{"blank leading", []string{"", "b"}},
		{"duplicate exact", []string{"coffee", "coffee"}},
		{"duplicate case-insensitive", []string{"Coffee Shop", "coffee shop"}},
		{"oversized ascii", []string{strings.Repeat("a", MaxMapQueryBytes+1)}},
		{"oversized unicode", []string{strings.Repeat("é", MaxMapQueryBytes/2+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ValidateEditableMapQueries(tc.input); err == nil {
				t.Fatalf("ValidateEditableMapQueries(%q) = %q, want error", tc.input, got)
			}
		})
	}
}
