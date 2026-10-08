package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/businessprofile"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// upsertLocationBusinessProfileRequest mirrors the project profile request
// with an optional local services snapshot. A nil Services leaves the
// stored snapshot untouched; an empty array clears it.
type upsertLocationBusinessProfileRequest struct {
	BrandName           string    `json:"brand_name"`
	WebsiteURL          string    `json:"website_url"`
	PrimaryCategory     string    `json:"primary_category"`
	PrimaryLocation     string    `json:"primary_location"`
	BusinessDescription string    `json:"business_description"`
	ProductDescription  string    `json:"product_description"`
	TargetAudience      string    `json:"target_audience"`
	BusinessCompetitors []string  `json:"business_competitors"`
	SeedPrompts         []string  `json:"seed_prompts"`
	Services            *[]string `json:"services"`
}

// locationBusinessProfileResponse reuses the project profile JSON shape so
// both endpoints stay identical, with location_id and the local services
// snapshot additionally present. Keyword lists stay empty until the keyword
// layer stores them.
type locationBusinessProfileResponse struct {
	projectBusinessProfileResponse
	LocationID string   `json:"location_id"`
	Services   []string `json:"services"`
}

type locationBusinessProfileStatusResponse struct {
	HasProfile       bool                             `json:"has_profile"`
	CanManageProfile bool                             `json:"can_manage_profile"`
	BusinessProfile  *locationBusinessProfileResponse `json:"business_profile,omitempty"`
}

const (
	selectLocationBusinessProfileSQL = `SELECT id, project_id, location_id, brand_name, website_url,
		primary_category, primary_location, business_description, product_description,
		target_audience, business_competitors, seed_prompts, services, created_at, updated_at
		FROM location_business_profiles WHERE project_id = $1 AND location_id = $2 LIMIT 1`
	upsertLocationBusinessProfileSQL = `INSERT INTO location_business_profiles
		(project_id, location_id, brand_name, website_url, primary_category, primary_location,
		business_description, product_description, target_audience, business_competitors, seed_prompts, services)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, COALESCE($12, '[]'::jsonb))
		ON CONFLICT (location_id) DO UPDATE SET
		brand_name = excluded.brand_name, website_url = excluded.website_url,
		primary_category = excluded.primary_category, primary_location = excluded.primary_location,
		business_description = excluded.business_description,
		product_description = excluded.product_description,
		target_audience = excluded.target_audience,
		business_competitors = excluded.business_competitors,
		seed_prompts = excluded.seed_prompts,
		services = CASE WHEN $12 IS NULL THEN location_business_profiles.services ELSE excluded.services END,
		updated_at = now()
		RETURNING id, project_id, location_id, brand_name, website_url,
		primary_category, primary_location, business_description, product_description,
		target_audience, business_competitors, seed_prompts, services, created_at, updated_at`
)

func newLocationBusinessProfileResponse(
	id, projectID, locationID pgtype.UUID,
	brandName, websiteURL string,
	primaryCategory, primaryLocation, businessDescription, productDescription, targetAudience pgtype.Text,
	rawBusinessCompetitors, rawSeedPrompts, rawServices []byte,
	createdAt, updatedAt pgtype.Timestamptz,
) (locationBusinessProfileResponse, error) {
	seedPrompts, err := businessprofile.DecodeSeedPrompts(rawSeedPrompts)
	if err != nil {
		return locationBusinessProfileResponse{}, err
	}
	businessCompetitors, err := decodeBusinessCompetitors(rawBusinessCompetitors)
	if err != nil {
		return locationBusinessProfileResponse{}, err
	}
	services, err := decodeStringSlice(rawServices)
	if err != nil {
		return locationBusinessProfileResponse{}, err
	}
	// product_description copies verbatim from the parent once and is stored
	// as-is afterwards; it is never regenerated here.
	emptyKeywords := []string{}
	return locationBusinessProfileResponse{
		projectBusinessProfileResponse: projectBusinessProfileResponse{
			ID:                  id.String(),
			ProjectID:           projectID.String(),
			BrandName:           brandName,
			WebsiteURL:          websiteURL,
			PrimaryCategory:     textValue(primaryCategory),
			PrimaryLocation:     textValue(primaryLocation),
			BusinessDescription: textValue(businessDescription),
			ProductDescription:  textValue(productDescription),
			TargetAudience:      textValue(targetAudience),
			BusinessCompetitors: businessCompetitors,
			BrandedKeywords:     emptyKeywords,
			NonBrandedKeywords:  emptyKeywords,
			SeedPrompts:         seedPrompts,
			TargetKeywords:      emptyKeywords,
			CreatedAt:           createdAt.Time.UTC().Format(time.RFC3339),
			UpdatedAt:           updatedAt.Time.UTC().Format(time.RFC3339),
		},
		LocationID: locationID.String(),
		Services:   services,
	}, nil
}

// locationProductDescription stores product_description byte-exact:
// leading/trailing whitespace and newlines are significant content copied
// verbatim from the parent, never normalized. Metadata fields still trim.
func locationProductDescription(body string) pgtype.Text {
	return pgText(body)
}

