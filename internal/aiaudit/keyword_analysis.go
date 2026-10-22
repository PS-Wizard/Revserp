package aiaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywordanalysis"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

const (
	// keywordAnalysisPageLimit caps how many crawl pages one keyword job reads.
	// The child crawl already carries the parent crawl page budget as its
	// max_pages, so this is the read-side half of the same budget.
	keywordAnalysisPageLimit = 10
	// keywordAnalysisPageChars caps the visible text kept per page.
	keywordAnalysisPageChars = 2000
	// keywordAnalysisContextPageLimit caps the pages quoted as estimate context.
	keywordAnalysisContextPageLimit = 5
	// keywordAnalysisContextPageChars caps the visible text kept per context page.
	keywordAnalysisContextPageChars = 500

	keywordAnalysisDefaultModel = "deepseek-flash"
)

const keywordAnalysisPrompt = `You are a search visibility analyst producing best-effort planning estimates. For each keyword below, estimate all five search metrics as a JSON object. These are AI-generated planning estimates only: never measured search data, never from a keyword tool. Provide a best-effort numeric estimate for every field of every keyword; never use null, never omit a field, never use "--" or missing values.

Rules:
- Output one JSON array and nothing else: no commentary, no markdown fences.
- Every entry: {"keyword": "<exact phrase from the list>", "kind": "brand"|"non_brand", "rank": <1-100>, "traffic": <monthly visits>, "volume": <monthly searches>, "kd": <0-100 difficulty>, "cpc": <dollars>}.
- rank is a best-effort estimated position 1-100. kd stays in 0-100. All counts are non-negative integers. All money is non-negative. All five metrics are required for every keyword.
- Keep every keyword from the list exactly once (same spelling, same kind).`

const competitorKeywordGenerationPrompt = `You are a search visibility analyst. Read the crawled competitor pages below and list the competitor's keywords as JSON: {"brand_keywords": [...], "non_brand_keywords": [...]} and nothing else: no commentary, no markdown fences.

Rules:
- brand_keywords: up to 10 brand terms for this competitor: its brand name plus obvious brand variants found in the pages.
- non_brand_keywords: 5-10 category and need keywords with no brand terms, grounded in what the pages actually sell or describe.
- Both lists are required and non-empty. Never invent search metrics; this call only names keywords.`

// handleKeywordAnalysis generates AI metric estimates for the project's
// combined keywords: the user and Revserp-suggested phrases the bootstrap
// already stored. Success advances the setup; the worker finalizer owns the
// transition.
func (w *Worker) handleKeywordAnalysis(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	lists, err := projectkeywords.LoadProjectKeywordLists(ctx, w.queries, job.ProjectID)
	if err != nil {
		return fmt.Errorf("load project keywords: %w", err)
	}
	combined := lists.Combined
	if len(combined) == 0 {
		return errors.New("no project keywords to analyze")
	}

	profile, err := w.queries.GetProjectBusinessProfileByProjectID(ctx, job.ProjectID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("load business profile: %w", err)
	}
	baseURL, err := w.queries.GetProjectBaseURLByID(ctx, job.ProjectID)
	if err != nil {
		return fmt.Errorf("load project base URL: %w", err)
	}

	phrases := combinedPhrases(combined)
	estimates, err := requestKeywordEstimates(ctx, w.cfg.DeepSeekAPIKey, w.cfg.DeepSeekModel, projectAnalysisContext(profile.BrandName, baseURL, w.contextPages(ctx, job.ProjectID)), phrases)
	if err != nil {
		return err
	}
	validated, err := validateEstimateBatch(estimates, phrases)
	if err != nil {
		return err
	}
	return w.withKeywordTx(ctx, func(q *sqlc.Queries) error {
		return keywordanalysis.UpsertProjectAnalyses(ctx, q, job.ProjectID, validated)
	})
}

