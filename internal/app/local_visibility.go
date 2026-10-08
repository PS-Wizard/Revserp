package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
)

const (
	defaultLocalVisibilityRadiusM = 5000
	minLocalVisibilityRadiusM     = 1000
	maxLocalVisibilityRadiusM     = 25000
	maxLocationNameBytes          = 200
	maxLocationPlaceIDBytes       = 500
)

type createProjectLocationRequest struct {
	Name       string   `json:"name"`
	PlaceID    string   `json:"place_id"`
	Latitude   *float64 `json:"latitude"`
	Longitude  *float64 `json:"longitude"`
	Address    string   `json:"address"`
	Locality   string   `json:"locality"`
	Localities []string `json:"localities"`
}

type createLocalVisibilityRunRequest struct {
	RadiusM         int  `json:"radius_m"`
	ExpectedCredits *int `json:"expected_credits"`
}

// localVisibilityLocationResponse is the Layer 4 location DTO. Services is
// the server-computed effective set and queries carries every draft record,
// both JSON arrays even when empty.
type localVisibilityLocationResponse struct {
	ID         string                      `json:"id"`
	ProjectID  string                      `json:"project_id"`
	Name       string                      `json:"name"`
	PlaceID    *string                     `json:"place_id"`
	Address    string                      `json:"address"`
	Locality   string                      `json:"locality"`
	Localities []string                    `json:"localities"`
	Services   []string                    `json:"services"`
	Latitude   float64                     `json:"latitude"`
	Longitude  float64                     `json:"longitude"`
	Queries    []layer4LocationQueryRecord `json:"queries"`
	RadiusM    int                         `json:"radius_m,omitempty"`
}

// localVisibilityCellResponse is one planned grid call. Rank is null unless the
// match is found; result_count is null unless a succeeded call returned a
// readable places array; error is null unless the call failed.
type localVisibilityCellResponse struct {
	QueryIndex     int      `json:"query_index"`
	PointIndex     int      `json:"point_index"`
	Latitude       float64  `json:"latitude"`
	Longitude      float64  `json:"longitude"`
	DistanceM      float64  `json:"distance_m"`
	Ring           string   `json:"ring"`
	Sector         string   `json:"sector"`
	CallStatus     string   `json:"call_status"`
	MatchStatus    string   `json:"match_status"`
	Rank           *int     `json:"rank"`
	Credits        int      `json:"credits"`
	CreditKnown    bool     `json:"credit_known"`
	RequestedLL    string   `json:"requested_ll"`
	EchoedLL       *string  `json:"echoed_ll"`
	ViewportDriftM *float64 `json:"viewport_drift_m"`
	ResultCount    *int     `json:"result_count"`
	Error          *string  `json:"error"`
}

// Reserved credits cover pending calls and started calls with unknown charges.
// Unconfirmed calls count only started calls without provider charge evidence;
// credits_used is confirmed spend, not a total that includes unknown charges.
type localVisibilityRunResponse struct {
	ID               string                        `json:"id"`
	LocationID       string                        `json:"location_id"`
	TargetPlaceID    string                        `json:"target_place_id"`
	Status           string                        `json:"status"`
	RadiusM          int                           `json:"radius_m"`
	ExpectedCredits  int                           `json:"expected_credits"`
	ReservedCredits  int                           `json:"reserved_credits"`
	CreditsUsed      int                           `json:"credits_used"`
	RetryCredits     int                           `json:"retry_credits"`
	UnconfirmedCalls int                           `json:"unconfirmed_calls"`
	CompletedCells   int                           `json:"completed_cells"`
	TotalCells       int                           `json:"total_cells"`
	Queries          []string                      `json:"queries"`
	Cells            []localVisibilityCellResponse `json:"cells"`
	Error            *string                       `json:"error"`
}

// localVisibilityRunCreatedResponse is the 202 body for an enqueued run.
type localVisibilityRunCreatedResponse struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	ExpectedCredits  int    `json:"expected_credits"`
	ReservedCredits  int    `json:"reserved_credits"`
	UnconfirmedCalls int    `json:"unconfirmed_calls"`
}

