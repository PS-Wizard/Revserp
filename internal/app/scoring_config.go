package app

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueengine "github.com/ps-wizard/revserp/internal/issues"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

type scoringConfigResponse struct {
	Config    issueshared.ScoringConfig `json:"config"`
	Default   issueshared.ScoringConfig `json:"default"`
	UpdatedAt string                    `json:"updated_at,omitempty"`
	UpdatedBy string                    `json:"updated_by_user_id,omitempty"`
}

type scoringConfigRequest struct {
	Config issueshared.ScoringConfig `json:"config"`
}

type scoringPreviewRequest struct {
	CrawlID string                    `json:"crawl_id"`
	Config  issueshared.ScoringConfig `json:"config"`
}

type scoringPreviewResponse struct {
	Breakdown issueshared.ScoreBreakdownSnapshot `json:"breakdown"`
	Scores    scoringPreviewScores               `json:"scores"`
}

type scoringPreviewScores struct {
	SEO       int32 `json:"seo_score"`
	AEO       int32 `json:"aeo_score"`
	PageSpeed int32 `json:"pagespeed_score"`
	Overall   int32 `json:"overall_score"`
}

// handleGetScoringConfig returns the global internal scoring config.
func (a *App) handleGetScoringConfig(w http.ResponseWriter, r *http.Request) {
	user, ok := a.ensureInternalScoringUser(w, r)
	if !ok {
		return
	}
	_ = user

	row, err := a.Queries.GetActiveScoringConfig(r.Context())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, scoringConfigResponse{Config: issueengine.DefaultScoringConfig(), Default: issueengine.DefaultScoringConfig()})
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	config, err := issueengine.ParseScoringConfig(row.ConfigJson)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "stored scoring config is invalid")
		return
	}
	writeJSON(w, http.StatusOK, scoringConfigResponse{
		Config:    config,
		Default:   issueengine.DefaultScoringConfig(),
		UpdatedAt: row.UpdatedAt.Time.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedBy: row.UpdatedByUserID.String(),
	})
}

// handlePutScoringConfig saves the global internal scoring config for future scoring runs.
func (a *App) handlePutScoringConfig(w http.ResponseWriter, r *http.Request) {
	user, ok := a.ensureInternalScoringUser(w, r)
	if !ok {
		return
	}

	var requestBody scoringConfigRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	configJSON, err := issueengine.MustMarshalScoringConfig(requestBody.Config)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	row, err := a.Queries.UpsertActiveScoringConfig(r.Context(), sqlc.UpsertActiveScoringConfigParams{
		ConfigJson:      configJSON,
		UpdatedByUserID: user.ID,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	config, err := issueengine.ParseScoringConfig(row.ConfigJson)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "stored scoring config is invalid")
		return
	}
	writeJSON(w, http.StatusOK, scoringConfigResponse{
		Config:    config,
		Default:   issueengine.DefaultScoringConfig(),
		UpdatedAt: row.UpdatedAt.Time.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedBy: row.UpdatedByUserID.String(),
	})
}

// handlePreviewScoringConfig builds a non-persisted score preview for one crawl and draft config.
func (a *App) handlePreviewScoringConfig(w http.ResponseWriter, r *http.Request) {
	user, ok := a.ensureInternalScoringUser(w, r)
	if !ok {
		return
	}
	a.previewScoringConfig(w, r, user.ID)
}

// handleAdminPreviewScoringConfig builds a preview for platform admins across all crawls.
func (a *App) handleAdminPreviewScoringConfig(w http.ResponseWriter, r *http.Request) {
	a.previewScoringConfig(w, r, pgtype.UUID{})
}

func (a *App) previewScoringConfig(w http.ResponseWriter, r *http.Request, userID pgtype.UUID) {
	var requestBody scoringPreviewRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	crawlID, err := parseUUIDParam(requestBody.CrawlID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}
	if err := issueengine.ValidateScoringConfig(requestBody.Config); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if userID.Valid {
		if _, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: userID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "crawl not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}

	crawlPageSignals, crawlIssueSignals, err := a.loadScoringPreviewSignals(r, crawlID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	breakdown := issueengine.BuildScoreBreakdownWithConfig(crawlID.String(), crawlPageSignals, crawlIssueSignals, requestBody.Config, nil)
	scores := breakdown.CrawlScores()
	writeJSON(w, http.StatusOK, scoringPreviewResponse{
		Breakdown: breakdown,
		Scores: scoringPreviewScores{
			SEO:       scores.SEOScore,
			AEO:       scores.AEOScore,
			PageSpeed: scores.PageSpeedScore,
			Overall:   scores.OverallScore,
		},
	})
}

// ensureInternalScoringUser currently requires only an authenticated user.
func (a *App) ensureInternalScoringUser(w http.ResponseWriter, r *http.Request) (sqlc.User, bool) {
	user, _, err := a.ensureCurrentUser(r, a.Queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return sqlc.User{}, false
	}
	return user, true
}

// loadScoringPreviewSignals loads persisted page and issue signals for preview scoring.
func (a *App) loadScoringPreviewSignals(r *http.Request, crawlID pgtype.UUID) ([]issueshared.CrawlPageSignal, []issueshared.CrawlIssueSignal, error) {
	crawlPages, err := a.Queries.ListCrawlPagesForCrawl(r.Context(), crawlID)
	if err != nil {
		return nil, nil, err
	}
	crawlIssues, err := a.Queries.ListCrawlIssuesForCrawl(r.Context(), crawlID)
	if err != nil {
		return nil, nil, err
	}

	crawlPageSignals := make([]issueshared.CrawlPageSignal, 0, len(crawlPages))
	for _, crawlPage := range crawlPages {
		crawlPageSignals = append(crawlPageSignals, issueshared.CrawlPageSignal{
			URL:            crawlPage.Url,
			StatusCode:     scoringInt32Value(crawlPage.StatusCode),
			ContentType:    scoringTextValue(crawlPage.ContentType),
			WordCount:      scoringInt32Value(crawlPage.WordCount),
			ResponseTimeMs: scoringInt32Value(crawlPage.ResponseTimeMs),
			SizeBytes:      scoringInt32Value(crawlPage.SizeBytes),
			OGTags:         crawlPage.OgTags,
			JSONLD:         crawlPage.JsonLd,
		})
	}

	crawlIssueSignals := make([]issueshared.CrawlIssueSignal, 0, len(crawlIssues))
	for _, crawlIssue := range crawlIssues {
		crawlIssueSignals = append(crawlIssueSignals, issueshared.CrawlIssueSignal{
			URL:       crawlIssue.Url,
			Pillar:    crawlIssue.Pillar,
			Bucket:    crawlIssue.Bucket,
			Severity:  crawlIssue.Severity,
			IssueType: crawlIssue.IssueType,
			Message:   crawlIssue.Message,
			Details:   crawlIssue.Details,
		})
	}
	return crawlPageSignals, crawlIssueSignals, nil
}

func scoringTextValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func scoringInt32Value(value pgtype.Int4) int32 {
	if !value.Valid {
		return 0
	}
	return value.Int32
}
