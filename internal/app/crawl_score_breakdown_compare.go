package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

const scoreCompareEpsilon = 0.000001

type scoreBreakdownCompareResponse struct {
	BaselineCrawlID string                       `json:"baseline_crawl_id"`
	CurrentCrawlID  string                       `json:"current_crawl_id"`
	Overall         scoreCompareSummary          `json:"overall"`
	Pillars         []pillarScoreCompareResponse `json:"pillars"`
}

type scoreCompareSummary struct {
	BeforeScore int32 `json:"before_score"`
	AfterScore  int32 `json:"after_score"`
	DeltaScore  int32 `json:"delta_score"`
}

type pillarScoreCompareResponse struct {
	ID      string                       `json:"id"`
	Label   string                       `json:"label"`
	Summary scoreCompareSummary          `json:"summary"`
	Buckets []bucketScoreCompareResponse `json:"buckets"`
}

type bucketScoreCompareResponse struct {
	ID      string              `json:"id"`
	Label   string              `json:"label"`
	Summary scoreCompareSummary `json:"summary"`
	Issues  issueCompareGroups  `json:"issues"`
}

type issueCompareGroups struct {
	Improved  []issueCompareResponse `json:"improved"`
	Regressed []issueCompareResponse `json:"regressed"`
	New       []issueCompareResponse `json:"new"`
	Resolved  []issueCompareResponse `json:"resolved"`
}

type issueCompareResponse struct {
	ID                     string   `json:"id"`
	Label                  string   `json:"label"`
	Severity               string   `json:"severity"`
	BucketID               string   `json:"bucket_id"`
	BucketLabel            string   `json:"bucket_label"`
	BeforeFinalPenalty     *float64 `json:"before_final_penalty"`
	AfterFinalPenalty      *float64 `json:"after_final_penalty"`
	DeltaFinalPenalty      float64  `json:"delta_final_penalty"`
	BeforeIssueRowCount    *int32   `json:"before_issue_row_count"`
	AfterIssueRowCount     *int32   `json:"after_issue_row_count"`
	BeforeAffectedURLCount *int32   `json:"before_affected_url_count"`
	AfterAffectedURLCount  *int32   `json:"after_affected_url_count"`
	ChangeType             string   `json:"change_type"`
	Message                string   `json:"message"`
	DetailsPreview         string   `json:"details_preview"`
}

type compareIssueURLStateResponse struct {
	CrawlPageID string `json:"crawl_page_id,omitempty"`
	Severity    string `json:"severity"`
	Message     string `json:"message"`
	Details     string `json:"details"`
}

type compareIssueURLResponse struct {
	URL        string                        `json:"url"`
	ChangeType string                        `json:"change_type"`
	Baseline   *compareIssueURLStateResponse `json:"baseline"`
	Current    *compareIssueURLStateResponse `json:"current"`
}

