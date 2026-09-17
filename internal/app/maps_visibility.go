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
)

type mapsVisibilityItemResponse struct {
	Position     int               `json:"position"`
	Title        string            `json:"title"`
	Address      string            `json:"address"`
	Category     string            `json:"category"`
	Types        []string          `json:"types"`
	Rating       float64           `json:"rating"`
	RatingCount  int               `json:"ratingCount"`
	Website      string            `json:"website"`
	Phone        string            `json:"phone_number"`
	Latitude     float64           `json:"latitude"`
	Longitude    float64           `json:"longitude"`
	CID          string            `json:"cid"`
	PlaceID      string            `json:"placeId"`
	FID          string            `json:"fid"`
	OpeningHours map[string]string `json:"opening_hours"`
	ThumbnailURL string            `json:"thumbnail_url"`
	Matched      bool              `json:"matched"`
}

// A completed test pins the pack for `MapsVisibilityCooldown` (24h by default):
// the local pack barely moves in a day, and each test costs Serper credits.
// Failed runs never start the cooldown, so a broken test is retryable
// immediately. Set MAPS_VISIBILITY_COOLDOWN=0s in dev to re-test freely.

type mapsVisibilityResponse struct {
	ID            string                       `json:"id"`
	Status        string                       `json:"status"`
	Question      string                       `json:"question"`
	LL            string                       `json:"ll"`
	Zoom          *int                         `json:"zoom"`
	OurRank       *int                         `json:"our_rank"`
	OurMatchBasis string                       `json:"our_match_basis"`
	CreditsUsed   *int                         `json:"credits_used"`
	Error         string                       `json:"error"`
	Listing       map[string]any               `json:"listing"`
	Items         []mapsVisibilityItemResponse `json:"items"`
	CreatedAt     string                       `json:"created_at"`
	CompletedAt   *string                      `json:"completed_at"`
	// Null when a new test is allowed. Otherwise the ISO time the cooldown
	// ends — the frontend hides the test button until then.
	TestAvailableAfter *string `json:"test_available_after"`
}

// handleGetMapsVisibility returns the latest maps visibility check for a
// project. 404 when no check has run yet — the frontend shows the
// "Test your rankings" button for that case.
func (a *App) handleGetMapsVisibility(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
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
	user := principal.User
	if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	row, err := queries.GetLatestMapsVisibilityCheckByProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "no maps visibility check yet")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, newMapsVisibilityResponseFromRow(row, a.Config.MapsVisibilityCooldown))
}

