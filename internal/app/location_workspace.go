package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/websitescope"
)

// locationWorkspaceDB is the pgx query surface shared by the pool and
// transactions, so scope reads stay on one helper.
type locationWorkspaceDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// locationWebsiteScopeRevision is one immutable branch website scope
// revision. The latest revision per location is current; a nil current
// scope or match "none" means the location has no branch audit.
type locationWebsiteScopeRevision struct {
	ID        string  `json:"id"`
	Revision  int32   `json:"revision"`
	URL       *string `json:"url"`
	Match     string  `json:"match"`
	CreatedAt string  `json:"created_at"`
}

type locationWorkspaceResponse struct {
	Location              localVisibilityLocationResponse `json:"location"`
	CanManage             bool                            `json:"can_manage"`
	WebsiteScope          *locationWebsiteScopeRevision   `json:"website_scope"`
	WebsiteScopeRevisions []locationWebsiteScopeRevision  `json:"website_scope_revisions"`
}

type locationWebsiteScopeResponse struct {
	WebsiteScope          *locationWebsiteScopeRevision  `json:"website_scope"`
	WebsiteScopeRevisions []locationWebsiteScopeRevision `json:"website_scope_revisions"`
}

type putLocationWebsiteScopeRequest struct {
	URL   *string `json:"url"`
	Match string  `json:"match"`
}

const (
	selectLocationWebsiteScopeRevisionsSQL = `SELECT id, revision, url, match, created_at
		FROM location_website_scopes WHERE location_id = $1 ORDER BY revision DESC`
	selectProjectBaseURLSQL                     = `SELECT base_url FROM projects WHERE id = $1 LIMIT 1`
	lockLocationWebsiteScopeRowSQL              = `SELECT 1 FROM project_locations WHERE id = $1 FOR UPDATE`
	selectLatestLocationWebsiteScopeRevisionSQL = `SELECT id, revision, url, match, created_at
		FROM location_website_scopes WHERE location_id = $1 ORDER BY revision DESC LIMIT 1 FOR UPDATE`
	insertLocationWebsiteScopeRevisionSQL = `INSERT INTO location_website_scopes
		(project_id, location_id, revision, url, match)
		SELECT $1, $2, COALESCE((SELECT MAX(revision) FROM location_website_scopes WHERE location_id = $2), 0) + 1, $3, $4
		RETURNING id, revision, created_at`
)

// loadLocationWorkspaceLocation verifies project membership and location
// ownership in one query, so foreign location IDs read as 404.
func (a *App) loadLocationWorkspaceLocation(w http.ResponseWriter, r *http.Request) (sqlc.GetProjectLocationForUserRow, pgtype.UUID, bool) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return sqlc.GetProjectLocationForUserRow{}, pgtype.UUID{}, false
	}
	locationID, err := parseUUIDParam(chi.URLParam(r, "locationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid location id")
		return sqlc.GetProjectLocationForUserRow{}, pgtype.UUID{}, false
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return sqlc.GetProjectLocationForUserRow{}, pgtype.UUID{}, false
	}
	location, err := a.Queries.GetProjectLocationForUser(r.Context(), sqlc.GetProjectLocationForUserParams{
		ID:     locationID,
		ID_2:   projectID,
		UserID: principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "location not found")
		} else {
			serverError(w, r, err)
		}
		return sqlc.GetProjectLocationForUserRow{}, pgtype.UUID{}, false
	}
	return location, principal.User.ID, true
}

func locationWorkspaceCanManage(role string) bool {
	return role == "owner"
}

func (a *App) locationWorkspaceRole(ctx context.Context, location sqlc.GetProjectLocationForUserRow, userID pgtype.UUID) (string, error) {
	membership, err := a.Queries.GetOrganizationMember(ctx, sqlc.GetOrganizationMemberParams{
		OrgID:  location.OrganizationID,
		UserID: userID,
	})
	if err != nil {
		return "", err
	}
	return membership.Role, nil
}

func scanLocationWebsiteScopeRevision(id pgtype.UUID, revision int32, rawURL pgtype.Text, match string, createdAt pgtype.Timestamptz) locationWebsiteScopeRevision {
	scope := locationWebsiteScopeRevision{
		ID:        id.String(),
		Revision:  revision,
		Match:     match,
		CreatedAt: createdAt.Time.UTC().Format(time.RFC3339),
	}
	if rawURL.Valid {
		url := rawURL.String
		scope.URL = &url
	}
	return scope
}

