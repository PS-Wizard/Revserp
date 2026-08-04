package app

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/app/aitools"
	internalauth "github.com/ps-wizard/revserp/internal/auth"
)

// Feature names the gateable product surfaces. AI tools are gated separately,
// per tool, because they are not a fixed set.
type Feature string

const (
	FeatureAutoCrawl    Feature = "auto_crawl"
	FeatureGSCConnector Feature = "gsc_connector"
	FeatureAIChat       Feature = "ai_chat"
)

// AIToolGroup is a presentation grouping of AI tools for the admin matrix.
// Gating is stored per tool; groups only decide how the checkboxes are drawn,
// so adding a tool to a group never requires a migration.
type AIToolGroup string

const (
	AIToolGroupRead   AIToolGroup = "read"
	AIToolGroupWrite  AIToolGroup = "write"
	AIToolGroupExport AIToolGroup = "export"
	AIToolGroupNav    AIToolGroup = "nav"
)

// aiToolGroups maps each group to its member tools. Every tool registered in
// aitools.NewRegistry must appear in exactly one group; TestAIToolGroupsCoverRegistry
// fails if a newly added tool is left out.
var aiToolGroups = map[AIToolGroup][]string{
	AIToolGroupRead: {
		"list_projects",
		"get_business_profile",
		"get_score_summary",
		"list_issues",
		"get_recommended_fix",
		"get_page_content",
		"list_pages",
		"get_search_console_data",
	},
	AIToolGroupWrite: {
		"update_business_profile",
		"start_crawl",
		"configure_auto_crawl",
	},
	AIToolGroupExport: {
		"export_crawl",
		"export_audit",
	},
	AIToolGroupNav: {
		"navigate",
		"switch_project",
		"compare_projects",
		"render_chart",
	},
}

// AIToolGroupOrder is the left-to-right column order of the admin matrix.
var AIToolGroupOrder = []AIToolGroup{AIToolGroupRead, AIToolGroupWrite, AIToolGroupExport, AIToolGroupNav}

// ToolsInGroup returns the tools belonging to one group.
func ToolsInGroup(group AIToolGroup) []string {
	return aiToolGroups[group]
}

// OrgFeatures is one workspace's resolved gating state.
type OrgFeatures struct {
	AutoCrawl       bool
	GSCConnector    bool
	AIChat          bool
	disabledAITools map[string]struct{}
}

// allFeaturesEnabled is the denylist default, used for a workspace with no
// organization_features row and as the safe value when resolution is skipped.
func allFeaturesEnabled() OrgFeatures {
	return OrgFeatures{
		AutoCrawl:       true,
		GSCConnector:    true,
		AIChat:          true,
		disabledAITools: map[string]struct{}{},
	}
}

// Enabled reports whether one top-level feature is on.
func (features OrgFeatures) Enabled(feature Feature) bool {
	switch feature {
	case FeatureAutoCrawl:
		return features.AutoCrawl
	case FeatureGSCConnector:
		return features.GSCConnector
	case FeatureAIChat:
		return features.AIChat
	}
	// An unrecognized feature is not something an admin can have disabled, so
	// failing open here matches the denylist default rather than silently
	// blocking a surface nobody gated.
	return true
}

// AIToolEnabled reports whether one AI tool may be offered or executed.
// Disabling AI chat disables every tool with it: the tools only exist to serve
// that surface, so leaving them callable would be a gap.
func (features OrgFeatures) AIToolEnabled(toolName string) bool {
	if !features.AIChat {
		return false
	}
	_, disabled := features.disabledAITools[toolName]
	return !disabled
}

// DisabledAITools returns the disabled tool names in stable order.
func (features OrgFeatures) DisabledAITools() []string {
	names := make([]string, 0, len(features.disabledAITools))
	for name := range features.disabledAITools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// featuresFromRow builds the resolved state from any of the feature query rows,
// which all share the same four columns.
func featuresFromRow(autoCrawl, gscConnector, aiChat bool, disabledTools []string) OrgFeatures {
	disabled := make(map[string]struct{}, len(disabledTools))
	for _, name := range disabledTools {
		disabled[name] = struct{}{}
	}
	return OrgFeatures{
		AutoCrawl:       autoCrawl,
		GSCConnector:    gscConnector,
		AIChat:          aiChat,
		disabledAITools: disabled,
	}
}

type orgFeaturesContextKey struct{}

// featuresFromContext returns features resolved earlier in the request, if the
// request passed through a gating middleware.
func featuresFromContext(ctx context.Context) (OrgFeatures, bool) {
	features, ok := ctx.Value(orgFeaturesContextKey{}).(OrgFeatures)
	return features, ok
}

// OrgFeaturesForOrg resolves one workspace's features. A workspace that has
// never been restricted has no row, which the query COALESCEs to all-enabled.
func (a *App) OrgFeaturesForOrg(ctx context.Context, orgID pgtype.UUID) (OrgFeatures, error) {
	row, err := a.Queries.GetOrganizationFeatures(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return allFeaturesEnabled(), nil
		}
		return allFeaturesEnabled(), err
	}
	return featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.DisabledAiTools), nil
}

