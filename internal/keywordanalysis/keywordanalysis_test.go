package keywordanalysis

import (
	"math"
	"testing"
)

func int32Ptr(v int32) *int32       { return &v }
func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }

func TestValidateEstimates(t *testing.T) {
	tests := []struct {
		name    string
		input   []Estimate
		wantErr bool
	}{
		{
			name: "valid mixed batch with nulls",
			input: []Estimate{
				{Keyword: "Acme", Kind: "brand", Rank: int32Ptr(3), KD: float64Ptr(12.5)},
				{Keyword: "crm software", Kind: "non_brand"},
			},
		},
		{name: "empty batch", input: nil, wantErr: true},
		{
			name:    "rank zero",
			input:   []Estimate{{Keyword: "Acme", Kind: "brand", Rank: int32Ptr(0)}},
			wantErr: true,
		},
		{
			name:    "rank above 100",
			input:   []Estimate{{Keyword: "Acme", Kind: "brand", Rank: int32Ptr(101)}},
			wantErr: true,
		},
		{
			name:    "negative traffic",
			input:   []Estimate{{Keyword: "Acme", Kind: "brand", Traffic: int64Ptr(-1)}},
			wantErr: true,
		},
		{
			name:    "negative volume",
			input:   []Estimate{{Keyword: "Acme", Kind: "brand", Volume: int64Ptr(-5)}},
			wantErr: true,
		},
		{
			name:    "kd above 100",
			input:   []Estimate{{Keyword: "Acme", Kind: "brand", KD: float64Ptr(100.5)}},
			wantErr: true,
		},
		{
			name:    "negative kd",
			input:   []Estimate{{Keyword: "Acme", Kind: "brand", KD: float64Ptr(-0.1)}},
			wantErr: true,
		},
		{
			name:    "negative cpc",
			input:   []Estimate{{Keyword: "Acme", Kind: "brand", CPC: float64Ptr(-2)}},
			wantErr: true,
		},
		{
			name:    "negative results count",
			input:   []Estimate{{Keyword: "Acme", Kind: "brand", ResultsCount: int64Ptr(-1)}},
			wantErr: true,
		},
		{
			name:    "bad kind",
			input:   []Estimate{{Keyword: "Acme", Kind: "branded"}},
			wantErr: true,
		},
		{
			name:    "blank keyword",
			input:   []Estimate{{Keyword: "   ", Kind: "brand"}},
			wantErr: true,
		},
		{
			name: "same phrase both kinds",
			input: []Estimate{
				{Keyword: "Acme", Kind: "brand"},
				{Keyword: "acme", Kind: "non_brand"},
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateEstimates(test.input)
			if (err != nil) != test.wantErr {
				t.Fatalf("ValidateEstimates err = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateEstimatesDedupesCaseVariants(t *testing.T) {
	validated, err := ValidateEstimates([]Estimate{
		{Keyword: "Acme CRM", Kind: "brand", Rank: int32Ptr(1)},
		{Keyword: "acme  crm", Kind: "brand", Rank: int32Ptr(2)},
	})
	if err != nil {
		t.Fatalf("ValidateEstimates: %v", err)
	}
	if len(validated) != 1 {
		t.Fatalf("validated = %d entries, want 1", len(validated))
	}
	if !validated[0].Rank.Valid || validated[0].Rank.Int32 != 1 {
		t.Fatalf("rank = %+v, want first write wins", validated[0].Rank)
	}
}

func TestValidateEstimatesBounds(t *testing.T) {
	validated, err := ValidateEstimates([]Estimate{
		{Keyword: "edge", Kind: "non_brand", Rank: int32Ptr(100), KD: float64Ptr(100), CPC: float64Ptr(0), Traffic: int64Ptr(0)},
	})
	if err != nil {
		t.Fatalf("ValidateEstimates: %v", err)
	}
	if !validated[0].KD.Valid || validated[0].KD.Float64 != 100 {
		t.Fatalf("kd = %+v, want 100", validated[0].KD)
	}
}

func TestValidateEstimatesRejectsNonFinite(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := ValidateEstimates([]Estimate{{Keyword: "Acme", Kind: "brand", KD: &value}}); err == nil {
			t.Fatalf("kd=%v accepted", value)
		}
		if _, err := ValidateEstimates([]Estimate{{Keyword: "Acme", Kind: "brand", CPC: &value}}); err == nil {
			t.Fatalf("cpc=%v accepted", value)
		}
	}
}

func TestValidateEstimateCoverage(t *testing.T) {
	expected := []ExpectedKeyword{{Keyword: "Acme", Kind: "brand"}, {Keyword: "crm software", Kind: "non_brand"}}
	if err := ValidateEstimateCoverage([]Estimate{
		{Keyword: "Acme", Kind: "brand", Rank: int32Ptr(3)},
		{Keyword: "CRM Software", Kind: "non_brand"},
	}, expected); err != nil {
		t.Fatalf("exact set: %v", err)
	}
	cases := []struct {
		name   string
		output []Estimate
	}{
		{"empty", nil},
		{"missing", []Estimate{{Keyword: "Acme", Kind: "brand"}}},
		{"extra", []Estimate{{Keyword: "Acme", Kind: "brand"}, {Keyword: "crm software", Kind: "non_brand"}, {Keyword: "other", Kind: "brand"}}},
		{"duplicate", []Estimate{{Keyword: "Acme", Kind: "brand"}, {Keyword: "ACME", Kind: "brand"}, {Keyword: "crm software", Kind: "non_brand"}}},
		{"kind change", []Estimate{{Keyword: "Acme", Kind: "non_brand"}, {Keyword: "crm software", Kind: "non_brand"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateEstimateCoverage(tc.output, expected); err == nil {
				t.Fatalf("accepted %v", tc.output)
			}
		})
	}
	if err := ValidateEstimateCoverage([]Estimate{{Keyword: "x", Kind: "brand"}}, nil); err == nil {
		t.Fatal("accepted empty expected set")
	}
}

func TestValidateCompleteEstimatesRequiresFiveMetrics(t *testing.T) {
	five := []Estimate{
		{Keyword: "Acme", Kind: "brand", Rank: int32Ptr(3), Traffic: int64Ptr(100), Volume: int64Ptr(500), KD: float64Ptr(20), CPC: float64Ptr(1.5)},
	}
	if _, err := ValidateCompleteEstimates(five); err != nil {
		t.Fatalf("five metrics without results_count: %v", err)
	}
	legacy := []Estimate{
		{Keyword: "Acme", Kind: "brand", Rank: int32Ptr(3), Traffic: int64Ptr(100), Volume: int64Ptr(500), KD: float64Ptr(20), CPC: float64Ptr(1.5), ResultsCount: int64Ptr(9000)},
	}
	if _, err := ValidateCompleteEstimates(legacy); err != nil {
		t.Fatalf("legacy results_count must stay accepted: %v", err)
	}
	missing := []Estimate{
		{Keyword: "Acme", Kind: "brand", Rank: int32Ptr(3), Traffic: int64Ptr(100), Volume: int64Ptr(500), KD: float64Ptr(20)},
	}
	if _, err := ValidateCompleteEstimates(missing); err == nil {
		t.Fatal("missing cpc completed validation")
	}
}
