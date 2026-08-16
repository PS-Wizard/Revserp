package app

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// adminWorkspaceFeaturesResponse is one row of the admin matrix.
type adminWorkspaceFeaturesResponse struct {
	OrgID                        string   `json:"org_id"`
	OrgName                      string   `json:"org_name"`
	AutoCrawl                    bool     `json:"auto_crawl"`
	GSCConnector                 bool     `json:"gsc_connector"`
	AIChat                       bool     `json:"ai_chat"`
	AIUseInternalPrompt          bool     `json:"ai_use_internal_prompt"`
	AIMonthlyMessageLimit        int32    `json:"ai_monthly_message_limit"`
	AIConcurrentTurnLimitPerUser int32    `json:"ai_concurrent_turn_limit_per_user"`
	AIAllowedReasoningEfforts    []string `json:"ai_allowed_reasoning_efforts"`
	DisabledAITools              []string `json:"disabled_ai_tools"`
	UpdatedAt                    string   `json:"updated_at,omitempty"`
}

// adminAIToolInfo is one tool in the admin catalog.
type adminAIToolInfo struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// GatedByFeature names an organization feature flag the tool depends on
	// (e.g. gsc_connector). Empty means standalone. The frontend uses it to
	// disable the toggle while the feature is off.
	GatedByFeature string `json:"gated_by_feature,omitempty"`
}

type adminFeaturesResponse struct {
	Workspaces []adminWorkspaceFeaturesResponse `json:"workspaces"`
	AITools    []adminAIToolInfo                `json:"ai_tools"`
}

// aiToolCatalogNames lists all implemented AI tool names in catalog order.
func aiToolCatalogNames() []string {
	defs := aichattools.CatalogDefs()
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	return names
}

// adminAIToolCatalog describes all implemented tools for the admin matrix.
func adminAIToolCatalog() []adminAIToolInfo {
	defs := aichattools.CatalogDefs()
	infos := make([]adminAIToolInfo, 0, len(defs))
	for _, def := range defs {
		infos = append(infos, adminAIToolInfo{Name: def.Name, Label: def.Label, Description: def.Description, GatedByFeature: def.Feature})
	}
	return infos
}

// normalizeDisabledAITools drops empty and unknown names, dedupes, orders the
// result by catalog order, and force-disables tools whose feature flag is off.
func normalizeDisabledAITools(tools []string, gscConnector bool) []string {
	disabled := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if tool != "" {
			disabled[tool] = true
		}
	}
	for name, feature := range aichattools.ToolFeatures() {
		if feature == "gsc_connector" && !gscConnector {
			disabled[name] = true
		}
	}
	normalized := make([]string, 0, len(tools))
	for _, name := range aiToolCatalogNames() {
		if disabled[name] {
			normalized = append(normalized, name)
		}
	}
	return normalized
}

// validateDisabledAITools rejects names outside the registry catalog and
// returns the normalized list (feature-off tools force-disabled).
func validateDisabledAITools(tools []string, gscConnector bool) ([]string, error) {
	for _, tool := range tools {
		if tool != "" && !containsString(aiToolCatalogNames(), tool) {
			return nil, fmt.Errorf("unknown ai tool %q; valid tools: %s", tool, strings.Join(aiToolCatalogNames(), ", "))
		}
	}
	return normalizeDisabledAITools(tools, gscConnector), nil
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
			OrgID:                        row.OrgID.String(),
			OrgName:                      row.OrgName,
			AutoCrawl:                    row.AutoCrawl,
			GSCConnector:                 row.GscConnector,
			AIChat:                       row.AiChat,
			AIUseInternalPrompt:          row.AiUseInternalPrompt,
			AIMonthlyMessageLimit:        row.AiMonthlyMessageLimit,
			AIConcurrentTurnLimitPerUser: row.AiConcurrentTurnLimitPerUser,
			AIAllowedReasoningEfforts:    normalizeAIReasoningEfforts(row.AiAllowedReasoningEfforts),
			DisabledAITools:              normalizeDisabledAITools(row.DisabledAiTools, row.GscConnector),
		}
		if row.UpdatedAt.Valid {
			workspace.UpdatedAt = row.UpdatedAt.Time.UTC().Format(time.RFC3339)
		}
		workspaces = append(workspaces, workspace)
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, adminFeaturesResponse{Workspaces: workspaces, AITools: adminAIToolCatalog()})
}

type adminPutFeaturesRequest struct {
	Workspaces []adminPutWorkspaceFeatures `json:"workspaces"`
}

type adminPutWorkspaceFeatures struct {
	OrgID                        string   `json:"org_id"`
	AutoCrawl                    bool     `json:"auto_crawl"`
	GSCConnector                 bool     `json:"gsc_connector"`
	AIChat                       bool     `json:"ai_chat"`
	AIUseInternalPrompt          bool     `json:"ai_use_internal_prompt"`
	AIMonthlyMessageLimit        int32    `json:"ai_monthly_message_limit"`
	AIConcurrentTurnLimitPerUser int32    `json:"ai_concurrent_turn_limit_per_user"`
	AIAllowedReasoningEfforts    []string `json:"ai_allowed_reasoning_efforts"`
	DisabledAITools              []string `json:"disabled_ai_tools"`
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
		orgID                        pgtype.UUID
		autoCrawl                    bool
		gscConnector                 bool
		aiChat                       bool
		aiUseInternalPrompt          bool
		aiMonthlyMessageLimit        int32
		aiConcurrentTurnLimitPerUser int32
		aiAllowedReasoningEfforts    []string
		disabledAITools              []string
	}
	parsed := make([]parsedWorkspace, 0, len(requestBody.Workspaces))
	for _, workspace := range requestBody.Workspaces {
		orgID, err := parseUUIDParam(workspace.OrgID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid org id")
			return
		}
		normalizedEfforts, err := validateAIChatSettings(workspace.AIMonthlyMessageLimit, workspace.AIConcurrentTurnLimitPerUser, workspace.AIAllowedReasoningEfforts)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		normalizedTools, err := validateDisabledAITools(workspace.DisabledAITools, workspace.GSCConnector)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		parsed = append(parsed, parsedWorkspace{
			orgID:                        orgID,
			autoCrawl:                    workspace.AutoCrawl,
			gscConnector:                 workspace.GSCConnector,
			aiChat:                       workspace.AIChat,
			aiUseInternalPrompt:          workspace.AIUseInternalPrompt,
			aiMonthlyMessageLimit:        workspace.AIMonthlyMessageLimit,
			aiConcurrentTurnLimitPerUser: workspace.AIConcurrentTurnLimitPerUser,
			aiAllowedReasoningEfforts:    normalizedEfforts,
			disabledAITools:              normalizedTools,
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
			OrgID:                        workspace.orgID,
			AutoCrawl:                    workspace.autoCrawl,
			GscConnector:                 workspace.gscConnector,
			AiChat:                       workspace.aiChat,
			AiUseInternalPrompt:          workspace.aiUseInternalPrompt,
			AiMonthlyMessageLimit:        workspace.aiMonthlyMessageLimit,
			AiConcurrentTurnLimitPerUser: workspace.aiConcurrentTurnLimitPerUser,
			AiAllowedReasoningEfforts:    workspace.aiAllowedReasoningEfforts,
			DisabledAiTools:              workspace.disabledAITools,
			UpdatedByUserID:              editorID,
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
