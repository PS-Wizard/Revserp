package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/ps-wizard/revserp/internal/aiprompt"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

const maxAIFixMessages = 10
const maxAIFixMessageLength = 4000
const maxAIFixContextIssueRows = 40
const maxAIFixScopedURLs = 20

type aiFixMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiFixRequest struct {
	PillarID     string         `json:"pillar_id"`
	BucketID     string         `json:"bucket_id"`
	BucketIDs    []string       `json:"bucket_ids"`
	IssueTypeIDs []string       `json:"issue_type_ids"`
	IssueURLs    []string       `json:"issue_urls"`
	Messages     []aiFixMessage `json:"messages"`
}

type aiFixResponse struct {
	Message aiFixMessage   `json:"message"`
	Scope   aiFixScopeInfo `json:"scope"`
}

type aiFixScopeInfo struct {
	PillarLabel string `json:"pillar_label"`
	BucketLabel string `json:"bucket_label"`
	IssueCount  int    `json:"issue_count"`
	URLCount    int32  `json:"url_count"`
}

type aiFixIssueRow struct {
	URL                string
	IssueType          string
	Severity           string
	Message            string
	Details            string
	CurrentTitle       string
	CurrentDescription string
	CurrentH1          string
}

// handleAIFix answers a scoped crawl issue question with DeepSeek.
func (a *App) handleAIFix(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	var requestBody aiFixRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	requestBody.PillarID = strings.TrimSpace(requestBody.PillarID)
	requestBody.BucketID = strings.TrimSpace(requestBody.BucketID)
	requestBody.BucketIDs = normalizeStringIDs(requestBody.BucketIDs)
	if len(requestBody.BucketIDs) == 0 && requestBody.BucketID != "" {
		requestBody.BucketIDs = []string{requestBody.BucketID}
	}
	requestBody.IssueTypeIDs = normalizeStringIDs(requestBody.IssueTypeIDs)
	requestBody.IssueURLs = normalizeStringIDs(requestBody.IssueURLs)
	if len(requestBody.IssueURLs) > maxAIFixScopedURLs {
		requestBody.IssueURLs = requestBody.IssueURLs[:maxAIFixScopedURLs]
	}
	requestBody.Messages = normalizeAIFixMessages(requestBody.Messages)
	if requestBody.PillarID == "" || len(requestBody.BucketIDs) == 0 {
		writeJSONError(w, http.StatusBadRequest, "pillar_id and bucket_ids are required")
		return
	}
	if len(requestBody.Messages) == 0 || requestBody.Messages[len(requestBody.Messages)-1].Role != "user" {
		writeJSONError(w, http.StatusBadRequest, "messages must end with a user message")
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

	crawl, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	breakdownRow, err := queries.GetCrawlScoreBreakdownByCrawlForUser(r.Context(), sqlc.GetCrawlScoreBreakdownByCrawlForUserParams{CrawlID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl score breakdown not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	var snapshot issueshared.ScoreBreakdownSnapshot
	if err := json.Unmarshal(breakdownRow.BreakdownJson, &snapshot); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	pillar, buckets, selectedIssues, err := resolveAIFixScope(snapshot, requestBody)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	issueRows, err := loadAIFixIssueRows(r, tx, crawlID, user.ID, requestBody.PillarID, requestBody.BucketIDs, requestBody.IssueTypeIDs, requestBody.IssueURLs)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if len(requestBody.IssueURLs) > 0 && len(issueRows) == 0 {
		writeJSONError(w, http.StatusBadRequest, "issue_urls did not match the selected issue scope")
		return
	}

	featureRow, err := queries.GetOrganizationFeaturesByProjectID(r.Context(), sqlc.GetOrganizationFeaturesByProjectIDParams{ProjectID: crawl.ProjectID, UserID: user.ID})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	businessProfile, hasBusinessProfile, err := getProjectBusinessProfileByProjectID(r.Context(), queries, crawl.ProjectID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	internalPrompt, externalPrompt := "", ""
	configRow, configErr := a.Queries.GetAIPromptConfig(r.Context())
	if configErr != nil && !errors.Is(configErr, pgx.ErrNoRows) {
		serverError(w, r, configErr)
		return
	}
	if configErr == nil {
		internalPrompt = configRow.InternalSystemPrompt
		externalPrompt = configRow.ExternalSystemPrompt
	}
	systemPrompt := aiprompt.SelectSystemPrompt(featureRow.AiUseInternalPrompt, internalPrompt, externalPrompt)
	prompt := buildAIFixPrompt(systemPrompt, pillar, buckets, selectedIssues, issueRows, businessProfile, hasBusinessProfile, requestBody.Messages)
	content, _, err := a.generateAIText(r.Context(), prompt)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, aiFixResponse{
		Message: aiFixMessage{Role: "assistant", Content: content},
		Scope: aiFixScopeInfo{
			PillarLabel: pillar.Label,
			BucketLabel: aiFixBucketLabel(buckets),
			IssueCount:  len(selectedIssues),
			URLCount:    aiFixBucketURLCount(buckets),
		},
	})
}
