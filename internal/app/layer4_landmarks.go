package app

import (
	"errors"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/locationlandmarks"
)

type layer4LandmarkRecord struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Latitude      float64  `json:"latitude"`
	Longitude     float64  `json:"longitude"`
	StraightLineM int32    `json:"straight_line_m"`
	Provider      string   `json:"provider"`
	ProviderRef   string   `json:"provider_ref"`
	Categories    []string `json:"categories"`
	FetchedAt     string   `json:"fetched_at"`
	Selected      bool     `json:"selected"`
}

func newLayer4LandmarkRecord(landmark sqlc.LocationLandmark) layer4LandmarkRecord {
	categories := landmark.Categories
	if categories == nil {
		categories = []string{}
	}
	return layer4LandmarkRecord{
		ID:            landmark.ID.String(),
		Name:          landmark.Name,
		Latitude:      landmark.Latitude,
		Longitude:     landmark.Longitude,
		StraightLineM: landmark.StraightLineM,
		Provider:      landmark.Provider,
		ProviderRef:   landmark.ProviderRef,
		Categories:    categories,
		FetchedAt:     landmark.FetchedAt.Time.UTC().Format(time.RFC3339),
		Selected:      landmark.Selected,
	}
}

type layer4PutLandmarkSelectionRequest struct {
	SelectedIDs []string `json:"selected_ids"`
}

func layer4ValidCoordinates(latitude, longitude float64) bool {
	if math.IsNaN(latitude) || math.IsInf(latitude, 0) || latitude < -90 || latitude > 90 {
		return false
	}
	if math.IsNaN(longitude) || math.IsInf(longitude, 0) || longitude < -180 || longitude > 180 {
		return false
	}
	return true
}

func layer4ParseSelectedIDs(raw []string) (map[string]pgtype.UUID, error) {
	if raw == nil {
		return nil, errors.New("selected_ids is required")
	}
	selected := make(map[string]pgtype.UUID, len(raw))
	for _, value := range raw {
		id, err := parseUUIDParam(value)
		if err != nil {
			return nil, errors.New("invalid landmark id")
		}
		selected[id.String()] = id
	}
	return selected, nil
}

func (a *App) landmarkDiscoveryClient() *locationlandmarks.Client {
	if a.Landmarks != nil {
		return a.Landmarks
	}
	return locationlandmarks.NewClient(locationlandmarks.Config{APIKey: a.Config.GoogleMapsAPIKey})
}

func (a *App) writeLayer4Landmarks(w http.ResponseWriter, r *http.Request, projectID, locationID, userID pgtype.UUID) {
	rows, err := a.Queries.ListLocationLandmarksForUser(r.Context(), sqlc.ListLocationLandmarksForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID})
	if err != nil {
		serverError(w, r, err)
		return
	}
	records := make([]layer4LandmarkRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, newLayer4LandmarkRecord(row))
	}
	writeJSON(w, http.StatusOK, records)
}

func (a *App) handleListProjectLocationLandmarks(w http.ResponseWriter, r *http.Request) {
	projectID, locationID, userID, _, ok := a.layer4Location(w, r)
	if !ok {
		return
	}
	a.writeLayer4Landmarks(w, r, projectID, locationID, userID)
}