// assembleLocationResponse builds the Layer 4 location DTO from a location
// row plus the server-computed effective services and every draft query
// record, so all location endpoints share one shape.
func (a *App) assembleLocationResponse(ctx context.Context, id, projectID pgtype.UUID, name string, placeID pgtype.Text, latitude, longitude float64, address, locality string, localitiesJSON []byte, radiusM int32, userID pgtype.UUID) (localVisibilityLocationResponse, error) {
	var localities []string
	if err := json.Unmarshal(localitiesJSON, &localities); err != nil {
		return localVisibilityLocationResponse{}, fmt.Errorf("location localities: %w", err)
	}
	if localities == nil {
		localities = []string{}
	}
	effective, err := a.Queries.ListEffectiveProjectLocationServiceLabelsForUser(ctx, sqlc.ListEffectiveProjectLocationServiceLabelsForUserParams{LocationID: id, ProjectID: projectID, UserID: userID})
	if err != nil {
		return localVisibilityLocationResponse{}, err
	}
	services := make([]string, 0, len(effective))
	for _, row := range effective {
		services = append(services, row.Label)
	}
	rows, err := a.Queries.ListProjectLocationQueriesForUser(ctx, sqlc.ListProjectLocationQueriesForUserParams{LocationID: id, ProjectID: projectID, UserID: userID})
	if err != nil {
		return localVisibilityLocationResponse{}, err
	}
	records := make([]layer4LocationQueryRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, newLayer4LocationQueryRecord(row))
	}
	return localVisibilityLocationResponse{
		ID:         id.String(),
		ProjectID:  projectID.String(),
		Name:       name,
		PlaceID:    localVisibilityNullableText(placeID),
		Address:    address,
		Locality:   locality,
		Localities: localities,
		Services:   services,
		RadiusM:    int(radiusM),
		Latitude:   latitude,
		Longitude:  longitude,
		Queries:    records,
	}, nil
}

// normalizeLocalities trims, drops blanks and case-insensitive repeats,
// keeping the caller's smallest-first order.
func normalizeLocalities(raw []string) []string {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, level := range raw {
		level = strings.TrimSpace(level)
		if level == "" {
			continue
		}
		if key := strings.ToLower(level); !seen[key] {
			seen[key] = true
			out = append(out, level)
		}
	}
	return out
}

// newLocalVisibilityRunResponse rebuilds every planned cell from the frozen
// snapshot points and the stored cell rows, keeping pending cells so an
// incomplete run never looks finished.
func newLocalVisibilityRunResponse(run sqlc.LocalVisibilityRun, cells []sqlc.GetLocalRunCellsRow, completedCells int) (localVisibilityRunResponse, error) {
	var snapshot localvisibility.LocalRunSnapshot
	if err := json.Unmarshal(run.Snapshot, &snapshot); err != nil {
		return localVisibilityRunResponse{}, fmt.Errorf("local visibility run snapshot: %w", err)
	}

	pointsByIndex := make(map[int]localvisibility.GridPoint, len(snapshot.Points))
	for _, point := range snapshot.Points {
		pointsByIndex[point.PointIndex] = point
	}
	type cellKey struct{ queryIndex, pointIndex int }
	resultsByCell := make(map[cellKey]sqlc.GetLocalRunCellsRow, len(cells))
	for _, cell := range cells {
		resultsByCell[cellKey{int(cell.QueryIndex), int(cell.PointIndex)}] = cell
	}

	response := localVisibilityRunResponse{
		ID:              run.ID.String(),
		LocationID:      run.LocationID.String(),
		TargetPlaceID:   snapshot.TargetPlaceID,
		Status:          run.Status,
		RadiusM:         int(run.RadiusM),
		ExpectedCredits: int(run.ExpectedCredits),
		ReservedCredits: int(run.ReservedCredits),
		CreditsUsed:     int(run.CreditsUsed),
		RetryCredits:    int(run.RetryCredits),
		CompletedCells:  completedCells,
		TotalCells:      len(snapshot.Queries) * len(snapshot.Points),
		Queries:         snapshot.Queries,
		Cells:           make([]localVisibilityCellResponse, 0, len(snapshot.Queries)*localvisibility.GridPointCount),
	}
	if response.Queries == nil {
		response.Queries = []string{}
	}
	if run.Error.Valid {
		message := run.Error.String
		response.Error = &message
	}

	for queryIndex := range snapshot.Queries {
		for pointIndex := 0; pointIndex < localvisibility.GridPointCount; pointIndex++ {
			point := pointsByIndex[pointIndex]
			cell := localVisibilityCellResponse{
				QueryIndex:  queryIndex,
				PointIndex:  pointIndex,
				Latitude:    point.Latitude,
				Longitude:   point.Longitude,
				DistanceM:   point.DistanceM,
				Ring:        point.Ring,
				Sector:      point.Sector,
				CallStatus:  "pending",
				MatchStatus: "unknown",
			}
			if pointIndex < len(snapshot.Viewports) {
				cell.RequestedLL = snapshot.Viewports[pointIndex]
			}
			row, ok := resultsByCell[cellKey{queryIndex, pointIndex}]
			if !ok {
				response.Cells = append(response.Cells, cell)
				continue
			}
			cell.CallStatus = row.CallStatus
			cell.MatchStatus = row.MatchStatus
			cell.Credits = int(row.Credits)
			cell.CreditKnown = row.CreditKnown
			if row.EchoedLl != "" {
				echoed := row.EchoedLl
				cell.EchoedLL = &echoed
			}
			if row.EchoedLl != "" && row.ViewportDriftM >= 0 {
				drift := row.ViewportDriftM
				cell.ViewportDriftM = &drift
			}
			if row.ResultCount >= 0 {
				count := int(row.ResultCount)
				cell.ResultCount = &count
			}
			if row.Rank.Valid {
				rank := int(row.Rank.Int32)
				cell.Rank = &rank
			}
			if row.Error.Valid {
				message := row.Error.String
				cell.Error = &message
			}
			if row.StartedAt.Valid && !row.CreditKnown {
				response.UnconfirmedCalls++
			}
			response.Cells = append(response.Cells, cell)
		}
	}
	return response, nil
}

