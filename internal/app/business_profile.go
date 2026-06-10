package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type upsertProjectBusinessProfileRequest struct {
	BrandName           string `json:"brand_name"`
	WebsiteURL          string `json:"website_url"`
	PrimaryCategory     string `json:"primary_category"`
	PrimaryLocation     string `json:"primary_location"`
	BusinessDescription string `json:"business_description"`
}

type projectBusinessProfileResponse struct {
	ID                  string `json:"id"`
	ProjectID           string `json:"project_id"`
	BrandName           string `json:"brand_name"`
	WebsiteURL          string `json:"website_url"`
	PrimaryCategory     string `json:"primary_category,omitempty"`
	PrimaryLocation     string `json:"primary_location,omitempty"`
	BusinessDescription string `json:"business_description,omitempty"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

type projectBusinessProfileStatusResponse struct {
	HasProfile       bool                            `json:"has_profile"`
	CanManageProfile bool                            `json:"can_manage_profile"`
	BusinessProfile  *projectBusinessProfileResponse `json:"business_profile,omitempty"`
}

// handleProjectBusinessProfile returns one project's business profile for any project member.
func (a *App) handleProjectBusinessProfile(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback(r.Context())

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	project, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	membership, err := queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{
		OrgID:  project.OrganizationID,
		UserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	profile, hasProfile, err := getProjectBusinessProfileByProjectID(r.Context(), queries, project.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	response := projectBusinessProfileStatusResponse{
		HasProfile:       hasProfile,
		CanManageProfile: membership.Role == "owner",
	}
	if hasProfile {
		profileResponse := newProjectBusinessProfileResponse(profile)
		response.BusinessProfile = &profileResponse
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

// handleUpsertProjectBusinessProfile stores one owner-managed business profile for a project.
func (a *App) handleUpsertProjectBusinessProfile(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var requestBody upsertProjectBusinessProfileRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	brandName := strings.TrimSpace(requestBody.BrandName)
	websiteURL := strings.TrimSpace(requestBody.WebsiteURL)
	primaryCategory := strings.TrimSpace(requestBody.PrimaryCategory)
	primaryLocation := strings.TrimSpace(requestBody.PrimaryLocation)
	businessDescription := strings.TrimSpace(requestBody.BusinessDescription)
	if brandName == "" || websiteURL == "" {
		writeJSONError(w, http.StatusBadRequest, "brand_name and website_url are required")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback(r.Context())

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	project, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := requireOrganizationOwner(r.Context(), queries, project.OrganizationID, user.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}

	profile, err := queries.UpsertProjectBusinessProfile(r.Context(), sqlc.UpsertProjectBusinessProfileParams{
		ProjectID:           project.ID,
		BrandName:           brandName,
		WebsiteUrl:          websiteURL,
		PrimaryCategory:     pgText(primaryCategory),
		PrimaryLocation:     pgText(primaryLocation),
		BusinessDescription: pgText(businessDescription),
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, newProjectBusinessProfileResponse(profile))
}

func getProjectBusinessProfileByProjectID(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID) (sqlc.ProjectBusinessProfile, bool, error) {
	profile, err := queries.GetProjectBusinessProfileByProjectID(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.ProjectBusinessProfile{}, false, nil
		}
		return sqlc.ProjectBusinessProfile{}, false, err
	}
	return profile, true, nil
}

func newProjectBusinessProfileResponse(profile sqlc.ProjectBusinessProfile) projectBusinessProfileResponse {
	return projectBusinessProfileResponse{
		ID:                  profile.ID.String(),
		ProjectID:           profile.ProjectID.String(),
		BrandName:           profile.BrandName,
		WebsiteURL:          profile.WebsiteUrl,
		PrimaryCategory:     textValue(profile.PrimaryCategory),
		PrimaryLocation:     textValue(profile.PrimaryLocation),
		BusinessDescription: textValue(profile.BusinessDescription),
		CreatedAt:           profile.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:           profile.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}
}
