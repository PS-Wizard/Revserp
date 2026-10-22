package competitorcrawls

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// EnqueueOneCompetitorCrawl creates the queued competitor crawl for one roster
// row under a completed home crawl, reusing the parent crawl page budget. A
// failed or cancelled child is replaced so the initialization retry gets a
// clean crawl; an existing queued, running or completed child is returned
// untouched.
func EnqueueOneCompetitorCrawl(ctx context.Context, queries *sqlc.Queries, parentCrawlID, competitorID, requestedByUserID pgtype.UUID) (pgtype.UUID, error) {
	parent, err := queries.GetCrawlByID(ctx, parentCrawlID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, nil
		}
		return pgtype.UUID{}, err
	}
	if parent.Status != "completed" {
		return pgtype.UUID{}, nil
	}
	if !crawler.IsManualLikeSource(parent.Source) {
		return pgtype.UUID{}, nil
	}

	pageCount, err := queries.CountCrawlPagesForCrawl(ctx, parentCrawlID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, nil
		}
		return pgtype.UUID{}, err
	}
	if pageCount <= 0 {
		return pgtype.UUID{}, nil
	}

	configSnapshot, err := competitorConfigSnapshot(parent.ConfigSnapshot, int32(pageCount))
	if err != nil {
		return pgtype.UUID{}, err
	}

	if existing, err := queries.GetCompetitorChildCrawlForParent(ctx, sqlc.GetCompetitorChildCrawlForParentParams{
		ParentCrawlID: parentCrawlID,
		CompetitorID:  competitorID,
	}); err == nil {
		switch existing.Status {
		case "failed", "cancelled":
			if err := queries.DeleteFailedCompetitorCrawlForParent(ctx, sqlc.DeleteFailedCompetitorCrawlForParentParams{
				ParentCrawlID: parentCrawlID,
				CompetitorID:  competitorID,
			}); err != nil {
				return pgtype.UUID{}, err
			}
		default:
			return existing.ID, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, err
	}

	crawl, err := queries.CreateCrawl(ctx, sqlc.CreateCrawlParams{
		ProjectID:         parent.ProjectID,
		RequestedByUserID: requestedByUserID,
		Source:            "competitor",
		Status:            "queued",
		ConfigSnapshot:    configSnapshot,
		StartedAt:         pgtype.Timestamptz{},
		CompetitorID:      competitorID,
		ParentCrawlID:     parentCrawlID,
	})
	if err != nil {
		if isParentCompetitorUniqueViolation(err) {
			existing, getErr := queries.GetCompetitorChildCrawlForParent(ctx, sqlc.GetCompetitorChildCrawlForParentParams{
				ParentCrawlID: parentCrawlID,
				CompetitorID:  competitorID,
			})
			if getErr != nil {
				return pgtype.UUID{}, getErr
			}
			return existing.ID, nil
		}
		return pgtype.UUID{}, err
	}
	return crawl.ID, nil
}
