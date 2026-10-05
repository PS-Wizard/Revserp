package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
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
type locationListingLookupResponse struct {
	ID              string                     `json:"id"`
	Status          string                     `json:"status"`
	ExpectedCredits int32                      `json:"expected_credits"`
	CreditsUsed     int64                      `json:"credits_used"`
	ReservedCredits int64                      `json:"reserved_credits"`
	CreditKnown     bool                       `json:"credit_known"`
	Error           *string                    `json:"error"`
	Candidates      []locationListingCandidate `json:"candidates"`
}

func listingLookupResponse(lookup sqlc.LocalListingLookup) (locationListingLookupResponse, error) {
	response := locationListingLookupResponse{ID: lookup.ID.String(), Status: lookup.Status, ExpectedCredits: lookup.ExpectedCredits, CreditsUsed: lookup.CreditsUsed, ReservedCredits: lookup.ReservedCredits, CreditKnown: lookup.CreditKnown, Error: localVisibilityNullableText(lookup.Error), Candidates: []locationListingCandidate{}}
	if lookup.Status != "completed" {
		return response, nil
	}
	var provider serper.PlacesResponse
	if err := json.Unmarshal(lookup.RawResponse, &provider); err != nil {
		return response, err
	}
	seen := map[string]bool{}
	for _, place := range provider.Places {
		if strings.TrimSpace(place.PlaceID) == "" || seen[place.PlaceID] {
			continue
		}
		seen[place.PlaceID] = true
		response.Candidates = append(response.Candidates, locationListingCandidate{PlaceID: place.PlaceID, Title: place.Title, Address: place.Address, Latitude: place.Latitude, Longitude: place.Longitude})
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
	var body struct{}
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	if location.PlaceID.Valid {
		writeJSONError(w, 409, "location already has a bound listing")
		return
	}
	if strings.TrimSpace(location.Address) == "" {
		writeJSONError(w, 400, "save the physical address before searching for a listing")
		return
	}
	if strings.TrimSpace(a.Config.SerperAPIKey) == "" || strings.TrimSpace(a.Config.SerperPlacesEndpoint) == "" {
		writeJSONError(w, 503, "Google listing lookup provider is not configured")
		return
	}
	store := localvisibility.ListingLookupStore{Pool: a.DB}
	lookup, err := store.ReserveListingLookup(r.Context(), userID, location.ProjectID, location.ID)
	if err != nil {
		switch {
		case errors.Is(err, localvisibility.ErrMapsBudgetUnavailable):
			writeJSONError(w, 409, err.Error())
		case errors.Is(err, localvisibility.ErrListingAlreadyBound):
			writeJSONError(w, http.StatusConflict, err.Error())
		case isLocalVisibilityActiveConflictError(err):
			writeJSONError(w, 409, "a listing lookup is active or has an unconfirmed charge")
		case errors.Is(err, pgx.ErrNoRows):
			writeJSONError(w, 404, "location not found")
		default:
			serverError(w, r, err)
		}
		return
	}
	provider := serper.NewClient(a.Config.SerperAPIKey, a.Config.SerperMapsEndpoint, a.Config.SerperPlacesEndpoint, a.Config.SerperReviewsEndpoint)
	result, providerErr := provider.Places(r.Context(), lookup.Query)
	// Cancellation must not discard a paid response or release an unknown charge.
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	recorded, err := store.RecordListingLookup(settleCtx, lookup.ID, result, providerErr)
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
	found := false
	for _, candidate := range candidates.Candidates {
		if candidate.PlaceID == selected {
			found = true
			break
		}
	}
	if !found {
		writeJSONError(w, 400, "place_id must be a candidate from this location's lookup")
		return
	}
	bound, err := a.Queries.BindLocationListingForUser(r.Context(), sqlc.BindLocationListingForUserParams{ID: location.ID, ID_2: location.ProjectID, UserID: userID, PlaceID: selected})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, 404, "location not found")
		} else {
			serverError(w, r, err)
		}
		return
	}
	response, err := newLocalVisibilityLocationResponse(bound.ID, bound.ProjectID, bound.Name, bound.PlaceID, bound.Latitude, bound.Longitude, bound.Queries, bound.Address, bound.Locality, bound.QueryService)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, 200, response)
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
		response, err := newLocalVisibilityLocationResponse(location.ID, location.ProjectID, location.Name, location.PlaceID, location.Latitude, location.Longitude, location.Queries, location.Address, location.Locality, location.QueryService)
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
		if errors.As(err, &pgErr) && pgErr.Code == "23503" && strings.Contains(pgErr.Message, "active or unconfirmed Maps spend") {
			writeJSONError(w, http.StatusConflict, "settle active or unconfirmed spending before deleting this location")
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
