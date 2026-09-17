package issues

import (
	"encoding/json"
	"slices"
	"sort"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// psiCoreWebVitalsBucket is the PageSpeed bucket whose score is overridden by
// the stored Google PSI mobile performance score instead of issue penalties.
const psiCoreWebVitalsBucket = "psi_cwv"

// recommendedPotentialDelta is the minimum overall gain (points) for a bucket
// to count as a meaningful, recommended fix in the combined scenario.
const recommendedPotentialDelta = int32(1)

// Scores holds one set of crawl scores.
type Scores struct {
	Overall   int32
	SEO       int32
	AEO       int32
	PageSpeed int32
}

// BucketPotential is the opportunity of fixing one bucket completely.
type BucketPotential struct {
	Bucket string
	Pillar string
	Scores Scores
	Delta  Scores
}

// PotentialScenario is one combined hypothetical: a set of buckets treated as
// fixed at the same time.
type PotentialScenario struct {
	Buckets []string
	Scores  Scores
	Delta   Scores
}

// PotentialResult is the full set of hypothetical scores for one crawl.
type PotentialResult struct {
	Current       Scores
	Opportunities []BucketPotential
	Best          PotentialScenario
	Top3          PotentialScenario
	Recommended   PotentialScenario
}

// ComputePotential reruns the real scorer (BuildScoreBreakdownWithConfig) with
// selected buckets' issue signals removed. Every score and delta comes from
// that rerun — nothing is added up by hand, because soft-sum scoring is
// non-additive. The baseline is the crawl scored with nothing removed, which
// must equal the stored score when the scoring config is unchanged.
func ComputePotential(pages []shared.CrawlPageSignal, crawlIssues []shared.CrawlIssueSignal, config shared.ScoringConfig, psi *shared.GooglePSIScoreInput) PotentialResult {
	baseline := computeScores(pages, crawlIssues, config, psi)
	result := PotentialResult{Current: baseline}

	for _, ref := range bucketRefs(config) {
		scores := computeScores(pages, withoutBucket(crawlIssues, ref.bucket), config, simulatedFixedPSI(ref.bucket, psi))
		result.Opportunities = append(result.Opportunities, BucketPotential{
			Bucket: ref.bucket,
			Pillar: ref.pillar,
			Scores: scores,
			Delta:  subtractScores(scores, baseline),
		})
	}

	sort.Slice(result.Opportunities, func(i, j int) bool {
		left, right := result.Opportunities[i], result.Opportunities[j]
		if left.Delta.Overall != right.Delta.Overall {
			return left.Delta.Overall > right.Delta.Overall
		}
		if left.Pillar != right.Pillar {
			return left.Pillar < right.Pillar
		}
		return left.Bucket < right.Bucket
	})

	result.Best = combinedPotential(bucketIDsOf(result.Opportunities[:min(len(result.Opportunities), 1)]), pages, crawlIssues, config, psi, baseline)
	result.Top3 = combinedPotential(bucketIDsOf(result.Opportunities[:min(len(result.Opportunities), 3)]), pages, crawlIssues, config, psi, baseline)

	var recommended []string
	for _, opportunity := range result.Opportunities {
		if opportunity.Delta.Overall >= recommendedPotentialDelta {
			recommended = append(recommended, opportunity.Bucket)
		}
	}
	result.Recommended = combinedPotential(recommended, pages, crawlIssues, config, psi, baseline)
	return result
}

type bucketRef struct{ bucket, pillar string }

// bucketRefs lists every bucket in the active config in a deterministic order.
func bucketRefs(config shared.ScoringConfig) []bucketRef {
	var refs []bucketRef
	for pillarID, pillarConfig := range config.Pillars {
		for _, bucketID := range shared.SortedBucketIDs(pillarConfig.BucketWeights) {
			refs = append(refs, bucketRef{bucketID, pillarID})
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].bucket != refs[j].bucket {
			return refs[i].bucket < refs[j].bucket
		}
		return refs[i].pillar < refs[j].pillar
	})
	return refs
}

// computeScores runs one scoring pass through the real scorer.
func computeScores(pages []shared.CrawlPageSignal, crawlIssues []shared.CrawlIssueSignal, config shared.ScoringConfig, psi *shared.GooglePSIScoreInput) Scores {
	breakdown := BuildScoreBreakdownWithConfig("potential", pages, crawlIssues, config, psi)
	crawlScores := breakdown.CrawlScores()
	return Scores{
		Overall:   crawlScores.OverallScore,
		SEO:       crawlScores.SEOScore,
		AEO:       crawlScores.AEOScore,
		PageSpeed: crawlScores.PageSpeedScore,
	}
}

// simulatedFixedPSI is the PSI input to use when one bucket is treated as
// fixed. The PSI bucket's score is the stored Google PSI mobile performance
// score, so removing its issues alone cannot move it — pass a hypothetical
// perfect score instead. Every other bucket keeps the real stored PSI input so
// the baseline matches the live crawl path.
func simulatedFixedPSI(bucketID string, psi *shared.GooglePSIScoreInput) *shared.GooglePSIScoreInput {
	if bucketID != psiCoreWebVitalsBucket {
		return psi
	}
	perfect := 100
	return &shared.GooglePSIScoreInput{MobilePerformanceScore: &perfect}
}

