package app

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// adminWorkspaceFeaturesResponse is one row of the admin matrix.
type adminWorkspaceFeaturesResponse struct {
	OrgID                         string   `json:"org_id"`
	OrgName                       string   `json:"org_name"`
	AutoCrawl                     bool     `json:"auto_crawl"`
	GSCConnector                  bool     `json:"gsc_connector"`
	AIChat                        bool     `json:"ai_chat"`
	Integrations                  bool     `json:"integrations"`
	AIUseInternalPrompt           bool     `json:"ai_use_internal_prompt"`
	AIMonthlyMessageLimit         int32    `json:"ai_monthly_message_limit"`
	AIVisibilityAuditMonthlyLimit int32    `json:"ai_visibility_audit_monthly_limit"`
	MaxCompetitors                int32    `json:"max_competitors"`
	MaxProjects                   int32    `json:"max_projects"`
	AIConcurrentTurnLimitPerUser  int32    `json:"ai_concurrent_turn_limit_per_user"`
	AIAllowedReasoningEfforts     []string `json:"ai_allowed_reasoning_efforts"`
	DisabledAITools               []string `json:"disabled_ai_tools"`
	UpdatedAt                     string   `json:"updated_at,omitempty"`
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

// dynamicMCPToolName reports whether name is a canonical generic MCP model
// alias, or a historical cms__/wp__ name kept for saved denylist history
// only. It accepts aliases this build's static catalogue has never seen, so
// a newly discovered tool can be disabled; it grants nothing, because the
// executing registry still comes only from the session's discovered tools.
// No legacy execution aliases exist: history-only names never reach the model.
func dynamicMCPToolName(name string) bool {
	if aichattools.IsMCPModelToolName(name) {
		return true
	}
	return isHistoricalCMSNamespacedName(name)
}

// isHistoricalCMSNamespacedName matches the retired cms__/wp__ spellings so
// previously saved denylists keep validating. It checks the retired local
// grammar (no double underscore, ASCII word characters, max 128) without
// importing the retired tool packages. Names that were never valid, like
// cms__a__b, stay rejected.
func isHistoricalCMSNamespacedName(name string) bool {
	for _, prefix := range []string{"cms__", "wp__"} {
		suffix, ok := cutPrefix(name, prefix)
		if ok && isHistoricalCMSToolName(suffix) {
			return true
		}
	}
	return false
}

func cutPrefix(name, prefix string) (string, bool) {
	if len(name) > len(prefix) && name[:len(prefix)] == prefix {
		return name[len(prefix):], true
	}
	return "", false
}

// isHistoricalCMSToolName is the retired transport name grammar: bounded
// ASCII letters, digits, underscores and dashes, first character
// alphanumeric, no doubled underscore.
func isHistoricalCMSToolName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	if strings.Contains(name, "__") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if !valid || i == 0 && !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// normalizeDisabledAITools drops empty and unknown names, dedupes, orders the
// catalog entries first and any valid dynamic MCP alias after them, and
// force-disables tools whose feature flag is off.
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
	seen := make(map[string]bool, len(tools))
	for _, name := range aiToolCatalogNames() {
		if disabled[name] {
			normalized = append(normalized, name)
			seen[name] = true
		}
	}
	dynamic := make([]string, 0, len(tools))
	for name := range disabled {
		if !seen[name] && dynamicMCPToolName(name) {
			dynamic = append(dynamic, name)
		}
	}
	sort.Strings(dynamic)
	return append(normalized, dynamic...)
}

// validateDisabledAITools rejects names outside the registry catalog and
// returns the normalized list (feature-off tools force-disabled). Valid
// dynamic MCP aliases (plus historical names for saved history) are accepted too; a guessed native name
// still fails.
func validateDisabledAITools(tools []string, gscConnector bool) ([]string, error) {
	for _, tool := range tools {
		if tool != "" && !containsString(aiToolCatalogNames(), tool) && !dynamicMCPToolName(tool) {
			return nil, fmt.Errorf("unknown ai tool %q; valid tools: %s", tool, strings.Join(aiToolCatalogNames(), ", "))
		}
	}
	return normalizeDisabledAITools(tools, gscConnector), nil
}

// validateMaxCompetitors rejects negative competitor limits;
// 0 disables competitor analysis entirely.
func validateMaxCompetitors(limit int32) error {
	if limit < 0 {
		return fmt.Errorf("max_competitors must be >= 0")
	}
	return nil
}

// validateMaxProjects rejects negative project limits;
// 0 disables creation of new projects entirely.
func validateMaxProjects(limit int32) error {
	if limit < 0 {
		return fmt.Errorf("max_projects must be >= 0")
	}
	return nil
}