func validateLocationInput(name, placeID string, latitude, longitude float64) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxLocationNameBytes {
		return "", "", fmt.Errorf("name must contain 1–%d bytes", maxLocationNameBytes)
	}
	placeID = strings.TrimSpace(placeID)
	if len(placeID) > maxLocationPlaceIDBytes {
		return "", "", fmt.Errorf("place_id must fit within %d bytes", maxLocationPlaceIDBytes)
	}
	if math.IsNaN(latitude) || math.IsInf(latitude, 0) || latitude < -90 || latitude > 90 {
		return "", "", errors.New("latitude must be a finite number between -90 and 90")
	}
	if math.IsNaN(longitude) || math.IsInf(longitude, 0) || longitude < -180 || longitude > 180 {
		return "", "", errors.New("longitude must be a finite number between -180 and 180")
	}
	return name, placeID, nil
}

// normalizeLocalVisibilityRadiusM applies the 5 km default and bounds the run
// radius to the fixed 1–25 km slider.
func normalizeLocalVisibilityRadiusM(radiusM int) (int, error) {
	if radiusM == 0 {
		return defaultLocalVisibilityRadiusM, nil
	}
	if radiusM < minLocalVisibilityRadiusM || radiusM > maxLocalVisibilityRadiusM {
		return 0, fmt.Errorf("radius_m must be between %d and %d", minLocalVisibilityRadiusM, maxLocalVisibilityRadiusM)
	}
	return radiusM, nil
}

