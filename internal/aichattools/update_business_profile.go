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
	"github.com/ps-wizard/revserp/internal/projectkeywords"
	"github.com/ps-wizard/revserp/internal/textnormalization"
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
    "branded_keywords": {"type": "array", "items": {"type": "string", "maxLength": 200}, "maxItems": 10, "description": "Legacy Revserp-suggested brand keywords; replaces the complete suggested brand list and never touches user-defined keywords. Both branded_keywords and non_branded_keywords are required together as complete non-empty lists whenever either is supplied, and both are required on creation. Keyword management is separate from the profile: prefer update_project_keywords for the Find Keywords flow (it enforces both lists by schema and runtime validation)."},
    "non_branded_keywords": {"type": "array", "items": {"type": "string", "maxLength": 200}, "maxItems": 10, "description": "Legacy Revserp-suggested non-brand keywords; replaces the complete suggested non-brand list and never touches user-defined keywords. Both branded_keywords and non_branded_keywords are required together as complete non-empty lists whenever either is supplied, and both are required on creation. Keyword management is separate from the profile: prefer update_project_keywords for the Find Keywords flow (it enforces both lists by schema and runtime validation)."},
    "seed_prompts": {"type": "array", "items": {"type": "string"}, "maxItems": 5, "description": "Seed prompts; replaces the complete list. Empty array clears. Max 5, no empty values."}
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
}

func updateBusinessProfileTool() Tool {
	return Tool{
		Def: Def{
			Name:        updateBusinessProfileName,
			Label:       "Update business profile",
			Description: "Update the business profile for the current project (PATCH). May be called only after the user clearly asks to save or change the profile. Provide only fields to change; omitted fields are preserved atomically. Arrays replace the complete list and [] clears. Requires organization owner; non-owners are denied. For creation when no profile exists, brand_name and website_url are required plus complete branded_keywords and non_branded_keywords lists. Keyword management is separate from the profile and never blocks manual profile editing: read lists with get_project_keywords and refresh REVSerp suggestions with update_project_keywords. Server authorization is the real boundary, not model instructions.",
			Schema:      json.RawMessage(updateBusinessProfileSchema),
		},
		Execute: executeUpdateBusinessProfile,
	}
}