// orgFeatureResolver loads the features governing one request. Each gated route
// shape needs its own, because the workspace is reached differently: through a
// project path param, a conversation, a crawl, or the caller's active org.
type orgFeatureResolver func(*App, *http.Request) (OrgFeatures, error)

// featuresByProjectParam resolves through the {projectID} path parameter.
func featuresByProjectParam(a *App, r *http.Request) (OrgFeatures, error) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		return allFeaturesEnabled(), err
	}
	row, err := a.Queries.GetOrganizationFeaturesByProjectID(r.Context(), projectID)
	if err != nil {
		return allFeaturesEnabled(), err
	}
	return featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.DisabledAiTools), nil
}

// featuresByConversationParam resolves through the {conversationID} path parameter.
func featuresByConversationParam(a *App, r *http.Request) (OrgFeatures, error) {
	conversationID, err := parseUUIDParam(chi.URLParam(r, "conversationID"))
	if err != nil {
		return allFeaturesEnabled(), err
	}
	row, err := a.Queries.GetOrganizationFeaturesByConversationID(r.Context(), conversationID)
	if err != nil {
		return allFeaturesEnabled(), err
	}
	return featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.DisabledAiTools), nil
}

// featuresByActiveOrg resolves through the caller's active workspace, for routes
// that carry no scoping path parameter. The active workspace lives on the
// session, and is reconciled against the user's memberships the same way /me
// does it, so a stale session pointer cannot gate against a workspace the user
// has since left.
func featuresByActiveOrg(a *App, r *http.Request) (OrgFeatures, error) {
	session, ok := internalauth.SessionFromContext(r.Context())
	if !ok {
		return allFeaturesEnabled(), errors.New("missing session")
	}

	_, organizations, err := a.ensureCurrentUser(r, a.Queries)
	if err != nil {
		return allFeaturesEnabled(), err
	}

	activeOrgID := resolveActiveOrganizationID(session.ActiveOrgID, organizations)
	if !activeOrgID.Valid {
		return allFeaturesEnabled(), nil
	}
	return a.OrgFeaturesForOrg(r.Context(), activeOrgID)
}

// requireFeature gates a route group on one feature, resolving the governing
// workspace with the given resolver and caching the result on the request so a
// handler can reuse it (notably the AI agent, which needs the tool denylist).
//
// A resolution error is treated as not-found rather than open: the alternative
// is serving a gated feature whenever a lookup fails.
func (a *App) requireFeature(feature Feature, resolve orgFeatureResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			features, err := resolve(a, r)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeJSONError(w, http.StatusNotFound, "not found")
					return
				}
				serverError(w, r, err)
				return
			}

			if !features.Enabled(feature) {
				writeJSONError(w, http.StatusForbidden, "feature not enabled for this workspace")
				return
			}

			ctx := context.WithValue(r.Context(), orgFeaturesContextKey{}, features)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// featureGatedRegistry hides a workspace's disabled AI tools. It satisfies the
// same narrow port the agent loop already depends on, so both places that reach
// for a tool are covered by one wrapper: Defs decides what the model is offered,
// and Get decides what it can actually run.
//
// Get returning false makes the agent loop answer "unknown tool", which is the
// right story for the model — a disabled tool should look absent, not forbidden.
type featureGatedRegistry struct {
	inner    agentToolRegistry
	features OrgFeatures
}

func (registry featureGatedRegistry) Defs() []ai.ToolDef {
	defs := registry.inner.Defs()
	// Zero-capacity reslice: never write into the registry's own backing array.
	allowed := defs[:0:0]
	for _, def := range defs {
		if registry.features.AIToolEnabled(def.Name) {
			allowed = append(allowed, def)
		}
	}
	return allowed
}

func (registry featureGatedRegistry) Get(name string) (aitools.Tool, bool) {
	if !registry.features.AIToolEnabled(name) {
		return aitools.Tool{}, false
	}
	return registry.inner.Get(name)
}

// gateRegistryForRequest wraps the shared registry with the workspace gating
// resolved for this request. A request that never passed a gating middleware
// keeps the full tool set, matching the enabled-by-default rule.
func gateRegistryForRequest(ctx context.Context, registry agentToolRegistry) agentToolRegistry {
	features, ok := featuresFromContext(ctx)
	if !ok {
		return registry
	}
	return featureGatedRegistry{inner: registry, features: features}
}

// orgFeaturesResponse is the wire shape shared by /me and the admin matrix.
type orgFeaturesResponse struct {
	AutoCrawl       bool     `json:"auto_crawl"`
	GSCConnector    bool     `json:"gsc_connector"`
	AIChat          bool     `json:"ai_chat"`
	DisabledAITools []string `json:"disabled_ai_tools"`
}

func newOrgFeaturesResponse(features OrgFeatures) orgFeaturesResponse {
	return orgFeaturesResponse{
		AutoCrawl:       features.AutoCrawl,
		GSCConnector:    features.GSCConnector,
		AIChat:          features.AIChat,
		DisabledAITools: features.DisabledAITools(),
	}
}
