package seo

import (
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

const (
	DuplicateIssueTypeExactDuplicateContent    = "exact_duplicate_content"
	DuplicateIssueTypeDuplicateTitle           = "duplicate_title"
	DuplicateIssueTypeDuplicateMetaDescription = "duplicate_meta_description"
)

// DuplicatePage identifies one persisted page in duplicate evidence.
type DuplicatePage struct {
	CrawlPageID pgtype.UUID
	URL         string
}

type DuplicateGroup struct {
	IssueType string
	Members   []DuplicatePage
}

// DuplicateEvidence holds structured duplicate evidence from one derivation.
type DuplicateEvidence struct {
	Groups []DuplicateGroup
}

func duplicatePages(pageFacts []shared.PageFact, indexes []int) []DuplicatePage {
	pages := make([]DuplicatePage, 0, len(indexes))
	for _, index := range indexes {
		pages = append(pages, DuplicatePage{CrawlPageID: pageFacts[index].ID, URL: pageFacts[index].URL})
	}
	return pages
}
