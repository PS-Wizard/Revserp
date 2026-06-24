package seo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

const minimumSharedShingleCountForNearDuplicateCandidate = 2
const nearDuplicateContentSimilarityThreshold = 0.58
const nearDuplicateCommonShinglePageCountFloor = 25
const nearDuplicateCommonShinglePageCountDivisor = 5

var nearDuplicateStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {},
	"for": {}, "from": {}, "in": {}, "is": {}, "it": {}, "of": {}, "on": {}, "or": {},
	"our": {}, "that": {}, "the": {}, "their": {}, "this": {}, "to": {}, "we": {},
	"with": {}, "you": {}, "your": {},
}

// EnrichPageFactsWithContentFingerprints computes normalized content hashes for duplicate detection.
func EnrichPageFactsWithContentFingerprints(pageFacts []shared.PageFact) {
	for pageFactIndex := range pageFacts {
		normalizedContent := normalizeDuplicateContent(pageFacts[pageFactIndex])
		if normalizedContent == "" {
			pageFacts[pageFactIndex].ContentSHA256 = ""
			continue
		}
		pageFacts[pageFactIndex].ContentSHA256 = hashNormalizedContentSHA256(normalizedContent)
	}
}

func deriveDuplicateContentIssues(pageFacts []shared.PageFact) []shared.DerivedIssue {
	var derivedIssues []shared.DerivedIssue
	exactDuplicateGroupsByHash := groupExactDuplicatePagesByHash(pageFacts)
	derivedIssues = append(derivedIssues, buildExactDuplicateIssues(pageFacts, exactDuplicateGroupsByHash)...)
	derivedIssues = append(derivedIssues, buildNearDuplicateIssues(pageFacts)...)
	return derivedIssues
}

