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
	"github.com/ps-wizard/revserp/internal/issues/shared"
	"github.com/ps-wizard/revserp/internal/keywords"
	"github.com/ps-wizard/revserp/internal/locationkeywords"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

type locationKeywordReader interface {
	GetProjectLocationForUser(ctx context.Context, arg sqlc.GetProjectLocationForUserParams) (sqlc.GetProjectLocationForUserRow, error)
}

type locationKeywordOwner interface {
	LockProjectLocationForQueryDraftForUser(ctx context.Context, arg sqlc.LockProjectLocationForQueryDraftForUserParams) (sqlc.LockProjectLocationForQueryDraftForUserRow, error)
	GetOrganizationMember(ctx context.Context, arg sqlc.GetOrganizationMemberParams) (sqlc.OrganizationMember, error)
}

func emitLocationKeywordsUpdated(ctx context.Context, db locationkeywords.DB, orgID, projectID, locationID pgtype.UUID, source string, brandCount, nonBrandCount int) error {
	payload, err := json.Marshal(map[string]interface{}{
		"location_id":        locationID.String(),
		"source":             source,
		"brand_keywords":     brandCount,
		"non_brand_keywords": nonBrandCount,
	})
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx,
		`SELECT insert_organization_event($1, $2, 'location_keywords.updated', $3, $4::jsonb)`,
		orgID, projectID, locationID, string(payload))
	return err
}

func checkLocationReadAccess(ctx context.Context, access locationKeywordReader, projectID, locationID, userID pgtype.UUID) (sqlc.GetProjectLocationForUserRow, error) {
	location, err := access.GetProjectLocationForUser(ctx, sqlc.GetProjectLocationForUserParams{
		ID:     locationID,
		ID_2:   projectID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.GetProjectLocationForUserRow{}, &modelError{msg: "location not found or access denied"}
		}
		return sqlc.GetProjectLocationForUserRow{}, err
	}
	return location, nil
}

// requireLocationOwner locks the location for writing and requires an
// organization owner. Members may read; only owners mutate keyword state.
func requireLocationOwner(ctx context.Context, access locationKeywordOwner, projectID, locationID, userID pgtype.UUID) (sqlc.LockProjectLocationForQueryDraftForUserRow, error) {
	locked, err := access.LockProjectLocationForQueryDraftForUser(ctx, sqlc.LockProjectLocationForQueryDraftForUserParams{
		LocationID: locationID,
		ProjectID:  projectID,
		UserID:     userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.LockProjectLocationForQueryDraftForUserRow{}, &modelError{msg: "location not found or access denied"}
		}
		return sqlc.LockProjectLocationForQueryDraftForUserRow{}, err
	}
	member, err := access.GetOrganizationMember(ctx, sqlc.GetOrganizationMemberParams{
		OrgID:  locked.OrganizationID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.LockProjectLocationForQueryDraftForUserRow{}, &modelError{msg: "only organization owners can update location keywords"}
		}
		return sqlc.LockProjectLocationForQueryDraftForUserRow{}, err
	}
	if member.Role != "owner" {
		return sqlc.LockProjectLocationForQueryDraftForUserRow{}, &modelError{msg: "only organization owners can update location keywords"}
	}
	return locked, nil
}

func nonNullLocationKeywordGroup(group locationkeywords.KeywordGroup) locationkeywords.KeywordGroup {
	if group.Branded == nil {
		group.Branded = []string{}
	}
	if group.NonBranded == nil {
		group.NonBranded = []string{}
	}
	return group
}

func parseLocalKeywordArgs(raw json.RawMessage) (updateProjectKeywordsArgs, error) {
	args := updateProjectKeywordsArgs{}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for key, value := range fields {
		if strings.TrimSpace(string(value)) == "null" {
			if key == "source" {
				return args, fmt.Errorf("argument %q must be \"selected\" or \"revserp\"", key)
			}
			return args, fmt.Errorf("argument %q must be an array of strings", key)
		}
		switch key {
		case "brand_keywords", "non_brand_keywords":
			var v []string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be an array of strings", key)
			}
			if key == "brand_keywords" {
				args.BrandKeywords = v
			} else {
				args.NonBrandKeywords = v
			}
		case "source":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be \"selected\" or \"revserp\"", key)
			}
			if v != locationkeywords.SourceSelected && v != locationkeywords.SourceRevserp {
				return args, fmt.Errorf("argument %q must be \"selected\" or \"revserp\"", key)
			}
			args.Source = v
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	if !fieldPresent(fields, "brand_keywords") || !fieldPresent(fields, "non_brand_keywords") {
		return args, fmt.Errorf("brand_keywords and non_brand_keywords are both required as complete lists")
	}
	if !fieldPresent(fields, "source") {
		return args, fmt.Errorf("argument \"source\" is required: must be \"selected\" or \"revserp\"")
	}
	return args, nil
}

