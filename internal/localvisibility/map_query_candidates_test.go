package localvisibility

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/textnormalization"
)

func TestGenerateMapQueryCandidatesExceedsFive(t *testing.T) {
	// The editable draft stores any candidate count; only enqueue caps at five.
	// Two services and three locality levels must all survive untruncated.
	services := []string{"life insurance", "car repair"}
	localities := []string{"Kalikasthan", "Kathmandu-29", "Kathmandu Metropolitan City"}

	got, err := GenerateMapQueryCandidates(services, localities)
	if err != nil {
		t.Fatalf("GenerateMapQueryCandidates returned error: %v", err)
	}
	want := []string{
		"life insurance",
		"car repair",
		"life insurance near me",
		"car repair near me",
		"life insurance in Kalikasthan",
		"car repair in Kalikasthan",
		"life insurance in Kathmandu-29",
		"car repair in Kathmandu-29",
		"life insurance in Kathmandu Metropolitan City",
		"car repair in Kathmandu Metropolitan City",
	}
	if len(got) <= MapQueryCount {
		t.Fatalf("len(got) = %d, want more than the %d run cap", len(got), MapQueryCount)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestGenerateMapQueryCandidatesDeterministicRepeats(t *testing.T) {
	services := []string{"plumber", "electrician"}
	localities := []string{"Baluwatar", "Kathmandu"}

	first, err := GenerateMapQueryCandidates(services, localities)
	if err != nil {
		t.Fatalf("first call returned error: %v", err)
	}
	second, err := GenerateMapQueryCandidates(services, localities)
	if err != nil {
		t.Fatalf("second call returned error: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated calls differ: %q vs %q", first, second)
	}
	if first == nil {
		t.Fatal("result is nil, want a non-nil slice")
	}
}

func TestGenerateMapQueryCandidatesDedupesWithSharedNormalization(t *testing.T) {
	// Mixed case and Unicode whitespace (NBSP) must fold to one candidate via
	// textnormalization.NormalizeTextDisplay/Key, keeping the first spelling.
	services := []string{" Coffee  Shop ", "coffee shop", "COFFEE\u00a0SHOP"}
	localities := []string{"Paris ", "paris", "PARIS\u00a0"}

	got, err := GenerateMapQueryCandidates(services, localities)
	if err != nil {
		t.Fatalf("GenerateMapQueryCandidates returned error: %v", err)
	}
	want := []string{"Coffee Shop", "Coffee Shop near me", "Coffee Shop in Paris"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if want[0] != textnormalization.NormalizeTextDisplay("  Coffee  Shop ") {
		t.Fatalf("display form %q is not the shared normalization of the first service", want[0])
	}
	if textnormalization.NormalizeTextKey("coffee shop") != textnormalization.NormalizeTextKey(want[0]) {
		t.Fatal("deduped service key does not match the shared helper key")
	}
	if want[1] != want[0]+" near me" || want[2] != want[0]+" in "+textnormalization.NormalizeTextDisplay("Paris ") {
		t.Fatalf("candidates %q do not use the normalized service and locality", got)
	}
}

func TestGenerateMapQueryCandidatesEmptyServicesError(t *testing.T) {
	for _, tc := range []struct {
		name     string
		services []string
	}{
		{"nil services", nil},
		{"empty services", []string{}},
		{"blank services", []string{"", "   ", "\t\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GenerateMapQueryCandidates(tc.services, []string{"Paris"})
			if err == nil {
				t.Fatalf("GenerateMapQueryCandidates(%q) = %q, want error", tc.services, got)
			}
		})
	}
}

func TestGenerateMapQueryCandidatesThinGeographyNoPadding(t *testing.T) {
	got, err := GenerateMapQueryCandidates([]string{"car repair"}, []string{"Kalopul"})
	if err != nil {
		t.Fatalf("GenerateMapQueryCandidates returned error: %v", err)
	}
	want := []string{"car repair", "car repair near me", "car repair in Kalopul"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}

	bare, err := GenerateMapQueryCandidates([]string{"car repair"}, nil)
	if err != nil {
		t.Fatalf("GenerateMapQueryCandidates with no localities returned error: %v", err)
	}
	if !reflect.DeepEqual(bare, []string{"car repair", "car repair near me"}) {
		t.Fatalf("bare candidates = %q, want service plus near me only", bare)
	}
}

func TestGenerateMapQueryCandidatesRejectsInvalidInputs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		services   []string
		localities []string
	}{
		{"invalid UTF-8 service", []string{"coffee\xff\xfeshop"}, nil},
		{"NUL service", []string{"coffee\x00shop"}, nil},
		{"oversized service", []string{strings.Repeat("a", MaxMapQueryBytes+1)}, nil},
		{"near me overflow", []string{strings.Repeat("é", MaxMapQueryBytes/2)}, nil},
		{"invalid UTF-8 locality", []string{"coffee shop"}, []string{"Paris\xff"}},
		{"NUL locality", []string{"coffee shop"}, []string{"Pa\x00ris"}},
		{"oversized locality", []string{"coffee shop"}, []string{strings.Repeat("a", MaxMapQueryBytes+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GenerateMapQueryCandidates(tc.services, tc.localities)
			if err == nil {
				t.Fatalf("GenerateMapQueryCandidates(%q,%q) = %q, want error", tc.services, tc.localities, got)
			}
		})
	}
}

func TestGenerateMapQueryCandidatesBoundaryServiceLength(t *testing.T) {
	// A bare query at exactly the byte limit is valid, but appending " near me"
	// pushes it over; that overflow must be an honest error, not a silent drop.
	service := strings.Repeat("a", MaxMapQueryBytes)
	if got, err := GenerateMapQueryCandidates([]string{service}, nil); err == nil {
		t.Fatalf("GenerateMapQueryCandidates(boundary) = %d candidates, want overflow error", len(got))
	}
}

func TestValidateMapQueryCandidateRejects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"blank", "   "},
		{"invalid UTF-8", "coffee\xffshop"},
		{"NUL byte", "coffee\x00shop"},
		{"oversized", strings.Repeat("a", MaxMapQueryBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateMapQueryCandidate(tc.query); err == nil {
				t.Fatalf("ValidateMapQueryCandidate(%q) = nil, want error", tc.query)
			}
		})
	}
	if err := ValidateMapQueryCandidate("coffee shop"); err != nil {
		t.Fatalf("ValidateMapQueryCandidate(valid) = %v, want nil", err)
	}
}