// validateAIVisibilityAuditMonthlyLimit rejects negative audit limits;
// 0 disables visibility audits entirely (reserve never succeeds).
func validateAIVisibilityAuditMonthlyLimit(limit int32) error {
	if limit < 0 {
		return fmt.Errorf("ai_visibility_audit_monthly_limit must be >= 0")
	}
	return nil
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
			OrgID:                         row.OrgID.String(),
			OrgName:                       row.OrgName,
			AutoCrawl:                     row.AutoCrawl,
			GSCConnector:                  row.GscConnector,
			AIChat:                        row.AiChat,
			Integrations:                  row.Integrations,
			AIUseInternalPrompt:           row.AiUseInternalPrompt,
			AIMonthlyMessageLimit:         row.AiMonthlyMessageLimit,
			AIVisibilityAuditMonthlyLimit: row.AiVisibilityAuditMonthlyLimit,
			MaxCompetitors:                row.MaxCompetitors,
			MaxProjects:                   row.MaxProjects,
			AIConcurrentTurnLimitPerUser:  row.AiConcurrentTurnLimitPerUser,
			AIAllowedReasoningEfforts:     normalizeAIReasoningEfforts(row.AiAllowedReasoningEfforts),
			DisabledAITools:               normalizeDisabledAITools(row.DisabledAiTools, row.GscConnector),
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
	OrgID                         string   `json:"org_id"`
	AutoCrawl                     bool     `json:"auto_crawl"`
	GSCConnector                  bool     `json:"gsc_connector"`
	AIChat                        bool     `json:"ai_chat"`
	Integrations                  bool     `json:"integrations"`
	AIUseInternalPrompt           bool     `json:"ai_use_internal_prompt"`
	AIMonthlyMessageLimit         int32    `json:"ai_monthly_message_limit"`
	AIVisibilityAuditMonthlyLimit int32    `json:"ai_visibility_audit_monthly_limit"`
	MaxCompetitors                int32    `json:"max_competitors"`
	MaxProjects                   int32    `json:"max_projects"`
	AIConcurrentTurnLimitPerUser  int32    `json:"ai_concurrent_turn_limit_per_user"`
	AIAllowedReasoningEfforts     []string `json:"ai_allowed_reasoning_efforts"`
	DisabledAITools               []string `json:"disabled_ai_tools"`
}

// handleAdminPutFeatures saves the edited rows in one transaction.
func (a *App) handleAdminPutFeatures(w http.ResponseWriter, r *http.Request) {
	var requestBody adminPutFeaturesRequest
	if !readJSONOrRespond(w, r, &requestBody) {
		return
	}
	if len(requestBody.Workspaces) == 0 {
		writeJSONError(w, http.StatusBadRequest, "no workspaces supplied")
		return
	}

	type parsedWorkspace struct {
		orgID                         pgtype.UUID
		autoCrawl                     bool
		gscConnector                  bool
		aiChat                        bool
		integrations                  bool
		aiUseInternalPrompt           bool
		aiMonthlyMessageLimit         int32
		aiVisibilityAuditMonthlyLimit int32
		maxCompetitors                int32
		maxProjects                   int32
		aiConcurrentTurnLimitPerUser  int32
		aiAllowedReasoningEfforts     []string
		disabledAITools               []string
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
		if err := validateAIVisibilityAuditMonthlyLimit(workspace.AIVisibilityAuditMonthlyLimit); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := validateMaxCompetitors(workspace.MaxCompetitors); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := validateMaxProjects(workspace.MaxProjects); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		normalizedTools, err := validateDisabledAITools(workspace.DisabledAITools, workspace.GSCConnector)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		parsed = append(parsed, parsedWorkspace{
			orgID:                         orgID,
			autoCrawl:                     workspace.AutoCrawl,
			gscConnector:                  workspace.GSCConnector,
			aiChat:                        workspace.AIChat,
			integrations:                  workspace.Integrations,
			aiUseInternalPrompt:           workspace.AIUseInternalPrompt,
			aiMonthlyMessageLimit:         workspace.AIMonthlyMessageLimit,
			aiVisibilityAuditMonthlyLimit: workspace.AIVisibilityAuditMonthlyLimit,
			maxCompetitors:                workspace.MaxCompetitors,
			maxProjects:                   workspace.MaxProjects,
			aiConcurrentTurnLimitPerUser:  workspace.AIConcurrentTurnLimitPerUser,
			aiAllowedReasoningEfforts:     normalizedEfforts,
			disabledAITools:               normalizedTools,
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
			OrgID:                         workspace.orgID,
			AutoCrawl:                     workspace.autoCrawl,
			GscConnector:                  workspace.gscConnector,
			AiChat:                        workspace.aiChat,
			Integrations:                  workspace.integrations,
			AiUseInternalPrompt:           workspace.aiUseInternalPrompt,
			AiMonthlyMessageLimit:         workspace.aiMonthlyMessageLimit,
			AiVisibilityAuditMonthlyLimit: workspace.aiVisibilityAuditMonthlyLimit,
			MaxCompetitors:                workspace.maxCompetitors,
			MaxProjects:                   workspace.maxProjects,
			AiConcurrentTurnLimitPerUser:  workspace.aiConcurrentTurnLimitPerUser,
			AiAllowedReasoningEfforts:     workspace.aiAllowedReasoningEfforts,
			DisabledAiTools:               workspace.disabledAITools,
			UpdatedByUserID:               editorID,
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
