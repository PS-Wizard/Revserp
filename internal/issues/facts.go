package issues

import (
	"context"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func loadFacts(ctx context.Context, queries *sqlc.Queries, crawlID pgtype.UUID) ([]shared.PageFact, []shared.LinkFact, error) {
	crawlPages, err := queries.ListCrawlPagesForCrawl(ctx, sqlc.ListCrawlPagesForCrawlParams{CrawlID: crawlID, Limit: math.MaxInt32})
	if err != nil {
		return nil, nil, fmt.Errorf("list crawl pages: %w", err)
	}
	internalCrawlLinks, err := queries.ListInternalCrawlLinksForCrawl(ctx, crawlID)
	if err != nil {
		return nil, nil, fmt.Errorf("list internal crawl links: %w", err)
	}

	pageFacts := make([]shared.PageFact, 0, len(crawlPages))
	for _, crawlPage := range crawlPages {
		pageFacts = append(pageFacts, shared.PageFact{
			ID:                      crawlPage.ID,
			URL:                     crawlPage.Url,
			ContentType:             textValue(crawlPage.ContentType),
			Depth:                   int32Value(crawlPage.Depth),
			Title:                   textValue(crawlPage.Title),
			MetaDescription:         textValue(crawlPage.MetaDescription),
			Author:                  textValue(crawlPage.Author),
			H1:                      textValue(crawlPage.H1),
			H1Count:                 int32Value(crawlPage.H1Count),
			H2Count:                 int32Value(crawlPage.H2Count),
			WordCount:               int32Value(crawlPage.WordCount),
			VisibleText:             textValue(crawlPage.VisibleText),
			CanonicalURL:            textValue(crawlPage.CanonicalUrl),
			Viewport:                textValue(crawlPage.Viewport),
			Lang:                    textValue(crawlPage.Lang),
			Robots:                  textValue(crawlPage.Robots),
			StatusCode:              int32Value(crawlPage.StatusCode),
			SizeBytes:               int32Value(crawlPage.SizeBytes),
			ImageCount:              int32Value(crawlPage.ImageCount),
			ImagesWithoutAltCount:   int32Value(crawlPage.ImagesWithoutAltCount),
			ImagesWithoutDimensions: int32Value(crawlPage.ImagesWithoutDimensions),
			ExternalLinks:           int32Value(crawlPage.ExternalLinks),
			ResponseTimeMs:          int32Value(crawlPage.ResponseTimeMs),
			OGTags:                  crawlPage.OgTags,
			JSONLD:                  crawlPage.JsonLd,
			HeadingOutline:          crawlPage.HeadingOutline,
			ContentSHA256:           textValue(crawlPage.ContentSha256),
		})
	}
	linkFacts := make([]shared.LinkFact, 0, len(internalCrawlLinks))
	for _, internalCrawlLink := range internalCrawlLinks {
		linkFacts = append(linkFacts, shared.LinkFact{
			SourceURL:    internalCrawlLink.SourceUrl,
			TargetURL:    internalCrawlLink.TargetUrl,
			TargetStatus: int32Value(internalCrawlLink.TargetStatus),
		})
	}
	return pageFacts, linkFacts, nil
}

func persistPageContentFingerprints(ctx context.Context, queries *sqlc.Queries, pageFacts []shared.PageFact) error {
	for _, pageFact := range pageFacts {
		if err := queries.UpdateCrawlPageContentFingerprints(ctx, sqlc.UpdateCrawlPageContentFingerprintsParams{
			ID:            pageFact.ID,
			ContentSha256: nullableText(pageFact.ContentSHA256),
		}); err != nil {
			return fmt.Errorf("update crawl page content fingerprints for %q: %w", pageFact.URL, err)
		}
	}
	return nil
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func int32Value(value pgtype.Int4) int32 {
	if !value.Valid {
		return 0
	}
	return value.Int32
}

func nullableText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}