func isPostgresUniqueConstraintConflict(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func (a *App) handleCreateProjectLocation(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	var body createProjectLocationRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	if body.Latitude == nil || body.Longitude == nil {
		writeJSONError(w, http.StatusBadRequest, "latitude and longitude are required")
		return
	}
	if strings.TrimSpace(body.PlaceID) != "" {
		writeJSONError(w, http.StatusBadRequest, "bind a listing from a recorded lookup after creating the location")
		return
	}
	name, _, err := validateLocationInput(body.Name, "", *body.Latitude, *body.Longitude)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	address := strings.TrimSpace(body.Address)
	locality := strings.TrimSpace(body.Locality)
	if len(address) > 1000 || len(locality) > 500 {
		writeJSONError(w, http.StatusBadRequest, "address or locality exceeds the allowed byte limit")
		return
	}
	localities := normalizeLocalities(body.Localities)
	for _, level := range localities {
		if len(level) > 500 {
			writeJSONError(w, http.StatusBadRequest, "locality exceeds the allowed byte limit")
			return
		}
	}
	if locality == "" && len(localities) > 0 {
		locality = localities[0]
	}
	if len(localities) == 0 && locality != "" {
		localities = []string{locality}
	}
	encodedLocalities, err := json.Marshal(localities)
	if err != nil {
		serverError(w, r, err)
		return
	}

	location, err := a.Queries.CreateLocationSetupForUser(r.Context(), sqlc.CreateLocationSetupForUserParams{
		ProjectID:  projectID,
		UserID:     principal.User.ID,
		Name:       name,
		Latitude:   *body.Latitude,
		Longitude:  *body.Longitude,
		Address:    address,
		Locality:   locality,
		Localities: encodedLocalities,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}
	response, err := a.assembleLocationResponse(r.Context(), location.ID, location.ProjectID, location.Name, location.PlaceID, location.Latitude, location.Longitude, location.Address, location.Locality, location.Localities, location.RadiusM, principal.User.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

// handleGetProjectLocation returns one location scoped to the project and
// organization of the authenticated user.
func (a *App) handleGetProjectLocation(w http.ResponseWriter, r *http.Request) {
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
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	location, err := a.Queries.GetProjectLocationForUser(r.Context(), sqlc.GetProjectLocationForUserParams{
		ID:     locationID,
		ID_2:   projectID,
		UserID: principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "location not found")
			return
		}
		serverError(w, r, err)
		return
	}

	response, err := a.assembleLocationResponse(r.Context(), location.ID, location.ProjectID, location.Name, location.PlaceID, location.Latitude, location.Longitude, location.Address, location.Locality, location.Localities, location.RadiusM, principal.User.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleCreateLocalVisibilityRun quotes the run from the enabled map draft,
// compares it against the caller's expected_credits before reserving anything,
// and enqueues one run for the location. A stale quote, budget exhaustion,
// and an in-flight run all answer 409 without spending.
func (a *App) handleCreateLocalVisibilityRun(w http.ResponseWriter, r *http.Request) {
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
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	var body createLocalVisibilityRunRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	radiusM, err := normalizeLocalVisibilityRadiusM(body.RadiusM)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ExpectedCredits == nil {
		writeJSONError(w, http.StatusBadRequest, "expected_credits is required")
		return
	}

	store := localvisibility.LocalVisibilityStore{Pool: a.DB, MapsEndpoint: a.Config.SerperMapsEndpoint}
	run, err := store.EnqueueRun(r.Context(), principal.User.ID, projectID, locationID, radiusM, *body.ExpectedCredits)
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeJSONError(w, http.StatusNotFound, "location not found")
		case errors.Is(err, localvisibility.ErrMapsBudgetUnavailable):
			writeJSONError(w, http.StatusConflict, "maps spending allowance is unavailable")
		case errors.Is(err, localvisibility.ErrLocationListingUnbound):
			writeJSONError(w, http.StatusConflict, err.Error())
		case errors.Is(err, localvisibility.ErrMapQueriesInvalid):
			writeJSONError(w, http.StatusConflict, err.Error())
		case errors.Is(err, localvisibility.ErrExpectedCreditsMismatch):
			writeJSONError(w, http.StatusConflict, err.Error())
		case isPostgresUniqueConstraintConflict(err, "local_visibility_runs_inflight_idx"):
			writeJSONError(w, http.StatusConflict, "A run for this location is already in progress. No additional credits were charged.")
		default:
			serverError(w, r, err)
		}
		return
	}

	writeJSON(w, http.StatusAccepted, localVisibilityRunCreatedResponse{
		ID:               run.ID.String(),
		Status:           run.Status,
		ExpectedCredits:  int(run.ExpectedCredits),
		ReservedCredits:  int(run.ReservedCredits),
		UnconfirmedCalls: 0,
	})
}

// handleGetLatestLocalVisibilityRun returns the most recent run for a location,
// or 404 when the location has never run.
func (a *App) handleGetLatestLocalVisibilityRun(w http.ResponseWriter, r *http.Request) {
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
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	run, err := a.Queries.GetLatestLocalVisibilityRunForUser(r.Context(), sqlc.GetLatestLocalVisibilityRunForUserParams{
		ID:     locationID,
		ID_2:   projectID,
		UserID: principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "no local visibility run yet")
			return
		}
		serverError(w, r, err)
		return
	}

	cells, err := a.Queries.GetLocalRunCells(r.Context(), run.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	completedCells, err := a.Queries.CountLocalVisibilityRunResultsForUser(r.Context(), sqlc.CountLocalVisibilityRunResultsForUserParams{
		RunID:  run.ID,
		ID:     locationID,
		ID_2:   projectID,
		UserID: principal.User.ID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	response, err := newLocalVisibilityRunResponse(run, cells, int(completedCells))
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// handleGetLocalVisibilityRun returns one run with all 45 planned cells.
func (a *App) handleGetLocalVisibilityRun(w http.ResponseWriter, r *http.Request) {
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
	runID, err := parseUUIDParam(chi.URLParam(r, "runID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid run id")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	run, err := a.Queries.GetLocalVisibilityRunForUser(r.Context(), sqlc.GetLocalVisibilityRunForUserParams{
		ID:     runID,
		ID_2:   locationID,
		ID_3:   projectID,
		UserID: principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "local visibility run not found")
			return
		}
		serverError(w, r, err)
		return
	}

	cells, err := a.Queries.GetLocalRunCells(r.Context(), run.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	completedCells, err := a.Queries.CountLocalVisibilityRunResultsForUser(r.Context(), sqlc.CountLocalVisibilityRunResultsForUserParams{
		RunID:  runID,
		ID:     locationID,
		ID_2:   projectID,
		UserID: principal.User.ID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	response, err := newLocalVisibilityRunResponse(run, cells, int(completedCells))
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func isUnsettledMapsSpendError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503" && strings.Contains(pgErr.Message, "active or unconfirmed Maps spend")
}

func localVisibilityNullableText(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}