func normalizeDuplicateContent(pageFact shared.PageFact) string {
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

func buildExactDuplicateIssues(pageFacts []shared.PageFact, exactDuplicateGroupsByHash map[string][]int) []shared.DerivedIssue {
	hashes := make([]string, 0, len(exactDuplicateGroupsByHash))
	for h := range exactDuplicateGroupsByHash {
		hashes = append(hashes, h)
	}
	sort.Strings(hashes)

	var derivedIssues []shared.DerivedIssue
	for _, h := range hashes {
		pageIndexes := exactDuplicateGroupsByHash[h]
		if len(pageIndexes) < 2 {
			continue
		}
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
	return derivedIssues
}

func buildNearDuplicateIssues(pageFacts []shared.PageFact) []shared.DerivedIssue {
	candidatePagePairs := buildNearDuplicateCandidatePairs(pageFacts)
	nearDuplicateNeighborsByPageIndex := make(map[int]map[int]struct{})
	for candidatePagePair := range candidatePagePairs {
		leftPageFact := pageFacts[candidatePagePair[0]]
		rightPageFact := pageFacts[candidatePagePair[1]]
		if leftPageFact.ContentSHA256 == "" || rightPageFact.ContentSHA256 == "" || leftPageFact.ContentSHA256 == rightPageFact.ContentSHA256 {
			continue
		}
		if calculateNearDuplicateContentSimilarity(leftPageFact, rightPageFact) < nearDuplicateContentSimilarityThreshold {
			continue
		}
		addNearDuplicateNeighbor(nearDuplicateNeighborsByPageIndex, candidatePagePair[0], candidatePagePair[1])
		addNearDuplicateNeighbor(nearDuplicateNeighborsByPageIndex, candidatePagePair[1], candidatePagePair[0])
	}

	var derivedIssues []shared.DerivedIssue
	for pageIndex := range pageFacts {
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
	return derivedIssues
}

func buildNearDuplicateCandidatePairs(pageFacts []shared.PageFact) map[[2]int]struct{} {
	pageIndexesByShingle := make(map[string][]int)
	shingleDocumentCounts := make(map[string]int)
	pageShinglesByIndex := make([][]string, len(pageFacts))
	for pageFactIndex, pageFact := range pageFacts {
		pageShingles := buildNearDuplicateCandidateShingles(pageFact)
		pageShinglesByIndex[pageFactIndex] = pageShingles
		for _, pageShingle := range pageShingles {
			pageIndexesByShingle[pageShingle] = append(pageIndexesByShingle[pageShingle], pageFactIndex)
			shingleDocumentCounts[pageShingle]++
		}
	}

	maximumCommonShinglePageCount := calculateMaximumCommonShinglePageCount(len(pageFacts))
	sharedShingleCountsByPair := make(map[[2]int]int)
	for pageShingle, pageIndexes := range pageIndexesByShingle {
		if shingleDocumentCounts[pageShingle] > maximumCommonShinglePageCount {
			continue
		}
		for leftIndex := 0; leftIndex < len(pageIndexes); leftIndex++ {
			for rightIndex := leftIndex + 1; rightIndex < len(pageIndexes); rightIndex++ {
				pairKey := [2]int{pageIndexes[leftIndex], pageIndexes[rightIndex]}
				sharedShingleCountsByPair[pairKey]++
			}
		}
	}

	candidatePagePairs := make(map[[2]int]struct{})
	for candidatePagePair, sharedShingleCount := range sharedShingleCountsByPair {
		if sharedShingleCount < minimumSharedShingleCountForNearDuplicateCandidate {
			continue
		}
		candidatePagePairs[candidatePagePair] = struct{}{}
	}
	return candidatePagePairs
}

func calculateMaximumCommonShinglePageCount(totalPageCount int) int {
	if totalPageCount <= 0 {
		return nearDuplicateCommonShinglePageCountFloor
	}
	maximumCommonShinglePageCount := totalPageCount / nearDuplicateCommonShinglePageCountDivisor
	if maximumCommonShinglePageCount < nearDuplicateCommonShinglePageCountFloor {
		return nearDuplicateCommonShinglePageCountFloor
	}
	return maximumCommonShinglePageCount
}

func buildNearDuplicateCandidateShingles(pageFact shared.PageFact) []string {
	candidateTokens := collectNearDuplicateCandidateTokens(pageFact)
	if len(candidateTokens) < 2 {
		return nil
	}
	uniqueShingles := make(map[string]struct{})
	for tokenIndex := 0; tokenIndex < len(candidateTokens)-1; tokenIndex++ {
		shingle := candidateTokens[tokenIndex] + " " + candidateTokens[tokenIndex+1]
		uniqueShingles[shingle] = struct{}{}
	}
	shingles := make([]string, 0, len(uniqueShingles))
	for shingle := range uniqueShingles {
		shingles = append(shingles, shingle)
	}
	return shingles
}

func collectNearDuplicateCandidateTokens(pageFact shared.PageFact) []string {
	var candidateTokens []string
	candidateTokens = append(candidateTokens, normalizeNearDuplicateFieldToTokens(pageFact.Title)...)
	candidateTokens = append(candidateTokens, normalizeNearDuplicateFieldToTokens(pageFact.MetaDescription)...)
	candidateTokens = append(candidateTokens, normalizeNearDuplicateFieldToTokens(pageFact.H1)...)
	candidateTokens = append(candidateTokens, normalizeNearDuplicateFieldToTokens(pageFact.VisibleText)...)
	return candidateTokens
}

func calculateNearDuplicateContentSimilarity(leftPageFact shared.PageFact, rightPageFact shared.PageFact) float64 {
	visibleTextSimilarity := calculateFieldSimilarity(leftPageFact.VisibleText, rightPageFact.VisibleText)
	titleSimilarity := calculateFieldSimilarity(leftPageFact.Title, rightPageFact.Title)
	metaDescriptionSimilarity := calculateFieldSimilarity(leftPageFact.MetaDescription, rightPageFact.MetaDescription)
	h1Similarity := calculateFieldSimilarity(leftPageFact.H1, rightPageFact.H1)
	return visibleTextSimilarity*0.55 + titleSimilarity*0.15 + metaDescriptionSimilarity*0.15 + h1Similarity*0.15
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
	leftTrigrams := buildCharacterTrigrams(leftNormalizedValue)
	rightTrigrams := buildCharacterTrigrams(rightNormalizedValue)
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

func buildCharacterTrigrams(value string) map[string]struct{} {
	if value == "" {
		return nil
	}
	runes := []rune(value)
	if len(runes) < 3 {
		return map[string]struct{}{value: {}}
	}
	characterTrigrams := make(map[string]struct{}, len(runes)-2)
	for trigramStartIndex := 0; trigramStartIndex <= len(runes)-3; trigramStartIndex++ {
		characterTrigrams[string(runes[trigramStartIndex:trigramStartIndex+3])] = struct{}{}
	}
	return characterTrigrams
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
