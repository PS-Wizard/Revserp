package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/geography"
	"github.com/ps-wizard/revserp/internal/googleplaces"
	"github.com/ps-wizard/revserp/internal/localvisibility"
	"github.com/ps-wizard/revserp/internal/serper"
)

type locationListingCandidate struct {
	PlaceID   string  `json:"place_id"`
	Title     string  `json:"title"`
	Address   string  `json:"address"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}
type locationListingSourceCandidate struct {
	DisplayName string  `json:"display_name"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
}

type locationListingLookupResponse struct {
	ID              string                          `json:"id"`
	Status          string                          `json:"status"`
	ExpectedCredits int32                           `json:"expected_credits"`
	CreditsUsed     int64                           `json:"credits_used"`
	ReservedCredits int64                           `json:"reserved_credits"`
	CreditKnown     bool                            `json:"credit_known"`
	Error           *string                         `json:"error"`
	Candidates      []locationListingCandidate      `json:"candidates"`
	Deduplicated    bool                            `json:"deduplicated,omitempty"`
	SourceCandidate *locationListingSourceCandidate `json:"source_candidate,omitempty"`
}

func listingLookupResponse(lookup sqlc.LocalListingLookup) (locationListingLookupResponse, error) {
	response := locationListingLookupResponse{ID: lookup.ID.String(), Status: lookup.Status, ExpectedCredits: lookup.ExpectedCredits, CreditsUsed: lookup.CreditsUsed, ReservedCredits: lookup.ReservedCredits, CreditKnown: lookup.CreditKnown, Error: localVisibilityNullableText(lookup.Error), Candidates: []locationListingCandidate{}}
	if lookup.SourceLatitude.Valid && lookup.SourceLongitude.Valid {
		response.SourceCandidate = &locationListingSourceCandidate{DisplayName: lookup.Query, Latitude: lookup.SourceLatitude.Float64, Longitude: lookup.SourceLongitude.Float64}
	}
	if lookup.Status != "completed" {
		return response, nil
	}
	var provider serper.MapsListingResponse
	if err := json.Unmarshal(lookup.RawResponse, &provider); err != nil {
		return response, err
	}
	seen := map[string]bool{}
	for _, place := range provider.Places {
		if strings.TrimSpace(place.PlaceID) == "" || seen[place.PlaceID] {
			continue
		}
		if (lookup.ExpectedCredits == 0 || lookup.ExpectedCredits == 3) && !place.HasMapCoordinates() {
			continue
		}
		seen[place.PlaceID] = true
		candidate := locationListingCandidate{PlaceID: place.PlaceID, Title: place.Title, Address: place.Address}
		if place.Latitude != nil {
			candidate.Latitude = *place.Latitude
		}
		if place.Longitude != nil {
			candidate.Longitude = *place.Longitude
		}
		response.Candidates = append(response.Candidates, candidate)
	}
	return response, nil
}

func (a *App) locationSetupLocation(w http.ResponseWriter, r *http.Request) (sqlc.GetProjectLocationForUserRow, pgtype.UUID, bool) {
	projectID, userID, ok := a.locationSetupProjectUser(w, r)
	if !ok {
		return sqlc.GetProjectLocationForUserRow{}, pgtype.UUID{}, false
	}
	locationID, err := parseUUIDParam(chi.URLParam(r, "locationID"))
	if err != nil {
		writeJSONError(w, 400, "invalid location id")
		return sqlc.GetProjectLocationForUserRow{}, pgtype.UUID{}, false
	}
	location, err := a.Queries.GetProjectLocationForUser(r.Context(), sqlc.GetProjectLocationForUserParams{ID: locationID, ID_2: projectID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, 404, "location not found")
		} else {
			serverError(w, r, err)
		}
		return location, userID, false
	}
	return location, userID, true
}

