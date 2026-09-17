package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/serper"
)

// Client-facing rejections for review fetches, kept distinct from internal
// failures so a database error never reads as a bad request.
var (
	errNoMapsCheck = errors.New("run a maps visibility check before loading reviews")
	errNotInPack   = errors.New("this listing is not part of the latest maps check")
)

type mapsReviewsResponse struct {
	PlaceID string `json:"place_id"`
	CID     string `json:"cid"`
	// Cached is true when the response came from the database instead of a
	// fresh Serper call, so the UI can say "up to date" rather than pretend it
	// just refetched.
	Cached      bool   `json:"cached"`
	CreditsUsed *int   `json:"credits_used"`
	FetchedAt   string `json:"fetched_at"`
	// Null when a refetch is allowed now. Mirrors the map pack's
	// test_available_after so both use the same cooldown knob.
	RefetchAvailableAfter *string         `json:"refetch_available_after"`
	Reviews               json.RawMessage `json:"reviews"`
}

// handleGetMapsListingReviews serves the cached reviews only. It never spends
// credits, so the drawer can call it freely on open.
func (a *App) handleGetMapsListingReviews(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	placeID := r.URL.Query().Get("place_id")
	if placeID == "" {
		writeJSONError(w, http.StatusBadRequest, "place_id is required")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	if !a.projectBelongsToUser(w, r, queries, projectID, principal.User.ID) {
		return
	}

	if _, err := reviewablePlaceID(r, queries, projectID, placeID); err != nil {
		writeReviewGuardError(w, err)
		return
	}

	row, err := queries.GetMapsListingReviews(r.Context(), placeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "no reviews cached for this listing")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, a.newMapsReviewsResponse(row, true))
}

// handleFetchMapsListingReviews loads a listing's reviews from Serper and
// stores them. A second call before the cooldown expires returns the stored
// rows with cached=true instead of spending another credit.
func (a *App) handleFetchMapsListingReviews(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var body struct {
		PlaceID string `json:"place_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PlaceID == "" {
		writeJSONError(w, http.StatusBadRequest, "place_id is required")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	if !a.projectBelongsToUser(w, r, queries, projectID, principal.User.ID) {
		return
	}

	cid, err := reviewablePlaceID(r, queries, projectID, body.PlaceID)
	if err != nil {
		writeReviewGuardError(w, err)
		return
	}

	// Stored reviews are served until the cooldown passes. This is deliberately
	// forgiving rather than a 429: the UI hides the refresh control during the
	// window, and a stray click should not look like a failure.
	if existing, err := queries.GetMapsListingReviews(r.Context(), body.PlaceID); err == nil {
		if time.Since(existing.FetchedAt.Time) < a.Config.MapsVisibilityCooldown {
			if err := tx.Commit(r.Context()); err != nil {
				writeJSONError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			writeJSON(w, http.StatusOK, a.newMapsReviewsResponse(existing, true))
			return
		}
	}

	client := serper.NewClient(a.Config.SerperAPIKey, a.Config.SerperMapsEndpoint, a.Config.SerperPlacesEndpoint, a.Config.SerperReviewsEndpoint)
	response, err := client.Reviews(r.Context(), body.PlaceID)
	if err != nil {
		// Log the real cause: the client 502 is deliberately generic, and a
		// silent failure here is indistinguishable from a Serper outage.
		log.Printf("maps reviews: fetch failed for place %s: %v", body.PlaceID, err)
		writeJSONError(w, http.StatusBadGateway, "could not load reviews right now")
		return
	}

	reviewsJSON, err := json.Marshal(response.Reviews)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	row, err := queries.UpsertMapsListingReviews(r.Context(), sqlc.UpsertMapsListingReviewsParams{
		PlaceID:       body.PlaceID,
		Cid:           pgtype.Text{String: cid, Valid: cid != ""},
		Reviews:       reviewsJSON,
		NextPageToken: pgtype.Text{String: response.NextPageToken, Valid: response.NextPageToken != ""},
		CreditsUsed:   pgtype.Int4{Int32: int32(response.Credits), Valid: response.Credits > 0},
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, a.newMapsReviewsResponse(row, false))
}

func (a *App) newMapsReviewsResponse(row sqlc.MapsListingReview, cached bool) mapsReviewsResponse {
	response := mapsReviewsResponse{
		PlaceID:   row.PlaceID,
		CID:       textValueJSON(row.Cid),
		Cached:    cached,
		FetchedAt: row.FetchedAt.Time.UTC().Format(time.RFC3339),
		Reviews:   json.RawMessage(row.Reviews),
	}
	if row.CreditsUsed.Valid {
		credits := int(row.CreditsUsed.Int32)
		response.CreditsUsed = &credits
	}
	until := row.FetchedAt.Time.Add(a.Config.MapsVisibilityCooldown)
	if time.Now().Before(until) {
		formatted := until.UTC().Format(time.RFC3339)
		response.RefetchAvailableAfter = &formatted
	}
	return response
}

// projectBelongsToUser writes the error response itself and reports whether the
// caller may continue.
func (a *App) projectBelongsToUser(
	w http.ResponseWriter,
	r *http.Request,
	queries *sqlc.Queries,
	projectID, userID pgtype.UUID,
) bool {
	if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: userID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return false
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return false
	}
	return true
}

// reviewablePlaceID refuses place IDs that are not part of this project's latest
// map pack or its cached listing, so an endpoint that spends credits cannot be
// pointed at arbitrary listings.
func reviewablePlaceID(
	r *http.Request,
	queries *sqlc.Queries,
	projectID pgtype.UUID,
	placeID string,
) (string, error) {
	check, err := queries.GetLatestMapsVisibilityCheckByProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", errNoMapsCheck
		}
		return "", fmt.Errorf("load latest maps check: %w", err)
	}

	if len(check.Results) > 0 {
		var results struct {
			Places []serper.Place `json:"places"`
		}
		if err := json.Unmarshal(check.Results, &results); err == nil {
			for _, place := range results.Places {
				if place.PlaceID == placeID {
					return place.CID, nil
				}
			}
		}
	}

	if len(check.Listing) > 0 {
		var listing struct {
			PlaceID string `json:"place_id"`
			CID     string `json:"cid"`
		}
		if err := json.Unmarshal(check.Listing, &listing); err == nil && listing.PlaceID == placeID {
			return listing.CID, nil
		}
	}

	return "", errNotInPack
}

func writeReviewGuardError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNoMapsCheck) || errors.Is(err, errNotInPack) {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSONError(w, http.StatusInternalServerError, "internal server error")
}
