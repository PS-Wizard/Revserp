package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/businessprofile"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	businessProfileName           = "get_business_profile"
	businessProfileMaxFieldRune   = 500
	businessProfileMaxPromptCount = 20
	businessProfileMaxPromptRune  = 200
)

// getBusinessProfileSchema takes at most one optional flag; the payload is
// one record per project, so there is nothing to filter or page.
const getBusinessProfileSchema = `{
  "type": "object",
  "properties": {
    "include_seed_prompts": {"type": "boolean", "description": "Also return the configured seed prompts (question seeds used for AI-assisted authorship work). Default false, because the seed list can be long."}
  },
  "additionalProperties": false
}`

// businessProfileReader reads one project's business profile through the
// user-membership join so tests can substitute fakes without a database.
type businessProfileReader interface {
	GetProjectBusinessProfileByProjectIDForUser(ctx context.Context, arg sqlc.GetProjectBusinessProfileByProjectIDForUserParams) (sqlc.GetProjectBusinessProfileByProjectIDForUserRow, error)
}

// businessProfileExecutor runs one get_business_profile call.
type businessProfileExecutor struct {
	profiles businessProfileReader
}

func getBusinessProfileTool() Tool {
	return Tool{
		Def: Def{
			Name:        businessProfileName,
			Label:       "Get business profile",
			Description: "Read the business profile configured for the current project: brand name, website, primary category, primary location, business description, product description, target audience, business competitors, and optionally the seed prompts. The branded_keywords, non_branded_keywords, and target_keywords fields are read-only combined projections of all keyword sources (always returned, never written here) for compatibility and context. Use it for who/what the business is, where it operates, what it sells, who it serves, and to ground brand-aware answers. Keyword management is separate: read full lists with source badges via get_project_keywords and refresh REVSerp suggestions with update_project_keywords. This is one record per project — no filters, no paging. Returns a plain explanation when no profile is configured.",
			Schema:      json.RawMessage(getBusinessProfileSchema),
		},
		Execute: executeGetBusinessProfile,
	}
}

// executeGetBusinessProfile adapts the tool contract to the narrow executor.
func executeGetBusinessProfile(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.Queries == nil {
		return Result{}, errors.New("get_business_profile: scope has no queries")
	}
	if s.LocationID.Valid {
		exec := businessProfileLocalExecutor{locations: s.Queries}
		return exec.runLocal(ctx, args, s.ProjectID, s.LocationID, s.UserID)
	}
	exec := businessProfileExecutor{profiles: s.Queries}
	return exec.run(ctx, args, s.ProjectID, s.UserID)
}

type businessProfileArgs struct {
	IncludeSeedPrompts bool
}

// businessProfileResponse is the JSON the model sees. The keyword fields are
// read-only combined projections across user-defined and REVSerp-suggested
// sources; writes go through update_project_keywords, never here.
type businessProfileResponse struct {
	BrandName           string    `json:"brand_name"`
	WebsiteURL          string    `json:"website_url"`
	PrimaryCategory     string    `json:"primary_category,omitempty"`
	PrimaryLocation     string    `json:"primary_location,omitempty"`
	BusinessDescription string    `json:"business_description,omitempty"`
	ProductDescription  string    `json:"product_description,omitempty"`
	TargetAudience      string    `json:"target_audience,omitempty"`
	BusinessCompetitors []string  `json:"business_competitors"`
	BrandedKeywords     []string  `json:"branded_keywords"`
	NonBrandedKeywords  []string  `json:"non_branded_keywords"`
	SeedPrompts         *[]string `json:"seed_prompts,omitempty"`
	TargetKeywords      []string  `json:"target_keywords"`
}