func (a *App) handleCreateListingLookup(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	var body createListingLookupRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	if body.ExpectedCredits != nil && *body.ExpectedCredits != 0 {
		writeJSONError(w, http.StatusBadRequest, "Places listing lookup requires zero expected credits")
		return
	}
	if location.PlaceID.Valid {
		writeJSONError(w, 409, "location already has a bound listing")
		return
	}
	selection, err := listingResolutionSelection(location, body)
	if err != nil {
		if errors.Is(err, errInvalidListingSelection) {
			writeJSONError(w, 400, err.Error())
		} else {
			serverError(w, r, err)
		}
		return
	}
	if strings.TrimSpace(a.Config.GoogleMapsAPIKey) == "" {
		writeJSONError(w, 503, "Google Maps listing resolution provider is not configured")
		return
	}
	store := localvisibility.ListingLookupStore{Pool: a.DB}
	lookup, created, err := store.StartPlacesListingLookup(r.Context(), userID, location.ProjectID, location.ID, selection)
	if err != nil {
		switch {
		case errors.Is(err, localvisibility.ErrListingAlreadyBound):
			writeJSONError(w, http.StatusConflict, err.Error())
		case isPostgresUniqueConstraintConflict(err, "local_listing_lookups_unsettled_idx"):
			writeJSONError(w, 409, "a listing lookup is active or has an unconfirmed charge")
		case errors.Is(err, pgx.ErrNoRows):
			writeJSONError(w, 404, "location not found")
		default:
			serverError(w, r, err)
		}
		return
	}
	if !created {
		response, err := listingLookupResponse(lookup)
		if err != nil {
			serverError(w, r, err)
			return
		}
		response.Deduplicated = true
		writeJSON(w, 200, response)
		return
	}
	provider := googleplaces.NewPlacesListingClient(a.Config.GoogleMapsAPIKey, "")
	result, providerErr := provider.LookupMapsListing(r.Context(), lookup.Query, lookup.SourceLatitude.Float64, lookup.SourceLongitude.Float64)
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	recorded, err := store.RecordPlacesListingLookup(recordCtx, lookup.ID, result, providerErr)
	if err != nil {
		serverError(w, r, err)
		return
	}
	response, err := listingLookupResponse(recorded)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, 200, response)
}

func (a *App) handleGetLatestListingLookup(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	lookup, err := a.Queries.GetLatestListingLookupForUser(r.Context(), sqlc.GetLatestListingLookupForUserParams{ID: location.ID, ID_2: location.ProjectID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, 404, "no listing lookup yet")
		} else {
			serverError(w, r, err)
		}
		return
	}
	response, err := listingLookupResponse(lookup)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, 200, response)
}

func (a *App) handleBindLocationListing(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	if location.PlaceID.Valid {
		writeJSONError(w, 409, "unbind the current listing before selecting another")
		return
	}
	var body struct {
		LookupID string `json:"lookup_id"`
		PlaceID  string `json:"place_id"`
	}
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	lookupID, err := parseUUIDParam(body.LookupID)
	if err != nil {
		writeJSONError(w, 400, "invalid listing lookup id")
		return
	}
	lookup, err := a.Queries.GetListingLookupForUser(r.Context(), sqlc.GetListingLookupForUserParams{ID: lookupID, ID_2: location.ID, ID_3: location.ProjectID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, 404, "listing lookup not found")
		} else {
			serverError(w, r, err)
		}
		return
	}
	if lookup.Status != "completed" || !lookup.CreditKnown {
		writeJSONError(w, 409, "select a listing from a completed lookup")
		return
	}
	candidates, err := listingLookupResponse(lookup)
	if err != nil {
		serverError(w, r, err)
		return
	}
	selected := strings.TrimSpace(body.PlaceID)
	var chosen *locationListingCandidate
	for _, candidate := range candidates.Candidates {
		if candidate.PlaceID == selected {
			chosen = &candidate
			break
		}
	}
	if chosen == nil {
		writeJSONError(w, 400, "place_id must be a candidate from this location's lookup")
		return
	}
	latitude, longitude := location.Latitude, location.Longitude
	if lookup.ExpectedCredits == 0 || lookup.ExpectedCredits == 3 {
		latitude, longitude = chosen.Latitude, chosen.Longitude
	}
	locality, localities := a.bindListingLocality(r.Context(), latitude, longitude)
	encodedLocalities, err := encodeBindListingLocalities(localities)
	if err != nil {
		serverError(w, r, err)
		return
	}
	bound, err := a.Queries.BindLocationListingForUser(r.Context(), sqlc.BindLocationListingForUserParams{ID: location.ID, ID_2: location.ProjectID, UserID: userID, PlaceID: selected, Latitude: latitude, Longitude: longitude, Locality: locality, Localities: encodedLocalities})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, 409, "listing binding changed; reload the location")
		} else {
			serverError(w, r, err)
		}
		return
	}
	response, err := a.assembleLocationResponse(r.Context(), bound.ID, bound.ProjectID, bound.Name, bound.PlaceID, bound.Latitude, bound.Longitude, bound.Address, bound.Locality, bound.Localities, bound.RadiusM, userID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, 200, response)
}

func (a *App) bindListingLocality(ctx context.Context, latitude, longitude float64) (string, []string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := a.locationGeographyResults(ctx, fmt.Sprintf("reverse:%.6f,%.6f", latitude, longitude), false, func() ([]geography.GeocodedAddress, error) {
		if a.Nominatim == nil {
			return nil, errors.New("location geography provider is unavailable")
		}
		address, err := a.Nominatim.ReverseAddress(ctx, latitude, longitude)
		if err != nil {
			return nil, err
		}
		return []geography.GeocodedAddress{address}, nil
	})
	if err != nil || len(response.Results) == 0 {
		return "", []string{}
	}
	return response.Results[0].Locality, normalizeLocalities(response.Results[0].Localities)
}

