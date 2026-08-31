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
			Description: "Read the business profile configured for the current project: brand name, website, primary category, primary location, business description, target keywords (always returned), and optionally the seed prompts. Use it for who/what the business is, where it operates, what it sells, and to ground brand-aware answers. This is one record per project — no filters, no paging. Returns a plain explanation when no profile is configured.",
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
	exec := businessProfileExecutor{profiles: s.Queries}
	return exec.run(ctx, args, s.ProjectID, s.UserID)
}

type businessProfileArgs struct {
	IncludeSeedPrompts bool
}

// businessProfileResponse is the JSON the model sees.
type businessProfileResponse struct {
	BrandName           string   `json:"brand_name"`
	WebsiteURL          string   `json:"website_url"`
	PrimaryCategory     string   `json:"primary_category,omitempty"`
	PrimaryLocation     string   `json:"primary_location,omitempty"`
	BusinessDescription string   `json:"business_description,omitempty"`
	SeedPrompts         []string `json:"seed_prompts,omitempty"`
	TargetKeywords      []string `json:"target_keywords"`
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
	}
	if keywords, err := businessprofile.DecodeTargetKeywords(profile.TargetKeywords); err == nil {
		response.TargetKeywords = keywords
		if response.TargetKeywords == nil {
			response.TargetKeywords = []string{}
		}
	} else {
		response.TargetKeywords = []string{}
	}

	if args.IncludeSeedPrompts && len(profile.SeedPrompts) > 0 {
		var prompts []string
		if err := json.Unmarshal(profile.SeedPrompts, &prompts); err == nil {
			response.SeedPrompts = capBusinessProfilePrompts(prompts)
		}
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
