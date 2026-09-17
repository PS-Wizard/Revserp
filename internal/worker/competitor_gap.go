package worker

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/competitorgaps"
)

// saveCompetitorGapReport builds the v1 gap snapshot after a competitor crawl
// completes. Failures are logged only; the crawl stays completed.
func (w *Worker) saveCompetitorGapReport(ctx context.Context, competitorCrawlID pgtype.UUID) {
	gapCtx, err := w.queries.GetCrawlGapContext(ctx, competitorCrawlID)
	if err != nil {
		log.Printf("competitor gap report: load crawl context failed: crawl_id=%s error=%v", competitorCrawlID.String(), err)
		return
	}
	if !gapCtx.ParentCrawlID.Valid {
		return
	}
	if _, err := competitorgaps.BuildAndSave(ctx, w.queries, gapCtx.ParentCrawlID, competitorCrawlID); err != nil {
		log.Printf("competitor gap report: build failed: crawl_id=%s error=%v", competitorCrawlID.String(), err)
	}
}