// encodeBindListingLocalities serializes a locality ladder, forcing JSON [] for a
// nil or empty ladder so a missing reverse result never stores JSON null where
// the project_locations.localities column expects an array.
func encodeBindListingLocalities(localities []string) ([]byte, error) {
	if localities == nil {
		localities = []string{}
	}
	return json.Marshal(localities)
}

func (a *App) handleListProjectLocations(w http.ResponseWriter, r *http.Request) {
	projectID, userID, ok := a.locationSetupProjectUser(w, r)
	if !ok {
		return
	}
	locations, err := a.Queries.ListProjectLocationsForUser(r.Context(), sqlc.ListProjectLocationsForUserParams{ID: projectID, UserID: userID})
	if err != nil {
		serverError(w, r, err)
		return
	}
	responses := make([]localVisibilityLocationResponse, 0, len(locations))
	for _, location := range locations {
		response, err := a.assembleLocationResponse(r.Context(), location.ID, location.ProjectID, location.Name, location.PlaceID, location.Latitude, location.Longitude, location.Address, location.Locality, location.Localities, location.RadiusM, userID)
		if err != nil {
			serverError(w, r, err)
			return
		}
		responses = append(responses, response)
	}
	writeJSON(w, 200, responses)
}

// handleDeleteLocationSetup permanently deletes one location and its
// location-only terminal Maps, AI audit, Revbot chat and settings history.
// The endpoint keeps its contract: success is 204, deletion is owner-only,
// the parent project, sibling locations and shared Google auth are untouched,
// and active Maps runs, unconfirmed Maps spend and live AI/chat worker state
// block with 409. Settled spend stays settled: budget ledgers are never
// refunded or deleted, only the location-scoped evidence rows go away.
func (a *App) handleDeleteLocationSetup(w http.ResponseWriter, r *http.Request) {
	projectID, userID, ok := a.locationSetupProjectUser(w, r)
	if !ok {
		return
	}
	locationID, err := parseUUIDParam(chi.URLParam(r, "locationID"))
	if err != nil {
		writeJSONError(w, 400, "invalid location id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := a.Queries.WithTx(tx)

	// Authorized scope transaction: lock the location row through project
	// membership so concurrent deletes serialize instead of racing.
	var organizationID pgtype.UUID
	err = tx.QueryRow(r.Context(), `SELECT p.organization_id FROM project_locations l
		JOIN projects p ON p.id = l.project_id
		JOIN organization_members m ON m.org_id = p.organization_id
		WHERE l.id = $1 AND p.id = $2 AND m.user_id = $3
		FOR UPDATE OF l`, locationID, projectID, userID).Scan(&organizationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "location not found")
		} else {
			serverError(w, r, err)
		}
		return
	}

	// Owner-only permanent deletion: members may read and edit, but only an
	// owner may destroy location history.
	if err := requireOrganizationOwner(r.Context(), queries, organizationID, userID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}

	blockMessage, blocked, err := locationDeleteBlocker(r.Context(), tx, projectID, locationID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if blocked {
		writeJSONError(w, http.StatusConflict, blockMessage)
		return
	}

	// Location-only terminal Revbot chat history. The composite FK has no
	// cascade, so terminal conversations are removed explicitly; their turns,
	// messages and tool state cascade. Live turns were blocked above, and the
	// locked conversation rows keep a worker from starting one mid-delete.
	lockedConversations, err := tx.Query(r.Context(), `SELECT id FROM ai_conversations WHERE project_id = $1 AND location_id = $2 FOR UPDATE`, projectID, locationID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	lockedConversations.Close()
	if _, err := tx.Exec(r.Context(), `DELETE FROM ai_conversations WHERE project_id = $1 AND location_id = $2`, projectID, locationID); err != nil {
		serverError(w, r, err)
		return
	}

	// Location-only terminal AI audit history. Active audits were blocked
	// above; only terminal rows go, and their runs cascade. The recount
	// catches an audit queued by a concurrent worker after the guard.
	if _, err := tx.Exec(r.Context(), `DELETE FROM ai_audits WHERE project_id = $1 AND location_id = $2 AND status NOT IN ('queued','running')`, projectID, locationID); err != nil {
		serverError(w, r, err)
		return
	}
	var remainingAudits int
	if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM ai_audits WHERE project_id = $1 AND location_id = $2`, projectID, locationID).Scan(&remainingAudits); err != nil {
		serverError(w, r, err)
		return
	}
	if remainingAudits > 0 {
		writeJSONError(w, http.StatusConflict, "this location has an active AI audit and cannot be deleted")
		return
	}

	// The existing location delete cascades every location-scoped child with
	// ON DELETE CASCADE (terminal Maps runs and lookups, services, queries,
	// landmarks, business profile, website scopes, keywords, Google bindings,
	// AI questions) while the unsettled-spend trigger still guards unconfirmed
	// Maps charges. No budget ledger row is touched, so settled spend is
	// preserved and nothing is refunded.
	count, err := queries.DeleteLocationSetupForUser(r.Context(), sqlc.DeleteLocationSetupForUserParams{ID: locationID, ID_2: projectID, UserID: userID})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			switch {
			case pgErr.ConstraintName == "ai_audits_location_id_project_id_fkey":
				writeJSONError(w, http.StatusConflict, "this location has AI audit history and cannot be deleted")
			case pgErr.ConstraintName == "ai_conversations_location_project_fkey":
				writeJSONError(w, http.StatusConflict, "this location has Revbot conversations and cannot be deleted")
			case strings.Contains(pgErr.Message, "active or unconfirmed Maps spend"):
				writeJSONError(w, http.StatusConflict, "location has active or unconfirmed Maps spend; settle active or unconfirmed spending before deleting this location")
			default:
				serverError(w, r, err)
			}
		} else {
			serverError(w, r, err)
		}
		return
	}
	if count == 0 {
		writeJSONError(w, http.StatusNotFound, "location not found")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// locationDeleteBlocker reports the 409 that must stop a permanent location
// deletion: active Maps runs, unconfirmed Maps spend, live AI audits, queued
// or running AI worker jobs, and live Revbot chat turns. It returns
// blocked=false when only terminal location history remains.
func locationDeleteBlocker(ctx context.Context, tx pgx.Tx, projectID, locationID pgtype.UUID) (string, bool, error) {
	var blocked bool
	// Existing Maps spend guards, mirrored without relaxing: a queued or
	// running run, or any reserved (unconfirmed) credit, blocks deletion.
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM local_visibility_runs WHERE location_id = $1 AND (status IN ('queued','running') OR reserved_credits > 0)
			UNION ALL SELECT 1 FROM local_listing_lookups WHERE location_id = $1 AND (status = 'running' OR reserved_credits > 0)
		)`, locationID).Scan(&blocked); err != nil {
		return "", false, err
	}
	if blocked {
		return "location has active or unconfirmed Maps spend; settle active or unconfirmed spending before deleting this location", true, nil
	}
	// Live AI audit or location-scoped worker state blocks deletion so a
	// worker never loses the row it is paid to produce.
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_audits WHERE project_id = $1 AND location_id = $2 AND status IN ('queued','running'))`, projectID, locationID).Scan(&blocked); err != nil {
		return "", false, err
	}
	if blocked {
		return "this location has an active AI audit and cannot be deleted", true, nil
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_worker_jobs j WHERE j.status IN ('pending','running')
			AND (j.location_id = $2
				OR EXISTS (SELECT 1 FROM ai_audits a WHERE a.id = j.audit_id AND a.project_id = $1 AND a.location_id = $2)
				OR EXISTS (SELECT 1 FROM local_visibility_runs r WHERE r.id = j.local_run_id AND r.location_id = $2)))`, projectID, locationID).Scan(&blocked); err != nil {
		return "", false, err
	}
	if blocked {
		return "this location has a queued or running AI job and cannot be deleted", true, nil
	}
	// Live Revbot chat turns block deletion so worker message state survives.
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_turns t JOIN ai_conversations c ON c.id = t.conversation_id
			WHERE c.project_id = $1 AND c.location_id = $2 AND t.status IN ('queued','running','waiting','waiting_for_user'))`, projectID, locationID).Scan(&blocked); err != nil {
		return "", false, err
	}
	if blocked {
		return "this location has an active Revbot conversation turn and cannot be deleted", true, nil
	}
	return "", false, nil
}

func (a *App) handleUnbindLocationListing(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	unbound, err := a.Queries.UnbindLocationListingForUser(r.Context(), sqlc.UnbindLocationListingForUserParams{ID: location.ID, ID_2: location.ProjectID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, 404, "location not found")
		} else {
			serverError(w, r, err)
		}
		return
	}
	response, err := a.assembleLocationResponse(r.Context(), unbound.ID, unbound.ProjectID, unbound.Name, unbound.PlaceID, unbound.Latitude, unbound.Longitude, unbound.Address, unbound.Locality, unbound.Localities, unbound.RadiusM, userID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, 200, response)
}