// handleCompetitorKeywordGeneration names the competitor's brand and non-brand
// keywords from its own crawl and stores them with NULL metrics for the
// analysis step to fill.
func (w *Worker) handleCompetitorKeywordGeneration(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	if !job.CompetitorID.Valid {
		return errors.New("competitor keyword generation job has no competitor")
	}
	competitor, err := w.queries.GetProjectCompetitorByID(ctx, job.CompetitorID)
	if err != nil {
		return fmt.Errorf("load competitor: %w", err)
	}
	setup, err := w.queries.GetCompetitorSetupByCompetitor(ctx, job.CompetitorID)
	if err != nil {
		return fmt.Errorf("load competitor setup: %w", err)
	}

	pages, err := w.queries.ListKeywordAnalysisPagesForCrawl(ctx, sqlc.ListKeywordAnalysisPagesForCrawlParams{
		CrawlID: setup.CrawlID,
		Limit:   keywordAnalysisPageLimit,
	})
	if err != nil {
		return fmt.Errorf("load competitor crawl pages: %w", err)
	}

	raw, err := keywordGenerationProvider(w.cfg.DeepSeekAPIKey, w.cfg.DeepSeekModel).GenerateText(ctx, competitorKeywordPrompt(competitor.Name, competitor.SeedUrl, pages))
	if err != nil {
		return fmt.Errorf("generate competitor keywords: %w", err)
	}
	brand, nonBrand, err := parseCompetitorKeywords(raw)
	if err != nil {
		return err
	}
	estimates := make([]keywordanalysis.Estimate, 0, len(brand)+len(nonBrand))
	for _, keyword := range brand {
		estimates = append(estimates, keywordanalysis.Estimate{Keyword: keyword, Kind: projectkeywords.ProjectKeywordKindBrand})
	}
	for _, keyword := range nonBrand {
		estimates = append(estimates, keywordanalysis.Estimate{Keyword: keyword, Kind: projectkeywords.ProjectKeywordKindNonBrand})
	}
	validated, err := keywordanalysis.ValidateEstimates(estimates)
	if err != nil {
		return fmt.Errorf("validate competitor keywords: %w", err)
	}
	return w.withKeywordTx(ctx, func(q *sqlc.Queries) error {
		if err := q.DeleteCompetitorKeywordAnalysisByCompetitor(ctx, job.CompetitorID); err != nil {
			return fmt.Errorf("clear competitor keywords: %w", err)
		}
		return keywordanalysis.UpsertCompetitorAnalyses(ctx, q, job.CompetitorID, validated)
	})
}

func (w *Worker) handleCompetitorKeywordAnalysis(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	if !job.CompetitorID.Valid {
		return errors.New("competitor keyword analysis job has no competitor")
	}
	rows, err := w.queries.ListCompetitorKeywordAnalysisByCompetitor(ctx, job.CompetitorID)
	if err != nil {
		return fmt.Errorf("load competitor keywords: %w", err)
	}
	if len(rows) == 0 {
		return errors.New("no competitor keywords to analyze")
	}

	competitor, err := w.queries.GetProjectCompetitorByID(ctx, job.CompetitorID)
	if err != nil {
		return fmt.Errorf("load competitor: %w", err)
	}
	setup, err := w.queries.GetCompetitorSetupByCompetitor(ctx, job.CompetitorID)
	if err != nil {
		return fmt.Errorf("load competitor setup: %w", err)
	}
	phrases := make([]keywordPhrase, 0, len(rows))
	for _, row := range rows {
		phrases = append(phrases, keywordPhrase{Keyword: row.Keyword, Kind: row.Kind})
	}
	estimates, err := requestKeywordEstimates(ctx, w.cfg.DeepSeekAPIKey, w.cfg.DeepSeekModel, competitorAnalysisContext(competitor.Name, competitor.SeedUrl, w.crawlPages(ctx, setup.CrawlID, keywordAnalysisContextPageLimit)), phrases)
	if err != nil {
		return err
	}
	validated, err := validateEstimateBatch(estimates, phrases)
	if err != nil {
		return err
	}
	return w.withKeywordTx(ctx, func(q *sqlc.Queries) error {
		return keywordanalysis.UpsertCompetitorAnalyses(ctx, q, job.CompetitorID, validated)
	})
}

func validateEstimateBatch(estimates []keywordanalysis.Estimate, phrases []keywordPhrase) ([]keywordanalysis.ValidatedEstimate, error) {
	expected := make([]keywordanalysis.ExpectedKeyword, 0, len(phrases))
	for _, phrase := range phrases {
		expected = append(expected, keywordanalysis.ExpectedKeyword{Keyword: phrase.Keyword, Kind: phrase.Kind})
	}
	if err := keywordanalysis.ValidateEstimateCoverage(estimates, expected); err != nil {
		return nil, fmt.Errorf("validate keyword estimate coverage: %w", err)
	}
	validated, err := keywordanalysis.ValidateCompleteEstimates(estimates)
	if err != nil {
		return nil, fmt.Errorf("validate keyword estimates: %w", err)
	}
	return validated, nil
}

func (w *Worker) withKeywordTx(ctx context.Context, fn func(q *sqlc.Queries) error) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin keyword batch: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(w.queries.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit keyword batch: %w", err)
	}
	return nil
}

