package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues/aeo"
	pagespeed "github.com/ps-wizard/revserp/internal/issues/page_speed"
	"github.com/ps-wizard/revserp/internal/issues/seo"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// DefaultScoringConfig returns the built-in global score-combination config.
func DefaultScoringConfig() shared.ScoringConfig {
	config := shared.DefaultScoringMathConfig()
	config.Pillars = map[string]shared.PillarScoringConfig{
		seo.PillarID: {
			Label:              seo.PillarLabel,
			Weight:             seo.PillarWeight,
			BucketWeights:      cloneFloatMap(seo.BucketWeights),
			IssuePenaltyByType: cloneFloatMap(seo.IssuePenaltyByType),
		},
		aeo.PillarID: {
			Label:                aeo.PillarLabel,
			Weight:               aeo.PillarWeight,
			MinimumIssueCoverage: 0.75,
			BucketWeights:        cloneFloatMap(aeo.BucketWeights),
			IssuePenaltyByType:   cloneFloatMap(aeo.IssuePenaltyByType),
		},
		pagespeed.PillarID: {
			Label:              pagespeed.PillarLabel,
			Weight:             pagespeed.PillarWeight,
			BucketWeights:      cloneFloatMap(pagespeed.BucketWeights),
			IssuePenaltyByType: cloneFloatMap(pagespeed.IssuePenaltyByType),
		},
	}
	return config
}

// ParseScoringConfig merges a JSON payload into the default scoring config and validates it.
func ParseScoringConfig(rawConfig []byte) (shared.ScoringConfig, error) {
	config := DefaultScoringConfig()
	if len(strings.TrimSpace(string(rawConfig))) == 0 {
		return config, nil
	}
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return shared.ScoringConfig{}, err
	}
	if err := ValidateScoringConfig(config); err != nil {
		return shared.ScoringConfig{}, err
	}
	return config, nil
}

// MustMarshalScoringConfig marshals a validated scoring config for persistence.
func MustMarshalScoringConfig(config shared.ScoringConfig) ([]byte, error) {
	if err := ValidateScoringConfig(config); err != nil {
		return nil, err
	}
	return json.Marshal(config)
}

// LoadActiveScoringConfig returns the persisted global config, falling back to defaults when unset.
func LoadActiveScoringConfig(ctx context.Context, queries *sqlc.Queries) (shared.ScoringConfig, error) {
	row, err := queries.GetActiveScoringConfig(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DefaultScoringConfig(), nil
		}
		return shared.ScoringConfig{}, fmt.Errorf("get active scoring config: %w", err)
	}
	return ParseScoringConfig(row.ConfigJson)
}

// ValidateScoringConfig rejects config values that would make scoring unstable.
func ValidateScoringConfig(config shared.ScoringConfig) error {
	if config.CoverageScale <= 0 {
		return errors.New("coverage_scale must be greater than zero")
	}
	if config.VolumePressureScale < 0 {
		return errors.New("volume_pressure_scale must be greater than or equal to zero")
	}
	if config.MaximumVolumePressure < 0 {
		return errors.New("maximum_volume_pressure must be greater than or equal to zero")
	}
	if len(config.SeverityMultipliers) == 0 {
		return errors.New("severity_multipliers is required")
	}
	for severity, multiplier := range config.SeverityMultipliers {
		if strings.TrimSpace(severity) == "" || multiplier < 0 {
			return fmt.Errorf("invalid severity multiplier for %q", severity)
		}
	}
	if len(config.OverallWeights) == 0 {
		return errors.New("overall_weights is required")
	}
	for pillarID, weight := range config.OverallWeights {
		if strings.TrimSpace(pillarID) == "" || weight < 0 {
			return fmt.Errorf("invalid overall weight for %q", pillarID)
		}
	}
	for _, pillarID := range []string{seo.PillarID, aeo.PillarID, pagespeed.PillarID} {
		pillarConfig, exists := config.Pillars[pillarID]
		if !exists {
			return fmt.Errorf("missing pillar config for %s", pillarID)
		}
		if strings.TrimSpace(pillarConfig.Label) == "" {
			return fmt.Errorf("missing label for pillar %s", pillarID)
		}
		if pillarConfig.Weight < 0 {
			return fmt.Errorf("invalid weight for pillar %s", pillarID)
		}
		if pillarConfig.MinimumIssueCoverage < 0 || pillarConfig.MinimumIssueCoverage > 1 {
			return fmt.Errorf("invalid minimum issue coverage for pillar %s", pillarID)
		}
		if len(pillarConfig.BucketWeights) == 0 {
			return fmt.Errorf("bucket_weights is required for pillar %s", pillarID)
		}
		for bucketID, weight := range pillarConfig.BucketWeights {
			if strings.TrimSpace(bucketID) == "" || weight < 0 {
				return fmt.Errorf("invalid bucket weight for %s/%s", pillarID, bucketID)
			}
		}
		if len(pillarConfig.IssuePenaltyByType) == 0 {
			return fmt.Errorf("issue_penalty_by_type is required for pillar %s", pillarID)
		}
		for issueType, penalty := range pillarConfig.IssuePenaltyByType {
			if strings.TrimSpace(issueType) == "" || penalty < 0 {
				return fmt.Errorf("invalid issue penalty for %s/%s", pillarID, issueType)
			}
		}
	}
	return nil
}

func cloneFloatMap(input map[string]float64) map[string]float64 {
	cloned := make(map[string]float64, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