// run executes one get_business_profile call. The payload is one bounded
// record, never crawl rows, so the call does not spend the turn row budget.
func (e *businessProfileExecutor) run(ctx context.Context, raw json.RawMessage, projectID, userID pgtype.UUID) (Result, error) {
	args, err := parseBusinessProfileArgs(raw)
	if err != nil {
		return Result{Content: businessProfileName + " error: " + err.Error()}, nil
	}

	profile, err := e.profiles.GetProjectBusinessProfileByProjectIDForUser(ctx, sqlc.GetProjectBusinessProfileByProjectIDForUserParams{ProjectID: projectID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{
				Content: "No business profile is configured for this project yet. An owner can add it in the project's business profile settings.",
				Summary: "business profile not configured",
			}, nil
		}
		return Result{}, fmt.Errorf("%s: read profile: %w", businessProfileName, err)
	}

	response := businessProfileResponse{
		BrandName:           capBusinessProfileText(profile.BrandName, businessProfileMaxFieldRune),
		WebsiteURL:          profile.WebsiteUrl,
		PrimaryCategory:     profileText(profile.PrimaryCategory),
		PrimaryLocation:     profileText(profile.PrimaryLocation),
		BusinessDescription: capBusinessProfileText(profileText(profile.BusinessDescription), businessProfileMaxFieldRune),
		ProductDescription:  capBusinessProfileText(profileText(profile.ProductDescription), businessProfileMaxFieldRune),
		TargetAudience:      capBusinessProfileText(profileText(profile.TargetAudience), businessProfileMaxFieldRune),
	}
	if keywords, err := businessprofile.DecodeTargetKeywords(profile.TargetKeywords); err == nil {
		response.TargetKeywords = keywords
		if response.TargetKeywords == nil {
			response.TargetKeywords = []string{}
		}
	} else {
		response.TargetKeywords = []string{}
	}
	if competitors, err := businessprofile.DecodeBusinessCompetitors(profile.BusinessCompetitors); err == nil {
		response.BusinessCompetitors = competitors
		if response.BusinessCompetitors == nil {
			response.BusinessCompetitors = []string{}
		}
	} else {
		response.BusinessCompetitors = []string{}
	}
	if branded, err := businessprofile.DecodeBrandedKeywords(profile.BrandedKeywords); err == nil {
		response.BrandedKeywords = branded
		if response.BrandedKeywords == nil {
			response.BrandedKeywords = []string{}
		}
	} else {
		response.BrandedKeywords = []string{}
	}
	if nonBranded, err := businessprofile.DecodeNonBrandedKeywords(profile.NonBrandedKeywords); err == nil {
		response.NonBrandedKeywords = nonBranded
		if response.NonBrandedKeywords == nil {
			response.NonBrandedKeywords = []string{}
		}
	} else {
		response.NonBrandedKeywords = []string{}
	}

	if args.IncludeSeedPrompts {
		prompts := []string{}
		if len(profile.SeedPrompts) > 0 {
			var stored []string
			if err := json.Unmarshal(profile.SeedPrompts, &stored); err == nil {
				prompts = capBusinessProfilePrompts(stored)
				if prompts == nil {
					prompts = []string{}
				}
			}
		}
		response.SeedPrompts = &prompts
	}

	content, err := json.Marshal(response)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal profile: %w", businessProfileName, err)
	}

	summary := fmt.Sprintf("business profile: %s", response.BrandName)
	if response.PrimaryCategory != "" {
		summary = fmt.Sprintf("%s (%s)", summary, response.PrimaryCategory)
	}
	return Result{Content: string(content), Summary: summary}, nil
}

