package app

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Feature names a gateable product surface.
type Feature string

const (
	FeatureAutoCrawl    Feature = "auto_crawl"
	FeatureGSCConnector Feature = "gsc_connector"
	// FeatureAIChat remains the workspace switch for the future chat rewrite.
	FeatureAIChat Feature = "ai_chat"
)

// OrgFeatures is one workspace's resolved gating state.
type OrgFeatures struct {
	AutoCrawl    bool
	GSCConnector bool
	AIChat       bool
}

// allFeaturesEnabled is used for a workspace with no organization_features row.
func allFeaturesEnabled() OrgFeatures {
	return OrgFeatures{AutoCrawl: true, GSCConnector: true, AIChat: true}
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

func featuresFromRow(autoCrawl, gscConnector, aiChat bool) OrgFeatures {
	return OrgFeatures{AutoCrawl: autoCrawl, GSCConnector: gscConnector, AIChat: aiChat}
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
	return featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat), nil
}

type orgFeatureResolver func(*App, *http.Request) (OrgFeatures, error)

// featuresByProjectParam resolves a project-scoped route to its workspace.
func featuresByProjectParam(a *App, r *http.Request) (OrgFeatures, error) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		return allFeaturesEnabled(), err
	}
	row, err := a.Queries.GetOrganizationFeaturesByProjectID(r.Context(), projectID)
	if err != nil {
		return allFeaturesEnabled(), err
	}
	return featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat), nil
}

// requireFeature gates a route group on its governing workspace feature.
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
			next.ServeHTTP(w, r)
		})
	}
}

// orgFeaturesResponse is the wire shape shared by /me and the admin matrix.
type orgFeaturesResponse struct {
	AutoCrawl    bool `json:"auto_crawl"`
	GSCConnector bool `json:"gsc_connector"`
	AIChat       bool `json:"ai_chat"`
}

func newOrgFeaturesResponse(features OrgFeatures) orgFeaturesResponse {
	return orgFeaturesResponse{AutoCrawl: features.AutoCrawl, GSCConnector: features.GSCConnector, AIChat: features.AIChat}
}
