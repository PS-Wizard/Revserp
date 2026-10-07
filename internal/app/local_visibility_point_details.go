package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
)

type localVisibilityPointPlaceResponse struct {
	Position    *int     `json:"position"`
	Title       string   `json:"title"`
	Address     string   `json:"address"`
	PlaceID     *string  `json:"place_id"`
	Rating      *float64 `json:"rating"`
	RatingCount *int     `json:"rating_count"`
	IsTarget    bool     `json:"is_target"`
}

type localVisibilityPointQueryResponse struct {
	QueryIndex  int                                 `json:"query_index"`
	Query       string                              `json:"query"`
	CallStatus  string                              `json:"call_status"`
	MatchStatus string                              `json:"match_status"`
	Rank        *int                                `json:"rank"`
	Error       *string                             `json:"error"`
	Places      []localVisibilityPointPlaceResponse `json:"places"`
}

type localVisibilityPointDetailsResponse struct {
	RunID         string                              `json:"run_id"`
	PointIndex    int                                 `json:"point_index"`
	TargetPlaceID string                              `json:"target_place_id"`
	Queries       []localVisibilityPointQueryResponse `json:"queries"`
}

type localVisibilityPointRawPlace struct {
	Position    *int     `json:"position"`
	Title       string   `json:"title"`
	Address     string   `json:"address"`
	PlaceID     *string  `json:"placeId"`
	Rating      *float64 `json:"rating"`
	RatingCount *int     `json:"ratingCount"`
}

type localVisibilityPointRawResponse struct {
	Places *[]localVisibilityPointRawPlace `json:"places"`
}

func buildLocalVisibilityPointDetails(runID string, pointIndex int, snapshot localvisibility.LocalRunSnapshot, rows []sqlc.GetLocalVisibilityPointResultsRow) localVisibilityPointDetailsResponse {
	rowsByQuery := make(map[int]sqlc.GetLocalVisibilityPointResultsRow, len(rows))
	for _, row := range rows {
		rowsByQuery[int(row.QueryIndex)] = row
	}
	queries := make([]localVisibilityPointQueryResponse, 0, len(snapshot.Queries))
	for queryIndex, queryText := range snapshot.Queries {
		entry := localVisibilityPointQueryResponse{
			QueryIndex:  queryIndex,
			Query:       queryText,
			CallStatus:  "pending",
			MatchStatus: "unknown",
			Places:      []localVisibilityPointPlaceResponse{},
		}
		row, ok := rowsByQuery[queryIndex]
		if !ok {
			queries = append(queries, entry)
			continue
		}
		entry.CallStatus = row.CallStatus
		entry.MatchStatus = row.MatchStatus
		if row.Rank.Valid {
			rank := int(row.Rank.Int32)
			entry.Rank = &rank
		}
		switch row.CallStatus {
		case "request_failed":
			message := "request failed"
			if row.Error.Valid && strings.TrimSpace(row.Error.String) != "" {
				message = row.Error.String
			}
			entry.Error = &message
		case "success_nonempty":
			places, err := decodeLocalVisibilityPointPlaces(row.RawResponse, snapshot.TargetPlaceID)
			if err != nil || len(places) == 0 {
				message := "Stored provider results could not be read."
				entry.Error = &message
				break
			}
			entry.Places = places
		}
		queries = append(queries, entry)
	}
	return localVisibilityPointDetailsResponse{
		RunID:         runID,
		PointIndex:    pointIndex,
		TargetPlaceID: snapshot.TargetPlaceID,
		Queries:       queries,
	}
}

func decodeLocalVisibilityPointPlaces(raw []byte, targetPlaceID string) ([]localVisibilityPointPlaceResponse, error) {
	if len(raw) == 0 {
		return nil, errors.New("local visibility point details: missing provider response")
	}
	var decoded localVisibilityPointRawResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	if decoded.Places == nil {
		return nil, errors.New("local visibility point details: missing provider places")
	}
	places := make([]localVisibilityPointPlaceResponse, 0, len(*decoded.Places))
	for _, rawPlace := range *decoded.Places {
		place := localVisibilityPointPlaceResponse{
			Position:    rawPlace.Position,
			Title:       rawPlace.Title,
			Address:     rawPlace.Address,
			Rating:      rawPlace.Rating,
			RatingCount: rawPlace.RatingCount,
		}
		if rawPlace.PlaceID != nil && *rawPlace.PlaceID != "" {
			placeID := *rawPlace.PlaceID
			place.PlaceID = &placeID
			place.IsTarget = targetPlaceID != "" && placeID == targetPlaceID
		}
		places = append(places, place)
	}
	return places, nil
}

func (a *App) handleGetLocalVisibilityRunPoint(w http.ResponseWriter, r *http.Request) {
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
	pointIndex, err := strconv.Atoi(chi.URLParam(r, "pointIndex"))
	if err != nil || pointIndex < 0 || pointIndex >= localvisibility.GridPointCount {
		writeJSONError(w, http.StatusBadRequest, "invalid point index")
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
	var snapshot localvisibility.LocalRunSnapshot
	if err := json.Unmarshal(run.Snapshot, &snapshot); err != nil {
		serverError(w, r, err)
		return
	}
	pointInSnapshot := false
	for _, point := range snapshot.Points {
		if point.PointIndex == pointIndex {
			pointInSnapshot = true
			break
		}
	}
	if !pointInSnapshot {
		writeJSONError(w, http.StatusBadRequest, "invalid point index")
		return
	}
	rows, err := a.Queries.GetLocalVisibilityPointResults(r.Context(), sqlc.GetLocalVisibilityPointResultsParams{
		RunID:      run.ID,
		PointIndex: int16(pointIndex),
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, buildLocalVisibilityPointDetails(run.ID.String(), pointIndex, snapshot, rows))
}