// parseBusinessProfileArgs parses the tool arguments strictly: unknown keys,
// duplicate keys, and trailing data are rejected. Empty input yields defaults.
func parseBusinessProfileArgs(raw json.RawMessage) (businessProfileArgs, error) {
	args := businessProfileArgs{}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for key, value := range fields {
		switch key {
		case "include_seed_prompts":
			if err := json.Unmarshal(value, &args.IncludeSeedPrompts); err != nil {
				return args, fmt.Errorf("argument %q must be a boolean", key)
			}
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	return args, nil
}

// profileText returns the value of a nullable text column as a plain string.
func profileText(value pgtype.Text) string {
	if value.Valid {
		return value.String
	}
	return ""
}

// capBusinessProfileText caps text at maxLen runes, appending a truncation
// marker when anything was cut.
func capBusinessProfileText(text string, maxLen int) string {
	if utf8.RuneCountInString(text) <= maxLen {
		return text
	}
	return string([]rune(text)[:maxLen]) + "\u2026"
}

// capBusinessProfilePrompts bounds the seed prompt list: at most
// businessProfileMaxPromptCount prompts, each capped at
// businessProfileMaxPromptRune runes.
func capBusinessProfilePrompts(prompts []string) []string {
	if len(prompts) > businessProfileMaxPromptCount {
		prompts = prompts[:businessProfileMaxPromptCount]
	}
	capped := make([]string, len(prompts))
	for i, prompt := range prompts {
		capped[i] = capBusinessProfileText(prompt, businessProfileMaxPromptRune)
	}
	return capped
}

// businessProfileLocationReader reads one location's independent profile
// through the generated location queries, so tests substitute fakes.
type businessProfileLocationReader interface {
	GetProjectLocationForUser(ctx context.Context, arg sqlc.GetProjectLocationForUserParams) (sqlc.GetProjectLocationForUserRow, error)
	GetLocationBusinessProfile(ctx context.Context, arg sqlc.GetLocationBusinessProfileParams) (sqlc.LocationBusinessProfile, error)
}

// businessProfileLocalResponse is the model-facing local profile: the
// project shape plus location identity and the local services snapshot.
// Keyword lists stay empty; location keywords live in the keyword layer.
type businessProfileLocalResponse struct {
	businessProfileResponse
	LocationID string   `json:"location_id"`
	Services   []string `json:"services"`
}

type businessProfileLocalExecutor struct {
	locations businessProfileLocationReader
}

func (e *businessProfileLocalExecutor) runLocal(ctx context.Context, raw json.RawMessage, projectID, locationID, userID pgtype.UUID) (Result, error) {
	args, err := parseBusinessProfileArgs(raw)
	if err != nil {
		return Result{Content: businessProfileName + " error: " + err.Error()}, nil
	}
	if _, err := e.locations.GetProjectLocationForUser(ctx, sqlc.GetProjectLocationForUserParams{ID: locationID, ID_2: projectID, UserID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{
				Content: "That location was not found in this project.",
				Summary: "location not found",
			}, nil
		}
		return Result{}, fmt.Errorf("%s: read location: %w", businessProfileName, err)
	}
	profile, err := e.locations.GetLocationBusinessProfile(ctx, sqlc.GetLocationBusinessProfileParams{ProjectID: projectID, LocationID: locationID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{
				Content: "No business profile is configured for this location yet. An owner can add it in the location's business profile settings.",
				Summary: "location business profile not configured",
			}, nil
		}
		return Result{}, fmt.Errorf("%s: read location profile: %w", businessProfileName, err)
	}
	response := newBusinessProfileLocalResponse(profile, locationID, args.IncludeSeedPrompts)
	content, err := json.Marshal(response)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal location profile: %w", businessProfileName, err)
	}
	summary := fmt.Sprintf("location business profile: %s", response.BrandName)
	if response.PrimaryCategory != "" {
		summary = fmt.Sprintf("%s (%s)", summary, response.PrimaryCategory)
	}
	return Result{Content: string(content), Summary: summary}, nil
}

func newBusinessProfileLocalResponse(profile sqlc.LocationBusinessProfile, locationID pgtype.UUID, includeSeeds bool) businessProfileLocalResponse {
	response := businessProfileLocalResponse{
		LocationID: locationID.String(),
		Services:   []string{},
	}
	response.BrandName = capBusinessProfileText(profile.BrandName, businessProfileMaxFieldRune)
	response.WebsiteURL = profile.WebsiteUrl
	response.PrimaryCategory = profileText(profile.PrimaryCategory)
	response.PrimaryLocation = profileText(profile.PrimaryLocation)
	response.BusinessDescription = capBusinessProfileText(profileText(profile.BusinessDescription), businessProfileMaxFieldRune)
	response.ProductDescription = capBusinessProfileText(profileText(profile.ProductDescription), businessProfileMaxFieldRune)
	response.TargetAudience = capBusinessProfileText(profileText(profile.TargetAudience), businessProfileMaxFieldRune)
	response.TargetKeywords = []string{}
	response.BrandedKeywords = []string{}
	response.NonBrandedKeywords = []string{}
	if competitors, err := businessprofile.DecodeBusinessCompetitors(profile.BusinessCompetitors); err == nil {
		response.BusinessCompetitors = competitors
		if response.BusinessCompetitors == nil {
			response.BusinessCompetitors = []string{}
		}
	} else {
		response.BusinessCompetitors = []string{}
	}
	if services, err := businessprofile.DecodeStringSlice(profile.Services); err == nil {
		response.Services = services
		if response.Services == nil {
			response.Services = []string{}
		}
	}
	if includeSeeds {
		prompts := []string{}
		if len(profile.SeedPrompts) > 0 {
			var stored []string
			if err := json.Unmarshal(profile.SeedPrompts, &stored); err == nil {
				prompts = capBusinessProfilePrompts(stored)
				if prompts == nil {
					prompts = []string{}
				}
			}
		}
		response.SeedPrompts = &prompts
	}
	return response
}
