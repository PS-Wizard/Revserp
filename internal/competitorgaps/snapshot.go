package competitorgaps

import (
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

type graphPage struct {
	URL             string
	StatusCode      int32
	ContentType     string
	Soft404         bool
	FetchError      string
	Title           string
	MetaDescription string
	H1              string
	Canonical       string
	OGTags          []byte
	JSONLD          []byte
	WordCount       int
}

type graphLink struct {
	SourceURL string
	TargetURL string
}

type pageIssue struct {
	URL       string
	Pillar    string
	Bucket    string
	IssueType string
	Severity  string
}

type crawlSnapshot struct {
	ID         string
	SeedURL    string
	HasLlmsTxt pgtype.Bool
	PSIResults []byte
	Pages      []graphPage
	Links      []graphLink
	Issues     []pageIssue
}

func pagesFromRows(rows []sqlc.ListCrawlPagesForCrawlRow) []graphPage {
	pages := make([]graphPage, 0, len(rows))
	for _, row := range rows {
		pages = append(pages, graphPage{
			URL:             row.Url,
			StatusCode:      int32Value(row.StatusCode),
			ContentType:     textValue(row.ContentType),
			Soft404:         row.Soft404,
			FetchError:      textValue(row.FetchError),
			Title:           textValue(row.Title),
			MetaDescription: textValue(row.MetaDescription),
			H1:              textValue(row.H1),
			Canonical:       textValue(row.CanonicalUrl),
			OGTags:          row.OgTags,
			JSONLD:          row.JsonLd,
			WordCount:       int(int32Value(row.WordCount)),
		})
	}
	return pages
}

func linksFromRows(rows []sqlc.ListInternalCrawlLinksForCrawlRow) []graphLink {
	links := make([]graphLink, 0, len(rows))
	for _, row := range rows {
		links = append(links, graphLink{SourceURL: row.SourceUrl, TargetURL: row.TargetUrl})
	}
	return links
}

func issuesFromRows(rows []sqlc.ListCrawlIssuesForCrawlRow) []pageIssue {
	issues := make([]pageIssue, 0, len(rows))
	for _, row := range rows {
		issues = append(issues, pageIssue{
			URL:       row.Url,
			Pillar:    row.Pillar,
			Bucket:    row.Bucket,
			IssueType: row.IssueType,
			Severity:  row.Severity,
		})
	}
	return issues
}

func pageIsScoreable(page graphPage) bool {
	return shared.IsScoreablePage(shared.CrawlPageSignal{
		StatusCode:  page.StatusCode,
		ContentType: page.ContentType,
		Soft404:     page.Soft404,
		FetchError:  page.FetchError,
	})
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
