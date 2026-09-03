package app

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/businessprofile"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type upsertProjectBusinessProfileRequest struct {
	BrandName           string   `json:"brand_name"`
	WebsiteURL          string   `json:"website_url"`
	PrimaryCategory     string   `json:"primary_category"`
	PrimaryLocation     string   `json:"primary_location"`
	BusinessDescription string   `json:"business_description"`
	SeedPrompts         []string `json:"seed_prompts"`
	TargetKeywords      []string `json:"target_keywords"`
}

type projectBusinessProfileResponse struct {
	ID                  string   `json:"id"`
	ProjectID           string   `json:"project_id"`
	BrandName           string   `json:"brand_name"`
	WebsiteURL          string   `json:"website_url"`
	PrimaryCategory     string   `json:"primary_category,omitempty"`
	PrimaryLocation     string   `json:"primary_location,omitempty"`
	BusinessDescription string   `json:"business_description,omitempty"`
	SeedPrompts         []string `json:"seed_prompts"`
	TargetKeywords      []string `json:"target_keywords"`
	CreatedAt           string   `json:"created_at"`
	UpdatedAt           string   `json:"updated_at"`
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
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
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
		profileResponse, err := newProjectBusinessProfileResponseFromGetRow(profile)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
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
	if !readJSONOrRespond(w, r, &requestBody) {
		return
	}

	brandName := strings.TrimSpace(requestBody.BrandName)
	websiteURL := strings.TrimSpace(requestBody.WebsiteURL)
	primaryCategory := strings.TrimSpace(requestBody.PrimaryCategory)
	primaryLocation := strings.TrimSpace(requestBody.PrimaryLocation)
	businessDescription := strings.TrimSpace(requestBody.BusinessDescription)
	seedPrompts, err := businessprofile.NormalizeSeedPrompts(requestBody.SeedPrompts)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	targetKeywords := businessprofile.NormalizeTargetKeywords(requestBody.TargetKeywords)
	if brandName == "" || websiteURL == "" {
		writeJSONError(w, http.StatusBadRequest, "brand_name and website_url are required")
		return
	}

	seedPromptsJSON, err := json.Marshal(seedPrompts)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	targetKeywordsJSON, err := json.Marshal(targetKeywords)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
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
	project, err := queries.GetProjectByIDForUserForBusinessProfileUpdate(r.Context(), sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams{
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
		SeedPrompts:         seedPromptsJSON,
		TargetKeywords:      targetKeywordsJSON,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := a.Queries.EnqueueAIWorkerJob(r.Context(), sqlc.EnqueueAIWorkerJobParams{
		JobType:   "prompt_generation",
		ProjectID: project.ID,
	}); err != nil {
		log.Printf("enqueue prompt_generation job for project %s: %v", project.ID.String(), err)
	}

	response, err := newProjectBusinessProfileResponseFromUpsertRow(profile)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func getProjectBusinessProfileByProjectID(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID) (sqlc.GetProjectBusinessProfileByProjectIDRow, bool, error) {
	profile, err := queries.GetProjectBusinessProfileByProjectID(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.GetProjectBusinessProfileByProjectIDRow{}, false, nil
		}
		return sqlc.GetProjectBusinessProfileByProjectIDRow{}, false, err
	}
	return profile, true, nil
}

func newProjectBusinessProfileResponseFromGetRow(profile sqlc.GetProjectBusinessProfileByProjectIDRow) (projectBusinessProfileResponse, error) {
	return newProjectBusinessProfileResponse(
		profile.ID,
		profile.ProjectID,
		profile.BrandName,
		profile.WebsiteUrl,
		profile.PrimaryCategory,
		profile.PrimaryLocation,
		profile.BusinessDescription,
		profile.SeedPrompts,
		profile.TargetKeywords,
		profile.CreatedAt,
		profile.UpdatedAt,
	)
}

func newProjectBusinessProfileResponseFromUpsertRow(profile sqlc.UpsertProjectBusinessProfileRow) (projectBusinessProfileResponse, error) {
	return newProjectBusinessProfileResponse(
		profile.ID,
		profile.ProjectID,
		profile.BrandName,
		profile.WebsiteUrl,
		profile.PrimaryCategory,
		profile.PrimaryLocation,
		profile.BusinessDescription,
		profile.SeedPrompts,
		profile.TargetKeywords,
		profile.CreatedAt,
		profile.UpdatedAt,
	)
}

func newProjectBusinessProfileResponse(
	id pgtype.UUID,
	projectID pgtype.UUID,
	brandName string,
	websiteURL string,
	primaryCategory pgtype.Text,
	primaryLocation pgtype.Text,
	businessDescription pgtype.Text,
	rawSeedPrompts []byte,
	rawTargetKeywords []byte,
	createdAt pgtype.Timestamptz,
	updatedAt pgtype.Timestamptz,
) (projectBusinessProfileResponse, error) {
	seedPrompts, err := businessprofile.DecodeSeedPrompts(rawSeedPrompts)
	if err != nil {
		return projectBusinessProfileResponse{}, err
	}
	targetKeywords, err := businessprofile.DecodeTargetKeywords(rawTargetKeywords)
	if err != nil {
		return projectBusinessProfileResponse{}, err
	}

	return projectBusinessProfileResponse{
		ID:                  id.String(),
		ProjectID:           projectID.String(),
		BrandName:           brandName,
		WebsiteURL:          websiteURL,
		PrimaryCategory:     textValue(primaryCategory),
		PrimaryLocation:     textValue(primaryLocation),
		BusinessDescription: textValue(businessDescription),
		SeedPrompts:         seedPrompts,
		TargetKeywords:      targetKeywords,
		CreatedAt:           createdAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:           updatedAt.Time.UTC().Format(time.RFC3339),
	}, nil
}

func normalizeSeedPrompts(prompts []string) ([]string, error) {
	return businessprofile.NormalizeSeedPrompts(prompts)
}

func decodeSeedPrompts(rawSeedPrompts []byte) ([]string, error) {
	return businessprofile.DecodeSeedPrompts(rawSeedPrompts)
}

func decodeTargetKeywords(rawTargetKeywords []byte) ([]string, error) {
	return businessprofile.DecodeTargetKeywords(rawTargetKeywords)
}

func decodeStringSlice(raw []byte) ([]string, error) {
	return businessprofile.DecodeStringSlice(raw)
}

func normalizeTargetKeywords(keywords []string) []string {
	return businessprofile.NormalizeTargetKeywords(keywords)
}
