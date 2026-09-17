package competitorcrawls

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

const competitorMaxDepth = 32

// EnqueueMissing creates queued competitor crawls for roster rows that do not
// yet have a child crawl for the given completed home crawl. Completed children
// are left alone, so a later "crawl this audit" only picks up newly added
// competitors. Failed or cancelled children are replaced so they can be retried.
func EnqueueMissing(ctx context.Context, queries *sqlc.Queries, parentCrawlID, requestedByUserID pgtype.UUID) ([]pgtype.UUID, error) {
	parent, err := queries.GetCrawlByID(ctx, parentCrawlID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if parent.Status != "completed" {
		return nil, nil
	}
	if !crawler.IsManualLikeSource(parent.Source) {
		return nil, nil
	}

	maxCompetitors, err := queries.GetOrganizationMaxCompetitorsByProjectID(ctx, parent.ProjectID)
	if err != nil {
		return nil, err
	}
	if maxCompetitors == 0 {
		return nil, nil
	}

	breakdownRow, err := queries.GetCrawlScoreBreakdownByCrawl(ctx, parentCrawlID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	var breakdown shared.ScoreBreakdownSnapshot
	if err := json.Unmarshal(breakdownRow.BreakdownJson, &breakdown); err != nil {
		return nil, err
	}
	if breakdown.TotalScoredPages <= 0 {
		return nil, nil
	}

	configSnapshot, err := competitorConfigSnapshot(parent.ConfigSnapshot, breakdown.TotalScoredPages)
	if err != nil {
		return nil, err
	}

	if err := queries.DeleteFailedCompetitorCrawlsForParent(ctx, parentCrawlID); err != nil {
		return nil, err
	}

	competitors, err := queries.ListProjectCompetitorsWithoutChildCrawl(ctx, sqlc.ListProjectCompetitorsWithoutChildCrawlParams{
		ProjectID:     parent.ProjectID,
		ParentCrawlID: parentCrawlID,
	})
	if err != nil {
		return nil, err
	}

	created := make([]pgtype.UUID, 0, len(competitors))
	for _, competitor := range competitors {
		crawl, err := queries.CreateCrawl(ctx, sqlc.CreateCrawlParams{
			ProjectID:         parent.ProjectID,
			RequestedByUserID: requestedByUserID,
			Source:            "competitor",
			Status:            "queued",
			ConfigSnapshot:    configSnapshot,
			StartedAt:         pgtype.Timestamptz{},
			CompetitorID:      competitor.ID,
			ParentCrawlID:     parentCrawlID,
		})
		if err != nil {
			if isParentCompetitorUniqueViolation(err) {
				continue
			}
			return created, err
		}
		created = append(created, crawl.ID)
	}

	return created, nil
}

func competitorConfigSnapshot(parentConfig []byte, maxPages int32) ([]byte, error) {
	parentSnapshot, _, err := crawler.NormalizeConfigSnapshot(parentConfig)
	if err != nil {
		parentSnapshot = crawler.CrawlConfigSnapshot{}
	}

	maxPagesInt := int(maxPages)
	snapshot := crawler.CrawlConfigSnapshot{
		MaxDepth:        competitorMaxDepth,
		MaxPages:        &maxPagesInt,
		SkipSitemapSeed: true,
		HonourRobotsTxt: parentSnapshot.HonourRobotsTxt,
	}
	if parentSnapshot.FetchTimeoutSeconds > 0 {
		snapshot.FetchTimeoutSeconds = parentSnapshot.FetchTimeoutSeconds
	}
	if parentSnapshot.RequestDelayMs != nil {
		snapshot.RequestDelayMs = parentSnapshot.RequestDelayMs
	}
	if parentSnapshot.RequestJitterMs != nil {
		snapshot.RequestJitterMs = parentSnapshot.RequestJitterMs
	}

	normalized, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	_, normalized, err = crawler.NormalizeConfigSnapshot(normalized)
	return normalized, err
}

func isParentCompetitorUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}
	return postgresError.Code == "23505" && postgresError.ConstraintName == "idx_crawls_parent_competitor"
}
