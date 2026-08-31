package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/businessprofile"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/pgnull"
)

const updateBusinessProfileName = "update_business_profile"

const updateBusinessProfileSchema = `{
  "type": "object",
  "properties": {
    "brand_name": {"type": "string", "description": "Business brand name. Trimmed; non-empty when provided, cannot be cleared."},
    "website_url": {"type": "string", "description": "Business website URL. Trimmed; non-empty when provided, cannot be cleared."},
    "primary_category": {"type": "string", "description": "Primary business category. Trimmed; empty string clears the field."},
    "primary_location": {"type": "string", "description": "Primary location. Trimmed; empty string clears the field."},
    "business_description": {"type": "string", "description": "Business description. Trimmed; empty string clears the field."},
    "seed_prompts": {"type": "array", "items": {"type": "string"}, "maxItems": 5, "description": "Seed prompts; replaces the complete list. Empty array clears. Max 5, no empty values."},
    "target_keywords": {"type": "array", "items": {"type": "string"}, "description": "Target keywords; replaces the complete list. Empty array clears. Trimmed, empty dropped, case-insensitive dedupe preserving first spelling/order."}
  },
  "additionalProperties": false
}`

type updateBusinessProfileArgs struct {
	BrandName           *string
	WebsiteURL          *string
	PrimaryCategory     *string
	PrimaryLocation     *string
	BusinessDescription *string
	SeedPrompts         *[]string
	TargetKeywords      *[]string
}

func updateBusinessProfileTool() Tool {
	return Tool{
		Def: Def{
			Name:        updateBusinessProfileName,
			Label:       "Update business profile",
			Description: "Update the business profile for the current project (PATCH). May be called only after the user clearly asks to save or change the profile. Provide only fields to change; omitted fields are preserved atomically. Arrays replace the complete list and [] clears. Requires organization owner; non-owners are denied. For creation when no profile exists, brand_name and website_url are required. Server authorization is the real boundary, not model instructions.",
			Schema:      json.RawMessage(updateBusinessProfileSchema),
		},
		Execute: executeUpdateBusinessProfile,
	}
}

func executeUpdateBusinessProfile(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.Queries == nil || s.DB == nil {
		return Result{}, errors.New("update_business_profile: scope has no queries or transaction support")
	}
	exec := updateBusinessProfileExecutor{queries: s.Queries, db: s.DB}
	return exec.run(ctx, args, s.ProjectID, s.UserID)
}

type modelError struct{ msg string }

func (e *modelError) Error() string { return e.msg }

func isModelError(err error) bool {
	var m *modelError
	return errors.As(err, &m)
}

// querier for the tool, implemented by *sqlc.Queries and fakes.
type updateBusinessProfileQuerier interface {
	GetProjectByIDForUserForBusinessProfileUpdate(ctx context.Context, arg sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams) (sqlc.Project, error)
	GetOrganizationMember(ctx context.Context, arg sqlc.GetOrganizationMemberParams) (sqlc.OrganizationMember, error)
	GetProjectBusinessProfileByProjectID(ctx context.Context, projectID pgtype.UUID) (sqlc.GetProjectBusinessProfileByProjectIDRow, error)
	UpsertProjectBusinessProfile(ctx context.Context, arg sqlc.UpsertProjectBusinessProfileParams) (sqlc.UpsertProjectBusinessProfileRow, error)
	EnqueueAIWorkerJob(ctx context.Context, arg sqlc.EnqueueAIWorkerJobParams) (sqlc.EnqueueAIWorkerJobRow, error)
}

type updateBusinessProfileExecutor struct {
	queries *sqlc.Queries
	db      Transactor
}

