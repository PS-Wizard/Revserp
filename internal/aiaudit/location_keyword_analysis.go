package aiaudit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywordanalysis"
	"github.com/ps-wizard/revserp/internal/locationkeywords"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

// LocationKeywordAnalysisJobType is the ai_worker_jobs job type the setup
// worker enqueues for per-location AI keyword metric estimates.
const LocationKeywordAnalysisJobType = "location_keyword_analysis"

// ErrLocationKeywordsEmpty skips the setup estimates step instead of failing
// it: an empty location is normal (keywords arrive later via the UI or
// RevBot), never a setup failure.
var ErrLocationKeywordsEmpty = errors.New("no location keywords to analyze")

// handleLocationKeywordAnalysis generates AI metric estimates for the
// location's combined saved keywords, reusing the project estimate prompt
// with the location brand and market as context. Success advances the
// setup; the worker finalizer owns the transition.
func (w *Worker) handleLocationKeywordAnalysis(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	locationID, ok := locationSetupScope(job)
	if !ok {
		return fmt.Errorf("location keyword analysis job %s has no location scope", job.ID.String())
	}
	rows, err := locationkeywords.LoadStoredKeywords(ctx, w.pool, locationID)
	if err != nil {
		return fmt.Errorf("load location keywords: %w", err)
	}
	phrases := locationKeywordPhrases(rows)
	if len(phrases) == 0 {
		return ErrLocationKeywordsEmpty
	}
	// Location lists carry no count cap, but one estimate batch does: keep
	// the first stored phrases so an enthusiastic list never fails setup.
	// The RevBot estimate tool backfills the rest on demand.
	if len(phrases) > keywordanalysis.MaxEstimatesPerScope {
		phrases = phrases[:keywordanalysis.MaxEstimatesPerScope]
	}
	brand := w.locationEstimateBrand(ctx, locationID, job.ProjectID)
	target, err := w.locationTargetReader().GetLocationTarget(ctx, locationID, job.ProjectID)
	if err != nil {
		return fmt.Errorf("load location target: %w", err)
	}
	estimates, err := requestKeywordEstimates(ctx, w.cfg.DeepSeekAPIKey, w.cfg.DeepSeekModel,
		locationAnalysisContext(brand, target, w.contextPages(ctx, job.ProjectID)), phrases)
	if err != nil {
		return err
	}
	validated, err := validateEstimateBatch(estimates, phrases)
	if err != nil {
		return err
	}
	return w.withKeywordTx(ctx, func(q *sqlc.Queries) error {
		return keywordanalysis.UpsertLocationAnalyses(ctx, q, locationID, validated)
	})
}

// locationEstimateBrand reads the location brand copy, falling back to the
// parent profile brand. Estimates are best-effort, so a missing profile
// degrades the context instead of failing the job.
func (w *Worker) locationEstimateBrand(ctx context.Context, locationID, projectID pgtype.UUID) string {
	if profile, err := w.locationProfileReader().GetLocationBusinessProfile(ctx, locationID, projectID); err == nil {
		if brand := strings.TrimSpace(profile.BrandName); brand != "" {
			return brand
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	parent, err := w.queries.GetProjectBusinessProfileByProjectID(ctx, projectID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(parent.BrandName)
}

func locationAnalysisContext(brand string, target LocationTarget, pages []sqlc.ListKeywordAnalysisPagesForCrawlRow) string {
	var builder strings.Builder
	if strings.TrimSpace(brand) == "" {
		brand = "unknown brand"
	}
	builder.WriteString("Business brand: ")
	builder.WriteString(strings.TrimSpace(brand))
	locality := strings.TrimSpace(target.Locality)
	if locality == "" {
		locality = strings.TrimSpace(target.Address)
	}
	if locality == "" {
		locality = "unknown locality"
	}
	builder.WriteString("\nLocation market (local SEO; ground local-intent estimates in this market, not the parent market): ")
	builder.WriteString(locality)
	appendCrawlContext(&builder, pages)
	return builder.String()
}

// locationKeywordPhrases dedupes stored location keywords by normalized key,
// first stored row wins, mirroring the combined coverage union.
func locationKeywordPhrases(rows []locationkeywords.StoredKeyword) []keywordPhrase {
	seen := make(map[string]struct{}, len(rows))
	out := make([]keywordPhrase, 0, len(rows))
	for _, row := range rows {
		key := projectkeywords.NormalizeProjectKeywordKey(row.Normalized)
		if key == "" {
			key = projectkeywords.NormalizeProjectKeywordKey(row.Keyword)
		}
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, keywordPhrase{Keyword: row.Keyword, Kind: row.Kind})
	}
	return out
}
