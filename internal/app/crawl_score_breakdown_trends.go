package app

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

const defaultTrendLimit = 20

type projectBucketTrendsResponse struct {
	Crawls []crawlTrendSnapshot `json:"crawls"`
}

type crawlTrendSnapshot struct {
	CrawlID      string                 `json:"crawl_id"`
	CompletedAt  string                 `json:"completed_at"`
	OverallScore int32                  `json:"overall_score"`
	SEOScore     int32                  `json:"seo_score"`
	AEOScore     int32                  `json:"aeo_score"`
	PageSpeed    int32                  `json:"pagespeed_score"`
	Pillars      []pillarTrendBreakdown `json:"pillars,omitempty"`
}

type pillarTrendBreakdown struct {
	ID      string                 `json:"id"`
	Label   string                 `json:"label"`
	Score   int32                  `json:"score"`
	Buckets []bucketTrendBreakdown `json:"buckets,omitempty"`
}

type bucketTrendBreakdown struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Score  int32  `json:"score"`
	Issues []struct {
		ID      string  `json:"id"`
		Label   string  `json:"label"`
		Penalty float64 `json:"penalty"`
	} `json:"issues,omitempty"`
}

// handleGetProjectBucketTrends returns score breakdown trends across completed crawls for a project.
func (a *App) handleGetProjectBucketTrends(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	user, _, err := a.ensureCurrentUser(r, a.Queries)
	if err != nil {
		serverError(w, r, err)
		return
	}

	if _, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}

	rows, err := a.Queries.ListCompletedProjectCrawlScoreBreakdownsForUser(r.Context(), sqlc.ListCompletedProjectCrawlScoreBreakdownsForUserParams{
		ProjectID: projectID,
		UserID:    user.ID,
		Limit:     defaultTrendLimit,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	crawls := make([]crawlTrendSnapshot, 0, len(rows))
	for _, row := range rows {
		var snapshot issueshared.ScoreBreakdownSnapshot
		if err := json.Unmarshal(row.BreakdownJson, &snapshot); err != nil {
			serverError(w, r, err)
			return
		}

		scores := snapshot.CrawlScores()
		completedAt := ""
		if row.CompletedAt.Valid {
			completedAt = row.CompletedAt.Time.Format("2006-01-02T15:04:05Z")
		}

		crawl := crawlTrendSnapshot{
			CrawlID:      row.CrawlID.String(),
			CompletedAt:  completedAt,
			OverallScore: scores.OverallScore,
			SEOScore:     scores.SEOScore,
			AEOScore:     scores.AEOScore,
			PageSpeed:    scores.PageSpeedScore,
			Pillars:      buildPillarTrendBreakdowns(snapshot.Pillars),
		}
		crawls = append(crawls, crawl)
	}

	writeJSON(w, http.StatusOK, projectBucketTrendsResponse{Crawls: crawls})
}

func buildPillarTrendBreakdowns(pillars []issueshared.PillarScoreBreakdown) []pillarTrendBreakdown {
	result := make([]pillarTrendBreakdown, 0, len(pillars))
	for _, pillar := range pillars {
		entry := pillarTrendBreakdown{
			ID:    pillar.ID,
			Label: pillar.Label,
			Score: pillar.Score,
		}
		if len(pillar.Buckets) > 0 {
			entry.Buckets = make([]bucketTrendBreakdown, 0, len(pillar.Buckets))
			for _, bucket := range pillar.Buckets {
				bucketEntry := bucketTrendBreakdown{
					ID:    bucket.ID,
					Label: bucket.Label,
					Score: bucket.Score,
				}
				if len(bucket.Issues) > 0 {
					for _, issue := range bucket.Issues {
						bucketEntry.Issues = append(bucketEntry.Issues, struct {
							ID      string  `json:"id"`
							Label   string  `json:"label"`
							Penalty float64 `json:"penalty"`
						}{ID: issue.ID, Label: issue.Label, Penalty: issue.FinalPenalty})
					}
				}
				entry.Buckets = append(entry.Buckets, bucketEntry)
			}
		}
		result = append(result, entry)
	}
	return result
}
