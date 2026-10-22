// Package keywordanalysis owns AI-generated keyword estimates: per-keyword
// rank, traffic, volume, keyword difficulty, CPC and result counts. Estimates
// are model guesses grounded in crawled content, never measured search data.
package keywordanalysis

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

// MaxEstimatesPerScope caps one analysis write well above the keyword lists it
// covers (10 brand plus 10 non-brand per scope).
const MaxEstimatesPerScope = 40

// Estimate is one AI numeric estimate for a keyword phrase. Metrics stay
// nullable so intermediate competitor keyword generation rows can persist
// with NULLs until the analysis step fills them; completed analyses must
// use ValidateCompleteEstimates instead.
type Estimate struct {
	Keyword      string
	Kind         string
	Rank         *int32
	Traffic      *int64
	Volume       *int64
	KD           *float64
	CPC          *float64
	ResultsCount *int64
}

// ValidatedEstimate is an Estimate that passed phrase, kind and bounds checks.
type ValidatedEstimate struct {
	Keyword      string
	Key          string
	Kind         string
	Rank         pgtype.Int4
	Traffic      pgtype.Int8
	Volume       pgtype.Int8
	KD           pgtype.Float8
	CPC          pgtype.Float8
	ResultsCount pgtype.Int8
}

// Analysis is the API shape of one stored estimate.
type Analysis struct {
	Keyword      string   `json:"keyword"`
	Kind         string   `json:"kind"`
	Rank         *int32   `json:"rank"`
	Traffic      *int64   `json:"traffic"`
	Volume       *int64   `json:"volume"`
	KD           *float64 `json:"kd"`
	CPC          *float64 `json:"cpc"`
	ResultsCount *int64   `json:"results_count"`
}

var (
	// ErrEstimateInvalid marks a blank phrase, bad kind, oversized batch, or an
	// out-of-bounds numeric estimate.
	ErrEstimateInvalid = errors.New("keyword analysis invalid")
	// ErrEstimateConflict marks the same phrase listed under both kinds.
	ErrEstimateConflict = errors.New("keyword analysis kind conflict")
)

// ValidateEstimates checks a whole analysis batch: non-empty, bounded, one
// kind per phrase, finite non-negative numerics with kd in 0..100 and rank in
// 1..100. Null metrics are allowed for intermediate competitor keyword
// generation rows; completed analyses must use ValidateCompleteEstimates.
func ValidateEstimates(estimates []Estimate) ([]ValidatedEstimate, error) {
	if len(estimates) == 0 {
		return nil, fmt.Errorf("%w: analysis list must not be empty", ErrEstimateInvalid)
	}
	if len(estimates) > MaxEstimatesPerScope {
		return nil, fmt.Errorf("%w: analysis list exceeds %d entries", ErrEstimateInvalid, MaxEstimatesPerScope)
	}
	kinds := make(map[string]string, len(estimates))
	out := make([]ValidatedEstimate, 0, len(estimates))
	for _, estimate := range estimates {
		validated, err := ValidateEstimate(estimate)
		if err != nil {
			return nil, err
		}
		if kind, dup := kinds[validated.Key]; dup && kind != validated.Kind {
			return nil, fmt.Errorf("%w: %q is already %s", ErrEstimateConflict, validated.Keyword, kind)
		}
		if _, dup := kinds[validated.Key]; dup {
			continue
		}
		kinds[validated.Key] = validated.Kind
		out = append(out, validated)
	}
	return out, nil
}

// ValidateCompleteEstimates checks a whole completed analysis batch: same
// bounds as ValidateEstimates plus a best-effort numeric value for rank,
// traffic, volume, kd and cpc on every entry, never null.
func ValidateCompleteEstimates(estimates []Estimate) ([]ValidatedEstimate, error) {
	validated, err := ValidateEstimates(estimates)
	if err != nil {
		return nil, err
	}
	for _, estimate := range validated {
		if !estimate.Rank.Valid || !estimate.Traffic.Valid || !estimate.Volume.Valid || !estimate.KD.Valid || !estimate.CPC.Valid {
			return nil, fmt.Errorf("%w: %q is missing metrics: provide best-effort numeric estimates for rank, traffic, volume, kd and cpc for every keyword; estimates are planning guesses, never null", ErrEstimateInvalid, estimate.Keyword)
		}
	}
	return validated, nil
}

// ExpectedKeyword is one input phrase the model had to estimate.
type ExpectedKeyword struct {
	Keyword string
	Kind    string
}

