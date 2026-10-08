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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/googleplaces"
	"github.com/ps-wizard/revserp/internal/serper"
)

// locationListingSearchTTL bounds how long a saved Places search stays
// bindable. Listings change, so a bound location must come from fresh server
// evidence, never from a stale client copy.
const locationListingSearchTTL = 24 * time.Hour

// locationListingSearchRequest is the POST location-listing-search body: a
// typed business name only, never coordinates or a place ID.
type locationListingSearchRequest struct {
	Query string `json:"query"`
}

// locationListingSearchResponse is the POST location-listing-search answer.
// Candidates reuse the existing listing candidate shape. ExpectedCredits is
// always 0: Google bills the Text Search itself and no app credits move.
type locationListingSearchResponse struct {
	ID              string                     `json:"id"`
	Candidates      []locationListingCandidate `json:"candidates"`
	ExpectedCredits int                        `json:"expected_credits"`
}

const insertLocationListingSearch = `INSERT INTO location_listing_searches(project_id, user_id, query, status, raw_response, error, expires_at)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id, created_at`

const selectLocationListingSearchForProject = `SELECT id, user_id, query, status, raw_response, error, created_at, expires_at
FROM location_listing_searches WHERE id = $1 AND project_id = $2`

// locationListingSearchRow is the typed evidence row shared by the search and
// bound handlers, so both read the same columns with one query each.
type locationListingSearchRow struct {
	ID          pgtype.UUID
	UserID      pgtype.UUID
	Query       string
	Status      string
	RawResponse []byte
	Error       pgtype.Text
	CreatedAt   pgtype.Timestamptz
	ExpiresAt   pgtype.Timestamptz
}

// validateLocationListingSearchQuery normalizes one typed business name. Free
// text never becomes coordinates here; the provider does the matching.
func validateLocationListingSearchQuery(query string) (string, error) {
	query = strings.Join(strings.Fields(query), " ")
	if query == "" {
		return "", errors.New("query is required")
	}
	if len(query) > 500 {
		return "", errors.New("query must fit within 500 bytes")
	}
	return query, nil
}

// locationListingSearchCandidates rebuilds the bindable candidates from saved
// provider evidence. Only a stored place ID with real finite coordinates is
// selectable; blanks, repeats and coordinate-less rows never bind.
func locationListingSearchCandidates(rawResponse []byte) ([]locationListingCandidate, error) {
	var provider serper.MapsListingResponse
	if err := json.Unmarshal(rawResponse, &provider); err != nil {
		return nil, err
	}
	candidates := []locationListingCandidate{}
	seen := map[string]bool{}
	for _, place := range provider.Places {
		if strings.TrimSpace(place.PlaceID) == "" || seen[place.PlaceID] {
			continue
		}
		if !place.HasMapCoordinates() {
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
		candidates = append(candidates, candidate)
	}
	return candidates, nil
}

// locationListingSearchExpired reports whether saved search evidence is too
// old to bind. Bound creation compares against the stored timestamp.
func locationListingSearchExpired(expiresAt, now time.Time) bool {
	return !now.Before(expiresAt)
}

func (a *App) locationListingSearchClient() *googleplaces.PlacesListingClient {
	if a.PlacesListings != nil {
		return a.PlacesListings
	}
	return googleplaces.NewPlacesListingClient(a.Config.GoogleMapsAPIKey, "")
}

// saveLocationListingSearch appends one immutable evidence row. The provider
// response is stored verbatim so bound creation can verify the selected place
// against server evidence instead of trusting client fields.
func saveLocationListingSearch(ctx context.Context, pool *pgxpool.Pool, projectID, userID pgtype.UUID, query, status string, providerResponse serper.MapsListingResponse, errorMessage string) (locationListingSearchRow, error) {
	raw, err := json.Marshal(providerResponse)
	if err != nil {
		return locationListingSearchRow{}, err
	}
	var errorValue *string
	if errorMessage != "" {
		errorValue = &errorMessage
	}
	var saved locationListingSearchRow
	saved.UserID = userID
	saved.Query = query
	saved.Status = status
	saved.RawResponse = raw
	if errorValue != nil {
		saved.Error = pgtype.Text{String: *errorValue, Valid: true}
	}
	err = pool.QueryRow(ctx, insertLocationListingSearch,
		projectID, userID, query, status, raw, errorValue, time.Now().UTC().Add(locationListingSearchTTL),
	).Scan(&saved.ID, &saved.CreatedAt)
	if err != nil {
		return locationListingSearchRow{}, err
	}
	return saved, nil
}

// loadLocationListingSearch reads one evidence row scoped to its project. A
// search from another project reads as not found, never as forbidden detail.
func loadLocationListingSearch(ctx context.Context, pool *pgxpool.Pool, searchID, projectID pgtype.UUID) (locationListingSearchRow, error) {
	var row locationListingSearchRow
	err := pool.QueryRow(ctx, selectLocationListingSearchForProject, searchID, projectID).Scan(
		&row.ID, &row.UserID, &row.Query, &row.Status, &row.RawResponse, &row.Error, &row.CreatedAt, &row.ExpiresAt,
	)
	if err != nil {
		return locationListingSearchRow{}, err
	}
	return row, nil
}

// handleSearchLocationListing answers POST /projects/{projectID}/location-listing-search.
// It searches Google Places listings directly by name without requiring or
// creating a permanent draft location, saves the immutable project/user-owned
// evidence, and returns the bindable candidates with zero app credits.
func (a *App) handleSearchLocationListing(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	var body locationListingSearchRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	query, err := validateLocationListingSearchQuery(body.Query)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
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
	if strings.TrimSpace(a.Config.GoogleMapsAPIKey) == "" {
		writeJSONError(w, http.StatusServiceUnavailable, "Google Maps listing search provider is not configured")
		return
	}

	providerResponse, providerErr := a.locationListingSearchClient().SearchListingsByName(r.Context(), query)
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	if providerErr != nil {
		if _, err := saveLocationListingSearch(recordCtx, a.DB, projectID, principal.User.ID, query, "failed", providerResponse, providerErr.Error()); err != nil {
			serverError(w, r, err)
			return
		}
		writeJSONError(w, http.StatusBadGateway, "location listing search failed")
		return
	}
	raw, err := json.Marshal(providerResponse)
	if err != nil {
		serverError(w, r, err)
		return
	}
	candidates, err := locationListingSearchCandidates(raw)
	if err != nil {
		serverError(w, r, err)
		return
	}
	saved, err := saveLocationListingSearch(recordCtx, a.DB, projectID, principal.User.ID, query, "completed", providerResponse, "")
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, locationListingSearchResponse{ID: saved.ID.String(), Candidates: candidates, ExpectedCredits: 0})
}
