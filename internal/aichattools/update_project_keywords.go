package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

const updateProjectKeywordsName = "update_project_keywords"

const updateProjectKeywordsSchema = `{
  "type": "object",
  "required": ["brand_keywords", "non_brand_keywords"],
  "properties": {
    "brand_keywords": {"type": "array", "items": {"type": "string", "maxLength": 200}, "minItems": 1, "maxItems": 10, "description": "Complete Revserp-suggested brand keyword list: the brand name plus obvious brand variants. Replaces the complete suggested brand set. Non-empty after trimming; ground suggestions in live Search Console data (get_search_console_data) when connected, otherwise crawled page content. Never invent search volume or rank numbers."},
    "non_brand_keywords": {"type": "array", "items": {"type": "string", "maxLength": 200}, "minItems": 1, "maxItems": 10, "description": "Complete Revserp-suggested non-brand keyword list: category and need keywords with no brand terms. Replaces the complete suggested non-brand set. Non-empty after trimming; ground suggestions in live Search Console data (get_search_console_data) when connected, otherwise crawled page content. Never invent search volume or rank numbers."}
  },
  "additionalProperties": false
}`

type updateProjectKeywordsArgs struct {
	BrandKeywords    []string
	NonBrandKeywords []string
}

func updateProjectKeywordsTool() Tool {
	return Tool{
		Def: Def{
			Name:        updateProjectKeywordsName,
			Label:       "Update project keywords",
			Description: "Refresh the Revserp-suggested keyword lists for the current project (Find Keywords flow). Call only after the user clearly asks to find, refresh, or save keywords. Both brand_keywords and non_brand_keywords are required as complete, non-empty lists; each call replaces the suggested source atomically and preserves user-defined keywords. Requires organization owner; non-owners are denied. Never invent search volume or rank numbers: use live Search Console data when connected, otherwise crawled content. Keyword edits never trigger question generation.",
			Schema:      json.RawMessage(updateProjectKeywordsSchema),
		},
		Execute: executeUpdateProjectKeywords,
	}
}

func executeUpdateProjectKeywords(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.Queries == nil || s.DB == nil {
		return Result{}, errors.New("update_project_keywords: scope has no queries or transaction support")
	}
	exec := updateProjectKeywordsExecutor{
		queries:   s.Queries,
		db:        s.DB,
		keywords:  contractProjectKeywordService{},
		emitEvent: emitProjectKeywordsUpdated,
	}
	return exec.run(ctx, args, s.ProjectID, s.UserID)
}

type updateProjectKeywordsQuerier interface {
	GetProjectByIDForUserForBusinessProfileUpdate(ctx context.Context, arg sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams) (sqlc.Project, error)
	GetOrganizationMember(ctx context.Context, arg sqlc.GetOrganizationMemberParams) (sqlc.OrganizationMember, error)
}

type updateProjectKeywordsExecutor struct {
	queries   *sqlc.Queries
	db        Transactor
	keywords  projectKeywordService
	emitEvent func(ctx context.Context, exec sqlExecer, orgID, projectID pgtype.UUID, brandCount, nonBrandCount int) error
}