// normalizeLocationProfileServices trims and dedupes service labels with the
// same display/key rules as project services, preserving first spelling.
func normalizeLocationProfileServices(raw []string) ([]string, error) {
	services := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, label := range raw {
		display, key, err := layer4ServiceLabelKey(label)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		services = append(services, display)
	}
	return services, nil
}

// handleGetLocationBusinessProfile serves the independent local profile.
// An absent row reads as has_profile=false, never as a parent fallback.
func (a *App) handleGetLocationBusinessProfile(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	membership, err := a.Queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{
		OrgID:  location.OrganizationID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
		} else {
			serverError(w, r, err)
		}
		return
	}
	profile, hasProfile, err := a.getLocationBusinessProfile(r.Context(), location.ProjectID, location.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	response := locationBusinessProfileStatusResponse{
		HasProfile:       hasProfile,
		CanManageProfile: locationWorkspaceCanManage(membership.Role),
		BusinessProfile:  profile,
	}
	writeJSON(w, http.StatusOK, response)
}

// handleUpsertLocationBusinessProfile stores the owner-managed local
// profile. It accepts and returns the same JSON as the project endpoint
// plus the optional local services snapshot.
func (a *App) handleUpsertLocationBusinessProfile(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	var body upsertLocationBusinessProfileRequest
	if !readJSONOrRespond(w, r, &body) {
		return
	}
	brandName := strings.TrimSpace(body.BrandName)
	websiteURL := strings.TrimSpace(body.WebsiteURL)
	if brandName == "" || websiteURL == "" {
		writeJSONError(w, http.StatusBadRequest, "brand_name and website_url are required")
		return
	}
	seedPrompts, err := businessprofile.NormalizeSeedPrompts(body.SeedPrompts)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	businessCompetitors := businessprofile.NormalizeBusinessCompetitors(body.BusinessCompetitors)
	var servicesJSON any
	if body.Services != nil {
		services, err := normalizeLocationProfileServices(*body.Services)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		encoded, err := json.Marshal(services)
		if err != nil {
			serverError(w, r, err)
			return
		}
		servicesJSON = encoded
	}
	seedPromptsJSON, err := json.Marshal(seedPrompts)
	if err != nil {
		serverError(w, r, err)
		return
	}
	businessCompetitorsJSON, err := json.Marshal(businessCompetitors)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := requireOrganizationOwner(r.Context(), a.Queries, location.OrganizationID, userID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}
	var id, projectID, locationID pgtype.UUID
	var outBrandName, outWebsiteURL string
	var primaryCategory, primaryLocation, businessDescription, productDescription, targetAudience pgtype.Text
	var rawBusinessCompetitors, rawSeedPrompts, rawServices []byte
	var createdAt, updatedAt pgtype.Timestamptz
	err = a.DB.QueryRow(r.Context(), upsertLocationBusinessProfileSQL,
		location.ProjectID, location.ID, brandName, websiteURL,
		pgText(strings.TrimSpace(body.PrimaryCategory)), pgText(strings.TrimSpace(body.PrimaryLocation)),
		pgText(strings.TrimSpace(body.BusinessDescription)), locationProductDescription(body.ProductDescription),
		pgText(strings.TrimSpace(body.TargetAudience)), businessCompetitorsJSON, seedPromptsJSON, servicesJSON,
	).Scan(&id, &projectID, &locationID, &outBrandName, &outWebsiteURL,
		&primaryCategory, &primaryLocation, &businessDescription, &productDescription,
		&targetAudience, &rawBusinessCompetitors, &rawSeedPrompts, &rawServices, &createdAt, &updatedAt)
	if err != nil {
		serverError(w, r, err)
		return
	}
	response, err := newLocationBusinessProfileResponse(id, projectID, locationID,
		outBrandName, outWebsiteURL, primaryCategory, primaryLocation, businessDescription,
		productDescription, targetAudience, rawBusinessCompetitors, rawSeedPrompts,
		rawServices, createdAt, updatedAt)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) getLocationBusinessProfile(ctx context.Context, projectID, locationID pgtype.UUID) (*locationBusinessProfileResponse, bool, error) {
	var id pgtype.UUID
	var outProjectID, outLocationID pgtype.UUID
	var brandName, websiteURL string
	var primaryCategory, primaryLocation, businessDescription, productDescription, targetAudience pgtype.Text
	var rawBusinessCompetitors, rawSeedPrompts, rawServices []byte
	var createdAt, updatedAt pgtype.Timestamptz
	err := a.DB.QueryRow(ctx, selectLocationBusinessProfileSQL, projectID, locationID).Scan(
		&id, &outProjectID, &outLocationID, &brandName, &websiteURL,
		&primaryCategory, &primaryLocation, &businessDescription, &productDescription,
		&targetAudience, &rawBusinessCompetitors, &rawSeedPrompts, &rawServices, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	response, err := newLocationBusinessProfileResponse(id, outProjectID, outLocationID,
		brandName, websiteURL, primaryCategory, primaryLocation, businessDescription,
		productDescription, targetAudience, rawBusinessCompetitors, rawSeedPrompts,
		rawServices, createdAt, updatedAt)
	if err != nil {
		return nil, false, err
	}
	return &response, true, nil
}