// ValidateEstimateCoverage requires the model output to cover exactly the
// requested input set: same normalized phrases, same kinds, no extras, no
// duplicates, and never empty. Call it on the raw parsed output before
// ValidateEstimates, which only checks per-entry bounds.
func ValidateEstimateCoverage(estimates []Estimate, expected []ExpectedKeyword) error {
	if len(expected) == 0 {
		return fmt.Errorf("%w: no keywords requested", ErrEstimateInvalid)
	}
	if len(estimates) == 0 {
		return fmt.Errorf("%w: model returned no estimates", ErrEstimateInvalid)
	}
	want := make(map[string]string, len(expected))
	for _, phrase := range expected {
		display, err := projectkeywords.ValidateProjectKeywordPhrase(phrase.Keyword)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrEstimateInvalid, err)
		}
		if err := projectkeywords.ValidateProjectKeywordKind(phrase.Kind); err != nil {
			return fmt.Errorf("%w: %v", ErrEstimateInvalid, err)
		}
		want[projectkeywords.NormalizeProjectKeywordKey(display)] = phrase.Kind
	}
	seen := make(map[string]struct{}, len(estimates))
	for _, estimate := range estimates {
		display, err := projectkeywords.ValidateProjectKeywordPhrase(estimate.Keyword)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrEstimateInvalid, err)
		}
		key := projectkeywords.NormalizeProjectKeywordKey(display)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("%w: duplicate estimate for %q", ErrEstimateInvalid, display)
		}
		seen[key] = struct{}{}
		kind, ok := want[key]
		if !ok {
			return fmt.Errorf("%w: unexpected estimate for %q", ErrEstimateInvalid, display)
		}
		if estimate.Kind != kind {
			return fmt.Errorf("%w: %q changed kind to %q", ErrEstimateInvalid, display, estimate.Kind)
		}
	}
	for key := range want {
		if _, ok := seen[key]; !ok {
			return fmt.Errorf("%w: missing estimate (phrase key %q)", ErrEstimateInvalid, key)
		}
	}
	return nil
}

// ValidateEstimate checks one estimate: a real phrase, brand or non_brand
// kind, and finite in-bounds numerics.
func ValidateEstimate(estimate Estimate) (ValidatedEstimate, error) {
	if err := projectkeywords.ValidateProjectKeywordKind(estimate.Kind); err != nil {
		return ValidatedEstimate{}, fmt.Errorf("%w: %v", ErrEstimateInvalid, err)
	}
	display, err := projectkeywords.ValidateProjectKeywordPhrase(estimate.Keyword)
	if err != nil {
		return ValidatedEstimate{}, fmt.Errorf("%w: %v", ErrEstimateInvalid, err)
	}
	rank, err := validateRank(estimate.Rank)
	if err != nil {
		return ValidatedEstimate{}, err
	}
	traffic, err := validateCount("traffic", estimate.Traffic)
	if err != nil {
		return ValidatedEstimate{}, err
	}
	volume, err := validateCount("volume", estimate.Volume)
	if err != nil {
		return ValidatedEstimate{}, err
	}
	kd, err := validateKD(estimate.KD)
	if err != nil {
		return ValidatedEstimate{}, err
	}
	cpc, err := validateMoney("cpc", estimate.CPC)
	if err != nil {
		return ValidatedEstimate{}, err
	}
	resultsCount, err := validateCount("results_count", estimate.ResultsCount)
	if err != nil {
		return ValidatedEstimate{}, err
	}
	return ValidatedEstimate{
		Keyword: display, Key: projectkeywords.NormalizeProjectKeywordKey(display), Kind: estimate.Kind,
		Rank: rank, Traffic: traffic, Volume: volume, KD: kd, CPC: cpc, ResultsCount: resultsCount,
	}, nil
}

func validateRank(rank *int32) (pgtype.Int4, error) {
	if rank == nil {
		return pgtype.Int4{}, nil
	}
	if *rank < 1 || *rank > 100 {
		return pgtype.Int4{}, fmt.Errorf("%w: rank %d is outside 1..100", ErrEstimateInvalid, *rank)
	}
	return pgtype.Int4{Int32: *rank, Valid: true}, nil
}

func validateCount(name string, value *int64) (pgtype.Int8, error) {
	if value == nil {
		return pgtype.Int8{}, nil
	}
	if *value < 0 {
		return pgtype.Int8{}, fmt.Errorf("%w: %s %d is negative", ErrEstimateInvalid, name, *value)
	}
	return pgtype.Int8{Int64: *value, Valid: true}, nil
}

func validateKD(kd *float64) (pgtype.Float8, error) {
	if kd == nil {
		return pgtype.Float8{}, nil
	}
	// Range comparisons alone accept NaN (all false) and +Inf (only one
	// side false): reject non-finite values explicitly.
	if math.IsNaN(*kd) || math.IsInf(*kd, 0) {
		return pgtype.Float8{}, fmt.Errorf("%w: kd %v is not finite", ErrEstimateInvalid, *kd)
	}
	if *kd < 0 || *kd > 100 {
		return pgtype.Float8{}, fmt.Errorf("%w: kd %v is outside 0..100", ErrEstimateInvalid, *kd)
	}
	return pgtype.Float8{Float64: *kd, Valid: true}, nil
}