// stored rows from db, suggestions from db plus generated location queries.
type locationKeywordListsReader interface {
	locationKeywordReader
	locationkeywords.Queries
}

// locationKeywordListsLocalExecutor runs the location branch of
// get_project_keywords. The db is the caller's read handle (a rolled-back tx
// in production); locations carries the location-scoped generated queries.
type locationKeywordListsLocalExecutor struct {
	db        locationkeywords.DB
	locations locationKeywordListsReader
}

// executeGetProjectKeywordsLocal dispatches a location-scoped call to the
// local executor. Reads run on a rolled-back tx; every return stays local
// and never falls through to the parent lists.
func executeGetProjectKeywordsLocal(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.Queries == nil || s.DB == nil {
		return Result{}, errors.New("get_project_keywords: scope has no queries or transaction support")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("get_project_keywords: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	exec := locationKeywordListsLocalExecutor{db: tx, locations: s.Queries.WithTx(tx)}
	return exec.runLocal(ctx, args, s.ProjectID, s.LocationID, s.UserID)
}

// runLocal reads local user, suggested, and selected lists. Suggestions
// derive from saved services, localities, and landmarks only; no provider or
// model is consulted.
func (e *locationKeywordListsLocalExecutor) runLocal(ctx context.Context, raw json.RawMessage, projectID, locationID, userID pgtype.UUID) (Result, error) {
	if err := rejectProjectKeywordsArgs(raw); err != nil {
		return Result{Content: getProjectKeywordsName + " error: " + err.Error()}, nil
	}
	location, err := checkLocationReadAccess(ctx, e.locations, projectID, locationID, userID)
	if err != nil {
		if isModelError(err) {
			return Result{Content: getProjectKeywordsName + " error: " + err.Error()}, nil
		}
		return Result{}, fmt.Errorf("get_project_keywords: check location access: %w", err)
	}
	rows, err := locationkeywords.LoadStoredKeywords(ctx, e.db, locationID)
	if err != nil {
		return Result{}, fmt.Errorf("get_project_keywords: load location keywords: %w", err)
	}
	suggested, suggestionKeys, err := locationkeywords.LoadSuggestedKeywords(ctx, e.db, e.locations, projectID, locationID, userID, location.Localities)
	if err != nil {
		return Result{}, fmt.Errorf("get_project_keywords: load location suggestions: %w", err)
	}
	userDefined := nonNullLocationKeywordGroup(locationkeywords.GroupStoredKeywords(rows, locationkeywords.SourceUser))
	suggestedGroup := nonNullLocationKeywordGroup(suggested)
	selectedGroup := nonNullLocationKeywordGroup(locationkeywords.GroupStoredKeywords(rows, locationkeywords.SourceSelected))
	response := map[string]any{
		"user_defined":      userDefined,
		"revserp_suggested": suggestedGroup,
		"selected":          selectedGroup,
		"suggested_origins": locationkeywords.SuggestedOriginsForGroup(suggested, suggestionKeys),
	}
	content, err := json.Marshal(response)
	if err != nil {
		return Result{}, fmt.Errorf("get_project_keywords: marshal location keywords: %w", err)
	}
	summary := fmt.Sprintf("location keywords: %d user-defined, %d suggested, %d selected",
		len(userDefined.Branded)+len(userDefined.NonBranded),
		len(suggestedGroup.Branded)+len(suggestedGroup.NonBranded),
		len(selectedGroup.Branded)+len(selectedGroup.NonBranded))
	return Result{Content: string(content), Summary: summary}, nil
}

type locationKeywordUpdateReader interface {
	locationKeywordReader
	locationKeywordOwner
	locationkeywords.Queries
}

// locationKeywordUpdateLocalExecutor runs the location branch of
// update_project_keywords: it replaces the LOCATION selected list only and
// syncs future Maps draft queries in the same transaction. Parent
// project_keywords rows are never touched and no prompt generation follows.
// The db is the caller's write transaction.
type locationKeywordUpdateLocalExecutor struct {
	db        locationkeywords.DB
	locations locationKeywordUpdateReader
}

// executeUpdateProjectKeywordsLocal dispatches a location-scoped call: the
// selected list and the future Maps draft update atomically, and every
// return stays local, never falling through to the parent lists.
func executeUpdateProjectKeywordsLocal(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.Queries == nil || s.DB == nil {
		return Result{}, errors.New("update_project_keywords: scope has no queries or transaction support")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("update_project_keywords: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	exec := locationKeywordUpdateLocalExecutor{db: tx, locations: s.Queries.WithTx(tx)}
	res, err := exec.runLocal(ctx, args, s.ProjectID, s.LocationID, s.UserID)
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("update_project_keywords: commit: %w", err)
	}
	return res, nil
}

// runLocal replaces one location keyword list from the model's complete
// brand/non_brand lists. The source is always explicit: source "selected"
// replaces the selected list and syncs future Maps draft queries, so the
// model must pass it only after the user explicitly asks to select Maps
// queries; empty clears every map draft enabled flag. Source "revserp" saves
// Find-keywords suggestions only: user-defined and selected rows stay
// untouched and no Maps query syncs.
func (e *locationKeywordUpdateLocalExecutor) runLocal(ctx context.Context, raw json.RawMessage, projectID, locationID, userID pgtype.UUID) (Result, error) {
	args, err := parseLocalKeywordArgs(raw)
	if err != nil {
		return Result{Content: updateProjectKeywordsName + " error: " + err.Error()}, nil
	}
	if args.Source == locationkeywords.SourceRevserp {
		return e.runRevserp(ctx, args, projectID, locationID, userID)
	}
	return e.runSelected(ctx, args, projectID, locationID, userID)
}

// runRevserp persists Find-keywords suggestions to the location's revserp
// source only: an owner-only atomic replace. User-defined and selected
// lists, product_description, history, and Maps drafts are never touched,
// and no question generation follows.
func (e *locationKeywordUpdateLocalExecutor) runRevserp(ctx context.Context, args updateProjectKeywordsArgs, projectID, locationID, userID pgtype.UUID) (Result, error) {
	brand, nonBrand, err := projectkeywords.NormalizeSuggestedProjectKeywords(args.BrandKeywords, args.NonBrandKeywords)
	if err != nil {
		if isProjectKeywordValidationError(err) {
			return Result{Content: updateProjectKeywordsName + " error: " + err.Error()}, nil
		}
		return Result{}, fmt.Errorf("%s: normalize suggested keywords: %w", updateProjectKeywordsName, err)
	}
	locked, err := requireLocationOwner(ctx, e.locations, projectID, locationID, userID)
	if err != nil {
		if isModelError(err) {
			return Result{Content: updateProjectKeywordsName + " error: " + err.Error()}, nil
		}
		return Result{}, fmt.Errorf("%s: check location owner: %w", updateProjectKeywordsName, err)
	}
	if err := locationkeywords.ReplaceRevserpKeywords(ctx, e.db, locationID, brand, nonBrand); err != nil {
		return Result{}, fmt.Errorf("%s: replace suggested keywords: %w", updateProjectKeywordsName, err)
	}
	if err := emitLocationKeywordsUpdated(ctx, e.db, locked.OrganizationID, projectID, locationID, locationkeywords.SourceRevserp, len(brand), len(nonBrand)); err != nil {
		return Result{}, fmt.Errorf("%s: emit location_keywords.updated: %w", updateProjectKeywordsName, err)
	}
	content, err := json.Marshal(map[string]interface{}{
		"brand_keywords":     brand,
		"non_brand_keywords": nonBrand,
		"source":             locationkeywords.SourceRevserp,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal response: %w", updateProjectKeywordsName, err)
	}
	summary := fmt.Sprintf("updated location suggested keywords: %d brand, %d non-brand (revserp-suggested; user-defined and selected preserved; Maps draft untouched)", len(brand), len(nonBrand))
	return Result{Content: string(content), Summary: summary}, nil
}

// runSelected replaces the location selected list and syncs future Maps
// draft queries in the same transaction. Parent project_keywords rows are
// never touched and no prompt generation follows.
func (e *locationKeywordUpdateLocalExecutor) runSelected(ctx context.Context, args updateProjectKeywordsArgs, projectID, locationID, userID pgtype.UUID) (Result, error) {
	brand, nonBrand, err := locationkeywords.NormalizeKeywordGroup(args.BrandKeywords, args.NonBrandKeywords)
	if err != nil {
		if errors.Is(err, locationkeywords.ErrKeywordConflict) || isProjectKeywordValidationError(err) {
			return Result{Content: updateProjectKeywordsName + " error: " + err.Error()}, nil
		}
		return Result{}, fmt.Errorf("%s: normalize selected keywords: %w", updateProjectKeywordsName, err)
	}
	locked, err := requireLocationOwner(ctx, e.locations, projectID, locationID, userID)
	if err != nil {
		if isModelError(err) {
			return Result{Content: updateProjectKeywordsName + " error: " + err.Error()}, nil
		}
		return Result{}, fmt.Errorf("%s: check location owner: %w", updateProjectKeywordsName, err)
	}
	if err := locationkeywords.ReplaceSelectedKeywords(ctx, e.db, locationID, brand, nonBrand); err != nil {
		return Result{}, fmt.Errorf("%s: replace selected keywords: %w", updateProjectKeywordsName, err)
	}
	if err := emitLocationKeywordsUpdated(ctx, e.db, locked.OrganizationID, projectID, locationID, locationkeywords.SourceSelected, len(brand), len(nonBrand)); err != nil {
		return Result{}, fmt.Errorf("%s: emit location_keywords.updated: %w", updateProjectKeywordsName, err)
	}
	location, err := e.locations.GetProjectLocationForUser(ctx, sqlc.GetProjectLocationForUserParams{
		ID:     locationID,
		ID_2:   projectID,
		UserID: userID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%s: read location localities: %w", updateProjectKeywordsName, err)
	}
	_, keys, err := locationkeywords.LoadSuggestedKeywords(ctx, e.db, e.locations, projectID, locationID, userID, location.Localities)
	if err != nil {
		return Result{}, fmt.Errorf("%s: load suggestion keys: %w", updateProjectKeywordsName, err)
	}
	if err := locationkeywords.SyncSelectedMapQueries(ctx, e.locations, locationID, projectID, userID, append(append([]string{}, brand...), nonBrand...), keys); err != nil {
		return Result{}, fmt.Errorf("%s: sync selected map queries: %w", updateProjectKeywordsName, err)
	}
	content, err := json.Marshal(map[string]interface{}{
		"brand_keywords":     brand,
		"non_brand_keywords": nonBrand,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal response: %w", updateProjectKeywordsName, err)
	}
	summary := fmt.Sprintf("updated location selected keywords: %d brand, %d non-brand (user-defined preserved; future Maps queries synced)", len(brand), len(nonBrand))
	return Result{Content: string(content), Summary: summary}, nil
}

type locationKeywordCoverageReader interface {
	locationKeywordReader
	GetLatestCompletedCrawlForProject(ctx context.Context, projectID pgtype.UUID) (pgtype.UUID, error)
	ListKeywordCoveragePagesForCrawl(ctx context.Context, crawlID pgtype.UUID) ([]sqlc.ListKeywordCoveragePagesForCrawlRow, error)
}

type locationKeywordCoverageLocalExecutor struct {
	db        locationkeywords.DB
	locations locationKeywordCoverageReader
	cover     func(pages []keywords.Page, targetKeywords []string, primaryLocation string) []keywords.Seed
}

func (e *locationKeywordCoverageLocalExecutor) coverFunc() func([]keywords.Page, []string, string) []keywords.Seed {
	if e.cover != nil {
		return e.cover
	}
	return keywords.Cover
}

// executeGetLocationKeywordCoverage dispatches a location-scoped call to the
// local coverage executor. Every return stays local and never falls through
// to the parent keyword coverage.
func executeGetLocationKeywordCoverage(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.Queries == nil || s.DB == nil {
		return Result{}, errors.New("get_keyword_coverage: scope has no queries or transaction support")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("get_keyword_coverage: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	exec := locationKeywordCoverageLocalExecutor{db: tx, locations: s.Queries.WithTx(tx)}
	return exec.runLocal(ctx, args, s.ProjectID, s.LocationID, s.UserID, s.RowBudget)
}

func (e *locationKeywordCoverageLocalExecutor) runLocal(ctx context.Context, raw json.RawMessage, projectID, locationID, userID pgtype.UUID, budget *Budget) (Result, error) {
	if budget != nil && budget.Remaining() == 0 {
		return Result{
			Content: "The row budget for this turn is exhausted. Do not call get_keyword_coverage again; synthesize your answer from the data you already have.",
			Summary: "row budget reached",
		}, nil
	}
	args, err := parseKeywordCoverageArgs(raw)
	if err != nil {
		return Result{Content: getKeywordCoverageName + " error: " + err.Error()}, nil
	}
	location, err := checkLocationReadAccess(ctx, e.locations, projectID, locationID, userID)
	if err != nil {
		if isModelError(err) {
			return Result{Content: getKeywordCoverageName + " error: " + err.Error()}, nil
		}
		return Result{}, fmt.Errorf("get_keyword_coverage: check location access: %w", err)
	}
	crawlID, err := e.locations.GetLatestCompletedCrawlForProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{
				Content: "get_keyword_coverage: no completed crawl yet for this project, so there is no keyword coverage to report. Ask the user to run a crawl first.",
				Summary: "no completed crawl",
			}, nil
		}
		return Result{}, fmt.Errorf("%s: read latest crawl: %w", getKeywordCoverageName, err)
	}
	rows, err := locationkeywords.LoadStoredKeywords(ctx, e.db, locationID)
	if err != nil {
		return Result{}, fmt.Errorf("get_keyword_coverage: load selected keywords: %w", err)
	}
	targetKeywords := locationkeywords.SelectedKeywordTexts(rows)
	// Local coverage targets the bound location's locality, never the parent
	// profile's primary_location (which may name a whole country).
	primaryLocation := location.Locality
	pageRows, err := e.locations.ListKeywordCoveragePagesForCrawl(ctx, crawlID)
	if err != nil {
		return Result{}, fmt.Errorf("%s: list coverage pages: %w", getKeywordCoverageName, err)
	}
	seeds := e.coverFunc()(keywordCoveragePagesFromRows(pageRows), targetKeywords, primaryLocation)
	if seeds == nil {
		seeds = []keywords.Seed{}
	}
	sortKeywordCoverageSeeds(seeds)
	counts := countKeywordCoverageStates(seeds)
	filtered := filterKeywordCoverageSeeds(seeds, args)
	truncated := len(filtered) > args.Limit
	filtered = filtered[:min(len(filtered), args.Limit)]
	out := shapeKeywordCoverageSeeds(filtered)
	if budget != nil {
		budget.Spend(len(out))
	}
	crawlIDStr := crawlID.String()
	response := keywordCoverageResponse{
		CrawlID:    &crawlIDStr,
		Counts:     counts,
		TotalSeeds: len(seeds),
		Seeds:      out,
		Truncated:  truncated,
	}
	content, err := json.Marshal(response)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal response: %w", getKeywordCoverageName, err)
	}
	return Result{
		Content: string(content),
		Summary: fmt.Sprintf("location coverage: %d targeted, %d gaps, %d cannibalized",
			counts.LikelyTargeted, counts.NoLandingPage, counts.Cannibalized),
	}, nil
}

func keywordCoveragePagesFromRows(pageRows []sqlc.ListKeywordCoveragePagesForCrawlRow) []keywords.Page {
	pages := make([]keywords.Page, 0, len(pageRows))
	for _, row := range pageRows {
		if !shared.IsScoreablePage(shared.CrawlPageSignal{
			StatusCode:  row.StatusCode,
			ContentType: row.ContentType,
			Soft404:     row.Soft404,
			FetchError:  row.FetchError,
		}) {
			continue
		}
		pages = append(pages, keywords.Page{URL: row.Url, Title: row.Title, H1: row.H1})
	}
	return pages
}
