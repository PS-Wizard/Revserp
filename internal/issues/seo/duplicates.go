package seo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

const (
	deriveCtxCheckPages = 256
	deriveCtxCheckLinks = 4096
	deriveCtxCheckPairs = 1024
	trigramCtxCheckSize = 65536
)

var nearDuplicateStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {},
	"for": {}, "from": {}, "in": {}, "is": {}, "it": {}, "of": {}, "on": {}, "or": {},
	"our": {}, "that": {}, "the": {}, "their": {}, "this": {}, "to": {}, "we": {},
	"with": {}, "you": {}, "your": {},
}

// EnrichPageFactsWithContentFingerprints computes normalized content hashes for duplicate detection.
func EnrichPageFactsWithContentFingerprints(pageFacts []shared.PageFact) {
	_ = EnrichPageFactsWithContentFingerprintsWithContext(context.Background(), pageFacts)
}

func EnrichPageFactsWithContentFingerprintsWithContext(ctx context.Context, pageFacts []shared.PageFact) error {
	for pageFactIndex := range pageFacts {
		if pageFactIndex%deriveCtxCheckPages == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		normalizedContent := normalizeDuplicateContent(pageFacts[pageFactIndex])
		if normalizedContent == "" {
			pageFacts[pageFactIndex].ContentSHA256 = ""
			continue
		}
		pageFacts[pageFactIndex].ContentSHA256 = hashNormalizedContentSHA256(normalizedContent)
	}
	return nil
}

// deriveDuplicateContentIssuesWithEvidence behaves like deriveDuplicateContentIssues but also returns the structured evidence behind the issues.
func deriveDuplicateContentIssuesWithEvidence(ctx context.Context, pageFacts []shared.PageFact) ([]shared.DerivedIssue, DuplicateEvidence, error) {
	var derivedIssues []shared.DerivedIssue
	var evidence DuplicateEvidence
	exactDuplicateGroupsByHash := groupExactDuplicatePagesByHash(pageFacts)
	contentHashes := make([]string, 0, len(exactDuplicateGroupsByHash))
	for contentHash := range exactDuplicateGroupsByHash {
		contentHashes = append(contentHashes, contentHash)
	}
	sort.Strings(contentHashes)
	for _, contentHash := range contentHashes {
		if err := ctx.Err(); err != nil {
			return nil, DuplicateEvidence{}, err
		}
		pageIndexes := exactDuplicateGroupsByHash[contentHash]
		if len(pageIndexes) < 2 {
			continue
		}
		evidence.Groups = append(evidence.Groups, DuplicateGroup{IssueType: DuplicateIssueTypeExactDuplicateContent, Members: duplicatePages(pageFacts, pageIndexes)})
		for _, pageIndex := range pageIndexes {
			matchingURLs := collectOtherDuplicateURLs(pageFacts, pageIndexes, pageIndex)
			derivedIssues = append(derivedIssues, newIssue(
				pageFacts[pageIndex],
				"content_quality",
				"exact_duplicate_content",
				"high",
				"Page has exact duplicate content",
				fmt.Sprintf("Normalized page content exactly matches %d other page(s): %s.", len(matchingURLs), strings.Join(matchingURLs, ", ")),
			))
		}
	}
	nearDuplicateIssues, nearDuplicateEvidence, err := deriveNearDuplicateIssuesWithEvidence(ctx, pageFacts)
	if err != nil {
		return nil, DuplicateEvidence{}, err
	}
	derivedIssues = append(derivedIssues, nearDuplicateIssues...)
	evidence.Relations = append(evidence.Relations, nearDuplicateEvidence.Relations...)
	return derivedIssues, evidence, nil
}

