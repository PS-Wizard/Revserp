package seo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
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
	var derivedIssues []shared.DerivedIssue
	for _, pageIndexes := range exactDuplicateGroupsByHash {
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

	// Precompute each page's normalized fields and character-trigram sets once.
	// The similarity check runs over tens of thousands of candidate pairs and a
	// single page appears in many of them, so rebuilding trigrams per pair (the
	// old path) recomputed the same sets thousands of times.
	profiles := buildNearDuplicateProfiles(pageFacts)

	candidatePairList := make([][2]int, 0, len(candidatePagePairs))
	for candidatePagePair := range candidatePagePairs {
		candidatePairList = append(candidatePairList, candidatePagePair)
	}

	matchingPairs := verifyNearDuplicatePairs(pageFacts, profiles, candidatePairList)

	nearDuplicateNeighborsByPageIndex := make(map[int]map[int]struct{})
	for _, matchingPair := range matchingPairs {
		addNearDuplicateNeighbor(nearDuplicateNeighborsByPageIndex, matchingPair[0], matchingPair[1])
		addNearDuplicateNeighbor(nearDuplicateNeighborsByPageIndex, matchingPair[1], matchingPair[0])
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

// nearDuplicateProfile holds a page's precomputed normalized fields and their
// character-trigram sets, so pairwise similarity avoids rebuilding them.
type nearDuplicateProfile struct {
	visibleText     nearDuplicateFieldProfile
	title           nearDuplicateFieldProfile
	metaDescription nearDuplicateFieldProfile
	h1              nearDuplicateFieldProfile
}

type nearDuplicateFieldProfile struct {
	normalized string
	trigrams   map[string]struct{}
}

func buildNearDuplicateProfiles(pageFacts []shared.PageFact) []nearDuplicateProfile {
	profiles := make([]nearDuplicateProfile, len(pageFacts))
	for pageIndex := range pageFacts {
		profiles[pageIndex] = nearDuplicateProfile{
			visibleText:     buildNearDuplicateFieldProfile(pageFacts[pageIndex].VisibleText),
			title:           buildNearDuplicateFieldProfile(pageFacts[pageIndex].Title),
			metaDescription: buildNearDuplicateFieldProfile(pageFacts[pageIndex].MetaDescription),
			h1:              buildNearDuplicateFieldProfile(pageFacts[pageIndex].H1),
		}
	}
	return profiles
}

func buildNearDuplicateFieldProfile(value string) nearDuplicateFieldProfile {
	normalized := normalizeNearDuplicateField(value)
	if normalized == "" {
		return nearDuplicateFieldProfile{}
	}
	return nearDuplicateFieldProfile{normalized: normalized, trigrams: buildCharacterTrigrams(normalized)}
}

// similarityFromProfiles mirrors calculateNearDuplicateContentSimilarity exactly
// but reads precomputed trigram sets instead of rebuilding them per pair.
func similarityFromProfiles(leftProfile nearDuplicateProfile, rightProfile nearDuplicateProfile) float64 {
	return fieldSimilarityFromProfiles(leftProfile.visibleText, rightProfile.visibleText)*0.55 +
		fieldSimilarityFromProfiles(leftProfile.title, rightProfile.title)*0.15 +
		fieldSimilarityFromProfiles(leftProfile.metaDescription, rightProfile.metaDescription)*0.15 +
		fieldSimilarityFromProfiles(leftProfile.h1, rightProfile.h1)*0.15
}

func fieldSimilarityFromProfiles(leftProfile nearDuplicateFieldProfile, rightProfile nearDuplicateFieldProfile) float64 {
	if leftProfile.normalized == "" || rightProfile.normalized == "" {
		return 0
	}
	if leftProfile.normalized == rightProfile.normalized {
		return 1
	}
	sharedTrigramCount := trigramIntersectionSize(leftProfile.trigrams, rightProfile.trigrams)
	if sharedTrigramCount == 0 {
		return 0
	}
	return float64(2*sharedTrigramCount) / float64(len(leftProfile.trigrams)+len(rightProfile.trigrams))
}

func trigramIntersectionSize(leftTrigrams map[string]struct{}, rightTrigrams map[string]struct{}) int {
	if len(leftTrigrams) > len(rightTrigrams) {
		leftTrigrams, rightTrigrams = rightTrigrams, leftTrigrams
	}
	sharedTrigramCount := 0
	for trigram := range leftTrigrams {
		if _, exists := rightTrigrams[trigram]; exists {
			sharedTrigramCount++
		}
	}
	return sharedTrigramCount
}

// verifyNearDuplicatePairs returns the candidate pairs whose content similarity
// meets the threshold. Each pair is independent, so verification fans out
// across CPUs (this is the dominant cost of duplicate detection on large sites).
func verifyNearDuplicatePairs(pageFacts []shared.PageFact, profiles []nearDuplicateProfile, candidatePairs [][2]int) [][2]int {
	if len(candidatePairs) == 0 {
		return nil
	}
	workerCount := runtime.NumCPU()
	if workerCount > len(candidatePairs) {
		workerCount = len(candidatePairs)
	}
	if workerCount < 1 {
		workerCount = 1
	}

	matchesByWorker := make([][][2]int, workerCount)
	var workerGroup sync.WaitGroup
	for workerIndex := 0; workerIndex < workerCount; workerIndex++ {
		workerGroup.Add(1)
		go func(workerIndex int) {
			defer workerGroup.Done()
			var localMatches [][2]int
			for pairIndex := workerIndex; pairIndex < len(candidatePairs); pairIndex += workerCount {
				candidatePair := candidatePairs[pairIndex]
				leftPageFact := pageFacts[candidatePair[0]]
				rightPageFact := pageFacts[candidatePair[1]]
				if leftPageFact.ContentSHA256 == "" || rightPageFact.ContentSHA256 == "" || leftPageFact.ContentSHA256 == rightPageFact.ContentSHA256 {
					continue
				}
				if similarityFromProfiles(profiles[candidatePair[0]], profiles[candidatePair[1]]) < nearDuplicateContentSimilarityThreshold {
					continue
				}
				localMatches = append(localMatches, candidatePair)
			}
			matchesByWorker[workerIndex] = localMatches
		}(workerIndex)
	}
	workerGroup.Wait()

	var matchingPairs [][2]int
	for _, localMatches := range matchesByWorker {
		matchingPairs = append(matchingPairs, localMatches...)
	}
	return matchingPairs
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
	if len(value) < 3 {
		return map[string]struct{}{value: {}}
	}
	characterTrigrams := make(map[string]struct{}, len(value)-2)
	for trigramStartIndex := 0; trigramStartIndex <= len(value)-3; trigramStartIndex++ {
		characterTrigrams[value[trigramStartIndex:trigramStartIndex+3]] = struct{}{}
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