func (a *App) handleGetCrawlScoreBreakdownCompare(w http.ResponseWriter, r *http.Request) {
	baselineCrawlID, err := parseUUIDParam(chi.URLParam(r, "baselineCrawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid baseline crawl id")
		return
	}
	currentCrawlID, err := parseUUIDParam(chi.URLParam(r, "currentCrawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid current crawl id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	baselineCrawl, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: baselineCrawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "baseline crawl not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	currentCrawl, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: currentCrawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "current crawl not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if baselineCrawl.ProjectID != currentCrawl.ProjectID {
		writeJSONError(w, http.StatusBadRequest, "crawls must belong to the same project")
		return
	}

	baselineSnapshot, err := loadScoreBreakdownSnapshot(r, queries, baselineCrawlID, user.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "baseline crawl score breakdown not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	currentSnapshot, err := loadScoreBreakdownSnapshot(r, queries, currentCrawlID, user.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "current crawl score breakdown not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, buildScoreBreakdownCompareResponse(baselineSnapshot, currentSnapshot))
}

func (a *App) handleListScoreBreakdownCompareIssueURLs(w http.ResponseWriter, r *http.Request) {
	baselineCrawlID, err := parseUUIDParam(chi.URLParam(r, "baselineCrawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid baseline crawl id")
		return
	}
	currentCrawlID, err := parseUUIDParam(chi.URLParam(r, "currentCrawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid current crawl id")
		return
	}
	pillar, ok := normalizeIssuePillar(chi.URLParam(r, "pillar"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "pillar must be seo, aeo, or pagespeed")
		return
	}
	bucket := strings.TrimSpace(chi.URLParam(r, "bucket"))
	issueType := strings.TrimSpace(chi.URLParam(r, "issueType"))
	if bucket == "" || issueType == "" {
		writeJSONError(w, http.StatusBadRequest, "bucket and issue type are required")
		return
	}
	changeType, ok := normalizeCompareURLChangeType(r.URL.Query().Get("change_type"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "change_type must be new, resolved, changed, unchanged, improved, regressed, or all")
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	baselineCrawl, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: baselineCrawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "baseline crawl not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	currentCrawl, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: currentCrawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "current crawl not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if baselineCrawl.ProjectID != currentCrawl.ProjectID {
		writeJSONError(w, http.StatusBadRequest, "crawls must belong to the same project")
		return
	}

	countParams := sqlc.CountCompareCrawlIssueURLsByTypeForCrawlByUserParams{
		CrawlID: baselineCrawlID, CrawlID_2: currentCrawlID, UserID: user.ID,
		Pillar: pillar, Bucket: bucket, IssueType: issueType, Column7: changeType,
	}
	total, err := queries.CountCompareCrawlIssueURLsByTypeForCrawlByUser(r.Context(), countParams)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	rows, err := queries.ListCompareCrawlIssueURLsByTypeForCrawlByUser(r.Context(), sqlc.ListCompareCrawlIssueURLsByTypeForCrawlByUserParams{
		CrawlID: baselineCrawlID, CrawlID_2: currentCrawlID, UserID: user.ID,
		Pillar: pillar, Bucket: bucket, IssueType: issueType, Column7: changeType,
		Limit: limit, Offset: offset,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	responses := make([]compareIssueURLResponse, 0, len(rows))
	for _, row := range rows {
		responses = append(responses, compareIssueURLResponse{
			URL:        row.Url,
			ChangeType: row.ChangeType,
			Baseline:   buildCompareIssueURLState(row.BaselineCrawlPageID, row.BaselineSeverity.String, row.BaselineSeverity.Valid, row.BaselineMessage.String, row.BaselineDetails.String),
			Current:    buildCompareIssueURLState(row.CurrentCrawlPageID, row.CurrentSeverity.String, row.CurrentSeverity.Valid, row.CurrentMessage.String, row.CurrentDetails.String),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"urls":       responses,
		"pagination": paginationResponse{Limit: limit, Offset: offset, Count: int32(len(responses)), Total: total},
	})
}

func loadScoreBreakdownSnapshot(r *http.Request, queries *sqlc.Queries, crawlID pgtype.UUID, userID pgtype.UUID) (issueshared.ScoreBreakdownSnapshot, error) {
	row, err := queries.GetCrawlScoreBreakdownByCrawlForUser(r.Context(), sqlc.GetCrawlScoreBreakdownByCrawlForUserParams{CrawlID: crawlID, UserID: userID})
	if err != nil {
		return issueshared.ScoreBreakdownSnapshot{}, err
	}
	var snapshot issueshared.ScoreBreakdownSnapshot
	if err := json.Unmarshal(row.BreakdownJson, &snapshot); err != nil {
		return issueshared.ScoreBreakdownSnapshot{}, err
	}
	return snapshot, nil
}

func buildScoreBreakdownCompareResponse(baseline issueshared.ScoreBreakdownSnapshot, current issueshared.ScoreBreakdownSnapshot) scoreBreakdownCompareResponse {
	return scoreBreakdownCompareResponse{
		BaselineCrawlID: baseline.CrawlID,
		CurrentCrawlID:  current.CrawlID,
		Overall:         buildScoreSummary(baseline.OverallScore, current.OverallScore),
		Pillars:         buildPillarCompareResponses(baseline.Pillars, current.Pillars),
	}
}

func buildPillarCompareResponses(baselinePillars []issueshared.PillarScoreBreakdown, currentPillars []issueshared.PillarScoreBreakdown) []pillarScoreCompareResponse {
	baselineByID := make(map[string]issueshared.PillarScoreBreakdown, len(baselinePillars))
	currentByID := make(map[string]issueshared.PillarScoreBreakdown, len(currentPillars))
	ids := make([]string, 0, len(currentPillars)+len(baselinePillars))
	seen := make(map[string]bool, len(currentPillars)+len(baselinePillars))
	for _, pillar := range currentPillars {
		currentByID[pillar.ID] = pillar
		if !seen[pillar.ID] {
			ids = append(ids, pillar.ID)
			seen[pillar.ID] = true
		}
	}
	for _, pillar := range baselinePillars {
		baselineByID[pillar.ID] = pillar
		if !seen[pillar.ID] {
			ids = append(ids, pillar.ID)
			seen[pillar.ID] = true
		}
	}

	responses := make([]pillarScoreCompareResponse, 0, len(ids))
	for _, id := range ids {
		baselinePillar, hasBaseline := baselineByID[id]
		currentPillar, hasCurrent := currentByID[id]
		label := currentPillar.Label
		if label == "" {
			label = baselinePillar.Label
		}
		responses = append(responses, pillarScoreCompareResponse{
			ID:      id,
			Label:   label,
			Summary: buildScoreSummary(scoreIfPillar(hasBaseline, baselinePillar), scoreIfPillar(hasCurrent, currentPillar)),
			Buckets: buildBucketCompareResponses(baselinePillar.Buckets, currentPillar.Buckets),
		})
	}
	return responses
}

func buildBucketCompareResponses(baselineBuckets []issueshared.BucketScoreBreakdown, currentBuckets []issueshared.BucketScoreBreakdown) []bucketScoreCompareResponse {
	baselineByID := make(map[string]issueshared.BucketScoreBreakdown, len(baselineBuckets))
	currentByID := make(map[string]issueshared.BucketScoreBreakdown, len(currentBuckets))
	ids := make([]string, 0, len(currentBuckets)+len(baselineBuckets))
	seen := make(map[string]bool, len(currentBuckets)+len(baselineBuckets))
	for _, bucket := range currentBuckets {
		currentByID[bucket.ID] = bucket
		if !seen[bucket.ID] {
			ids = append(ids, bucket.ID)
			seen[bucket.ID] = true
		}
	}
	for _, bucket := range baselineBuckets {
		baselineByID[bucket.ID] = bucket
		if !seen[bucket.ID] {
			ids = append(ids, bucket.ID)
			seen[bucket.ID] = true
		}
	}

	responses := make([]bucketScoreCompareResponse, 0, len(ids))
	for _, id := range ids {
		baselineBucket, hasBaseline := baselineByID[id]
		currentBucket, hasCurrent := currentByID[id]
		label := currentBucket.Label
		if label == "" {
			label = baselineBucket.Label
		}
		responses = append(responses, bucketScoreCompareResponse{
			ID:      id,
			Label:   label,
			Summary: buildScoreSummary(scoreIfBucket(hasBaseline, baselineBucket), scoreIfBucket(hasCurrent, currentBucket)),
			Issues:  buildIssueCompareGroups(id, label, baselineBucket.Issues, currentBucket.Issues),
		})
	}
	return responses
}

func buildIssueCompareGroups(bucketID string, bucketLabel string, baselineIssues []issueshared.IssueTypeScoreBreakdown, currentIssues []issueshared.IssueTypeScoreBreakdown) issueCompareGroups {
	baselineByID := make(map[string]issueshared.IssueTypeScoreBreakdown, len(baselineIssues))
	currentByID := make(map[string]issueshared.IssueTypeScoreBreakdown, len(currentIssues))
	ids := make([]string, 0, len(currentIssues)+len(baselineIssues))
	seen := make(map[string]bool, len(currentIssues)+len(baselineIssues))
	for _, issue := range currentIssues {
		currentByID[issue.ID] = issue
		if !seen[issue.ID] {
			ids = append(ids, issue.ID)
			seen[issue.ID] = true
		}
	}
	for _, issue := range baselineIssues {
		baselineByID[issue.ID] = issue
		if !seen[issue.ID] {
			ids = append(ids, issue.ID)
			seen[issue.ID] = true
		}
	}

	groups := issueCompareGroups{
		Improved:  make([]issueCompareResponse, 0),
		Regressed: make([]issueCompareResponse, 0),
		New:       make([]issueCompareResponse, 0),
		Resolved:  make([]issueCompareResponse, 0),
	}
	for _, id := range ids {
		baselineIssue, hasBaseline := baselineByID[id]
		currentIssue, hasCurrent := currentByID[id]
		switch {
		case !hasBaseline && hasCurrent:
			groups.New = append(groups.New, buildIssueCompareResponse(bucketID, bucketLabel, nil, &currentIssue, "new"))
		case hasBaseline && !hasCurrent:
			groups.Resolved = append(groups.Resolved, buildIssueCompareResponse(bucketID, bucketLabel, &baselineIssue, nil, "resolved"))
		case hasBaseline && hasCurrent:
			changeType := classifyIssueChange(baselineIssue, currentIssue)
			switch changeType {
			case "improved":
				groups.Improved = append(groups.Improved, buildIssueCompareResponse(bucketID, bucketLabel, &baselineIssue, &currentIssue, "improved"))
			case "regressed":
				groups.Regressed = append(groups.Regressed, buildIssueCompareResponse(bucketID, bucketLabel, &baselineIssue, &currentIssue, "regressed"))
			}
		}
	}
	sortIssueCompareResponses(groups.Improved, false)
	sortIssueCompareResponses(groups.Regressed, true)
	sortIssueCompareResponses(groups.New, true)
	sortIssueCompareResponses(groups.Resolved, false)
	return groups
}

func buildIssueCompareResponse(bucketID string, bucketLabel string, baseline *issueshared.IssueTypeScoreBreakdown, current *issueshared.IssueTypeScoreBreakdown, changeType string) issueCompareResponse {
	issue := current
	if issue == nil {
		issue = baseline
	}
	response := issueCompareResponse{
		ID:             issue.ID,
		Label:          issue.Label,
		Severity:       issue.Severity,
		BucketID:       bucketID,
		BucketLabel:    bucketLabel,
		ChangeType:     changeType,
		Message:        issue.Message,
		DetailsPreview: issue.DetailsPreview,
	}
	if baseline != nil {
		beforePenalty := baseline.FinalPenalty
		response.BeforeFinalPenalty = &beforePenalty
		beforeCount := baseline.IssueRowCount
		response.BeforeIssueRowCount = &beforeCount
		beforeURLCount := baseline.AffectedURLCount
		response.BeforeAffectedURLCount = &beforeURLCount
		response.DeltaFinalPenalty -= baseline.FinalPenalty
	}
	if current != nil {
		afterPenalty := current.FinalPenalty
		response.AfterFinalPenalty = &afterPenalty
		afterCount := current.IssueRowCount
		response.AfterIssueRowCount = &afterCount
		afterURLCount := current.AffectedURLCount
		response.AfterAffectedURLCount = &afterURLCount
		response.DeltaFinalPenalty += current.FinalPenalty
	}
	return response
}

func classifyIssueChange(baseline issueshared.IssueTypeScoreBreakdown, current issueshared.IssueTypeScoreBreakdown) string {
	deltaPenalty := current.FinalPenalty - baseline.FinalPenalty
	if deltaPenalty < -scoreCompareEpsilon {
		return "improved"
	}
	if deltaPenalty > scoreCompareEpsilon {
		return "regressed"
	}
	if current.AffectedURLCount < baseline.AffectedURLCount {
		return "improved"
	}
	if current.AffectedURLCount > baseline.AffectedURLCount {
		return "regressed"
	}
	if current.IssueRowCount < baseline.IssueRowCount {
		return "improved"
	}
	if current.IssueRowCount > baseline.IssueRowCount {
		return "regressed"
	}
	return "unchanged"
}

func sortIssueCompareResponses(items []issueCompareResponse, descending bool) {
	sort.Slice(items, func(i int, j int) bool {
		if items[i].DeltaFinalPenalty > items[j].DeltaFinalPenalty-scoreCompareEpsilon && items[i].DeltaFinalPenalty < items[j].DeltaFinalPenalty+scoreCompareEpsilon {
			left := int32Value(items[i].AfterAffectedURLCount) + int32Value(items[i].BeforeAffectedURLCount)
			right := int32Value(items[j].AfterAffectedURLCount) + int32Value(items[j].BeforeAffectedURLCount)
			if descending {
				return left > right
			}
			return left < right
		}
		if descending {
			return items[i].DeltaFinalPenalty > items[j].DeltaFinalPenalty
		}
		return items[i].DeltaFinalPenalty < items[j].DeltaFinalPenalty
	})
}

func buildScoreSummary(beforeScore int32, afterScore int32) scoreCompareSummary {
	return scoreCompareSummary{BeforeScore: beforeScore, AfterScore: afterScore, DeltaScore: afterScore - beforeScore}
}

func scoreIfPillar(ok bool, pillar issueshared.PillarScoreBreakdown) int32 {
	if !ok {
		return 0
	}
	return pillar.Score
}

func scoreIfBucket(ok bool, bucket issueshared.BucketScoreBreakdown) int32 {
	if !ok {
		return 0
	}
	return bucket.Score
}

func normalizeCompareURLChangeType(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return "", true
	case "new", "resolved", "changed", "unchanged", "improved", "regressed":
		return strings.ToLower(strings.TrimSpace(value)), true
	default:
		return "", false
	}
}

func buildCompareIssueURLState(crawlPageID pgtype.UUID, severity string, valid bool, message string, details string) *compareIssueURLStateResponse {
	if !valid {
		return nil
	}
	response := &compareIssueURLStateResponse{Severity: severity, Message: message, Details: details}
	if crawlPageID.Valid {
		response.CrawlPageID = crawlPageID.String()
	}
	return response
}

func int32Value(value *int32) int32 {
	if value == nil {
		return 0
	}
	return *value
}
