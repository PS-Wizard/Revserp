package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"


	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywords"
	"github.com/ps-wizard/revserp/internal/locationkeywords"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

type locationKeywordGroup = locationkeywords.KeywordGroup
type locationStoredKeyword = locationkeywords.StoredKeyword
type locationSuggestionKeys = locationkeywords.SuggestionKeys

type locationKeywordListsResponse struct {
	UserDefined       locationKeywordGroup `json:"user_defined"`
	RevserpSuggested  locationKeywordGroup `json:"revserp_suggested"`
	Selected          locationKeywordGroup `json:"selected"`
	SuggestedOrigins  map[string][]string  `json:"suggested_origins"`
	CanManageKeywords bool                 `json:"can_manage_keywords"`
}

type putLocationKeywordListsRequest struct {
	UserDefined *locationKeywordGroup `json:"user_defined"`
	Selected    *locationKeywordGroup `json:"selected"`
}

type locationKeywordCoverageResponse struct {
	ProjectID  string          `json:"project_id"`
	LocationID string          `json:"location_id"`
	CrawlID    *string         `json:"crawl_id"`
	Seeds      []keywords.Seed `json:"seeds"`
}

func (a *App) handleGetLocationKeywordLists(w http.ResponseWriter, r *http.Request) {
	projectID, locationID, userID, location, ok := a.layer4Location(w, r)
	if !ok {
		return
	}
	rows, err := locationkeywords.LoadStoredKeywords(r.Context(), a.DB, locationID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	suggested, suggestionKeys, err := locationkeywords.LoadSuggestedKeywords(r.Context(), a.DB, a.Queries, projectID, locationID, userID, location.Localities)
	if err != nil {
		serverError(w, r, err)
		return
	}
	canManage := false
	if membership, err := a.Queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{
		OrgID:  location.OrganizationID,
		UserID: userID,
	}); err == nil {
		canManage = membership.Role == "owner"
	} else if !errors.Is(err, pgx.ErrNoRows) {
		serverError(w, r, err)
		return
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, locationKeywordListsResponse{
		UserDefined:       locationkeywords.GroupStoredKeywords(rows, "user"),
		RevserpSuggested:  suggested,
		Selected:          locationkeywords.GroupStoredKeywords(rows, "selected"),
		SuggestedOrigins:  locationkeywords.SuggestedOriginsForGroup(suggested, suggestionKeys),
		CanManageKeywords: canManage,
	})
}

func (a *App) handlePutLocationKeywordLists(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	locationID, err := parseUUIDParam(chi.URLParam(r, "locationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid location id")
		return
	}
	var body putLocationKeywordListsRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	if body.UserDefined == nil || body.Selected == nil {
		writeJSONError(w, http.StatusBadRequest, "user_defined and selected are required")
		return
	}
	userBrand, userNonBrand, err := locationkeywords.NormalizeKeywordGroup(body.UserDefined.Branded, body.UserDefined.NonBranded)
	if err != nil {
		writeLocationKeywordError(w, err)
		return
	}
	selectedBrand, selectedNonBrand, err := locationkeywords.NormalizeKeywordGroup(body.Selected.Branded, body.Selected.NonBranded)
	if err != nil {
		writeLocationKeywordError(w, err)
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(r.Context())
		}
	}()
	queries := a.Queries.WithTx(tx)
	locked, err := queries.LockProjectLocationForQueryDraftForUser(r.Context(), sqlc.LockProjectLocationForQueryDraftForUserParams{
		LocationID: locationID,
		ProjectID:  projectID,
		UserID:     principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "location not found")
		} else {
			serverError(w, r, err)
		}
		return
	}
	if err := requireOrganizationOwner(r.Context(), queries, locked.OrganizationID, principal.User.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}
	if err := locationkeywords.ReplaceStoredKeywords(r.Context(), tx, locationID, userBrand, userNonBrand, selectedBrand, selectedNonBrand); err != nil {
		serverError(w, r, err)
		return
	}
	// Selected phrases mirror into the live Maps draft only: existing draft
	// rows and frozen run snapshots are never edited or deleted here.
	suggested, suggestionKeys, err := locationkeywords.LoadSuggestedKeywords(r.Context(), tx, queries, projectID, locationID, principal.User.ID, lockedLocalities(r.Context(), queries, projectID, locationID, principal.User.ID))
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := locationkeywords.SyncSelectedMapQueries(r.Context(), queries, locationID, projectID, principal.User.ID, append(append([]string{}, selectedBrand...), selectedNonBrand...), suggestionKeys); err != nil {
		serverError(w, r, err)
		return
	}
	rows, err := locationkeywords.LoadStoredKeywords(r.Context(), tx, locationID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := emitLocationKeywordsUpdatedEvent(r.Context(), tx, locked.OrganizationID, projectID, locationID, len(userBrand)+len(selectedBrand), len(userNonBrand)+len(selectedNonBrand)); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	committed = true
	writeJSON(w, http.StatusOK, locationKeywordListsResponse{
		UserDefined:       locationkeywords.GroupStoredKeywords(rows, "user"),
		RevserpSuggested:  suggested,
		Selected:          locationkeywords.GroupStoredKeywords(rows, "selected"),
		SuggestedOrigins:  locationkeywords.SuggestedOriginsForGroup(suggested, suggestionKeys),
		CanManageKeywords: true,
	})
}

