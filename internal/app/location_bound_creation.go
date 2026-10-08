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

// createBoundLocationRequest is the POST locations/bound body. SearchID names
// the saved server evidence; only PlaceID and RadiusM come from the client and
// both are verified against that evidence before anything is stored.
//
// WebsiteScope is optional: callers that omit it get no initial branch scope,
// which reads as match "none". The creation wizard always sends it.
type createBoundLocationRequest struct {
	SearchID     string                           `json:"search_id"`
	PlaceID      string                           `json:"place_id"`
	RadiusM      int                              `json:"radius_m"`
	WebsiteScope *createBoundLocationWebsiteScope `json:"website_scope"`
}

// createBoundLocationWebsiteScope is the optional initial branch scope the
// caller submits with the new location.
type createBoundLocationWebsiteScope struct {
	Match string  `json:"match"`
	URL   *string `json:"url"`
}

// boundLocationWebsiteScope is a normalized scope ready to store beside the
// location. A nil scope means no revision row at all (the field was omitted).
type boundLocationWebsiteScope struct {
	url   any
	match string
}

const insertBoundProjectLocation = `INSERT INTO project_locations(project_id, name, place_id, latitude, longitude, address, locality, localities, radius_m)
VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)
RETURNING id, project_id, name, place_id, latitude, longitude, created_at, updated_at, address, locality, localities, radius_m`

// resolveBoundLocationWebsiteScope validates and canonicalizes the optional
// initial scope against the authorized parent origin. It returns nil when the
// caller omitted the field, so old callers keep the pre-scope behaviour. It
// reuses the same normalization and origin rule as the scope PUT endpoint.
func resolveBoundLocationWebsiteScope(scope *createBoundLocationWebsiteScope, parentBaseURL string) (*boundLocationWebsiteScope, error) {
	if scope == nil {
		return nil, nil
	}
	storedURL, match, err := normalizeWebsiteScopeInput(scope.URL, scope.Match)
	if err != nil {
		return nil, err
	}
	if match != websitescope.ScopeMatchNone {
		canonical, ok := storedURL.(string)
		if !ok || !websitescope.ScopeMatchesParentOrigin(canonical, parentBaseURL) {
			return nil, errors.New("scope url must share the parent website origin")
		}
	}
	return &boundLocationWebsiteScope{url: storedURL, match: match}, nil
}

// insertBoundLocationWithScope writes the bound location and, when scope is
// non-nil, its initial website-scope revision on the caller's transaction. The
// caller commits once, so a failed scope insert leaves no partial location and
// the scope history starts exactly with the location.
func insertBoundLocationWithScope(
	ctx context.Context,
	tx pgx.Tx,
	projectID pgtype.UUID,
	name, placeID string,
	latitude, longitude float64,
	address string,
	radiusM int,
	scope *boundLocationWebsiteScope,
) (sqlc.ProjectLocation, error) {
	var created sqlc.ProjectLocation
	// Saved listing evidence is identity-only (place id, display name,
	// coordinates, formatted address), so there is no structured locality or
	// country to bind. Locality stays empty rather than guessing from the
	// formatted address or copying the parent; the explicit local-geography
	// workflow fills it later.
	if err := tx.QueryRow(ctx, insertBoundProjectLocation,
		projectID, name, placeID, latitude, longitude, address, "", []byte("[]"), radiusM,
	).Scan(&created.ID, &created.ProjectID, &created.Name, &created.PlaceID,
		&created.Latitude, &created.Longitude, &created.CreatedAt, &created.UpdatedAt,
		&created.Address, &created.Locality, &created.Localities, &created.RadiusM); err != nil {
		return created, err
	}
	if scope != nil {
		var scopeID pgtype.UUID
		var revision int32
		var createdAt pgtype.Timestamptz
		if err := tx.QueryRow(ctx, insertLocationWebsiteScopeRevisionSQL,
			created.ProjectID, created.ID, scope.url, scope.match,
		).Scan(&scopeID, &revision, &createdAt); err != nil {
			return created, err
		}
	}
	return created, nil
}

// selectBoundLocationCandidate matches one client-selected place ID against
// the candidates rebuilt from saved server evidence. Anything else is
// rejected: the client never supplies title or coordinates.
func selectBoundLocationCandidate(candidates []locationListingCandidate, placeID string) (locationListingCandidate, error) {
	placeID = strings.TrimSpace(placeID)
	if placeID == "" {
		return locationListingCandidate{}, errors.New("place_id is required")
	}
	for _, candidate := range candidates {
		if candidate.PlaceID == placeID {
			return candidate, nil
		}
	}
	return locationListingCandidate{}, errors.New("place_id must be a candidate from this project search")
}

// handleCreateBoundLocation answers POST /projects/{projectID}/locations/bound.
// It atomically creates one bound location from a saved successful Places
// search: the selected place ID must come from that evidence, its coordinates
// must be real and finite, and the radius must fit the 1-25 km grid. When the
// optional website_scope is present it is canonicalized, checked against the
// parent origin, and stored as the first revision on the same transaction, so
// an invalid or failed scope leaves no partial location. It starts no paid
// scans, runs no visibility or crawl, and touches no Maps budgets. The
// migration 99 trigger owns the independent local profile copy on insert.
func (a *App) handleCreateBoundLocation(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	var body createBoundLocationRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	searchID, err := parseUUIDParam(body.SearchID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid search id")
		return
	}
	project, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: principal.User.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
		} else {
			serverError(w, r, err)
		}
		return
	}
	if err := requireOrganizationOwner(r.Context(), a.Queries, project.OrganizationID, principal.User.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}
	if r.Context().Err() != nil {
		writeJSONError(w, http.StatusBadRequest, "request cancelled")
		return
	}
	search, err := loadLocationListingSearch(r.Context(), a.DB, searchID, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "listing search not found")
		} else {
			serverError(w, r, err)
		}
		return
	}
	if search.Status != "completed" {
		writeJSONError(w, http.StatusConflict, "select a listing from a completed search")
		return
	}
	if search.ExpiresAt.Valid && locationListingSearchExpired(search.ExpiresAt.Time, time.Now().UTC()) {
		writeJSONError(w, http.StatusGone, "listing search expired")
		return
	}
	candidates, err := locationListingSearchCandidates(search.RawResponse)
	if err != nil {
		serverError(w, r, err)
		return
	}
	chosen, err := selectBoundLocationCandidate(candidates, body.PlaceID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	radiusM, err := normalizeLocalVisibilityRadiusM(body.RadiusM)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(chosen.Title)
	if name == "" {
		name = strings.TrimSpace(search.Query)
	}
	if _, _, err := validateLocationInput(name, chosen.PlaceID, chosen.Latitude, chosen.Longitude); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	scope, err := resolveBoundLocationWebsiteScope(body.WebsiteScope, project.BaseUrl)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(r.Context())) }()
	created, err := insertBoundLocationWithScope(r.Context(), tx, projectID, name,
		chosen.PlaceID, chosen.Latitude, chosen.Longitude, strings.TrimSpace(chosen.Address),
		radiusM, scope)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	response, err := a.assembleLocationResponse(r.Context(), created.ID, created.ProjectID, created.Name,
		created.PlaceID, created.Latitude, created.Longitude, created.Address, created.Locality, created.Localities, created.RadiusM, principal.User.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, locationResponseWithRadiusM(response, created.RadiusM))
}
