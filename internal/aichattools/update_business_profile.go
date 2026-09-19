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
    "product_description": {"type": "string", "description": "Product description. Trimmed; empty string clears the field."},
    "target_audience": {"type": "string", "description": "Target audience. Trimmed; empty string clears the field."},
    "business_competitors": {"type": "array", "items": {"type": "string"}, "description": "Competitor names; replaces the complete list. Empty array clears. Max 20, trimmed, empty dropped, case-insensitive dedupe preserving first spelling/order."},
    "branded_keywords": {"type": "array", "items": {"type": "string"}, "description": "Branded keywords; replaces the complete list. Empty array clears. Max 50 per keyword list. Trimmed, empty dropped, case-insensitive dedupe; entries also present in non_branded_keywords are dropped from this list."},
    "non_branded_keywords": {"type": "array", "items": {"type": "string"}, "description": "Non-branded keywords; replaces the complete list. Empty array clears. Max 50 per keyword list. Trimmed, empty dropped, case-insensitive dedupe; wins over branded_keywords on overlap."},
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
	ProductDescription  *string
	TargetAudience      *string
	BusinessCompetitors *[]string
	BrandedKeywords     *[]string
	NonBrandedKeywords  *[]string
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
	exec := updateBusinessProfileExecutor{queries: s.Queries, db: s.DB, suppressPromptGeneration: s.SuppressPromptGeneration}
	return exec.run(ctx, args, s.ProjectID, s.UserID)
}

// promptGenerationEnqueuer is the narrow surface the post-commit chat follow-up
// needs, so its suppression contract is testable without a database.
type promptGenerationEnqueuer interface {
	EnqueueAIWorkerJob(ctx context.Context, arg sqlc.EnqueueAIWorkerJobParams) (sqlc.EnqueueAIWorkerJobRow, error)
}

