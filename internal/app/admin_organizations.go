package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueengine "github.com/ps-wizard/revserp/internal/issues"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

type adminOrganizationResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

type adminOrgScoringConfigResponse struct {
	Config     issueshared.ScoringConfig `json:"config"`
	Default    issueshared.ScoringConfig `json:"default"`
	IsOverride bool                      `json:"is_override"`
	UpdatedAt  string                    `json:"updated_at,omitempty"`
}

// handleAdminListOrganizations returns all organizations.
func (a *App) handleAdminListOrganizations(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	orgs, err := a.Queries.ListAllOrganizations(r.Context(), sqlc.ListAllOrganizationsParams{Limit: limit, Offset: offset})
	if err != nil {
		serverError(w, r, err)
		return
	}

	items := make([]adminOrganizationResponse, 0, len(orgs))
	for _, org := range orgs {
		items = append(items, adminOrganizationResponse{
			ID:        org.ID.String(),
			Name:      org.Name,
			CreatedAt: org.CreatedAt.Time.Format("2006-01-02T15:04:05Z"),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"organizations": items})
}

// handleAdminGetOrgScoringConfig returns the effective scoring config for an organization.
func (a *App) handleAdminGetOrgScoringConfig(w http.ResponseWriter, r *http.Request) {
	orgID, err := parseUUIDParam(chi.URLParam(r, "orgID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}

	defaultConfig := issueengine.DefaultScoringConfig()

	row, err := a.Queries.GetOrgScoringConfig(r.Context(), orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, adminOrgScoringConfigResponse{
				Config:     defaultConfig,
				Default:    defaultConfig,
				IsOverride: false,
			})
			return
		}
		serverError(w, r, err)
		return
	}

	config, err := issueengine.ParseScoringConfig(row.ConfigJson)
	if err != nil {
		serverError(w, r, err)
		return
	}

	resp := adminOrgScoringConfigResponse{
		Config:     config,
		Default:    defaultConfig,
		IsOverride: true,
	}
	if row.UpdatedAt.Valid {
		resp.UpdatedAt = row.UpdatedAt.Time.Format("2006-01-02T15:04:05Z")
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleAdminPutOrgScoringConfig saves an organization scoring override.
func (a *App) handleAdminPutOrgScoringConfig(w http.ResponseWriter, r *http.Request) {
	orgID, err := parseUUIDParam(chi.URLParam(r, "orgID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}

	var req struct {
		Config issueshared.ScoringConfig `json:"config"`
	}
	if err := readJSON(r, &req); err != nil {
		if errors.Is(err, errRequestBodyTooLarge) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if err := issueengine.ValidateScoringConfig(req.Config); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	configJSON, err := issueengine.MustMarshalScoringConfig(req.Config)
	if err != nil {
		serverError(w, r, err)
		return
	}

	userID, err := a.currentUserID(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	row, err := a.Queries.UpsertOrgScoringConfig(r.Context(), sqlc.UpsertOrgScoringConfigParams{
		OrgID:           orgID,
		ConfigJson:      configJSON,
		UpdatedByUserID: userID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	config, err := issueengine.ParseScoringConfig(row.ConfigJson)
	if err != nil {
		serverError(w, r, err)
		return
	}

	resp := adminOrgScoringConfigResponse{
		Config:     config,
		Default:    issueengine.DefaultScoringConfig(),
		IsOverride: true,
	}
	if row.UpdatedAt.Valid {
		resp.UpdatedAt = row.UpdatedAt.Time.Format("2006-01-02T15:04:05Z")
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleAdminDeleteOrgScoringConfig removes an organization scoring override.
func (a *App) handleAdminDeleteOrgScoringConfig(w http.ResponseWriter, r *http.Request) {
	orgID, err := parseUUIDParam(chi.URLParam(r, "orgID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}

	if err := a.Queries.DeleteOrgScoringConfig(r.Context(), orgID); err != nil {
		serverError(w, r, err)
		return
	}

	defaultConfig := issueengine.DefaultScoringConfig()
	writeJSON(w, http.StatusOK, adminOrgScoringConfigResponse{
		Config:     defaultConfig,
		Default:    defaultConfig,
		IsOverride: false,
	})
}
