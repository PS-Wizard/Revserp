package seo

import (
	"github.com/jackc/pgx/v5/pgtype"
	"sort"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// Duplicate issue types that carry structured group or relationship evidence.
const (
	DuplicateIssueTypeExactDuplicateContent    = "exact_duplicate_content"
	DuplicateIssueTypeNearDuplicateContent     = "near_duplicate_content"
	DuplicateIssueTypeDuplicateTitle           = "duplicate_title"
	DuplicateIssueTypeDuplicateMetaDescription = "duplicate_meta_description"
)

// DuplicatePage identifies one persisted page in duplicate evidence.
type DuplicatePage struct {
	CrawlPageID pgtype.UUID
	URL         string
}

// DuplicateGroup is one exact duplicate group detected during derivation.
type DuplicateGroup struct {
	IssueType string
	Members   []DuplicatePage
}

// DuplicateRelation is one near-duplicate page pair.
type DuplicateRelation struct {
	IssueType  string
	LeftPage   DuplicatePage
	RightPage  DuplicatePage
	Similarity float64
}

// DuplicateEvidence holds structured duplicate evidence from one derivation.
type DuplicateEvidence struct {
	Groups    []DuplicateGroup
	Relations []DuplicateRelation
}

func duplicatePages(pageFacts []shared.PageFact, indexes []int) []DuplicatePage {
	pages := make([]DuplicatePage, 0, len(indexes))
	for _, index := range indexes {
		pages = append(pages, DuplicatePage{CrawlPageID: pageFacts[index].ID, URL: pageFacts[index].URL})
	}
	return pages
}

func buildNearDuplicateGroups(pageFacts []shared.PageFact, neighbors map[int]map[int]struct{}) []DuplicateGroup {
	visited := make(map[int]bool, len(neighbors))
	groups := make([]DuplicateGroup, 0)
	starts := make([]int, 0, len(neighbors))
	for index := range neighbors {
		starts = append(starts, index)
	}
	sort.Ints(starts)
	for _, start := range starts {
		if visited[start] {
			continue
		}
		stack := []int{start}
		visited[start] = true
		indexes := make([]int, 0)
		for len(stack) > 0 {
			index := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			indexes = append(indexes, index)
			for neighbor := range neighbors[index] {
				if !visited[neighbor] {
					visited[neighbor] = true
					stack = append(stack, neighbor)
				}
			}
		}
		sort.Ints(indexes)
		if len(indexes) > 1 {
			groups = append(groups, DuplicateGroup{IssueType: DuplicateIssueTypeNearDuplicateContent, Members: duplicatePages(pageFacts, indexes)})
		}
	}
	return groups
}