// enqueuePromptGenerationAfterProfileWrite preserves the chat behavior where a
// saved profile triggers question generation. Setup chaining sets suppress and
// enqueues prompt_generation itself once setup status advances, so it must not
// race a second job in from here.
func enqueuePromptGenerationAfterProfileWrite(ctx context.Context, q promptGenerationEnqueuer, projectID pgtype.UUID, suppress bool) {
	if suppress {
		return
	}
	if _, err := q.EnqueueAIWorkerJob(ctx, sqlc.EnqueueAIWorkerJobParams{JobType: "prompt_generation", ProjectID: projectID}); err != nil {
		log.Printf("enqueue prompt_generation job for project %s: %v", projectID.String(), err)
	}
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
	queries                  *sqlc.Queries
	db                       Transactor
	suppressPromptGeneration bool
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
	enqueuePromptGenerationAfterProfileWrite(ctx, e.queries, projectID, e.suppressPromptGeneration)
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

	var existingSeed, existingKeywords, existingCompetitors, existingBranded, existingNonBranded []string
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
		if args.BusinessCompetitors == nil {
			v, err := businessprofile.DecodeBusinessCompetitors(existing.BusinessCompetitors)
			if err != nil {
				return Result{}, fmt.Errorf("%s: decode business_competitors: %w", updateBusinessProfileName, err)
			}
			existingCompetitors = v
		} else {
			if v, err := businessprofile.DecodeBusinessCompetitors(existing.BusinessCompetitors); err == nil {
				existingCompetitors = v
			} else {
				existingCompetitors = []string{}
			}
		}
		if args.BrandedKeywords == nil && args.NonBrandedKeywords == nil {
			v, err := businessprofile.DecodeBrandedKeywords(existing.BrandedKeywords)
			if err != nil {
				return Result{}, fmt.Errorf("%s: decode branded_keywords: %w", updateBusinessProfileName, err)
			}
			existingBranded = v
			v, err = businessprofile.DecodeNonBrandedKeywords(existing.NonBrandedKeywords)
			if err != nil {
				return Result{}, fmt.Errorf("%s: decode non_branded_keywords: %w", updateBusinessProfileName, err)
			}
			existingNonBranded = v
		} else {
			if v, err := businessprofile.DecodeBrandedKeywords(existing.BrandedKeywords); err == nil {
				existingBranded = v
			} else {
				existingBranded = []string{}
			}
			if v, err := businessprofile.DecodeNonBrandedKeywords(existing.NonBrandedKeywords); err == nil {
				existingNonBranded = v
			} else {
				existingNonBranded = []string{}
			}
		}
	} else {
		existingSeed = []string{}
		existingKeywords = []string{}
		existingCompetitors = []string{}
		existingBranded = []string{}
		existingNonBranded = []string{}
	}

	var finalBrand, finalWebsite string
	var finalCategory, finalLocation, finalDesc, finalProduct, finalAudience pgtype.Text
	var finalSeed, finalKeywords, finalCompetitors, finalBranded, finalNonBranded []string
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
	// product_description
	if args.ProductDescription != nil {
		trim := strings.TrimSpace(*args.ProductDescription)
		finalProduct = pgnull.Text(trim)
		existingVal := ""
		if exists && existing.ProductDescription.Valid {
			existingVal = existing.ProductDescription.String
		}
		if trim != existingVal {
			changed = append(changed, "product_description")
		}
	} else {
		if exists {
			finalProduct = existing.ProductDescription
		}
	}
	// target_audience
	if args.TargetAudience != nil {
		trim := strings.TrimSpace(*args.TargetAudience)
		finalAudience = pgnull.Text(trim)
		existingVal := ""
		if exists && existing.TargetAudience.Valid {
			existingVal = existing.TargetAudience.String
		}
		if trim != existingVal {
			changed = append(changed, "target_audience")
		}
	} else {
		if exists {
			finalAudience = existing.TargetAudience
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
	// business_competitors
	if args.BusinessCompetitors != nil {
		norm := businessprofile.NormalizeBusinessCompetitors(*args.BusinessCompetitors)
		finalCompetitors = norm
		if !reflect.DeepEqual(norm, existingCompetitors) {
			changed = append(changed, "business_competitors")
		}
	} else {
		finalCompetitors = existingCompetitors
	}
	// branded_keywords / non_branded_keywords: merged pair must stay disjoint,
	// non-branded wins, even when only one side was supplied.
	{
		brandedRaw := existingBranded
		if args.BrandedKeywords != nil {
			brandedRaw = businessprofile.NormalizeStringList(*args.BrandedKeywords, businessprofile.MaxTargetKeywords)
		}
		nonBrandedRaw := existingNonBranded
		if args.NonBrandedKeywords != nil {
			nonBrandedRaw = businessprofile.NormalizeStringList(*args.NonBrandedKeywords, businessprofile.MaxTargetKeywords)
		}
		finalBranded, finalNonBranded = businessprofile.NormalizeKeywordLists(brandedRaw, nonBrandedRaw)
		if args.BrandedKeywords != nil || args.NonBrandedKeywords != nil {
			if !reflect.DeepEqual(finalBranded, existingBranded) {
				changed = append(changed, "branded_keywords")
			}
			if !reflect.DeepEqual(finalNonBranded, existingNonBranded) {
				changed = append(changed, "non_branded_keywords")
			}
		}
	}
	if finalSeed == nil {
		finalSeed = []string{}
	}
	if finalKeywords == nil {
		finalKeywords = []string{}
	}
	if finalCompetitors == nil {
		finalCompetitors = []string{}
	}
	if finalBranded == nil {
		finalBranded = []string{}
	}
	if finalNonBranded == nil {
		finalNonBranded = []string{}
	}

	seedJSON, err := json.Marshal(finalSeed)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal seed: %w", updateBusinessProfileName, err)
	}
	kwJSON, err := json.Marshal(finalKeywords)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal keywords: %w", updateBusinessProfileName, err)
	}
	competitorsJSON, err := json.Marshal(finalCompetitors)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal competitors: %w", updateBusinessProfileName, err)
	}
	brandedJSON, err := json.Marshal(finalBranded)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal branded: %w", updateBusinessProfileName, err)
	}
	nonBrandedJSON, err := json.Marshal(finalNonBranded)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal non-branded: %w", updateBusinessProfileName, err)
	}

	upserted, err := q.UpsertProjectBusinessProfile(ctx, sqlc.UpsertProjectBusinessProfileParams{
		ProjectID:           projectID,
		BrandName:           finalBrand,
		WebsiteUrl:          finalWebsite,
		PrimaryCategory:     finalCategory,
		PrimaryLocation:     finalLocation,
		BusinessDescription: finalDesc,
		ProductDescription:  finalProduct,
		TargetAudience:      finalAudience,
		BusinessCompetitors: competitorsJSON,
		BrandedKeywords:     brandedJSON,
		NonBrandedKeywords:  nonBrandedJSON,
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
		"product_description":  profileText(upserted.ProductDescription),
		"target_audience":      profileText(upserted.TargetAudience),
		"business_competitors": finalCompetitors,
		"branded_keywords":     finalBranded,
		"non_branded_keywords": finalNonBranded,
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
		return args, errors.New("no fields provided; provide at least one of brand_name, website_url, primary_category, primary_location, business_description, product_description, target_audience, business_competitors, branded_keywords, non_branded_keywords, seed_prompts, target_keywords")
	}
	for key, value := range fields {
		trimmedVal := strings.TrimSpace(string(value))
		if trimmedVal == "null" {
			// JSON null is never allowed: scalars must be string, arrays must be array
			switch key {
			case "seed_prompts", "target_keywords", "business_competitors", "branded_keywords", "non_branded_keywords":
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
		case "product_description":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.ProductDescription = &v
		case "target_audience":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.TargetAudience = &v
		case "business_competitors":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
			if v == nil {
				v = []string{}
			}
			args.BusinessCompetitors = &v
		case "branded_keywords":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
			if v == nil {
				v = []string{}
			}
			args.BrandedKeywords = &v
		case "non_branded_keywords":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
			if v == nil {
				v = []string{}
			}
			args.NonBrandedKeywords = &v
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
