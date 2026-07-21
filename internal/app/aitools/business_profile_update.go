package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// BusinessProfileUpdate is the merged, validated business profile the app layer
// persists. Every field is the final value after merging model-supplied
// changes over the existing profile.
type BusinessProfileUpdate struct {
	BrandName           string
	WebsiteURL          string
	PrimaryCategory     string
	PrimaryLocation     string
	BusinessDescription string
	SeedPrompts         []string
}

// BusinessProfileUpdater is the application-owned, authorized business profile
// write path. It enforces the org-owner requirement before persisting.
type BusinessProfileUpdater func(context.Context, Scope, BusinessProfileUpdate) error

// businessProfileUpdateArgs captures which fields the model provided. A nil
// pointer means "not provided", so the existing value is preserved on merge.
type businessProfileUpdateArgs struct {
	BrandName           *string
	WebsiteURL          *string
	PrimaryCategory     *string
	PrimaryLocation     *string
	BusinessDescription *string
	SeedPrompts         *[]string
}

func updateBusinessProfileTool(update BusinessProfileUpdater) Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "update_business_profile",
			Description: "Create or update the current project's business profile. All fields are optional for partial updates; unset fields keep their existing value. brand_name and website_url must end up non-empty. Requires organization owner permission.",
			Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "brand_name": {"type": "string"},
    "website_url": {"type": "string"},
    "primary_category": {"type": "string"},
    "primary_location": {"type": "string"},
    "business_description": {"type": "string"},
    "seed_prompts": {"type": "array", "items": {"type": "string"}}
  },
  "additionalProperties": false
}`),
		},
		Execute: func(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
			return execUpdateBusinessProfile(ctx, s, s.Queries, args, update)
		},
	}
}

func execUpdateBusinessProfile(ctx context.Context, s Scope, reader businessProfileReader, args json.RawMessage, update BusinessProfileUpdater) (Result, error) {
	parsed, err := parseUpdateBusinessProfileArgs(args)
	if err != nil {
		return Result{}, err
	}
	if update == nil {
		return Result{}, errors.New("business profile updates are unavailable")
	}
	merged, err := mergeBusinessProfile(ctx, s.ProjectID, s.UserID, reader, parsed)
	if err != nil {
		return Result{}, err
	}
	if err := update(ctx, s, merged); err != nil {
		return Result{}, err
	}
	return Result{Content: `{"status":"updated"}`, Summary: "business profile updated"}, nil
}

func parseUpdateBusinessProfileArgs(raw json.RawMessage) (businessProfileUpdateArgs, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return businessProfileUpdateArgs{}, errors.New("arguments must be an object")
	}
	var parsed businessProfileUpdateArgs
	for name, value := range fields {
		switch name {
		case "brand_name", "website_url", "primary_category", "primary_location", "business_description":
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return businessProfileUpdateArgs{}, fmt.Errorf("%s must be a string", name)
			}
			switch name {
			case "brand_name":
				parsed.BrandName = &text
			case "website_url":
				parsed.WebsiteURL = &text
			case "primary_category":
				parsed.PrimaryCategory = &text
			case "primary_location":
				parsed.PrimaryLocation = &text
			case "business_description":
				parsed.BusinessDescription = &text
			}
		case "seed_prompts":
			var prompts []string
			if err := json.Unmarshal(value, &prompts); err != nil {
				return businessProfileUpdateArgs{}, errors.New("seed_prompts must be an array of strings")
			}
			parsed.SeedPrompts = &prompts
		default:
			return businessProfileUpdateArgs{}, fmt.Errorf("unknown argument %q", name)
		}
	}
	return parsed, nil
}

// mergeBusinessProfile reads the existing profile and overlays the
// model-supplied fields, keeping existing values for anything not provided.
func mergeBusinessProfile(ctx context.Context, projectID, userID pgtype.UUID, reader businessProfileReader, parsed businessProfileUpdateArgs) (BusinessProfileUpdate, error) {
	row, err := reader.GetProjectBusinessProfileByProjectIDForUser(ctx, sqlc.GetProjectBusinessProfileByProjectIDForUserParams{
		ProjectID: projectID,
		UserID:    userID,
	})
	exists := true
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			exists = false
		} else {
			return BusinessProfileUpdate{}, err
		}
	}

	merged := BusinessProfileUpdate{SeedPrompts: []string{}}
	if exists {
		merged.BrandName = row.BrandName
		merged.WebsiteURL = row.WebsiteUrl
		merged.PrimaryCategory = textValue(row.PrimaryCategory)
		merged.PrimaryLocation = textValue(row.PrimaryLocation)
		merged.BusinessDescription = textValue(row.BusinessDescription)
		prompts, err := decodeSeedPrompts(row.SeedPrompts)
		if err != nil {
			return BusinessProfileUpdate{}, err
		}
		merged.SeedPrompts = prompts
	}

	if parsed.BrandName != nil {
		merged.BrandName = strings.TrimSpace(*parsed.BrandName)
	}
	if parsed.WebsiteURL != nil {
		merged.WebsiteURL = strings.TrimSpace(*parsed.WebsiteURL)
	}
	if parsed.PrimaryCategory != nil {
		merged.PrimaryCategory = strings.TrimSpace(*parsed.PrimaryCategory)
	}
	if parsed.PrimaryLocation != nil {
		merged.PrimaryLocation = strings.TrimSpace(*parsed.PrimaryLocation)
	}
	if parsed.BusinessDescription != nil {
		merged.BusinessDescription = strings.TrimSpace(*parsed.BusinessDescription)
	}
	if parsed.SeedPrompts != nil {
		prompts, err := normalizeSeedPrompts(*parsed.SeedPrompts)
		if err != nil {
			return BusinessProfileUpdate{}, err
		}
		merged.SeedPrompts = prompts
	}

	if merged.BrandName == "" || merged.WebsiteURL == "" {
		return BusinessProfileUpdate{}, errors.New("brand_name and website_url are required")
	}
	return merged, nil
}

// decodeSeedPrompts unmarshals the stored seed_prompts JSON, tolerating an
// empty column.
func decodeSeedPrompts(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	var prompts []string
	if err := json.Unmarshal(raw, &prompts); err != nil {
		return nil, err
	}
	return prompts, nil
}

// normalizeSeedPrompts trims prompts and rejects empties, matching the HTTP
// upsert handler's rules (at most 5, no blank entries).
func normalizeSeedPrompts(prompts []string) ([]string, error) {
	if len(prompts) > 5 {
		return nil, errors.New("seed_prompts cannot contain more than 5 prompts")
	}
	normalized := make([]string, 0, len(prompts))
	for _, prompt := range prompts {
		trimmed := strings.TrimSpace(prompt)
		if trimmed == "" {
			return nil, errors.New("seed_prompts cannot contain empty prompts")
		}
		normalized = append(normalized, trimmed)
	}
	return normalized, nil
}