func normalizeDuplicateContent(pageFact shared.PageFact) string {
	// Extraction-failed bodies are unreliable: hashing the remaining metadata
	// would group unrelated failed pages as fake exact body duplicates.
	if pageFact.ContentExtractionFailed() {
		return ""
	}
	normalizedFields := []string{
		normalizeDuplicateContentField(pageFact.Title),
		normalizeDuplicateContentField(pageFact.MetaDescription),
		normalizeDuplicateContentField(pageFact.H1),
		normalizeDuplicateContentField(pageFact.VisibleText),
	}
	var nonEmptyFields []string
	for _, normalizedField := range normalizedFields {
		if normalizedField == "" {
			continue
		}
		nonEmptyFields = append(nonEmptyFields, normalizedField)
	}
	return strings.Join(nonEmptyFields, "\n")
}

func normalizeDuplicateContentField(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func hashNormalizedContentSHA256(normalizedContent string) string {
	contentHash := sha256.Sum256([]byte(normalizedContent))
	return hex.EncodeToString(contentHash[:])
}

func groupExactDuplicatePagesByHash(pageFacts []shared.PageFact) map[string][]int {
	exactDuplicateGroupsByHash := make(map[string][]int)
	for pageFactIndex, pageFact := range pageFacts {
		if pageFact.ContentSHA256 == "" {
			continue
		}
		exactDuplicateGroupsByHash[pageFact.ContentSHA256] = append(exactDuplicateGroupsByHash[pageFact.ContentSHA256], pageFactIndex)
	}
	return exactDuplicateGroupsByHash
}

func deriveNearDuplicateIssuesWithEvidence(ctx context.Context, pageFacts []shared.PageFact) ([]shared.DerivedIssue, DuplicateEvidence, error) {
	matchingPairs, err := runGaoyaNearDuplicateDetection(ctx, pageFacts)
	if err != nil {
		return nil, DuplicateEvidence{}, err
	}

	var evidence DuplicateEvidence
	nearDuplicateNeighborsByPageIndex := make(map[int]map[int]struct{})
	for pairIndex, matchingPair := range matchingPairs {
		if pairIndex%deriveCtxCheckPairs == 0 {
			if err := ctx.Err(); err != nil {
				return nil, DuplicateEvidence{}, err
			}
		}
		if leftHash := pageFacts[matchingPair.left].ContentSHA256; leftHash != "" && leftHash == pageFacts[matchingPair.right].ContentSHA256 {
			continue
		}
		evidence.Relations = append(evidence.Relations, DuplicateRelation{
			IssueType:  DuplicateIssueTypeNearDuplicateContent,
			LeftPage:   DuplicatePage{CrawlPageID: pageFacts[matchingPair.left].ID, URL: pageFacts[matchingPair.left].URL},
			RightPage:  DuplicatePage{CrawlPageID: pageFacts[matchingPair.right].ID, URL: pageFacts[matchingPair.right].URL},
			Similarity: matchingPair.similarity,
		})
		addNearDuplicateNeighbor(nearDuplicateNeighborsByPageIndex, matchingPair.left, matchingPair.right)
		addNearDuplicateNeighbor(nearDuplicateNeighborsByPageIndex, matchingPair.right, matchingPair.left)
	}
	nearDuplicateGroups, err := buildNearDuplicateGroups(ctx, pageFacts, nearDuplicateNeighborsByPageIndex)
	if err != nil {
		return nil, DuplicateEvidence{}, err
	}
	evidence.Groups = append(evidence.Groups, nearDuplicateGroups...)

	var derivedIssues []shared.DerivedIssue
	for pageIndex := range pageFacts {
		if pageIndex%deriveCtxCheckPages == 0 {
			if err := ctx.Err(); err != nil {
				return nil, DuplicateEvidence{}, err
			}
		}
		matchingPageIndexesByIndex, hasNearDuplicateMatches := nearDuplicateNeighborsByPageIndex[pageIndex]
		if !hasNearDuplicateMatches || len(matchingPageIndexesByIndex) == 0 {
			continue
		}
		matchingPageIndexes := make([]int, 0, len(matchingPageIndexesByIndex))
		for matchingPageIndex := range matchingPageIndexesByIndex {
			matchingPageIndexes = append(matchingPageIndexes, matchingPageIndex)
		}
		sort.Ints(matchingPageIndexes)
		matchingURLs := collectOtherDuplicateURLs(pageFacts, matchingPageIndexes, -1)
		derivedIssues = append(derivedIssues, newIssue(
			pageFacts[pageIndex],
			"content_quality",
			"near_duplicate_content",
			"medium",
			"Page has near-duplicate content",
			fmt.Sprintf("Normalized page content closely matches %d other page(s): %s.", len(matchingURLs), strings.Join(matchingURLs, ", ")),
		))
	}
	return derivedIssues, evidence, nil
}

func calculateFieldSimilarity(leftValue string, rightValue string) float64 {
	leftNormalizedValue := normalizeNearDuplicateField(leftValue)
	rightNormalizedValue := normalizeNearDuplicateField(rightValue)
	if leftNormalizedValue == "" || rightNormalizedValue == "" {
		return 0
	}
	if leftNormalizedValue == rightNormalizedValue {
		return 1
	}
	leftTrigrams, _ := buildCharacterTrigrams(context.Background(), leftNormalizedValue)
	rightTrigrams, _ := buildCharacterTrigrams(context.Background(), rightNormalizedValue)
	sharedTrigramCount := 0
	for trigram := range leftTrigrams {
		if _, exists := rightTrigrams[trigram]; exists {
			sharedTrigramCount++
		}
	}
	if sharedTrigramCount == 0 {
		return 0
	}
	return float64(2*sharedTrigramCount) / float64(len(leftTrigrams)+len(rightTrigrams))
}

func normalizeNearDuplicateField(value string) string {
	return strings.Join(normalizeNearDuplicateFieldToTokens(value), " ")
}

func normalizeNearDuplicateFieldToTokens(value string) []string {
	rawTokens := strings.Fields(strings.ToLower(strings.TrimSpace(value)))
	normalizedTokens := make([]string, 0, len(rawTokens))
	for _, rawToken := range rawTokens {
		normalizedToken := normalizeNearDuplicateToken(rawToken)
		if normalizedToken == "" {
			continue
		}
		if _, isStopWord := nearDuplicateStopWords[normalizedToken]; isStopWord {
			continue
		}
		normalizedTokens = append(normalizedTokens, normalizedToken)
	}
	return normalizedTokens
}

func normalizeNearDuplicateToken(value string) string {
	trimmedToken := strings.TrimFunc(value, func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character)
	})
	if trimmedToken == "" {
		return ""
	}
	return strings.ToLower(trimmedToken)
}

