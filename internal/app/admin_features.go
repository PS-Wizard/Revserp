package app

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// adminWorkspaceFeaturesResponse is one row of the admin matrix.
type adminWorkspaceFeaturesResponse struct {
	OrgID                     string   `json:"org_id"`
	OrgName                   string   `json:"org_name"`
	AutoCrawl                 bool     `json:"auto_crawl"`
	GSCConnector              bool     `json:"gsc_connector"`
	AIChat                    bool     `json:"ai_chat"`
	AIMonthlyMessageLimit     int32    `json:"ai_monthly_message_limit"`
	AIAllowedReasoningEfforts []string `json:"ai_allowed_reasoning_efforts"`
	UpdatedAt                 string   `json:"updated_at,omitempty"`
}

type adminFeaturesResponse struct {
	Workspaces []adminWorkspaceFeaturesResponse `json:"workspaces"`
}

// handleAdminListFeatures returns every workspace's gating state.
func (a *App) handleAdminListFeatures(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Queries.ListOrganizationFeaturesForAdmin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}

	workspaces := make([]adminWorkspaceFeaturesResponse, 0, len(rows))
	for _, row := range rows {
		workspace := adminWorkspaceFeaturesResponse{
			OrgID:                     row.OrgID.String(),
			OrgName:                   row.OrgName,
			AutoCrawl:                 row.AutoCrawl,
			GSCConnector:              row.GscConnector,
			AIChat:                    row.AiChat,
			AIMonthlyMessageLimit:     row.AiMonthlyMessageLimit,
			AIAllowedReasoningEfforts: normalizeAIReasoningEfforts(row.AiAllowedReasoningEfforts),
		}
		if row.UpdatedAt.Valid {
			workspace.UpdatedAt = row.UpdatedAt.Time.UTC().Format(time.RFC3339)
		}
		workspaces = append(workspaces, workspace)
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, adminFeaturesResponse{Workspaces: workspaces})
}

type adminPutFeaturesRequest struct {
	Workspaces []adminPutWorkspaceFeatures `json:"workspaces"`
}

type adminPutWorkspaceFeatures struct {
	OrgID                     string   `json:"org_id"`
	AutoCrawl                 bool     `json:"auto_crawl"`
	GSCConnector              bool     `json:"gsc_connector"`
	AIChat                    bool     `json:"ai_chat"`
	AIMonthlyMessageLimit     int32    `json:"ai_monthly_message_limit"`
	AIAllowedReasoningEfforts []string `json:"ai_allowed_reasoning_efforts"`
}

// handleAdminPutFeatures saves the edited rows in one transaction.
func (a *App) handleAdminPutFeatures(w http.ResponseWriter, r *http.Request) {
	var requestBody adminPutFeaturesRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(requestBody.Workspaces) == 0 {
		writeJSONError(w, http.StatusBadRequest, "no workspaces supplied")
		return
	}

	type parsedWorkspace struct {
		orgID                     pgtype.UUID
		autoCrawl                 bool
		gscConnector              bool
		aiChat                    bool
		aiMonthlyMessageLimit     int32
		aiAllowedReasoningEfforts []string
	}
	parsed := make([]parsedWorkspace, 0, len(requestBody.Workspaces))
	for _, workspace := range requestBody.Workspaces {
		orgID, err := parseUUIDParam(workspace.OrgID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid org id")
			return
		}
		normalizedEfforts, err := validateAIChatSettings(workspace.AIMonthlyMessageLimit, workspace.AIAllowedReasoningEfforts)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		parsed = append(parsed, parsedWorkspace{
			orgID:                     orgID,
			autoCrawl:                 workspace.AutoCrawl,
			gscConnector:              workspace.GSCConnector,
			aiChat:                    workspace.AIChat,
			aiMonthlyMessageLimit:     workspace.AIMonthlyMessageLimit,
			aiAllowedReasoningEfforts: normalizedEfforts,
		})
	}

	editorID, err := a.currentUserID(r)
	if err != nil {
		serverError(w, r, err)
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	for _, workspace := range parsed {
		if err := queries.UpsertOrganizationFeatures(r.Context(), sqlc.UpsertOrganizationFeaturesParams{
			OrgID:                     workspace.orgID,
			AutoCrawl:                 workspace.autoCrawl,
			GscConnector:              workspace.gscConnector,
			AiChat:                    workspace.aiChat,
			AiMonthlyMessageLimit:     workspace.aiMonthlyMessageLimit,
			AiAllowedReasoningEfforts: workspace.aiAllowedReasoningEfforts,
			UpdatedByUserID:           editorID,
		}); err != nil {
			serverError(w, r, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	a.handleAdminListFeatures(w, r)
}