func (e *updateProjectKeywordsExecutor) run(ctx context.Context, raw json.RawMessage, projectID, userID pgtype.UUID) (Result, error) {
	args, err := parseUpdateProjectKeywordsArgs(raw)
	if err != nil {
		return Result{Content: updateProjectKeywordsName + " error: " + err.Error()}, nil
	}
	tx, err := e.db.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("%s: begin tx: %w", updateProjectKeywordsName, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := e.queries.WithTx(tx)
	res, err := e.apply(ctx, args, projectID, userID, qtx, qtx, tx)
	if err != nil {
		if isModelError(err) {
			return Result{Content: updateProjectKeywordsName + " error: " + err.Error()}, nil
		}
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("%s: commit: %w", updateProjectKeywordsName, err)
	}
	return res, nil
}

func (e *updateProjectKeywordsExecutor) apply(ctx context.Context, args updateProjectKeywordsArgs, projectID, userID pgtype.UUID, q updateProjectKeywordsQuerier, qtx *sqlc.Queries, exec sqlExecer) (Result, error) {
	project, err := q.GetProjectByIDForUserForBusinessProfileUpdate(ctx, sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams{ID: projectID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, &modelError{msg: "project not found or access denied"}
		}
		return Result{}, fmt.Errorf("%s: lock project: %w", updateProjectKeywordsName, err)
	}
	member, err := q.GetOrganizationMember(ctx, sqlc.GetOrganizationMemberParams{OrgID: project.OrganizationID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, &modelError{msg: "only organization owners can update project keywords"}
		}
		return Result{}, fmt.Errorf("%s: get membership: %w", updateProjectKeywordsName, err)
	}
	if member.Role != "owner" {
		return Result{}, &modelError{msg: "only organization owners can update project keywords"}
	}
	brand, nonBrand, err := projectkeywords.NormalizeSuggestedProjectKeywords(args.BrandKeywords, args.NonBrandKeywords)
	if err != nil {
		if isProjectKeywordValidationError(err) {
			return Result{}, &modelError{msg: err.Error()}
		}
		return Result{}, fmt.Errorf("%s: normalize suggested keywords: %w", updateProjectKeywordsName, err)
	}
	keywords := e.keywords
	if keywords == nil {
		keywords = contractProjectKeywordService{}
	}
	if err := keywords.ReplaceSuggestedKeywords(ctx, qtx, projectID, brand, nonBrand); err != nil {
		if isProjectKeywordValidationError(err) {
			return Result{}, &modelError{msg: err.Error()}
		}
		return Result{}, fmt.Errorf("%s: replace suggested keywords: %w", updateProjectKeywordsName, err)
	}
	emit := e.emitEvent
	if emit == nil {
		emit = emitProjectKeywordsUpdated
	}
	if err := emit(ctx, exec, project.OrganizationID, projectID, len(brand), len(nonBrand)); err != nil {
		return Result{}, fmt.Errorf("%s: emit project_keywords.updated: %w", updateProjectKeywordsName, err)
	}
	content, err := json.Marshal(map[string]interface{}{
		"brand_keywords":     brand,
		"non_brand_keywords": nonBrand,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal response: %w", updateProjectKeywordsName, err)
	}
	summary := fmt.Sprintf("updated project keywords: %d brand, %d non-brand (Revserp-suggested; user-defined preserved)", len(brand), len(nonBrand))
	return Result{Content: string(content), Summary: summary}, nil
}

func parseUpdateProjectKeywordsArgs(raw json.RawMessage) (updateProjectKeywordsArgs, error) {
	args := updateProjectKeywordsArgs{}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for key, value := range fields {
		if strings.TrimSpace(string(value)) == "null" {
			return args, fmt.Errorf("argument %q must be an array of strings", key)
		}
		switch key {
		case "brand_keywords":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
			args.BrandKeywords = v
		case "non_brand_keywords":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
			args.NonBrandKeywords = v
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	if len(fields) == 0 {
		return args, errors.New("brand_keywords and non_brand_keywords are required as complete, non-empty lists")
	}
	if !fieldPresent(fields, "brand_keywords") || !fieldPresent(fields, "non_brand_keywords") {
		return args, errors.New("brand_keywords and non_brand_keywords are both required as complete, non-empty lists")
	}
	if !hasNonBlankStrings(args.BrandKeywords) || !hasNonBlankStrings(args.NonBrandKeywords) {
		return args, errors.New("brand_keywords and non_brand_keywords must each be non-empty after trimming")
	}
	return args, nil
}

func fieldPresent(fields map[string]json.RawMessage, key string) bool {
	_, ok := fields[key]
	return ok
}

func hasNonBlankStrings(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}