func (w *Worker) contextPages(ctx context.Context, projectID pgtype.UUID) []sqlc.ListKeywordAnalysisPagesForCrawlRow {
	crawlID, err := w.queries.GetLatestCompletedCrawlForProject(ctx, projectID)
	if err != nil {
		return nil
	}
	return w.crawlPages(ctx, crawlID, keywordAnalysisContextPageLimit)
}

func (w *Worker) crawlPages(ctx context.Context, crawlID pgtype.UUID, limit int32) []sqlc.ListKeywordAnalysisPagesForCrawlRow {
	if !crawlID.Valid {
		return nil
	}
	pages, err := w.queries.ListKeywordAnalysisPagesForCrawl(ctx, sqlc.ListKeywordAnalysisPagesForCrawlParams{
		CrawlID: crawlID,
		Limit:   limit,
	})
	if err != nil {
		return nil
	}
	return pages
}

type keywordPhrase struct {
	Keyword string
	Kind    string
}

func combinedPhrases(combined []projectkeywords.CombinedKeyword) []keywordPhrase {
	out := make([]keywordPhrase, 0, len(combined))
	for _, keyword := range combined {
		out = append(out, keywordPhrase{Keyword: keyword.Keyword, Kind: keyword.Kind})
	}
	return out
}

func projectAnalysisContext(brandName, baseURL string, pages []sqlc.ListKeywordAnalysisPagesForCrawlRow) string {
	var builder strings.Builder
	brandName = strings.TrimSpace(brandName)
	if brandName == "" {
		brandName = "unknown brand"
	}
	builder.WriteString("Business brand: ")
	builder.WriteString(brandName)
	builder.WriteString("\nWebsite domain: ")
	builder.WriteString(strings.TrimSpace(baseURL))
	appendCrawlContext(&builder, pages)
	return builder.String()
}

func competitorAnalysisContext(name, seedURL string, pages []sqlc.ListKeywordAnalysisPagesForCrawlRow) string {
	var builder strings.Builder
	name = strings.TrimSpace(name)
	if name == "" {
		name = "unknown"
	}
	builder.WriteString("Competitor: ")
	builder.WriteString(name)
	builder.WriteString("\nWebsite domain: ")
	builder.WriteString(strings.TrimSpace(seedURL))
	appendCrawlContext(&builder, pages)
	return builder.String()
}

func appendCrawlContext(builder *strings.Builder, pages []sqlc.ListKeywordAnalysisPagesForCrawlRow) {
	if len(pages) == 0 {
		builder.WriteString("\nNo crawl pages are available; estimate from the name and domain.")
		return
	}
	builder.WriteString("\nCrawled pages (ground estimates in what the site sells):")
	for _, page := range pages {
		builder.WriteString("\n- ")
		builder.WriteString(page.Url)
		if page.Title.Valid && strings.TrimSpace(page.Title.String) != "" {
			builder.WriteString(" | ")
			builder.WriteString(strings.TrimSpace(page.Title.String))
		}
		if page.H1.Valid && strings.TrimSpace(page.H1.String) != "" {
			builder.WriteString(" | ")
			builder.WriteString(strings.TrimSpace(page.H1.String))
		}
		if page.VisibleText.Valid {
			text := strings.TrimSpace(page.VisibleText.String)
			if len(text) > keywordAnalysisContextPageChars {
				text = text[:keywordAnalysisContextPageChars]
			}
			if text != "" {
				builder.WriteString("\n  ")
				builder.WriteString(text)
			}
		}
	}
}

func requestKeywordEstimates(ctx context.Context, apiKey, model, businessContext string, phrases []keywordPhrase) ([]keywordanalysis.Estimate, error) {
	var builder strings.Builder
	builder.WriteString(keywordAnalysisPrompt)
	builder.WriteString("\n\n")
	builder.WriteString(businessContext)
	builder.WriteString("\n\nKeywords (return exactly ")
	builder.WriteString(strconv.Itoa(len(phrases)))
	builder.WriteString(" entries):\n")
	for _, phrase := range phrases {
		builder.WriteString("- [")
		builder.WriteString(phrase.Kind)
		builder.WriteString("] ")
		builder.WriteString(phrase.Keyword)
		builder.WriteString("\n")
	}
	raw, err := keywordGenerationProvider(apiKey, model).GenerateText(ctx, builder.String())
	if err != nil {
		return nil, fmt.Errorf("generate keyword estimates: %w", err)
	}
	return parseKeywordEstimates(raw)
}

