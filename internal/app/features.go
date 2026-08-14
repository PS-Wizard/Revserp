package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Feature names a gateable product surface.
type Feature string

const (
	FeatureAutoCrawl    Feature = "auto_crawl"
	FeatureGSCConnector Feature = "gsc_connector"
	// FeatureAIChat remains the workspace switch for the future chat rewrite.
	FeatureAIChat Feature = "ai_chat"
)

const (
	defaultAIMonthlyMessageLimit int32 = 50
)

var canonicalAIReasoningEfforts = []string{"none", "low", "high", "max"}

// OrgFeatures is one workspace's resolved gating state.
type OrgFeatures struct {
	AutoCrawl                 bool
	GSCConnector              bool
	AIChat                    bool
	AIMonthlyMessageLimit     int32
	AIAllowedReasoningEfforts []string
}

// allFeaturesEnabled is used for a workspace with no organization_features row.
func allFeaturesEnabled() OrgFeatures {
	return OrgFeatures{
		AutoCrawl:                 true,
		GSCConnector:              true,
		AIChat:                    true,
		AIMonthlyMessageLimit:     defaultAIMonthlyMessageLimit,
		AIAllowedReasoningEfforts: append([]string(nil), canonicalAIReasoningEfforts...),
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
	default:
		return true
	}
}

func normalizeAIReasoningEfforts(efforts []string) []string {
	hasEffort := make(map[string]bool, len(efforts))
	for _, effort := range efforts {
		hasEffort[effort] = true
	}
	normalized := make([]string, 0, len(efforts))
	for _, effort := range canonicalAIReasoningEfforts {
		if hasEffort[effort] {
			normalized = append(normalized, effort)
		}
	}
	return normalized
}

func validateAIChatSettings(limit int32, efforts []string) ([]string, error) {
	if limit < 0 || limit > 1000000 {
		return nil, fmt.Errorf("ai_monthly_message_limit must be between 0 and 1000000")
	}
	if len(efforts) == 0 {
		return nil, fmt.Errorf("ai_allowed_reasoning_efforts must not be empty")
	}
	seen := make(map[string]struct{}, len(efforts))
	for _, effort := range efforts {
		if _, ok := seen[effort]; ok {
			return nil, fmt.Errorf("ai_allowed_reasoning_efforts must not contain duplicates")
		}
		seen[effort] = struct{}{}
		if !containsString(canonicalAIReasoningEfforts, effort) {
			return nil, fmt.Errorf("invalid ai reasoning effort")
		}
	}
	return normalizeAIReasoningEfforts(efforts), nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func featuresFromRow(autoCrawl, gscConnector, aiChat bool, monthlyLimit int32, efforts []string) OrgFeatures {
	return OrgFeatures{
		AutoCrawl:                 autoCrawl,
		GSCConnector:              gscConnector,
		AIChat:                    aiChat,
		AIMonthlyMessageLimit:     monthlyLimit,
		AIAllowedReasoningEfforts: normalizeAIReasoningEfforts(efforts),
	}
}

// OrgFeaturesForOrg resolves one workspace's features.
func (a *App) OrgFeaturesForOrg(ctx context.Context, orgID pgtype.UUID) (OrgFeatures, error) {
	row, err := a.Queries.GetOrganizationFeatures(ctx, orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return allFeaturesEnabled(), nil
		}
		return allFeaturesEnabled(), err
	}
	return featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.AiMonthlyMessageLimit, row.AiAllowedReasoningEfforts), nil
}

type orgFeatureResolver func(*App, *http.Request) (OrgFeatures, error)

type featureParamError string

func (err featureParamError) Error() string { return string(err) }

// featuresByProjectParam resolves a project-scoped route to its workspace.
func featuresByProjectParam(a *App, r *http.Request) (OrgFeatures, error) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		return allFeaturesEnabled(), featureParamError("invalid project id")
	}
	user, _, err := a.ensureCurrentUser(r, a.Queries)
	if err != nil {
		return allFeaturesEnabled(), err
	}
	row, err := a.Queries.GetOrganizationFeaturesByProjectID(r.Context(), sqlc.GetOrganizationFeaturesByProjectIDParams{
		ProjectID: projectID,
		UserID:    user.ID,
	})
	if err != nil {
		return allFeaturesEnabled(), err
	}
	return featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.AiMonthlyMessageLimit, row.AiAllowedReasoningEfforts), nil
}

// featuresByConversationParam resolves a conversation route to its project workspace.
func featuresByConversationParam(a *App, r *http.Request) (OrgFeatures, error) {
	conversationID, err := parseUUIDParam(chi.URLParam(r, "conversationID"))
	if err != nil {
		return allFeaturesEnabled(), featureParamError("invalid conversation id")
	}
	user, _, err := a.ensureCurrentUser(r, a.Queries)
	if err != nil {
		return allFeaturesEnabled(), err
	}
	row, err := a.Queries.GetOrganizationFeaturesByConversationID(r.Context(), sqlc.GetOrganizationFeaturesByConversationIDParams{
		ConversationID: conversationID,
		UserID:         user.ID,
	})
	if err != nil {
		return allFeaturesEnabled(), err
	}
	return featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.AiMonthlyMessageLimit, row.AiAllowedReasoningEfforts), nil
}

// requireFeature gates a route group on its governing workspace feature.
func (a *App) requireFeature(feature Feature, resolve orgFeatureResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			features, err := resolve(a, r)
			if err != nil {
				var paramErr featureParamError
				if errors.As(err, &paramErr) {
					writeJSONError(w, http.StatusBadRequest, paramErr.Error())
					return
				}
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
			next.ServeHTTP(w, r)
		})
	}
}

// orgFeaturesResponse is the wire shape shared by /me and the admin matrix.
type orgFeaturesResponse struct {
	AutoCrawl                 bool     `json:"auto_crawl"`
	GSCConnector              bool     `json:"gsc_connector"`
	AIChat                    bool     `json:"ai_chat"`
	AIMonthlyMessageLimit     int32    `json:"ai_monthly_message_limit"`
	AIAllowedReasoningEfforts []string `json:"ai_allowed_reasoning_efforts"`
}

func newOrgFeaturesResponse(features OrgFeatures) orgFeaturesResponse {
	return orgFeaturesResponse{
		AutoCrawl:                 features.AutoCrawl,
		GSCConnector:              features.GSCConnector,
		AIChat:                    features.AIChat,
		AIMonthlyMessageLimit:     features.AIMonthlyMessageLimit,
		AIAllowedReasoningEfforts: normalizeAIReasoningEfforts(features.AIAllowedReasoningEfforts),
	}
}