func validateMoney(name string, value *float64) (pgtype.Float8, error) {
	if value == nil {
		return pgtype.Float8{}, nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) {
		return pgtype.Float8{}, fmt.Errorf("%w: %s %v is not finite", ErrEstimateInvalid, name, *value)
	}
	if *value < 0 {
		return pgtype.Float8{}, fmt.Errorf("%w: %s %v is negative", ErrEstimateInvalid, name, *value)
	}
	return pgtype.Float8{Float64: *value, Valid: true}, nil
}

// UpsertProjectAnalyses replaces the stored estimates for the given phrases,
// leaving other phrases untouched.
func UpsertProjectAnalyses(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID, estimates []ValidatedEstimate) error {
	for _, estimate := range estimates {
		if _, err := queries.UpsertProjectKeywordAnalysis(ctx, sqlc.UpsertProjectKeywordAnalysisParams{
			ProjectID:         projectID,
			Keyword:           estimate.Keyword,
			NormalizedKeyword: estimate.Key,
			Kind:              estimate.Kind,
			Rank:              estimate.Rank,
			Traffic:           estimate.Traffic,
			Volume:            estimate.Volume,
			Kd:                estimate.KD,
			Cpc:               estimate.CPC,
			ResultsCount:      estimate.ResultsCount,
		}); err != nil {
			return fmt.Errorf("upsert project keyword analysis: %w", err)
		}
	}
	return nil
}

// UpsertCompetitorAnalyses replaces the stored estimates for the given
// phrases, leaving other phrases untouched.
func UpsertCompetitorAnalyses(ctx context.Context, queries *sqlc.Queries, competitorID pgtype.UUID, estimates []ValidatedEstimate) error {
	for _, estimate := range estimates {
		if _, err := queries.UpsertCompetitorKeywordAnalysis(ctx, sqlc.UpsertCompetitorKeywordAnalysisParams{
			CompetitorID:      competitorID,
			Keyword:           estimate.Keyword,
			NormalizedKeyword: estimate.Key,
			Kind:              estimate.Kind,
			Rank:              estimate.Rank,
			Traffic:           estimate.Traffic,
			Volume:            estimate.Volume,
			Kd:                estimate.KD,
			Cpc:               estimate.CPC,
			ResultsCount:      estimate.ResultsCount,
		}); err != nil {
			return fmt.Errorf("upsert competitor keyword analysis: %w", err)
		}
	}
	return nil
}

// ListProjectAnalyses returns the stored project estimates in kind order.
func ListProjectAnalyses(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID) ([]Analysis, error) {
	rows, err := queries.ListProjectKeywordAnalysisByProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project keyword analysis: %w", err)
	}
	out := make([]Analysis, 0, len(rows))
	for _, row := range rows {
		out = append(out, projectRowToAnalysis(row))
	}
	return out, nil
}

// ListCompetitorAnalyses returns the stored competitor estimates in kind order.
func ListCompetitorAnalyses(ctx context.Context, queries *sqlc.Queries, competitorID pgtype.UUID) ([]Analysis, error) {
	rows, err := queries.ListCompetitorKeywordAnalysisByCompetitor(ctx, competitorID)
	if err != nil {
		return nil, fmt.Errorf("list competitor keyword analysis: %w", err)
	}
	out := make([]Analysis, 0, len(rows))
	for _, row := range rows {
		out = append(out, Analysis{
			Keyword: row.Keyword, Kind: row.Kind,
			Rank: int4Ptr(row.Rank), Traffic: int8Ptr(row.Traffic), Volume: int8Ptr(row.Volume),
			KD: float8Ptr(row.Kd), CPC: float8Ptr(row.Cpc), ResultsCount: int8Ptr(row.ResultsCount),
		})
	}
	return out, nil
}

func projectRowToAnalysis(row sqlc.ProjectKeywordAnalysis) Analysis {
	return Analysis{
		Keyword: row.Keyword, Kind: row.Kind,
		Rank: int4Ptr(row.Rank), Traffic: int8Ptr(row.Traffic), Volume: int8Ptr(row.Volume),
		KD: float8Ptr(row.Kd), CPC: float8Ptr(row.Cpc), ResultsCount: int8Ptr(row.ResultsCount),
	}
}

func int4Ptr(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	out := value.Int32
	return &out
}

func int8Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

func float8Ptr(value pgtype.Float8) *float64 {
	if !value.Valid {
		return nil
	}
	out := value.Float64
	return &out
}