// combinedPotential builds one combined scenario: all selected buckets removed
// and scored in a single rerun.
func combinedPotential(bucketIDs []string, pages []shared.CrawlPageSignal, crawlIssues []shared.CrawlIssueSignal, config shared.ScoringConfig, psi *shared.GooglePSIScoreInput, baseline Scores) PotentialScenario {
	bucketIDs = uniqueSortedCopy(bucketIDs)
	simulatedPSI := psi
	for _, bucketID := range bucketIDs {
		if bucketID == psiCoreWebVitalsBucket {
			simulatedPSI = simulatedFixedPSI(bucketID, psi)
			break
		}
	}
	scores := computeScores(pages, withoutBuckets(crawlIssues, bucketIDs), config, simulatedPSI)
	return PotentialScenario{
		Buckets: bucketIDs,
		Scores:  scores,
		Delta:   subtractScores(scores, baseline),
	}
}

func withoutBucket(crawlIssues []shared.CrawlIssueSignal, bucketID string) []shared.CrawlIssueSignal {
	filtered := make([]shared.CrawlIssueSignal, 0, len(crawlIssues))
	for _, issue := range crawlIssues {
		if issue.Bucket != bucketID {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

func withoutBuckets(crawlIssues []shared.CrawlIssueSignal, bucketIDs []string) []shared.CrawlIssueSignal {
	removed := make(map[string]struct{}, len(bucketIDs))
	for _, bucketID := range bucketIDs {
		removed[bucketID] = struct{}{}
	}
	filtered := make([]shared.CrawlIssueSignal, 0, len(crawlIssues))
	for _, issue := range crawlIssues {
		if _, ok := removed[issue.Bucket]; !ok {
			filtered = append(filtered, issue)
		}
	}
	return filtered
}

func bucketIDsOf(opportunities []BucketPotential) []string {
	ids := make([]string, 0, len(opportunities))
	for _, opportunity := range opportunities {
		ids = append(ids, opportunity.Bucket)
	}
	return ids
}

func uniqueSortedCopy(values []string) []string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	return slices.Compact(values)
}

func subtractScores(fixed, baseline Scores) Scores {
	return Scores{
		Overall:   fixed.Overall - baseline.Overall,
		SEO:       fixed.SEO - baseline.SEO,
		AEO:       fixed.AEO - baseline.AEO,
		PageSpeed: fixed.PageSpeed - baseline.PageSpeed,
	}
}

// PageSignalsFromRows converts the narrow scoring-signal page rows into score
// inputs. Only the fields the score calculation actually reads are loaded; the
// heavy page text (visible text, headings, JSON-LD) is never fetched.
func PageSignalsFromRows(rows []sqlc.ListCrawlPageSignalsForCrawlRow) []shared.CrawlPageSignal {
	signals := make([]shared.CrawlPageSignal, 0, len(rows))
	for _, row := range rows {
		signals = append(signals, shared.CrawlPageSignal{
			URL:            row.Url,
			StatusCode:     int32Value(row.StatusCode),
			ContentType:    textValue(row.ContentType),
			WordCount:      int32Value(row.WordCount),
			ResponseTimeMs: int32Value(row.ResponseTimeMs),
			SizeBytes:      int32Value(row.SizeBytes),
			Soft404:        row.Soft404,
			FetchError:     textValue(row.FetchError),
		})
	}
	return signals
}

// IssueSignalsFromRows converts persisted issue rows into score inputs.
func IssueSignalsFromRows(rows []sqlc.ListCrawlIssuesForCrawlRow) []shared.CrawlIssueSignal {
	signals := make([]shared.CrawlIssueSignal, 0, len(rows))
	for _, row := range rows {
		signals = append(signals, shared.CrawlIssueSignal{
			URL:       row.Url,
			Pillar:    row.Pillar,
			Bucket:    row.Bucket,
			Severity:  row.Severity,
			IssueType: row.IssueType,
			Message:   row.Message,
			Details:   row.Details,
		})
	}
	return signals
}

// ParseStoredGooglePSI reconstructs the PSI scoring input from the crawl's
// stored google_psi_results JSON array, matching how the live scoring path
// builds it. Returns nil when nothing usable is stored.
func ParseStoredGooglePSI(raw []byte) *shared.GooglePSIScoreInput {
	if len(raw) == 0 {
		return nil
	}
	var stored []struct {
		Mobile struct {
			Success          bool `json:"success"`
			PerformanceScore *int `json:"performance_score,omitempty"`
		} `json:"mobile"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil
	}
	for _, entry := range stored {
		if entry.Mobile.Success && entry.Mobile.PerformanceScore != nil {
			score := *entry.Mobile.PerformanceScore
			return &shared.GooglePSIScoreInput{MobilePerformanceScore: &score}
		}
	}
	return nil
}