func (e *updateBusinessProfileExecutor) run(ctx context.Context, raw json.RawMessage, projectID, userID pgtype.UUID) (Result, error) {
	args, err := parseUpdateBusinessProfileArgs(raw)
	if err != nil {
		return Result{Content: updateBusinessProfileName + " error: " + err.Error()}, nil
	}
	tx, err := e.db.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("%s: begin tx: %w", updateBusinessProfileName, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := e.queries.WithTx(tx)
	res, err := e.patch(ctx, args, projectID, userID, qtx)
	if err != nil {
		if isModelError(err) {
			return Result{Content: updateBusinessProfileName + " error: " + err.Error()}, nil
		}
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("%s: commit: %w", updateBusinessProfileName, err)
	}
	if _, err := e.queries.EnqueueAIWorkerJob(ctx, sqlc.EnqueueAIWorkerJobParams{JobType: "prompt_generation", ProjectID: projectID}); err != nil {
		log.Printf("enqueue prompt_generation job for project %s: %v", projectID.String(), err)
	}
	return res, nil
}

func (e *updateBusinessProfileExecutor) patch(ctx context.Context, args updateBusinessProfileArgs, projectID, userID pgtype.UUID, q updateBusinessProfileQuerier) (Result, error) {
	// Lock project row to serialize concurrent profile writes (including creation).
	project, err := q.GetProjectByIDForUserForBusinessProfileUpdate(ctx, sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams{ID: projectID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, &modelError{msg: "project not found or access denied"}
		}
		return Result{}, fmt.Errorf("%s: lock project: %w", updateBusinessProfileName, err)
	}
	member, err := q.GetOrganizationMember(ctx, sqlc.GetOrganizationMemberParams{OrgID: project.OrganizationID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, &modelError{msg: "only organization owners can update the business profile"}
		}
		return Result{}, fmt.Errorf("%s: get membership: %w", updateBusinessProfileName, err)
	}
	if member.Role != "owner" {
		return Result{}, &modelError{msg: "only organization owners can update the business profile"}
	}

	existing, err := q.GetProjectBusinessProfileByProjectID(ctx, projectID)
	exists := true
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			exists = false
		} else {
			return Result{}, fmt.Errorf("%s: read profile: %w", updateBusinessProfileName, err)
		}
	}
	if !exists {
		if args.BrandName == nil || strings.TrimSpace(*args.BrandName) == "" || args.WebsiteURL == nil || strings.TrimSpace(*args.WebsiteURL) == "" {
			return Result{}, &modelError{msg: "no business profile exists yet; to create one, provide non-empty brand_name and website_url"}
		}
	}

	var existingSeed, existingKeywords []string
	if exists {
		if args.SeedPrompts == nil {
			v, err := businessprofile.DecodeSeedPrompts(existing.SeedPrompts)
			if err != nil {
				return Result{}, fmt.Errorf("%s: decode seed_prompts: %w", updateBusinessProfileName, err)
			}
			existingSeed = v
		} else {
			if v, err := businessprofile.DecodeSeedPrompts(existing.SeedPrompts); err == nil {
				existingSeed = v
			} else {
				existingSeed = []string{}
			}
		}
		if args.TargetKeywords == nil {
			v, err := businessprofile.DecodeTargetKeywords(existing.TargetKeywords)
			if err != nil {
				return Result{}, fmt.Errorf("%s: decode target_keywords: %w", updateBusinessProfileName, err)
			}
			existingKeywords = v
		} else {
			if v, err := businessprofile.DecodeTargetKeywords(existing.TargetKeywords); err == nil {
				existingKeywords = v
			} else {
				existingKeywords = []string{}
			}
		}
	} else {
		existingSeed = []string{}
		existingKeywords = []string{}
	}

	var finalBrand, finalWebsite string
	var finalCategory, finalLocation, finalDesc pgtype.Text
	var finalSeed, finalKeywords []string
	changed := []string{}

	// brand_name
	if args.BrandName != nil {
		trim := strings.TrimSpace(*args.BrandName)
		if trim == "" {
			return Result{}, &modelError{msg: "brand_name cannot be empty"}
		}
		finalBrand = trim
		if !exists || trim != existing.BrandName {
			changed = append(changed, "brand_name")
		}
	} else {
		if exists {
			finalBrand = existing.BrandName
		}
	}
	// website_url
	if args.WebsiteURL != nil {
		trim := strings.TrimSpace(*args.WebsiteURL)
		if trim == "" {
			return Result{}, &modelError{msg: "website_url cannot be empty"}
		}
		finalWebsite = trim
		if !exists || trim != existing.WebsiteUrl {
			changed = append(changed, "website_url")
		}
	} else {
		if exists {
			finalWebsite = existing.WebsiteUrl
		}
	}
	if exists {
		// brand/site must be non-empty after merge
		if strings.TrimSpace(finalBrand) == "" || strings.TrimSpace(finalWebsite) == "" {
			return Result{}, &modelError{msg: "brand_name and website_url are required"}
		}
	} else {
		// creation already validated both provided, so they are set
	}

	// primary_category
	if args.PrimaryCategory != nil {
		trim := strings.TrimSpace(*args.PrimaryCategory)
		finalCategory = pgnull.Text(trim)
		existingVal := ""
		if exists && existing.PrimaryCategory.Valid {
			existingVal = existing.PrimaryCategory.String
		}
		if trim != existingVal {
			changed = append(changed, "primary_category")
		}
	} else {
		if exists {
			finalCategory = existing.PrimaryCategory
		}
	}
	// primary_location
	if args.PrimaryLocation != nil {
		trim := strings.TrimSpace(*args.PrimaryLocation)
		finalLocation = pgnull.Text(trim)
		existingVal := ""
		if exists && existing.PrimaryLocation.Valid {
			existingVal = existing.PrimaryLocation.String
		}
		if trim != existingVal {
			changed = append(changed, "primary_location")
		}
	} else {
		if exists {
			finalLocation = existing.PrimaryLocation
		}
	}
	// business_description
	if args.BusinessDescription != nil {
		trim := strings.TrimSpace(*args.BusinessDescription)
		finalDesc = pgnull.Text(trim)
		existingVal := ""
		if exists && existing.BusinessDescription.Valid {
			existingVal = existing.BusinessDescription.String
		}
		if trim != existingVal {
			changed = append(changed, "business_description")
		}
	} else {
		if exists {
			finalDesc = existing.BusinessDescription
		}
	}

	// seed_prompts
	if args.SeedPrompts != nil {
		norm, err := businessprofile.NormalizeSeedPrompts(*args.SeedPrompts)
		if err != nil {
			return Result{}, &modelError{msg: err.Error()}
		}
		finalSeed = norm
		if !reflect.DeepEqual(norm, existingSeed) {
			changed = append(changed, "seed_prompts")
		}
	} else {
		finalSeed = existingSeed
	}
	// target_keywords
	if args.TargetKeywords != nil {
		norm := businessprofile.NormalizeTargetKeywords(*args.TargetKeywords)
		finalKeywords = norm
		if !reflect.DeepEqual(norm, existingKeywords) {
			changed = append(changed, "target_keywords")
		}
	} else {
		finalKeywords = existingKeywords
	}
	if finalSeed == nil {
		finalSeed = []string{}
	}
	if finalKeywords == nil {
		finalKeywords = []string{}
	}

	seedJSON, err := json.Marshal(finalSeed)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal seed: %w", updateBusinessProfileName, err)
	}
	kwJSON, err := json.Marshal(finalKeywords)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal keywords: %w", updateBusinessProfileName, err)
	}

	upserted, err := q.UpsertProjectBusinessProfile(ctx, sqlc.UpsertProjectBusinessProfileParams{
		ProjectID:           projectID,
		BrandName:           finalBrand,
		WebsiteUrl:          finalWebsite,
		PrimaryCategory:     finalCategory,
		PrimaryLocation:     finalLocation,
		BusinessDescription: finalDesc,
		SeedPrompts:         seedJSON,
		TargetKeywords:      kwJSON,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%s: upsert: %w", updateBusinessProfileName, err)
	}

	resp := map[string]interface{}{
		"brand_name":           upserted.BrandName,
		"website_url":          upserted.WebsiteUrl,
		"primary_category":     profileText(upserted.PrimaryCategory),
		"primary_location":     profileText(upserted.PrimaryLocation),
		"business_description": profileText(upserted.BusinessDescription),
		"seed_prompts":         finalSeed,
		"target_keywords":      finalKeywords,
	}
	content, err := json.Marshal(resp)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal response: %w", updateBusinessProfileName, err)
	}
	sort.Strings(changed)
	summary := "no changes"
	if len(changed) > 0 {
		summary = "updated " + strings.Join(changed, ", ")
		if !exists {
			summary = "created business profile: " + strings.Join(changed, ", ")
		}
	} else if !exists {
		summary = "created business profile"
	}
	return Result{Content: string(content), Summary: summary}, nil
}

func parseUpdateBusinessProfileArgs(raw json.RawMessage) (updateBusinessProfileArgs, error) {
	args := updateBusinessProfileArgs{}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	if len(fields) == 0 {
		return args, errors.New("no fields provided; provide at least one of brand_name, website_url, primary_category, primary_location, business_description, seed_prompts, target_keywords")
	}
	for key, value := range fields {
		trimmedVal := strings.TrimSpace(string(value))
		if trimmedVal == "null" {
			// JSON null is never allowed: scalars must be string, arrays must be array
			switch key {
			case "seed_prompts", "target_keywords":
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			default:
				return args, fmt.Errorf("argument %q must be a string", key)
			}
		}
		switch key {
		case "brand_name":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.BrandName = &v
		case "website_url":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.WebsiteURL = &v
		case "primary_category":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.PrimaryCategory = &v
		case "primary_location":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.PrimaryLocation = &v
		case "business_description":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.BusinessDescription = &v
		case "seed_prompts":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
			if v == nil {
				v = []string{}
			}
			args.SeedPrompts = &v
		case "target_keywords":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
			if v == nil {
				v = []string{}
			}
			args.TargetKeywords = &v
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	return args, nil
}
