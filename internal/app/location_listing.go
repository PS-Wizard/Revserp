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
	response, err := a.assembleLocationResponse(r.Context(), bound.ID, bound.ProjectID, bound.Name, bound.PlaceID, bound.Latitude, bound.Longitude, bound.Address, bound.Locality, bound.Localities, userID)
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
		response, err := a.assembleLocationResponse(r.Context(), location.ID, location.ProjectID, location.Name, location.PlaceID, location.Latitude, location.Longitude, location.Address, location.Locality, location.Localities, userID)
		if err != nil {
			serverError(w, r, err)
			return
		}
		responses = append(responses, response)
	}
	writeJSON(w, 200, responses)
}

func (a *App) handleDeleteLocationSetup(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	count, err := a.Queries.DeleteLocationSetupForUser(r.Context(), sqlc.DeleteLocationSetupForUserParams{ID: location.ID, ID_2: location.ProjectID, UserID: userID})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			switch {
			case pgErr.ConstraintName == "ai_audits_location_id_project_id_fkey":
				writeJSONError(w, http.StatusConflict, "this location has AI audit history and cannot be deleted")
			case strings.Contains(pgErr.Message, "active or unconfirmed Maps spend"):
				writeJSONError(w, http.StatusConflict, "settle active or unconfirmed spending before deleting this location")
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
	w.WriteHeader(http.StatusNoContent)
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
	response, err := a.assembleLocationResponse(r.Context(), unbound.ID, unbound.ProjectID, unbound.Name, unbound.PlaceID, unbound.Latitude, unbound.Longitude, unbound.Address, unbound.Locality, unbound.Localities, userID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, 200, response)
}