func executeUpdateBusinessProfile(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.Queries == nil {
		return Result{}, errors.New("update_business_profile: scope has no queries")
	}
	if s.LocationID.Valid {
		exec := updateBusinessProfileLocalExecutor{locations: s.Queries}
		return exec.runLocal(ctx, args, s.ProjectID, s.LocationID, s.UserID)
	}
	if s.DB == nil {
		return Result{}, errors.New("update_business_profile: scope has no transaction support")
	}
	exec := updateBusinessProfileExecutor{queries: s.Queries, db: s.DB, suppressPromptGeneration: s.SuppressPromptGeneration, keywords: contractProjectKeywordService{}}
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

// shouldEnqueuePromptGenerationAfterProfileWrite keeps the existing workflow
// for real profile changes but never triggers question generation for
// keyword-only edits or no-change saves: refreshing Revserp suggestions or
// resubmitting identical keywords must not enqueue prompt_generation.
// Creation always enqueues; mixed real profile changes still do.
func shouldEnqueuePromptGenerationAfterProfileWrite(exists bool, changed []string) bool {
	if !exists {
		return true
	}
	if len(changed) == 0 {
		return false
	}
	for _, field := range changed {
		if field != "branded_keywords" && field != "non_branded_keywords" {
			return true
		}
	}
	return false
}

type modelError struct{ msg string }

func (e *modelError) Error() string { return e.msg }

func isModelError(err error) bool {
	var m *modelError
	return errors.As(err, &m)
}

// isProjectKeywordValidationError reports service-side keyword validation
// failures (blank, overlong, or capped lists) so they stay model-visible
// instead of surfacing as infrastructure errors.
func isProjectKeywordValidationError(err error) bool {
	return errors.Is(err, projectkeywords.ErrProjectKeywordInvalid) ||
		errors.Is(err, projectkeywords.ErrProjectKeywordLimit) ||
		errors.Is(err, projectkeywords.ErrProjectKeywordConflict)
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
	keywords                 projectKeywordService
}

func (e *updateBusinessProfileExecutor) keywordService() projectKeywordService {
	if e.keywords != nil {
		return e.keywords
	}
	return contractProjectKeywordService{}
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
	res, changed, existed, err := e.patch(ctx, args, projectID, userID, qtx, qtx)
	if err != nil {
		if isModelError(err) {
			return Result{Content: updateBusinessProfileName + " error: " + err.Error()}, nil
		}
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("%s: commit: %w", updateBusinessProfileName, err)
	}
	suppress := e.suppressPromptGeneration || !shouldEnqueuePromptGenerationAfterProfileWrite(existed, changed)
	enqueuePromptGenerationAfterProfileWrite(ctx, e.queries, projectID, suppress)
	return res, nil
}

// patch applies one update and returns the model-facing result, the changed
// field names, and whether the profile already existed. run needs changed and
// existed to decide the prompt_generation follow-up: creation always keeps
// it, keyword-only edits never trigger it.
func (e *updateBusinessProfileExecutor) patch(ctx context.Context, args updateBusinessProfileArgs, projectID, userID pgtype.UUID, q updateBusinessProfileQuerier, qtx *sqlc.Queries) (Result, []string, bool, error) {
	// Lock project row to serialize concurrent profile writes (including creation).
	project, err := q.GetProjectByIDForUserForBusinessProfileUpdate(ctx, sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams{ID: projectID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, nil, false, &modelError{msg: "project not found or access denied"}
		}
		return Result{}, nil, false, fmt.Errorf("%s: lock project: %w", updateBusinessProfileName, err)
	}
	member, err := q.GetOrganizationMember(ctx, sqlc.GetOrganizationMemberParams{OrgID: project.OrganizationID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, nil, false, &modelError{msg: "only organization owners can update the business profile"}
		}
		return Result{}, nil, false, fmt.Errorf("%s: get membership: %w", updateBusinessProfileName, err)
	}
	if member.Role != "owner" {
		return Result{}, nil, false, &modelError{msg: "only organization owners can update the business profile"}
	}

	existing, err := q.GetProjectBusinessProfileByProjectID(ctx, projectID)
	exists := true
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			exists = false
		} else {
			return Result{}, nil, false, fmt.Errorf("%s: read profile: %w", updateBusinessProfileName, err)
		}
	}
	if !exists {
		if args.BrandName == nil || strings.TrimSpace(*args.BrandName) == "" || args.WebsiteURL == nil || strings.TrimSpace(*args.WebsiteURL) == "" {
			return Result{}, nil, false, &modelError{msg: "no business profile exists yet; to create one, provide non-empty brand_name and website_url"}
		}
		if args.BrandedKeywords == nil || args.NonBrandedKeywords == nil {
			return Result{}, nil, false, &modelError{msg: "no business profile exists yet; to create one, also provide complete branded_keywords and non_branded_keywords lists (each non-empty)"}
		}
	}

	var existingSeed, existingCompetitors []string
	if exists {
		if args.SeedPrompts == nil {
			v, err := businessprofile.DecodeSeedPrompts(existing.SeedPrompts)
			if err != nil {
				return Result{}, nil, true, fmt.Errorf("%s: decode seed_prompts: %w", updateBusinessProfileName, err)
			}
			existingSeed = v
		} else {
			if v, err := businessprofile.DecodeSeedPrompts(existing.SeedPrompts); err == nil {
				existingSeed = v
			} else {
				existingSeed = []string{}
			}
		}
		if args.BusinessCompetitors == nil {
			v, err := businessprofile.DecodeBusinessCompetitors(existing.BusinessCompetitors)
			if err != nil {
				return Result{}, nil, true, fmt.Errorf("%s: decode business_competitors: %w", updateBusinessProfileName, err)
			}
			existingCompetitors = v
		} else {
			if v, err := businessprofile.DecodeBusinessCompetitors(existing.BusinessCompetitors); err == nil {
				existingCompetitors = v
			} else {
				existingCompetitors = []string{}
			}
		}
	} else {
		existingSeed = []string{}
		existingCompetitors = []string{}
	}

	// Revserp-suggested baseline always comes from the keyword service, never
	// the combined read aliases: feeding the union back into a suggested-only
	// replace would copy user-defined terms into the suggested source.
	baselineBrand, baselineNonBrand, err := e.loadSuggestedBaseline(ctx, qtx, projectID)
	if err != nil {
		return Result{}, nil, exists, err
	}

	var finalBrand, finalWebsite string
	var finalCategory, finalLocation, finalDesc, finalProduct, finalAudience pgtype.Text
	var finalSeed, finalCompetitors, finalBranded, finalNonBranded []string
	changed := []string{}

	// brand_name
	if args.BrandName != nil {
		trim := strings.TrimSpace(*args.BrandName)
		if trim == "" {
			return Result{}, nil, exists, &modelError{msg: "brand_name cannot be empty"}
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
			return Result{}, nil, exists, &modelError{msg: "website_url cannot be empty"}
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
			return Result{}, nil, exists, &modelError{msg: "brand_name and website_url are required"}
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
			return Result{}, nil, exists, &modelError{msg: err.Error()}
		}
		finalSeed = norm
		if !reflect.DeepEqual(norm, existingSeed) {
			changed = append(changed, "seed_prompts")
		}
	} else {
		finalSeed = existingSeed
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
	// branded_keywords / non_branded_keywords route to the Revserp-suggested
	// source only: both lists are required together, each non-empty, and the
	// user-defined source is never read or written here.
	if !exists || args.BrandedKeywords != nil || args.NonBrandedKeywords != nil {
		if exists && (args.BrandedKeywords == nil || args.NonBrandedKeywords == nil) {
			return Result{}, nil, exists, &modelError{msg: "branded_keywords and non_branded_keywords must be provided together as complete non-empty lists"}
		}
		brand, nonBrand, err := projectkeywords.NormalizeSuggestedProjectKeywords(derefStringList(args.BrandedKeywords), derefStringList(args.NonBrandedKeywords))
		if err != nil {
			if isProjectKeywordValidationError(err) {
				return Result{}, nil, exists, &modelError{msg: err.Error()}
			}
			return Result{}, nil, exists, fmt.Errorf("%s: normalize suggested keywords: %w", updateBusinessProfileName, err)
		}
		if !exists || !equalProjectKeywordSets(brand, baselineBrand) || !equalProjectKeywordSets(nonBrand, baselineNonBrand) {
			if err := e.keywordService().ReplaceSuggestedKeywords(ctx, qtx, projectID, brand, nonBrand); err != nil {
				if isProjectKeywordValidationError(err) {
					return Result{}, nil, exists, &modelError{msg: err.Error()}
				}
				return Result{}, nil, exists, fmt.Errorf("%s: replace suggested keywords: %w", updateBusinessProfileName, err)
			}
			finalBranded, finalNonBranded = brand, nonBrand
			if !reflect.DeepEqual(brand, baselineBrand) {
				changed = append(changed, "branded_keywords")
			}
			if !reflect.DeepEqual(nonBrand, baselineNonBrand) {
				changed = append(changed, "non_branded_keywords")
			}
		} else {
			finalBranded, finalNonBranded = baselineBrand, baselineNonBrand
		}
	} else {
		finalBranded, finalNonBranded = baselineBrand, baselineNonBrand
	}
	if finalSeed == nil {
		finalSeed = []string{}
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
		return Result{}, nil, exists, fmt.Errorf("%s: marshal seed: %w", updateBusinessProfileName, err)
	}
	competitorsJSON, err := json.Marshal(finalCompetitors)
	if err != nil {
		return Result{}, nil, exists, fmt.Errorf("%s: marshal competitors: %w", updateBusinessProfileName, err)
	}

	// The profile upsert carries no keyword columns: suggested keywords persist
	// through the keyword service in this same transaction, atomically.
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
		SeedPrompts:         seedJSON,
	})
	if err != nil {
		return Result{}, nil, exists, fmt.Errorf("%s: upsert: %w", updateBusinessProfileName, err)
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
	}
	content, err := json.Marshal(resp)
	if err != nil {
		return Result{}, nil, exists, fmt.Errorf("%s: marshal response: %w", updateBusinessProfileName, err)
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
	return Result{Content: string(content), Summary: summary}, changed, exists, nil
}

