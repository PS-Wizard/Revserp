package app

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// adminFeatureGroupResponse describes one AI tool group so the admin UI does not
// hard-code the grouping. Adding a tool to a group on the server moves the
// checkbox without a frontend change.
type adminFeatureGroupResponse struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Tools []string `json:"tools"`
}

// adminWorkspaceFeaturesResponse is one row of the admin matrix.
type adminWorkspaceFeaturesResponse struct {
	OrgID           string   `json:"org_id"`
	OrgName         string   `json:"org_name"`
	AutoCrawl       bool     `json:"auto_crawl"`
	GSCConnector    bool     `json:"gsc_connector"`
	AIChat          bool     `json:"ai_chat"`
	DisabledAITools []string `json:"disabled_ai_tools"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

type adminFeaturesResponse struct {
	Workspaces []adminWorkspaceFeaturesResponse `json:"workspaces"`
	ToolGroups []adminFeatureGroupResponse      `json:"tool_groups"`
}

var adminFeatureGroupLabels = map[AIToolGroup]string{
	AIToolGroupRead:   "Read",
	AIToolGroupWrite:  "Write",
	AIToolGroupExport: "Export",
	AIToolGroupNav:    "Nav",
}

// handleAdminListFeatures returns every workspace's gating state plus the tool
// group definitions that shape the matrix columns.
func (a *App) handleAdminListFeatures(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Queries.ListOrganizationFeaturesForAdmin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}

	workspaces := make([]adminWorkspaceFeaturesResponse, 0, len(rows))
	for _, row := range rows {
		workspace := adminWorkspaceFeaturesResponse{
			OrgID:           row.OrgID.String(),
			OrgName:         row.OrgName,
			AutoCrawl:       row.AutoCrawl,
			GSCConnector:    row.GscConnector,
			AIChat:          row.AiChat,
			DisabledAITools: row.DisabledAiTools,
		}
		if workspace.DisabledAITools == nil {
			workspace.DisabledAITools = []string{}
		}
		if row.UpdatedAt.Valid {
			workspace.UpdatedAt = row.UpdatedAt.Time.UTC().Format(time.RFC3339)
		}
		workspaces = append(workspaces, workspace)
	}

	groups := make([]adminFeatureGroupResponse, 0, len(AIToolGroupOrder))
	for _, group := range AIToolGroupOrder {
		groups = append(groups, adminFeatureGroupResponse{
			ID:    string(group),
			Label: adminFeatureGroupLabels[group],
			Tools: ToolsInGroup(group),
		})
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, adminFeaturesResponse{Workspaces: workspaces, ToolGroups: groups})
}

// adminPutFeaturesRequest is a batch save: the admin edits the whole matrix and
// submits once, so a session of toggling costs one request and one write per
// changed workspace rather than a write per checkbox.
type adminPutFeaturesRequest struct {
	Workspaces []adminPutWorkspaceFeatures `json:"workspaces"`
}

type adminPutWorkspaceFeatures struct {
	OrgID           string   `json:"org_id"`
	AutoCrawl       bool     `json:"auto_crawl"`
	GSCConnector    bool     `json:"gsc_connector"`
	AIChat          bool     `json:"ai_chat"`
	DisabledAITools []string `json:"disabled_ai_tools"`
}

// handleAdminPutFeatures saves the edited rows of the matrix in one transaction,
// so a partial failure cannot leave half the workspaces on the new settings.
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

	knownTools := knownAIToolNames()
	type parsedWorkspace struct {
		orgID         pgtype.UUID
		autoCrawl     bool
		gscConnector  bool
		aiChat        bool
		disabledTools []string
	}

	parsed := make([]parsedWorkspace, 0, len(requestBody.Workspaces))
	for _, workspace := range requestBody.Workspaces {
		orgID, err := parseUUIDParam(workspace.OrgID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid org id")
			return
		}

		// Reject unknown tool names rather than storing them. A typo would
		// otherwise persist as a permanently-disabled phantom tool that never
		// appears in the UI and can never be re-enabled.
		disabled := make([]string, 0, len(workspace.DisabledAITools))
		seen := make(map[string]struct{}, len(workspace.DisabledAITools))
		for _, tool := range workspace.DisabledAITools {
			if _, known := knownTools[tool]; !known {
				writeJSONError(w, http.StatusBadRequest, "unknown ai tool: "+tool)
				return
			}
			if _, duplicate := seen[tool]; duplicate {
				continue
			}
			seen[tool] = struct{}{}
			disabled = append(disabled, tool)
		}

		parsed = append(parsed, parsedWorkspace{
			orgID:         orgID,
			autoCrawl:     workspace.AutoCrawl,
			gscConnector:  workspace.GSCConnector,
			aiChat:        workspace.AIChat,
			disabledTools: disabled,
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
			OrgID:           workspace.orgID,
			AutoCrawl:       workspace.autoCrawl,
			GscConnector:    workspace.gscConnector,
			AiChat:          workspace.aiChat,
			DisabledAiTools: workspace.disabledTools,
			UpdatedByUserID: editorID,
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

// knownAIToolNames is the set of tool names an admin may reference, taken from
// the groups rather than the registry so the two cannot disagree — the coverage
// test already pins the groups to the registry.
func knownAIToolNames() map[string]struct{} {
	names := make(map[string]struct{})
	for _, group := range AIToolGroupOrder {
		for _, tool := range ToolsInGroup(group) {
			names[tool] = struct{}{}
		}
	}
	return names
}