// emitLocationKeywordsUpdatedEvent writes the location_keywords.updated
// organization event inside the keyword-write transaction, so the row becomes
// visible exactly on commit and the existing SSE stream (pg_notify trigger on
// organization_events) delivers it. The payload always carries the location
// id, so subscribers invalidate exactly one location card.
func emitLocationKeywordsUpdatedEvent(ctx context.Context, tx pgx.Tx, orgID, projectID, locationID pgtype.UUID, brandCount, nonBrandCount int) error {
	payload, err := json.Marshal(map[string]any{
		"location_id":        locationID.String(),
		"source":             "user_selected",
		"brand_keywords":     brandCount,
		"non_brand_keywords": nonBrandCount,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`SELECT insert_organization_event($1, $2, 'location_keywords.updated', $3, $4::jsonb)`,
		orgID, projectID, locationID, string(payload))
	return err
}

// lockedLocalities re-reads localities under the write lock so suggestions
// match the row the PUT serialized against.
func lockedLocalities(ctx context.Context, queries *sqlc.Queries, projectID, locationID, userID pgtype.UUID) []byte {
	location, err := queries.GetProjectLocationForUser(ctx, sqlc.GetProjectLocationForUserParams{
		ID:     locationID,
		ID_2:   projectID,
		UserID: userID,
	})
	if err != nil {
		return nil
	}
	return location.Localities
}

func writeLocationKeywordError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, locationkeywords.ErrKeywordConflict):
		writeJSONError(w, http.StatusConflict, err.Error())
	case errors.Is(err, projectkeywords.ErrProjectKeywordInvalid):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, projectkeywords.ErrProjectKeywordLimit):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSONError(w, http.StatusBadRequest, err.Error())
	}
}

// handleGetLocationKeywordCoverage reuses the parent coverage algorithm and
// response over full-site stored pages, keyed on location selected keywords
// rather than the branch website scope.
func (a *App) handleGetLocationKeywordCoverage(w http.ResponseWriter, r *http.Request) {
	projectID, locationID, _, location, ok := a.layer4Location(w, r)
	if !ok {
		return
	}
	rows, err := locationkeywords.LoadStoredKeywords(r.Context(), a.DB, locationID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	targetKeywords := locationkeywords.SelectedKeywordTexts(rows)
	// The geo seed is the bound location's own place, never the parent
	// project profile: a copied parent primary_location can be a whole
	// country and must not seed a local report with a wrong market.
	primaryLocation := localCoveragePrimaryLocation(location.Locality, location.Localities)
	var crawlID *string
	pages := make([]keywords.Page, 0)
	latestCrawlID, err := a.Queries.GetLatestCompletedCrawlForProject(r.Context(), projectID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		serverError(w, r, err)
		return
	}
	if err == nil {
		id := latestCrawlID.String()
		crawlID = &id
		pageRows, listErr := a.Queries.ListKeywordCoveragePagesForCrawl(r.Context(), latestCrawlID)
		if listErr != nil {
			serverError(w, r, listErr)
			return
		}
		pages = keywordCoveragePages(pageRows)
	}
	seeds := keywords.Cover(pages, targetKeywords, primaryLocation)
	if seeds == nil {
		seeds = []keywords.Seed{}
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, locationKeywordCoverageResponse{
		ProjectID:  projectID.String(),
		LocationID: locationID.String(),
		CrawlID:    crawlID,
		Seeds:      seeds,
	})
}

// localCoveragePrimaryLocation picks the bound location's own authoritative
// place for the coverage geo seed. Localities are stored smallest-first, so
// the chosen locality wins and the first non-blank level is the fallback. An
// empty result seeds no geo keyword instead of guessing a wider market.
func localCoveragePrimaryLocation(locality string, localitiesJSON []byte) string {
	if trimmed := strings.TrimSpace(locality); trimmed != "" {
		return trimmed
	}
	var levels []string
	if len(localitiesJSON) > 0 {
		if err := json.Unmarshal(localitiesJSON, &levels); err != nil {
			return ""
		}
	}
	for _, level := range levels {
		if trimmed := strings.TrimSpace(level); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
