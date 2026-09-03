package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type createAIAuditRequest struct {
	CrawlID string `json:"crawl_id"`
}

type aiAuditRunResponse struct {
	ID                 string          `json:"id"`
	AuditID            string          `json:"audit_id"`
	QuestionText       string          `json:"question_text"`
	DisplayOrder       int32           `json:"display_order"`
	ModelName          string          `json:"model_name"`
	Status             string          `json:"status"`
	RawResponse        string          `json:"raw_response,omitempty"`
	ParsedResponseJSON json.RawMessage `json:"parsed_response_json,omitempty"`
	MentionedTarget    *bool           `json:"mentioned_target,omitempty"`
	TargetRank         *int32          `json:"target_rank,omitempty"`
	VisibilityScore    *int32          `json:"visibility_score,omitempty"`
	ErrorMessage       string          `json:"error_message,omitempty"`
	StartedAt          string          `json:"started_at,omitempty"`
	CompletedAt        string          `json:"completed_at,omitempty"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
}

type aiAuditResponse struct {
	ID           string               `json:"id"`
	ProjectID    string               `json:"project_id"`
	CrawlID      string               `json:"crawl_id,omitempty"`
	Status       string               `json:"status"`
	Score        *int32               `json:"score,omitempty"`
	ErrorMessage string               `json:"error_message,omitempty"`
	StartedAt    string               `json:"started_at,omitempty"`
	CompletedAt  string               `json:"completed_at,omitempty"`
	CreatedAt    string               `json:"created_at"`
	UpdatedAt    string               `json:"updated_at"`
	Runs         []aiAuditRunResponse `json:"runs,omitempty"`
}

// handleCreateAIAudit creates one AI visibility audit for a project a member can access.
func (a *App) handleCreateAIAudit(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var requestBody createAIAuditRequest
	if !readJSONOrRespond(w, r, &requestBody) {
		return
	}

	if strings.TrimSpace(requestBody.CrawlID) == "" {
		writeJSONError(w, http.StatusBadRequest, "crawl_id is required")
		return
	}
	crawlID, err := parseUUIDParam(requestBody.CrawlID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
	project, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	crawl, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusBadRequest, "crawl not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if crawl.ProjectID != project.ID {
		writeJSONError(w, http.StatusBadRequest, "crawl does not belong to project")
		return
	}

	if _, err := queries.GetProjectAIQuestions(r.Context(), project.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusBadRequest, "ai questions must be generated before running a visibility audit")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := queries.GetActiveAIAuditByCrawlAndProject(r.Context(), sqlc.GetActiveAIAuditByCrawlAndProjectParams{
		ProjectID: project.ID,
		CrawlID:   crawlID,
	}); err == nil {
		writeJSONError(w, http.StatusConflict, "a visibility audit is already in progress for this crawl")
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	featureRow, err := queries.GetOrganizationFeaturesByProjectID(r.Context(), sqlc.GetOrganizationFeaturesByProjectIDParams{ProjectID: project.ID, UserID: user.ID})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// Reserving a monthly audit atomically enforces the workspace quota;
	// ErrNoRows means the limit is 0 or already exhausted. Prior audits are
	// never deleted — audit history is append-only.
	if _, err := queries.ReserveAIWorkspaceMonthlyAudit(r.Context(), sqlc.ReserveAIWorkspaceMonthlyAuditParams{
		OrganizationID: project.OrganizationID,
		MonthlyLimit:   featureRow.AiVisibilityAuditMonthlyLimit,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusTooManyRequests, "visibility audit monthly limit reached")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	audit, err := queries.CreateAIAudit(r.Context(), sqlc.CreateAIAuditParams{
		ProjectID:    project.ID,
		CrawlID:      crawlID,
		Status:       "queued",
		Score:        pgtype.Int4{},
		ErrorMessage: pgtype.Text{},
		StartedAt:    pgtype.Timestamptz{},
		CompletedAt:  pgtype.Timestamptz{},
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := queries.EnqueueAIWorkerJob(r.Context(), sqlc.EnqueueAIWorkerJobParams{
		JobType:   "visibility_run",
		ProjectID: project.ID,
		AuditID:   pgtype.UUID{Bytes: audit.ID.Bytes, Valid: true},
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, newAIAuditResponseFromCreateRow(audit))
}

// handleListAIAudits lists AI audits for a project a member can access.
func (a *App) handleListAIAudits(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	statusFilter, err := parseAIAuditStatusFilter(r)
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
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
	if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	total, err := queries.CountAIAuditsForProject(r.Context(), sqlc.CountAIAuditsForProjectParams{ProjectID: projectID, Column2: statusFilter})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	audits, err := queries.ListAIAuditsForProject(r.Context(), sqlc.ListAIAuditsForProjectParams{
		ProjectID: projectID,
		Column2:   statusFilter,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	responses := make([]aiAuditResponse, 0, len(audits))
	for _, audit := range audits {
		responses = append(responses, newAIAuditResponseFromListRow(audit))
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"ai_audits": responses,
		"pagination": paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(responses)),
			Total:  total,
		},
	})
}

// handleGetAIAudit returns one AI audit and its current prompt/run state for a member.
func (a *App) handleGetAIAudit(w http.ResponseWriter, r *http.Request) {
	auditID, err := parseUUIDParam(chi.URLParam(r, "auditID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid ai audit id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
	audit, err := queries.GetAIAuditByIDForUser(r.Context(), sqlc.GetAIAuditByIDForUserParams{ID: auditID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "ai audit not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	runs, err := queries.ListAIAuditRunsByAuditID(r.Context(), audit.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	response := newAIAuditResponseFromGetRow(audit)
	response.Runs = newAIAuditRunResponses(runs)
	writeJSON(w, http.StatusOK, response)
}

func newAIAuditResponseFromCreateRow(audit sqlc.AiAudit) aiAuditResponse {
	return buildAIAuditResponse(audit.ID, audit.ProjectID, audit.CrawlID, audit.Status, audit.Score, audit.ErrorMessage, audit.StartedAt, audit.CompletedAt, audit.CreatedAt, audit.UpdatedAt)
}

func newAIAuditResponseFromListRow(audit sqlc.AiAudit) aiAuditResponse {
	return buildAIAuditResponse(audit.ID, audit.ProjectID, audit.CrawlID, audit.Status, audit.Score, audit.ErrorMessage, audit.StartedAt, audit.CompletedAt, audit.CreatedAt, audit.UpdatedAt)
}

func newAIAuditResponseFromGetRow(audit sqlc.AiAudit) aiAuditResponse {
	return buildAIAuditResponse(audit.ID, audit.ProjectID, audit.CrawlID, audit.Status, audit.Score, audit.ErrorMessage, audit.StartedAt, audit.CompletedAt, audit.CreatedAt, audit.UpdatedAt)
}

func buildAIAuditResponse(id, projectID, crawlID pgtype.UUID, status string, score pgtype.Int4, errorMessage pgtype.Text, startedAt, completedAt, createdAt, updatedAt pgtype.Timestamptz) aiAuditResponse {
	response := aiAuditResponse{
		ID:        id.String(),
		ProjectID: projectID.String(),
		Status:    status,
		CreatedAt: formatTimestamp(createdAt),
		UpdatedAt: formatTimestamp(updatedAt),
	}
	if crawlID.Valid {
		response.CrawlID = crawlID.String()
	}
	if score.Valid {
		response.Score = &score.Int32
	}
	if errorMessage.Valid {
		response.ErrorMessage = errorMessage.String
	}
	if startedAt.Valid {
		response.StartedAt = formatTimestamp(startedAt)
	}
	if completedAt.Valid {
		response.CompletedAt = formatTimestamp(completedAt)
	}
	return response
}

func newAIAuditRunResponses(runs []sqlc.AiAuditRun) []aiAuditRunResponse {
	responses := make([]aiAuditRunResponse, 0, len(runs))
	for _, run := range runs {
		response := aiAuditRunResponse{
			ID:           run.ID.String(),
			AuditID:      run.AuditID.String(),
			QuestionText: run.QuestionText,
			DisplayOrder: run.DisplayOrder,
			ModelName:    run.ModelName,
			Status:       run.Status,
			CreatedAt:    formatTimestamp(run.CreatedAt),
			UpdatedAt:    formatTimestamp(run.UpdatedAt),
		}
		if run.RawResponse.Valid && strings.TrimSpace(run.RawResponse.String) != "" {
			response.RawResponse = run.RawResponse.String
		}
		if len(run.ParsedResponseJson) > 0 {
			response.ParsedResponseJSON = json.RawMessage(run.ParsedResponseJson)
		}
		if run.MentionedTarget.Valid {
			response.MentionedTarget = &run.MentionedTarget.Bool
		}
		if run.TargetRank.Valid {
			response.TargetRank = &run.TargetRank.Int32
		}
		if run.VisibilityScore.Valid {
			response.VisibilityScore = &run.VisibilityScore.Int32
		}
		if run.ErrorMessage.Valid {
			response.ErrorMessage = run.ErrorMessage.String
		}
		if run.StartedAt.Valid {
			response.StartedAt = formatTimestamp(run.StartedAt)
		}
		if run.CompletedAt.Valid {
			response.CompletedAt = formatTimestamp(run.CompletedAt)
		}
		responses = append(responses, response)
	}
	return responses
}

func parseAIAuditStatusFilter(r *http.Request) (string, error) {
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	if statusFilter == "" {
		return "", nil
	}

	switch statusFilter {
	case "queued", "running", "completed", "completed_with_failures", "failed":
		return statusFilter, nil
	default:
		return "", errors.New("invalid status")
	}
}
