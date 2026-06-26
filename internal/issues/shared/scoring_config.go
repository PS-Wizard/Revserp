package shared

// ScoringConfig holds the score-combination knobs that can be edited without changing issue derivation.
type ScoringConfig struct {
	Version             string                         `json:"version"`
	MinimumOverallScore int32                          `json:"minimum_overall_score"`
	CoverageScale       float64                        `json:"coverage_scale"`
	SoftSumDecay        float64                        `json:"soft_sum_decay"`
	SeverityMultipliers map[string]float64             `json:"severity_multipliers"`
	OverallWeights      map[string]float64             `json:"overall_weights"`
	Pillars             map[string]PillarScoringConfig `json:"pillars"`
}

// PillarScoringConfig holds score weights and issue penalties for one scoring pillar.
type PillarScoringConfig struct {
	Label                string             `json:"label"`
	Weight               float64            `json:"weight"`
	MinimumIssueCoverage float64            `json:"minimum_issue_coverage,omitempty"`
	BucketWeights        map[string]float64 `json:"bucket_weights"`
	IssuePenaltyByType   map[string]float64 `json:"issue_penalty_by_type"`
}

// DefaultScoringMathConfig returns the shared scoring math defaults.
func DefaultScoringMathConfig() ScoringConfig {
	return ScoringConfig{
		Version:             "v9-soft-sum",
		MinimumOverallScore: MinimumOverallScore,
		CoverageScale:       CoverageScale,
		SoftSumDecay:        SoftSumDecay,
		SeverityMultipliers: map[string]float64{
			"high":   1.0,
			"medium": 0.6,
			"low":    0.3,
		},
		OverallWeights: map[string]float64{
			"seo":       0.65,
			"aeo":       0.20,
			"pagespeed": 0.15,
		},
		Pillars: map[string]PillarScoringConfig{},
	}
}