// loadSuggestedBaseline reads the current Revserp-suggested lists for diffing
// and responses. User-defined keywords stay out of this path entirely.
func (e *updateBusinessProfileExecutor) loadSuggestedBaseline(ctx context.Context, qtx *sqlc.Queries, projectID pgtype.UUID) (brand, nonBrand []string, err error) {
	lists, err := e.keywordService().LoadProjectKeywordLists(ctx, qtx, projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: load suggested keywords: %w", updateBusinessProfileName, err)
	}
	brand, nonBrand = splitRevserpSuggested(lists)
	return brand, nonBrand, nil
}

// splitRevserpSuggested splits suggested rows into brand and non-brand display
// phrases for keyword set comparison.
func splitRevserpSuggested(lists projectkeywords.KeywordLists) (brand, nonBrand []string) {
	brand = []string{}
	nonBrand = []string{}
	for _, entry := range lists.RevserpSuggested {
		if entry.Kind == projectkeywords.ProjectKeywordKindBrand {
			brand = append(brand, entry.Keyword)
		} else {
			nonBrand = append(nonBrand, entry.Keyword)
		}
	}
	return brand, nonBrand
}

// equalProjectKeywordSets compares keyword phrases as normalized sets so row
// ordering or display-only drift never triggers a spurious suggested replace.
func equalProjectKeywordSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, phrase := range a {
		counts[projectkeywords.NormalizeProjectKeywordKey(phrase)]++
	}
	for _, phrase := range b {
		key := projectkeywords.NormalizeProjectKeywordKey(phrase)
		if counts[key] == 0 {
			return false
		}
		counts[key]--
	}
	return true
}