func (a *App) handleRefreshProjectLocationLandmarks(w http.ResponseWriter, r *http.Request) {
	projectID, locationID, userID, location, ok := a.layer4Location(w, r)
	if !ok {
		return
	}
	if !location.PlaceID.Valid || strings.TrimSpace(location.PlaceID.String) == "" {
		writeJSONError(w, http.StatusUnprocessableEntity, "location has no bound listing")
		return
	}
	if !layer4ValidCoordinates(location.Latitude, location.Longitude) {
		writeJSONError(w, http.StatusUnprocessableEntity, "location has no valid coordinates")
		return
	}
	// Landmarks refresh uses Google Places Nearby Search (searchNearby), one call
	// per explicit refresh on the key's independent monthly allowance; usage
	// beyond the allowance is billable, and zero app credits must never be read
	// as guarantee-free.
	discovered, err := a.landmarkDiscoveryClient().Discover(r.Context(), location.Latitude, location.Longitude)
	if err != nil {
		log.Printf("landmark refresh failed: location_id=%s provider=google_places error=%q", locationID.String(), err.Error())
		message := "landmark provider error"
		if errors.Is(err, locationlandmarks.ErrProviderBusy) {
			message = "The landmark provider is busy right now. Nothing was charged. Try again in about a minute."
		}
		writeJSONError(w, http.StatusBadGateway, message)
		return
	}
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		locked, err := queries.LockProjectLocationForLandmarkRefreshForUser(r.Context(), sqlc.LockProjectLocationForLandmarkRefreshForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "location not found")
			} else {
				serverError(w, r, err)
			}
			return err
		}
		if locked.PlaceID.Valid != location.PlaceID.Valid || locked.PlaceID.String != location.PlaceID.String || locked.Latitude != location.Latitude || locked.Longitude != location.Longitude {
			writeJSONError(w, http.StatusConflict, "location changed during refresh")
			return errors.New("location changed during refresh")
		}
		for _, landmark := range discovered {
			categories := landmark.Categories
			if categories == nil {
				categories = []string{}
			}
			if _, err := queries.UpsertLocationLandmarkForUser(r.Context(), sqlc.UpsertLocationLandmarkForUserParams{
				Name:          landmark.Name,
				Latitude:      landmark.Latitude,
				Longitude:     landmark.Longitude,
				StraightLineM: int32(landmark.StraightLineM),
				Provider:      landmark.Provider,
				ProviderRef:   landmark.ProviderRef,
				Categories:    categories,
				FetchedAt:     timestamptzValue(landmark.FetchedAt),
				LocationID:    locationID,
				ProjectID:     projectID,
				UserID:        userID,
			}); err != nil {
				serverError(w, r, err)
				return err
			}
		}
		refs := make([]string, 0, len(discovered))
		for _, landmark := range discovered {
			refs = append(refs, landmark.ProviderRef)
		}
		if _, err := queries.DeleteRemovedLocationLandmarksForUser(r.Context(), sqlc.DeleteRemovedLocationLandmarksForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID, KeepProviderRefs: refs}); err != nil {
			serverError(w, r, err)
			return err
		}
		return a.persistLandmarkGeneratedQueries(w, r, queries, projectID, locationID, userID)
	}) {
		return
	}
	a.writeLayer4Landmarks(w, r, projectID, locationID, userID)
}

func (a *App) persistLandmarkGeneratedQueries(w http.ResponseWriter, r *http.Request, queries *sqlc.Queries, projectID, locationID, userID pgtype.UUID) error {
	landmarks, err := queries.ListLocationLandmarksForUser(r.Context(), sqlc.ListLocationLandmarksForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID})
	if err != nil {
		serverError(w, r, err)
		return err
	}
	if len(landmarks) == 0 {
		return nil
	}
	services, err := queries.ListEffectiveProjectLocationServiceLabelsForUser(r.Context(), sqlc.ListEffectiveProjectLocationServiceLabelsForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID})
	if err != nil {
		serverError(w, r, err)
		return err
	}
	if len(services) == 0 {
		return nil
	}
	existing, err := queries.ListProjectLocationQueriesForUser(r.Context(), sqlc.ListProjectLocationQueriesForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID})
	if err != nil {
		serverError(w, r, err)
		return err
	}
	labels := make([]string, 0, len(services))
	for _, service := range services {
		labels = append(labels, service.Label)
	}
	candidates, err := GenerateLandmarkQueryCandidates(labels, landmarks)
	if err != nil {
		serverError(w, r, err)
		return err
	}
	for _, insert := range planLandmarkQueryInserts(candidates, existing, projectID, locationID, userID) {
		if _, err := queries.InsertProjectLocationQueryForUser(r.Context(), insert); err != nil {
			serverError(w, r, err)
			return err
		}
	}
	return nil
}

func (a *App) handlePutProjectLocationLandmarkSelection(w http.ResponseWriter, r *http.Request) {
	projectID, locationID, userID, _, ok := a.layer4Location(w, r)
	if !ok {
		return
	}
	var body layer4PutLandmarkSelectionRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	selected, err := layer4ParseSelectedIDs(body.SelectedIDs)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		if _, err := queries.LockProjectLocationForLandmarkRefreshForUser(r.Context(), sqlc.LockProjectLocationForLandmarkRefreshForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "location not found")
			} else {
				serverError(w, r, err)
			}
			return err
		}
		current, err := queries.ListLocationLandmarksForUser(r.Context(), sqlc.ListLocationLandmarksForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		known := make(map[string]bool, len(current))
		for _, row := range current {
			known[row.ID.String()] = true
		}
		for id := range selected {
			if !known[id] {
				writeJSONError(w, http.StatusUnprocessableEntity, "unknown landmark")
				return errors.New("unknown landmark")
			}
		}
		for _, row := range current {
			_, want := selected[row.ID.String()]
			if want == row.Selected {
				continue
			}
			if _, err := queries.UpdateLocationLandmarkSelectionForUser(r.Context(), sqlc.UpdateLocationLandmarkSelectionForUserParams{Selected: want, ID: row.ID, LocationID: locationID, ProjectID: projectID, UserID: userID}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeJSONError(w, http.StatusUnprocessableEntity, "unknown landmark")
				} else {
					serverError(w, r, err)
				}
				return err
			}
		}
		return nil
	}) {
		return
	}
	a.writeLayer4Landmarks(w, r, projectID, locationID, userID)
}
