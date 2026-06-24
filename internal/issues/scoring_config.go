package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
			MinimumIssueCoverage: aeo.MinimumIssueCoverage,
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
	// Overall weights are inherently relative. Normalize them to sum to 1.0 at
	// load time so a stored config whose weights drifted (e.g. an admin saved
	// 1.17) still produces in-range scores instead of failing the whole crawl.
	// Strict rejection of bad weights happens at write time via the explicit
	// ValidateScoringConfig calls in the admin handlers, not here on the read path.
	normalizeOverallWeights(&config)
	if err := ValidateScoringConfig(config); err != nil {
		return shared.ScoringConfig{}, err
	}
	return config, nil
}

// normalizeOverallWeights scales OverallWeights so they sum to 1.0, preserving
// their relative proportions. A degenerate (<=0) sum is left untouched for
// ValidateScoringConfig to reject.
func normalizeOverallWeights(config *shared.ScoringConfig) {
	sum := 0.0
	for _, weight := range config.OverallWeights {
		sum += weight
	}
	if sum <= 0 || math.Abs(sum-1.0) < 1e-3 {
		return
	}
	log.Printf("scoring: normalizing overall_weights (sum was %.4f, scaling to 1.0)", sum)
	for pillarID, weight := range config.OverallWeights {
		config.OverallWeights[pillarID] = weight / sum
	}
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
	// Weights only need to be positive in aggregate; they are normalized to
	// sum to 1.0 at load time (see normalizeOverallWeights), so any positive
	// sum is acceptable. A zero/negative sum is degenerate and cannot be normalized.
	overallWeightSum := 0.0
	for _, weight := range config.OverallWeights {
		overallWeightSum += weight
	}
	if overallWeightSum <= 0 {
		return fmt.Errorf("overall_weights must sum to a positive value (got %.4f)", overallWeightSum)
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

// LoadEffectiveScoringConfig returns the effective scoring config for the given organization.
// It checks for an organization override first; if none exists, the global config is used.
func LoadEffectiveScoringConfig(ctx context.Context, queries *sqlc.Queries, orgID pgtype.UUID) (shared.ScoringConfig, error) {
	if orgID.Valid {
		row, err := queries.GetOrgScoringConfig(ctx, orgID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return shared.ScoringConfig{}, fmt.Errorf("get org scoring config: %w", err)
		}
		if err == nil {
			return ParseScoringConfig(row.ConfigJson)
		}
	}
	return LoadActiveScoringConfig(ctx, queries)
}