func listLocationWebsiteScopeRevisions(ctx context.Context, db locationWorkspaceDB, locationID pgtype.UUID) ([]locationWebsiteScopeRevision, error) {
	rows, err := db.Query(ctx, selectLocationWebsiteScopeRevisionsSQL, locationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	revisions := []locationWebsiteScopeRevision{}
	for rows.Next() {
		var id pgtype.UUID
		var revision int32
		var rawURL pgtype.Text
		var match string
		var createdAt pgtype.Timestamptz
		if err := rows.Scan(&id, &revision, &rawURL, &match, &createdAt); err != nil {
			return nil, err
		}
		revisions = append(revisions, scanLocationWebsiteScopeRevision(id, revision, rawURL, match, createdAt))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return revisions, nil
}

// normalizeWebsiteScopeInput maps a PUT body to the stored (url, match)
// pair. match "none" always clears the URL; exact and subtree require a
// canonical absolute URL.
func normalizeWebsiteScopeInput(rawURL *string, match string) (any, string, error) {
	trimmedMatch := strings.TrimSpace(match)
	if !websitescope.ValidScopeMatch(trimmedMatch) {
		return nil, "", errors.New("match must be one of none, exact, subtree")
	}
	if trimmedMatch == websitescope.ScopeMatchNone {
		return nil, trimmedMatch, nil
	}
	if rawURL == nil || strings.TrimSpace(*rawURL) == "" {
		return nil, "", errors.New("url is required for exact and subtree scopes")
	}
	canonical, err := websitescope.CanonicalizeWebsiteScopeURL(*rawURL)
	if err != nil {
		return nil, "", err
	}
	return canonical, trimmedMatch, nil
}

// handleGetLocationWorkspace serves the scoped shell payload: the existing
// location shape plus website scope history. No provider calls, no writes.
func (a *App) handleGetLocationWorkspace(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	role, err := a.locationWorkspaceRole(r.Context(), location, userID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	response, err := a.assembleLocationResponse(r.Context(), location.ID, location.ProjectID,
		location.Name, location.PlaceID, location.Latitude, location.Longitude,
		location.Address, location.Locality, location.Localities, location.RadiusM, userID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	revisions, err := listLocationWebsiteScopeRevisions(r.Context(), a.DB, location.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	workspace := locationWorkspaceResponse{
		Location:              response,
		CanManage:             locationWorkspaceCanManage(role),
		WebsiteScopeRevisions: revisions,
	}
	if len(revisions) > 0 {
		workspace.WebsiteScope = &revisions[0]
	}
	writeJSON(w, http.StatusOK, workspace)
}

// handleGetLocationWebsiteScope serves the current scope plus immutable
// history without touching parent crawls or historical evidence.
func (a *App) handleGetLocationWebsiteScope(w http.ResponseWriter, r *http.Request) {
	location, _, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	revisions, err := listLocationWebsiteScopeRevisions(r.Context(), a.DB, location.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	response := locationWebsiteScopeResponse{WebsiteScopeRevisions: revisions}
	if len(revisions) > 0 {
		response.WebsiteScope = &revisions[0]
	}
	writeJSON(w, http.StatusOK, response)
}

// locationWebsiteScopeSameOrigin loads the authorized parent website and
// rejects scope URLs from a foreign origin before anything persists.
// Membership was already verified by the location load, so reading the
// parent base URL here stays within the same authorization.
func (a *App) locationWebsiteScopeSameOrigin(w http.ResponseWriter, r *http.Request, location sqlc.GetProjectLocationForUserRow, storedURL any) bool {
	canonical, ok := storedURL.(string)
	if !ok {
		return true
	}
	var baseURL string
	if err := a.DB.QueryRow(r.Context(), selectProjectBaseURLSQL, location.ProjectID).Scan(&baseURL); err != nil {
		serverError(w, r, err)
		return false
	}
	if !websitescope.ScopeMatchesParentOrigin(canonical, baseURL) {
		writeJSONError(w, http.StatusBadRequest, "scope url must share the parent website origin")
		return false
	}
	return true
}

// handlePutLocationWebsiteScope appends a scope revision, never overwriting.
// Repeating the current normalized input returns it idempotently.
func (a *App) handlePutLocationWebsiteScope(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	var body putLocationWebsiteScopeRequest
	if !readJSONOrRespond(w, r, &body) {
		return
	}
	storedURL, match, err := normalizeWebsiteScopeInput(body.URL, body.Match)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if match != websitescope.ScopeMatchNone {
		if !a.locationWebsiteScopeSameOrigin(w, r, location, storedURL) {
			return
		}
	}
	if err := requireOrganizationOwner(r.Context(), a.Queries, location.OrganizationID, userID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var locked int
	if err := tx.QueryRow(r.Context(), lockLocationWebsiteScopeRowSQL, location.ID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "location not found")
		} else {
			serverError(w, r, err)
		}
		return
	}
	var latestID pgtype.UUID
	var latestRevision int32
	var latestURL pgtype.Text
	var latestMatch string
	var latestAt pgtype.Timestamptz
	latestErr := tx.QueryRow(r.Context(), selectLatestLocationWebsiteScopeRevisionSQL, location.ID).
		Scan(&latestID, &latestRevision, &latestURL, &latestMatch, &latestAt)
	if latestErr != nil && !errors.Is(latestErr, pgx.ErrNoRows) {
		serverError(w, r, latestErr)
		return
	}
	if latestErr == nil {
		var currentURL *string
		if latestURL.Valid {
			url := latestURL.String
			currentURL = &url
		}
		if latestMatch == match && equalNullableScopeURL(currentURL, storedURL) {
			if err := tx.Commit(r.Context()); err != nil {
				serverError(w, r, err)
				return
			}
			writeJSON(w, http.StatusOK, scanLocationWebsiteScopeRevision(latestID, latestRevision, latestURL, latestMatch, latestAt))
			return
		}
	}
	var id pgtype.UUID
	var revision int32
	var createdAt pgtype.Timestamptz
	if err := tx.QueryRow(r.Context(), insertLocationWebsiteScopeRevisionSQL,
		location.ProjectID, location.ID, storedURL, match).Scan(&id, &revision, &createdAt); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	var rawURL pgtype.Text
	if canonical, ok := storedURL.(string); ok {
		rawURL = pgtype.Text{String: canonical, Valid: true}
	}
	writeJSON(w, http.StatusOK, scanLocationWebsiteScopeRevision(id, revision, rawURL, match, createdAt))
}

func equalNullableScopeURL(current *string, stored any) bool {
	canonical, ok := stored.(string)
	if !ok {
		return current == nil
	}
	return current != nil && *current == canonical
}