func buildCharacterTrigrams(ctx context.Context, value string) (map[string]struct{}, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) < 3 {
		return map[string]struct{}{value: {}}, nil
	}
	characterTrigrams := make(map[string]struct{})
	for trigramStartIndex := 0; trigramStartIndex <= len(value)-3; trigramStartIndex++ {
		if trigramStartIndex%trigramCtxCheckSize == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		characterTrigrams[value[trigramStartIndex:trigramStartIndex+3]] = struct{}{}
	}
	return characterTrigrams, nil
}

func addNearDuplicateNeighbor(neighborsByPageIndex map[int]map[int]struct{}, sourcePageIndex int, targetPageIndex int) {
	if _, exists := neighborsByPageIndex[sourcePageIndex]; !exists {
		neighborsByPageIndex[sourcePageIndex] = make(map[int]struct{})
	}
	neighborsByPageIndex[sourcePageIndex][targetPageIndex] = struct{}{}
}

func collectOtherDuplicateURLs(pageFacts []shared.PageFact, pageIndexes []int, currentPageIndex int) []string {
	matchingURLs := make([]string, 0, len(pageIndexes)-1)
	for _, pageIndex := range pageIndexes {
		if pageIndex == currentPageIndex {
			continue
		}
		matchingURLs = append(matchingURLs, pageFacts[pageIndex].URL)
	}
	sort.Strings(matchingURLs)
	return matchingURLs
}
