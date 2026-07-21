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
	"github.com/ps-wizard/revserp/internal/app/aitools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type upsertProjectBusinessProfileRequest struct {
	BrandName           string   `json:"brand_name"`
	WebsiteURL          string   `json:"website_url"`
	PrimaryCategory     string   `json:"primary_category"`
	PrimaryLocation     string   `json:"primary_location"`
	BusinessDescription string   `json:"business_description"`
	SeedPrompts         []string `json:"seed_prompts"`
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
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	brandName := strings.TrimSpace(requestBody.BrandName)
	websiteURL := strings.TrimSpace(requestBody.WebsiteURL)
	primaryCategory := strings.TrimSpace(requestBody.PrimaryCategory)
	primaryLocation := strings.TrimSpace(requestBody.PrimaryLocation)
	businessDescription := strings.TrimSpace(requestBody.BusinessDescription)
	seedPrompts, err := normalizeSeedPrompts(requestBody.SeedPrompts)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if brandName == "" || websiteURL == "" {
		writeJSONError(w, http.StatusBadRequest, "brand_name and website_url are required")
		return
	}

	seedPromptsJSON, err := json.Marshal(seedPrompts)
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
		SeedPrompts:         seedPromptsJSON,
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

// updateBusinessProfileForAgent runs the authorized business profile write
// path for the AI agent's update_business_profile tool, in its own
// transaction. The org-owner requirement is enforced exactly as the HTTP
// handler enforces it before the upsert. The merged, validated profile is
// supplied by the tool.
func (a *App) updateBusinessProfileForAgent(ctx context.Context, scope aitools.Scope, update aitools.BusinessProfileUpdate) error {
	seedPromptsJSON, err := json.Marshal(update.SeedPrompts)
	if err != nil {
		return err
	}

	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := a.Queries.WithTx(tx)
	project, err := queries.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{
		ID:     scope.ProjectID,
		UserID: scope.UserID,
	})
	if err != nil {
		return err
	}

	if err := requireOrganizationOwner(ctx, queries, project.OrganizationID, scope.UserID); err != nil {
		return err
	}

	if _, err := queries.UpsertProjectBusinessProfile(ctx, sqlc.UpsertProjectBusinessProfileParams{
		ProjectID:           project.ID,
		BrandName:           update.BrandName,
		WebsiteUrl:          update.WebsiteURL,
		PrimaryCategory:     pgText(update.PrimaryCategory),
		PrimaryLocation:     pgText(update.PrimaryLocation),
		BusinessDescription: pgText(update.BusinessDescription),
		SeedPrompts:         seedPromptsJSON,
	}); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	if _, err := a.Queries.EnqueueAIWorkerJob(ctx, sqlc.EnqueueAIWorkerJobParams{
		JobType:   "prompt_generation",
		ProjectID: project.ID,
	}); err != nil {
		log.Printf("enqueue prompt_generation job for project %s: %v", project.ID.String(), err)
	}
	return nil
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
	createdAt pgtype.Timestamptz,
	updatedAt pgtype.Timestamptz,
) (projectBusinessProfileResponse, error) {
	seedPrompts, err := decodeSeedPrompts(rawSeedPrompts)
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
		CreatedAt:           createdAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:           updatedAt.Time.UTC().Format(time.RFC3339),
	}, nil
}

func normalizeSeedPrompts(prompts []string) ([]string, error) {
	if len(prompts) > 5 {
		return nil, errors.New("seed_prompts cannot contain more than 5 prompts")
	}

	normalizedPrompts := make([]string, 0, len(prompts))
	for _, prompt := range prompts {
		trimmedPrompt := strings.TrimSpace(prompt)
		if trimmedPrompt == "" {
			return nil, errors.New("seed_prompts cannot contain empty prompts")
		}
		normalizedPrompts = append(normalizedPrompts, trimmedPrompt)
	}

	return normalizedPrompts, nil
}

func decodeSeedPrompts(rawSeedPrompts []byte) ([]string, error) {
	if len(rawSeedPrompts) == 0 {
		return []string{}, nil
	}

	var seedPrompts []string
	if err := json.Unmarshal(rawSeedPrompts, &seedPrompts); err != nil {
		return nil, err
	}

	return seedPrompts, nil
}