func derefStringList(values *[]string) []string {
	if values == nil {
		return nil
	}
	return *values
}

func parseUpdateBusinessProfileArgs(raw json.RawMessage) (updateBusinessProfileArgs, error) {
	args := updateBusinessProfileArgs{}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	if len(fields) == 0 {
		return args, errors.New("no fields provided; provide at least one of brand_name, website_url, primary_category, primary_location, business_description, product_description, target_audience, business_competitors, branded_keywords, non_branded_keywords, seed_prompts")
	}
	for key, value := range fields {
		if key == "target_keywords" {
			return args, errors.New(`argument "target_keywords" is no longer supported as a profile write; manage Revserp-suggested keywords with update_project_keywords and read all lists with get_project_keywords`)
		}
		trimmedVal := strings.TrimSpace(string(value))
		if trimmedVal == "null" {
			// JSON null is never allowed: scalars must be string, arrays must be array
			switch key {
			case "seed_prompts", "business_competitors", "branded_keywords", "non_branded_keywords":
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
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	return args, nil
}

// updateBusinessProfileLocationQuerier is the generated-query surface the
// location branch needs, implemented by *sqlc.Queries and fakes.
type updateBusinessProfileLocationQuerier interface {
	GetProjectLocationForUser(ctx context.Context, arg sqlc.GetProjectLocationForUserParams) (sqlc.GetProjectLocationForUserRow, error)
	GetOrganizationMember(ctx context.Context, arg sqlc.GetOrganizationMemberParams) (sqlc.OrganizationMember, error)
	GetLocationBusinessProfile(ctx context.Context, arg sqlc.GetLocationBusinessProfileParams) (sqlc.LocationBusinessProfile, error)
	UpsertLocationBusinessProfile(ctx context.Context, arg sqlc.UpsertLocationBusinessProfileParams) (sqlc.LocationBusinessProfile, error)
}

type updateBusinessProfileLocalArgs struct {
	BrandName           *string
	WebsiteURL          *string
	PrimaryCategory     *string
	PrimaryLocation     *string
	BusinessDescription *string
	ProductDescription  *string
	TargetAudience      *string
	BusinessCompetitors *[]string
	SeedPrompts         *[]string
	Services            *[]string
}

type updateBusinessProfileLocalExecutor struct {
	locations updateBusinessProfileLocationQuerier
}

func (e *updateBusinessProfileLocalExecutor) runLocal(ctx context.Context, raw json.RawMessage, projectID, locationID, userID pgtype.UUID) (Result, error) {
	args, err := parseUpdateBusinessProfileLocalArgs(raw)
	if err != nil {
		return Result{Content: updateBusinessProfileName + " error: " + err.Error()}, nil
	}
	res, _, _, err := e.patchLocal(ctx, args, projectID, locationID, userID, e.locations)
	if err != nil {
		if isModelError(err) {
			return Result{Content: updateBusinessProfileName + " error: " + err.Error()}, nil
		}
		return Result{}, err
	}
	return res, nil
}

// patchLocal merges one location update and returns the model-facing
// result, the changed field names, and whether the profile existed. Omitted
// fields keep their stored values; a nil services list preserves the
// snapshot while an empty list clears it.
func (e *updateBusinessProfileLocalExecutor) patchLocal(ctx context.Context, args updateBusinessProfileLocalArgs, projectID, locationID, userID pgtype.UUID, q updateBusinessProfileLocationQuerier) (Result, []string, bool, error) {
	location, err := q.GetProjectLocationForUser(ctx, sqlc.GetProjectLocationForUserParams{ID: locationID, ID_2: projectID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, nil, false, &modelError{msg: "location not found or access denied"}
		}
		return Result{}, nil, false, fmt.Errorf("%s: read location: %w", updateBusinessProfileName, err)
	}
	member, err := q.GetOrganizationMember(ctx, sqlc.GetOrganizationMemberParams{OrgID: location.OrganizationID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, nil, false, &modelError{msg: "only organization owners can update the business profile"}
		}
		return Result{}, nil, false, fmt.Errorf("%s: get membership: %w", updateBusinessProfileName, err)
	}
	if member.Role != "owner" {
		return Result{}, nil, false, &modelError{msg: "only organization owners can update the business profile"}
	}

	existing, err := q.GetLocationBusinessProfile(ctx, sqlc.GetLocationBusinessProfileParams{ProjectID: projectID, LocationID: locationID})
	exists := true
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			exists = false
		} else {
			return Result{}, nil, false, fmt.Errorf("%s: read location profile: %w", updateBusinessProfileName, err)
		}
	}
	if !exists {
		if args.BrandName == nil || strings.TrimSpace(*args.BrandName) == "" || args.WebsiteURL == nil || strings.TrimSpace(*args.WebsiteURL) == "" {
			return Result{}, nil, false, &modelError{msg: "no business profile exists for this location yet; to create one, provide non-empty brand_name and website_url"}
		}
	}

	var existingSeed, existingCompetitors, existingServices []string
	if exists {
		if v, err := businessprofile.DecodeSeedPrompts(existing.SeedPrompts); err == nil {
			existingSeed = v
		} else if args.SeedPrompts == nil {
			return Result{}, nil, true, fmt.Errorf("%s: decode seed_prompts: %w", updateBusinessProfileName, err)
		} else {
			existingSeed = []string{}
		}
		if v, err := businessprofile.DecodeBusinessCompetitors(existing.BusinessCompetitors); err == nil {
			existingCompetitors = v
		} else if args.BusinessCompetitors == nil {
			return Result{}, nil, true, fmt.Errorf("%s: decode business_competitors: %w", updateBusinessProfileName, err)
		} else {
			existingCompetitors = []string{}
		}
		if v, err := businessprofile.DecodeStringSlice(existing.Services); err == nil {
			existingServices = v
		} else if args.Services == nil {
			return Result{}, nil, true, fmt.Errorf("%s: decode services: %w", updateBusinessProfileName, err)
		} else {
			existingServices = []string{}
		}
	} else {
		existingSeed = []string{}
		existingCompetitors = []string{}
		existingServices = []string{}
	}

	var finalBrand, finalWebsite string
	var finalCategory, finalLocation, finalDesc, finalProduct, finalAudience pgtype.Text
	var finalSeed, finalCompetitors, finalServices []string
	var servicesColumn any
	changed := []string{}

	mergeText := func(arg *string, field string, current pgtype.Text) pgtype.Text {
		if arg == nil {
			return current
		}
		trim := strings.TrimSpace(*arg)
		existingVal := ""
		if current.Valid {
			existingVal = current.String
		}
		if trim != existingVal {
			changed = append(changed, field)
		}
		return pgnull.Text(trim)
	}

	if args.BrandName != nil {
		trim := strings.TrimSpace(*args.BrandName)
		if trim == "" {
			return Result{}, nil, exists, &modelError{msg: "brand_name cannot be empty"}
		}
		finalBrand = trim
		if !exists || trim != existing.BrandName {
			changed = append(changed, "brand_name")
		}
	} else if exists {
		finalBrand = existing.BrandName
	}
	if args.WebsiteURL != nil {
		trim := strings.TrimSpace(*args.WebsiteURL)
		if trim == "" {
			return Result{}, nil, exists, &modelError{msg: "website_url cannot be empty"}
		}
		finalWebsite = trim
		if !exists || trim != existing.WebsiteUrl {
			changed = append(changed, "website_url")
		}
	} else if exists {
		finalWebsite = existing.WebsiteUrl
	}
	if exists && (strings.TrimSpace(finalBrand) == "" || strings.TrimSpace(finalWebsite) == "") {
		return Result{}, nil, exists, &modelError{msg: "brand_name and website_url are required"}
	}
	if exists {
		finalCategory = mergeText(args.PrimaryCategory, "primary_category", existing.PrimaryCategory)
		finalLocation = mergeText(args.PrimaryLocation, "primary_location", existing.PrimaryLocation)
		finalDesc = mergeText(args.BusinessDescription, "business_description", existing.BusinessDescription)
		finalProduct = mergeExactText(args.ProductDescription, existing.ProductDescription, &changed)
		finalAudience = mergeText(args.TargetAudience, "target_audience", existing.TargetAudience)
	} else {
		finalCategory = pgnull.Text(trimArg(args.PrimaryCategory))
		finalLocation = pgnull.Text(trimArg(args.PrimaryLocation))
		finalDesc = pgnull.Text(trimArg(args.BusinessDescription))
		finalProduct = pgnull.Text(exactArg(args.ProductDescription))
		finalAudience = pgnull.Text(trimArg(args.TargetAudience))
	}

	if args.SeedPrompts != nil {
		norm, err := businessprofile.NormalizeSeedPrompts(*args.SeedPrompts)
		if err != nil {
			return Result{}, nil, exists, &modelError{msg: err.Error()}
		}
		finalSeed = norm
		if !reflect.DeepEqual(norm, existingSeed) {
			changed = append(changed, "seed_prompts")
		}
	} else {
		finalSeed = existingSeed
	}
	if args.BusinessCompetitors != nil {
		norm := businessprofile.NormalizeBusinessCompetitors(*args.BusinessCompetitors)
		finalCompetitors = norm
		if !reflect.DeepEqual(norm, existingCompetitors) {
			changed = append(changed, "business_competitors")
		}
	} else {
		finalCompetitors = existingCompetitors
	}
	if args.Services != nil {
		norm, err := normalizeLocationServices(*args.Services)
		if err != nil {
			return Result{}, nil, exists, &modelError{msg: err.Error()}
		}
		finalServices = norm
		if !reflect.DeepEqual(norm, existingServices) {
			changed = append(changed, "services")
		}
		encoded, err := json.Marshal(finalServices)
		if err != nil {
			return Result{}, nil, exists, fmt.Errorf("%s: marshal services: %w", updateBusinessProfileName, err)
		}
		servicesColumn = encoded
	} else {
		finalServices = existingServices
	}
	if finalSeed == nil {
		finalSeed = []string{}
	}
	if finalCompetitors == nil {
		finalCompetitors = []string{}
	}
	if finalServices == nil {
		finalServices = []string{}
	}

	seedJSON, err := json.Marshal(finalSeed)
	if err != nil {
		return Result{}, nil, exists, fmt.Errorf("%s: marshal seed: %w", updateBusinessProfileName, err)
	}
	competitorsJSON, err := json.Marshal(finalCompetitors)
	if err != nil {
		return Result{}, nil, exists, fmt.Errorf("%s: marshal competitors: %w", updateBusinessProfileName, err)
	}

	upserted, err := q.UpsertLocationBusinessProfile(ctx, sqlc.UpsertLocationBusinessProfileParams{
		ProjectID:           projectID,
		LocationID:          locationID,
		BrandName:           finalBrand,
		WebsiteUrl:          finalWebsite,
		PrimaryCategory:     finalCategory,
		PrimaryLocation:     finalLocation,
		BusinessDescription: finalDesc,
		ProductDescription:  finalProduct,
		TargetAudience:      finalAudience,
		BusinessCompetitors: competitorsJSON,
		SeedPrompts:         seedJSON,
		Column12:            servicesColumn,
	})
	if err != nil {
		return Result{}, nil, exists, fmt.Errorf("%s: upsert location profile: %w", updateBusinessProfileName, err)
	}

	resp := map[string]interface{}{
		"location_id":          locationID.String(),
		"brand_name":           upserted.BrandName,
		"website_url":          upserted.WebsiteUrl,
		"primary_category":     profileText(upserted.PrimaryCategory),
		"primary_location":     profileText(upserted.PrimaryLocation),
		"business_description": profileText(upserted.BusinessDescription),
		"product_description":  profileText(upserted.ProductDescription),
		"target_audience":      profileText(upserted.TargetAudience),
		"business_competitors": finalCompetitors,
		"seed_prompts":         finalSeed,
		"services":             finalServices,
	}
	content, err := json.Marshal(resp)
	if err != nil {
		return Result{}, nil, exists, fmt.Errorf("%s: marshal response: %w", updateBusinessProfileName, err)
	}
	sort.Strings(changed)
	summary := "no changes"
	if len(changed) > 0 {
		summary = "updated " + strings.Join(changed, ", ")
		if !exists {
			summary = "created location business profile: " + strings.Join(changed, ", ")
		}
	} else if !exists {
		summary = "created location business profile"
	}
	return Result{Content: string(content), Summary: summary}, changed, exists, nil
}

// mergeExactText merges product_description byte-exact: leading/trailing
// whitespace and newlines are significant content copied verbatim from the
// parent, never normalized. Only this field merges exact; metadata trims.
func mergeExactText(arg *string, current pgtype.Text, changed *[]string) pgtype.Text {
	if arg == nil {
		return current
	}
	existingVal := ""
	if current.Valid {
		existingVal = current.String
	}
	if *arg != existingVal {
		*changed = append(*changed, "product_description")
	}
	return pgnull.Text(*arg)
}

// exactArg passes an optional argument through byte-exact, treating
// absence as empty.
func exactArg(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// trimArg trims an optional scalar argument, treating absence as empty.
func trimArg(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func normalizeLocationServices(raw []string) ([]string, error) {
	services := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, label := range raw {
		if strings.ContainsRune(label, 0) {
			return nil, errors.New("service label must not contain nul")
		}
		display := textnormalization.NormalizeTextDisplay(label)
		if display == "" {
			continue
		}
		if len(display) > 200 {
			return nil, errors.New("service label must fit within 200 bytes")
		}
		key := textnormalization.NormalizeTextKey(label)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		services = append(services, display)
	}
	return services, nil
}

func parseUpdateBusinessProfileLocalArgs(raw json.RawMessage) (updateBusinessProfileLocalArgs, error) {
	args := updateBusinessProfileLocalArgs{}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	if len(fields) == 0 {
		return args, errors.New("no fields provided; provide at least one of brand_name, website_url, primary_category, primary_location, business_description, product_description, target_audience, business_competitors, seed_prompts, services")
	}
	for key, value := range fields {
		if key == "branded_keywords" || key == "non_branded_keywords" || key == "target_keywords" {
			return args, fmt.Errorf("argument %q is managed through location keyword lists, not here", key)
		}
		trimmedVal := strings.TrimSpace(string(value))
		if trimmedVal == "null" {
			switch key {
			case "seed_prompts", "business_competitors", "services":
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
		case "seed_prompts":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
			if v == nil {
				v = []string{}
			}
			args.SeedPrompts = &v
		case "services":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
			if v == nil {
				v = []string{}
			}
			args.Services = &v
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	return args, nil
}