func keywordGenerationProvider(apiKey, model string) ai.Provider {
	if strings.TrimSpace(model) == "" {
		model = keywordAnalysisDefaultModel
	}
	provider, err := ai.NewProvider(ai.ProviderConfig{Name: "deepseek", APIKey: apiKey, Model: model})
	if err != nil {
		return failingKeywordProvider{err: err}
	}
	return provider
}

type failingKeywordProvider struct{ err error }

func (f failingKeywordProvider) GenerateText(_ context.Context, _ string) (string, error) {
	return "", f.err
}

type rawKeywordEstimate struct {
	Keyword      string   `json:"keyword"`
	Kind         string   `json:"kind"`
	Rank         *int32   `json:"rank"`
	Traffic      *int64   `json:"traffic"`
	Volume       *int64   `json:"volume"`
	KD           *float64 `json:"kd"`
	CPC          *float64 `json:"cpc"`
	ResultsCount *int64   `json:"results_count"`
}

func parseKeywordEstimates(raw string) ([]keywordanalysis.Estimate, error) {
	var parsed []rawKeywordEstimate
	if err := json.Unmarshal([]byte(extractJSONArray(raw)), &parsed); err != nil {
		return nil, fmt.Errorf("parse keyword estimates: %w", err)
	}
	estimates := make([]keywordanalysis.Estimate, 0, len(parsed))
	for _, entry := range parsed {
		estimates = append(estimates, keywordanalysis.Estimate{
			Keyword: entry.Keyword, Kind: entry.Kind, Rank: entry.Rank,
			Traffic: entry.Traffic, Volume: entry.Volume, KD: entry.KD,
			CPC: entry.CPC, ResultsCount: entry.ResultsCount,
		})
	}
	return estimates, nil
}

type rawCompetitorKeywords struct {
	BrandKeywords    []string `json:"brand_keywords"`
	NonBrandKeywords []string `json:"non_brand_keywords"`
}

func parseCompetitorKeywords(raw string) (brand, nonBrand []string, err error) {
	var parsed rawCompetitorKeywords
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &parsed); err != nil {
		return nil, nil, fmt.Errorf("parse competitor keywords: %w", err)
	}
	return projectkeywords.NormalizeSuggestedProjectKeywords(parsed.BrandKeywords, parsed.NonBrandKeywords)
}

func competitorKeywordPrompt(name, seedURL string, pages []sqlc.ListKeywordAnalysisPagesForCrawlRow) string {
	var builder strings.Builder
	builder.WriteString(competitorKeywordGenerationPrompt)
	builder.WriteString("\n\nCompetitor: ")
	builder.WriteString(strings.TrimSpace(name))
	builder.WriteString("\nWebsite: ")
	builder.WriteString(strings.TrimSpace(seedURL))
	if len(pages) == 0 {
		builder.WriteString("\nNo crawl pages are available; infer from the name and URL.\n")
		return builder.String()
	}
	builder.WriteString("\n\nCrawled pages:\n")
	for _, page := range pages {
		builder.WriteString("\n--- ")
		builder.WriteString(page.Url)
		if page.Title.Valid && strings.TrimSpace(page.Title.String) != "" {
			builder.WriteString(" | ")
			builder.WriteString(strings.TrimSpace(page.Title.String))
		}
		if page.H1.Valid && strings.TrimSpace(page.H1.String) != "" {
			builder.WriteString("\nH1: ")
			builder.WriteString(strings.TrimSpace(page.H1.String))
		}
		if page.MetaDescription.Valid && strings.TrimSpace(page.MetaDescription.String) != "" {
			builder.WriteString("\nDescription: ")
			builder.WriteString(strings.TrimSpace(page.MetaDescription.String))
		}
		if page.VisibleText.Valid {
			text := strings.TrimSpace(page.VisibleText.String)
			if len(text) > keywordAnalysisPageChars {
				text = text[:keywordAnalysisPageChars]
			}
			if text != "" {
				builder.WriteString("\n")
				builder.WriteString(text)
			}
		}
	}
	return builder.String()
}

// extractJSONArray tolerates a fenced or prefixed model reply by slicing the
// outer array. extractJSONObject does the same for the outer object.
func extractJSONArray(raw string) string {
	trimmed := strings.TrimSpace(raw)
	start := strings.Index(trimmed, "[")
	end := strings.LastIndex(trimmed, "]")
	if start < 0 || end <= start {
		return trimmed
	}
	return trimmed[start : end+1]
}

func extractJSONObject(raw string) string {
	trimmed := strings.TrimSpace(raw)
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return trimmed
	}
	return trimmed[start : end+1]
}