// handleCreateMapsVisibilityCheck starts a queued run. The question comes
// from the latest generation; the worker performs the Serper call.
func (a *App) handleCreateMapsVisibilityCheck(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
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
	user := principal.User
	if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// The location question must exist; it is generated with the discovery
	// questions on profile save and skipped when the profile has no location.
	questions, err := queries.GetProjectAIQuestions(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusBadRequest, "questions must be generated before a maps check")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	var locationQuestions []string
	if err := json.Unmarshal(questions.LocationQuestions, &locationQuestions); err != nil && len(questions.LocationQuestions) > 0 {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	// No location question → an explicit client error, not silent magic: the
	// backfill migration covers every profile with a location, so reaching
	// this means the profile genuinely has no location (or generation failed
	// and the user should regenerate from the business profile).
	if len(locationQuestions) == 0 || locationQuestions[0] == "" {
		writeJSONError(w, http.StatusBadRequest, "failed to test: no maps question has been generated — add a location to your business profile and regenerate")
		return
	}
	question := locationQuestions[0]

	// The partial unique index enforces one in-flight run per project, so the
	// button cannot double-charge credits. A finished run is never blocking:
	// re-testing creates a fresh row.
	existing, err := queries.GetLatestMapsVisibilityCheckByProject(r.Context(), projectID)
	if err == nil && (existing.Status == "queued" || existing.Status == "running") {
		writeJSON(w, http.StatusOK, newMapsVisibilityResponseFromRow(existing, a.Config.MapsVisibilityCooldown))
		return
	}
	// Cooldown: the latest COMPLETED run blocks new spends for 24h. A failed
	// latest run never triggers it — retry a broken test right away.
	if err == nil && existing.Status == "completed" && existing.CompletedAt.Valid {
		until := existing.CompletedAt.Time.Add(a.Config.MapsVisibilityCooldown)
		if time.Now().Before(until) {
			writeJSONError(w, http.StatusTooManyRequests, fmt.Sprintf(
				"a test already ran recently — next test available after %s",
				until.UTC().Format(time.RFC3339)))
			return
		}
	}

	check, err := queries.CreateMapsVisibilityCheck(r.Context(), sqlc.CreateMapsVisibilityCheckParams{
		ProjectID: projectID,
		Question:  question,
	})
	if err != nil {
		writeJSONError(w, http.StatusConflict, "a maps visibility check is already in progress")
		return
	}

	if _, err := queries.EnqueueAIWorkerJob(r.Context(), sqlc.EnqueueAIWorkerJobParams{
		JobType:   "maps_visibility",
		ProjectID: projectID,
	}); err != nil {
		log.Printf("enqueue maps_visibility job for project %s: %v", projectID.String(), err)
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusAccepted, newMapsVisibilityResponseFromRow(check, a.Config.MapsVisibilityCooldown))
}

func newMapsVisibilityResponseFromRow(row sqlc.MapsVisibilityCheck, cooldown time.Duration) mapsVisibilityResponse {
	response := mapsVisibilityResponse{
		ID:        row.ID.String(),
		Status:    row.Status,
		Question:  row.Question,
		LL:        textValueJSON(row.Ll),
		Listing:   map[string]any{"resolved": false},
		Items:     []mapsVisibilityItemResponse{},
		CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339),
	}
	if row.Zoom.Valid {
		zoom := int(row.Zoom.Int32)
		response.Zoom = &zoom
	}
	if row.OurRank.Valid {
		rank := int(row.OurRank.Int32)
		response.OurRank = &rank
	}
	if row.OurMatchBasis.Valid {
		response.OurMatchBasis = row.OurMatchBasis.String
	}
	if row.CreditsUsed.Valid {
		credits := int(row.CreditsUsed.Int32)
		response.CreditsUsed = &credits
	}
	if row.Error.Valid {
		response.Error = row.Error.String
	}
	if row.CompletedAt.Valid {
		completed := row.CompletedAt.Time.UTC().Format(time.RFC3339)
		response.CompletedAt = &completed
	}
	if row.Status == "completed" && row.CompletedAt.Valid {
		until := row.CompletedAt.Time.Add(cooldown)
		if time.Now().Before(until) {
			formatted := until.UTC().Format(time.RFC3339)
			response.TestAvailableAfter = &formatted
		}
	}
	if len(row.Results) > 0 {
		var results struct {
			Places []serperPlaceDTO `json:"places"`
		}
		if err := json.Unmarshal(row.Results, &results); err == nil {
			response.Items = make([]mapsVisibilityItemResponse, 0, len(results.Places))
			for _, place := range results.Places {
				response.Items = append(response.Items, mapsVisibilityItemFromSerper(place))
			}
		}
	}
	if len(row.Listing) > 0 {
		var listing map[string]any
		if err := json.Unmarshal(row.Listing, &listing); err == nil && listing != nil {
			response.Listing = listing
		}
	}
	return response
}

// serperPlaceDTO mirrors serper's response item field names for decoding the
type serperPlaceDTO struct {
	Position     int               `json:"position"`
	Title        string            `json:"title"`
	Address      string            `json:"address"`
	Latitude     float64           `json:"latitude"`
	Longitude    float64           `json:"longitude"`
	Rating       float64           `json:"rating"`
	RatingCount  int               `json:"ratingCount"`
	Type         string            `json:"type"`
	Types        []string          `json:"types"`
	Website      string            `json:"website"`
	PhoneNumber  string            `json:"phoneNumber"`
	Description  string            `json:"description"`
	CID          string            `json:"cid"`
	PlaceID      string            `json:"placeId"`
	FID          string            `json:"fid"`
	OpeningHours map[string]string `json:"openingHours"`
	ThumbnailURL string            `json:"thumbnailUrl"`
}

func mapsVisibilityItemFromSerper(place serperPlaceDTO) mapsVisibilityItemResponse {
	if place.Types == nil {
		place.Types = []string{}
	}
	if place.OpeningHours == nil {
		place.OpeningHours = map[string]string{}
	}
	return mapsVisibilityItemResponse{
		Position:     place.Position,
		Title:        place.Title,
		Address:      place.Address,
		Category:     place.Type,
		Types:        place.Types,
		Rating:       place.Rating,
		RatingCount:  place.RatingCount,
		Website:      place.Website,
		Phone:        place.PhoneNumber,
		Latitude:     place.Latitude,
		Longitude:    place.Longitude,
		CID:          place.CID,
		PlaceID:      place.PlaceID,
		FID:          place.FID,
		OpeningHours: place.OpeningHours,
		ThumbnailURL: place.ThumbnailURL,
	}
}

func textValueJSON(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return textValue(value)
}
