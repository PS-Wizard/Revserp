package competitorgaps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueengine "github.com/ps-wizard/revserp/internal/issues"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

var (
	ErrNotCompetitorCrawl = errors.New("crawl is not a competitor crawl")
	ErrParentMismatch     = errors.New("parent crawl does not match competitor parent_crawl_id")
	ErrParentNotReady     = errors.New("parent crawl is missing or not completed")
)

func Build(ctx context.Context, queries *sqlc.Queries, parentCrawlID, competitorCrawlID pgtype.UUID) (Report, error) {
	competitor, err := queries.GetCrawlGapContext(ctx, competitorCrawlID)
	if err != nil {
		return Report{}, fmt.Errorf("get competitor crawl: %w", err)
	}
	if competitor.Source != "competitor" {
		return Report{}, ErrNotCompetitorCrawl
	}
	if !competitor.ParentCrawlID.Valid {
		return Report{}, ErrParentNotReady
	}
	if competitor.ParentCrawlID != parentCrawlID {
		return Report{}, ErrParentMismatch
	}

	parent, err := queries.GetCrawlGapContext(ctx, parentCrawlID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Report{}, ErrParentNotReady
		}
		return Report{}, fmt.Errorf("get parent crawl: %w", err)
	}
	if parent.Status != "completed" || (parent.Source != "manual" && parent.Source != "auto") {
		return Report{}, ErrParentNotReady
	}

	parentSnapshot, err := loadSnapshot(ctx, queries, parent)
	if err != nil {
		return Report{}, fmt.Errorf("load parent crawl snapshot: %w", err)
	}
	competitorSnapshot, err := loadSnapshot(ctx, queries, competitor)
	if err != nil {
		return Report{}, fmt.Errorf("load competitor crawl snapshot: %w", err)
	}

	scoringConfig, err := issueengine.LoadActiveScoringConfig(ctx, queries)
	if err != nil {
		return Report{}, fmt.Errorf("load scoring config: %w", err)
	}

	return buildReportWithConfig(parentSnapshot, competitorSnapshot, scoringConfig), nil
}

func Save(ctx context.Context, queries *sqlc.Queries, parentCrawlID, competitorCrawlID pgtype.UUID, report Report) error {
	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshal competitor gap report: %w", err)
	}
	if err := queries.UpsertCompetitorGapReport(ctx, sqlc.UpsertCompetitorGapReportParams{
		ParentCrawlID:     parentCrawlID,
		CompetitorCrawlID: competitorCrawlID,
		Version:           ReportVersion,
		ReportJson:        payload,
	}); err != nil {
		return fmt.Errorf("upsert competitor gap report: %w", err)
	}
	return nil
}

func Load(ctx context.Context, queries *sqlc.Queries, competitorCrawlID pgtype.UUID) (Report, error) {
	row, err := queries.GetCompetitorGapReportByCompetitorCrawlID(ctx, competitorCrawlID)
	if err != nil {
		return Report{}, err
	}
	var report Report
	if err := json.Unmarshal(row.ReportJson, &report); err != nil {
		return Report{}, fmt.Errorf("unmarshal competitor gap report: %w", err)
	}
	return report, nil
}

func BuildAndSave(ctx context.Context, queries *sqlc.Queries, parentCrawlID, competitorCrawlID pgtype.UUID) (Report, error) {
	report, err := Build(ctx, queries, parentCrawlID, competitorCrawlID)
	if err != nil {
		return Report{}, err
	}
	if err := Save(ctx, queries, parentCrawlID, competitorCrawlID, report); err != nil {
		return Report{}, err
	}
	return report, nil
}

func loadSnapshot(ctx context.Context, queries *sqlc.Queries, crawl sqlc.GetCrawlGapContextRow) (crawlSnapshot, error) {
	pages, err := queries.ListCrawlPagesForCrawl(ctx, crawl.ID)
	if err != nil {
		return crawlSnapshot{}, fmt.Errorf("list crawl pages: %w", err)
	}
	links, err := queries.ListInternalCrawlLinksForCrawl(ctx, crawl.ID)
	if err != nil {
		return crawlSnapshot{}, fmt.Errorf("list internal crawl links: %w", err)
	}
	issues, err := queries.ListCrawlIssuesForCrawl(ctx, crawl.ID)
	if err != nil {
		return crawlSnapshot{}, fmt.Errorf("list crawl issues: %w", err)
	}
	return crawlSnapshot{
		ID:         crawl.ID.String(),
		SeedURL:    crawl.SeedUrl,
		HasLlmsTxt: crawl.HasLlmsTxt,
		PSIResults: crawl.GooglePsiResults,
		Pages:      pagesFromRows(pages),
		Links:      linksFromRows(links),
		Issues:     issuesFromRows(issues),
	}, nil
}

func buildReport(parent, competitor crawlSnapshot) Report {
	return buildReportWithConfig(parent, competitor, issueengine.DefaultScoringConfig())
}

func buildReportWithConfig(parent, competitor crawlSnapshot, scoringConfig shared.ScoringConfig) Report {
	competitorHops := hopsFromHome(competitor.Pages, competitor.Links, competitor.SeedURL)
	radius := maxHop(competitorHops)
	if len(competitorHops) == 0 {
		radius = 0
	}

	parentHops := hopsFromHome(parent.Pages, parent.Links, parent.SeedURL)
	parentSlice := slicePages(parent.Pages, parentHops, radius)
	competitorSlice := slicePages(competitor.Pages, competitorHops, radius)

	return Report{
		Version:       ReportVersion,
		Radius:        radius,
		YourPages:     len(parentSlice),
		TheirPages:    len(competitorSlice),
		YouBreakdown:  scoreSlice(parent.ID, parentSlice, parent.Issues, parent.PSIResults, scoringConfig),
		ThemBreakdown: scoreSlice(competitor.ID, competitorSlice, competitor.Issues, competitor.PSIResults, scoringConfig),
		YourSlice:     slicePageRows(parentSlice, parentHops),
		TheirSlice:    slicePageRows(competitorSlice, competitorHops),
		Issues:        issueCoverageRows(parentSlice, competitorSlice, parent.Issues, competitor.Issues),
		Spread:        spreadRows(parentSlice, competitorSlice, parent.Issues, competitor.Issues),
		PageHealth: PageHealthCompare{
			You:  pageHealthSide(parentSlice, parent.Issues),
			Them: pageHealthSide(competitorSlice, competitor.Issues),
		},
		Presence: presenceRows(parent.Issues, competitor.Issues, parent.HasLlmsTxt, competitor.HasLlmsTxt),
		PSI: PSICompare{
			You:  psiMetricsFromResults(parent.PSIResults),
			Them: psiMetricsFromResults(competitor.PSIResults),
		},
		Homepage: HomepageCompare{
			You:  homepageSignals(parent.Pages),
			Them: homepageSignals(competitor.Pages),
		},
		Content: ContentCompare{
			You:  contentSignals(parentSlice),
			Them: contentSignals(competitorSlice),
		},
	}
}
