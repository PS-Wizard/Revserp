package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/locationkeywords"
)

const locationLandmarksName = "get_location_landmarks"

const getLocationLandmarksSchema = `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

type locationLandmarkReader interface {
	locationKeywordReader
	locationkeywords.Queries
}

type locationLandmarkExecutor struct {
	locations locationLandmarkReader
}

func getLocationLandmarksTool() Tool {
	return Tool{
		Def: Def{
			Name:        locationLandmarksName,
			Label:       "Get location landmarks",
			Description: "Read the current location's owned geometry and its saved nearby landmarks: location name, address, locality, localities, latitude, longitude, plus every saved landmark with its straight-line distance, categories, and selection. Read-only; it never discovers places and never writes. An empty landmark list is honest, never a reason to invent place names. Only runs inside a location conversation; outside one it explains that instead of guessing.",
			Schema:      json.RawMessage(getLocationLandmarksSchema),
		},
		Execute: executeGetLocationLandmarks,
	}
}

// executeGetLocationLandmarks serves location-scoped calls from the read-only
// executor. Outside a location conversation it answers the model directly
// instead of touching parent data.
func executeGetLocationLandmarks(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if !s.LocationID.Valid {
		return Result{Content: locationLandmarksName + " error: this tool only runs inside a location conversation"}, nil
	}
	if s.Queries == nil || s.DB == nil {
		return Result{}, errors.New("get_location_landmarks: scope has no queries or transaction support")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("get_location_landmarks: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	exec := locationLandmarkExecutor{locations: s.Queries.WithTx(tx)}
	return exec.runLocal(ctx, args, s.ProjectID, s.LocationID, s.UserID, s.RowBudget)
}

type locationLandmarkResponse struct {
	Name          string   `json:"name"`
	StraightLineM int32    `json:"straight_line_m"`
	Categories    []string `json:"categories"`
	Selected      bool     `json:"selected"`
}

type locationGeometryResponse struct {
	Name       string   `json:"name"`
	Address    string   `json:"address"`
	Locality   string   `json:"locality"`
	Localities []string `json:"localities"`
	Latitude   float64  `json:"latitude"`
	Longitude  float64  `json:"longitude"`
}

func (e *locationLandmarkExecutor) runLocal(ctx context.Context, raw json.RawMessage, projectID, locationID, userID pgtype.UUID, budget *Budget) (Result, error) {
	if budget != nil && budget.Remaining() == 0 {
		return Result{
			Content: "The row budget for this turn is exhausted. Do not call get_location_landmarks again; synthesize your answer from the data you already have.",
			Summary: "row budget reached",
		}, nil
	}
	if fields, err := strictJSONFields(raw); err != nil {
		return Result{Content: locationLandmarksName + " error: " + err.Error()}, nil
	} else {
		for key := range fields {
			return Result{Content: locationLandmarksName + " error: unknown argument " + fmt.Sprintf("%q", key)}, nil
		}
	}
	location, err := checkLocationReadAccess(ctx, e.locations, projectID, locationID, userID)
	if err != nil {
		if isModelError(err) {
			return Result{Content: locationLandmarksName + " error: " + err.Error()}, nil
		}
		return Result{}, fmt.Errorf("get_location_landmarks: check location access: %w", err)
	}
	rows, err := e.locations.ListLocationLandmarksForUser(ctx, sqlc.ListLocationLandmarksForUserParams{
		LocationID: locationID,
		ProjectID:  projectID,
		UserID:     userID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("get_location_landmarks: list landmarks: %w", err)
	}
	landmarks := make([]locationLandmarkResponse, 0, len(rows))
	for _, row := range rows {
		categories := row.Categories
		if categories == nil {
			categories = []string{}
		}
		landmarks = append(landmarks, locationLandmarkResponse{
			Name:          row.Name,
			StraightLineM: row.StraightLineM,
			Categories:    categories,
			Selected:      row.Selected,
		})
	}
	totalSaved := len(landmarks)
	truncated := false
	if budget != nil && len(landmarks) > budget.Remaining() {
		landmarks = landmarks[:budget.Remaining()]
		truncated = true
	}
	if budget != nil {
		budget.Spend(len(landmarks))
	}
	content, err := json.Marshal(map[string]any{
		"location": locationGeometryResponse{
			Name:       location.Name,
			Address:    location.Address,
			Locality:   location.Locality,
			Localities: decodeLocationLocalities(location.Localities),
			Latitude:   location.Latitude,
			Longitude:  location.Longitude,
		},
		"landmarks":   landmarks,
		"truncated":   truncated,
		"total_saved": totalSaved,
	})
	if err != nil {
		return Result{}, fmt.Errorf("get_location_landmarks: marshal response: %w", err)
	}
	summary := fmt.Sprintf("location landmarks: %d saved nearby places for %s", len(landmarks), location.Name)
	if truncated {
		summary = fmt.Sprintf("location landmarks: %d of %d saved nearby places for %s (row budget reached)", len(landmarks), totalSaved, location.Name)
	}
	return Result{
		Content: string(content),
		Summary: summary,
	}, nil
}

// decodeLocationLocalities reads the stored localities array, tolerating an
// absent or malformed value as no localities rather than failing the read.
func decodeLocationLocalities(raw []byte) []string {
	var localities []string
	if len(raw) == 0 {
		return []string{}
	}
	if err := json.Unmarshal(raw, &localities); err != nil {
		return []string{}
	}
	if localities == nil {
		return []string{}
	}
	return localities
}
