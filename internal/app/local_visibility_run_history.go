package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
)

// Persisted Maps run history for one location. The list reads stored runs
// only: no provider calls, no writes, no credit movement.

const (
	defaultRunHistoryLimit = 20
	maxRunHistoryLimit     = 100
)

const countLocalVisibilityRunsForHistory = `SELECT COUNT(*) FROM local_visibility_runs WHERE location_id = $1`

const selectLocalVisibilityRunsForHistory = `SELECT id, status, radius_m, expected_credits, credits_used, error, snapshot, created_at, completed_at
FROM local_visibility_runs WHERE location_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`

// localVisibilityRunHistoryItem is one stored run with its frozen grid shape.
// Cell detail stays on the single-run endpoints; the list carries metadata.
type localVisibilityRunHistoryItem struct {
	ID              string  `json:"id"`
	Status          string  `json:"status"`
	RadiusM         int     `json:"radius_m"`
	ExpectedCredits int     `json:"expected_credits"`
	CreditsUsed     int     `json:"credits_used"`
	QueryCount      int     `json:"query_count"`
	GridPointCount  int     `json:"grid_point_count"`
	TotalCells      int     `json:"total_cells"`
	CreatedAt       string  `json:"created_at"`
	CompletedAt     *string `json:"completed_at,omitempty"`
	Error           *string `json:"error,omitempty"`
}

// localVisibilityRunHistoryResponse is the paginated run list for a location.
type localVisibilityRunHistoryResponse struct {
	Total int                             `json:"total"`
	Runs  []localVisibilityRunHistoryItem `json:"runs"`
}

// localVisibilityRunHistoryRow is the stored run row shared by the list query
// and its item builder, so column order lives in exactly one place.
type localVisibilityRunHistoryRow struct {
	ID              pgtype.UUID
	Status          string
	RadiusM         int32
	ExpectedCredits int32
	CreditsUsed     int32
	Error           pgtype.Text
	Snapshot        []byte
	CreatedAt       pgtype.Timestamptz
	CompletedAt     pgtype.Timestamptz
}

func scanLocalVisibilityRunHistoryRow(scanner interface {
	Scan(...any) error
}) (localVisibilityRunHistoryRow, error) {
	var row localVisibilityRunHistoryRow
	err := scanner.Scan(&row.ID, &row.Status, &row.RadiusM, &row.ExpectedCredits, &row.CreditsUsed,
		&row.Error, &row.Snapshot, &row.CreatedAt, &row.CompletedAt)
	return row, err
}

// parseRunHistoryPagination bounds the list window. Missing params use the
// defaults; anything non-numeric or outside [1,100]/negative fails before any
// database read.
func parseRunHistoryPagination(values url.Values) (limit, offset int, err error) {
	limit = defaultRunHistoryLimit
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxRunHistoryLimit {
			return 0, 0, errors.New("limit must be an integer between 1 and 100")
		}
	}
	if raw := values.Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return 0, 0, errors.New("offset must be a non-negative integer")
		}
	}
	return limit, offset, nil
}

// newLocalVisibilityRunHistoryItem projects one stored run. Counts come from
// the frozen snapshot, so a run keeps its grid shape even after later query
// edits. An unreadable snapshot yields zero counts rather than failing the
// whole list: stored evidence stays listable.
func newLocalVisibilityRunHistoryItem(row localVisibilityRunHistoryRow) localVisibilityRunHistoryItem {
	item := localVisibilityRunHistoryItem{
		ID:              row.ID.String(),
		Status:          row.Status,
		RadiusM:         int(row.RadiusM),
		ExpectedCredits: int(row.ExpectedCredits),
		CreditsUsed:     int(row.CreditsUsed),
		CreatedAt:       row.CreatedAt.Time.UTC().Format(time.RFC3339),
	}
	if row.CompletedAt.Valid {
		completed := row.CompletedAt.Time.UTC().Format(time.RFC3339)
		item.CompletedAt = &completed
	}
	if row.Error.Valid {
		message := row.Error.String
		item.Error = &message
	}
	var snapshot localvisibility.LocalRunSnapshot
	if err := json.Unmarshal(row.Snapshot, &snapshot); err == nil {
		item.QueryCount = len(snapshot.Queries)
		item.GridPointCount = len(snapshot.Points)
		item.TotalCells = len(snapshot.Queries) * len(snapshot.Points)
	}
	return item
}

// locationResponseWithRadiusM attaches the persisted grid radius to a location
// DTO. Generated location rows now carry RadiusM from migration 101; callers
// that select it convert here instead of repeating the cast.
func locationResponseWithRadiusM(response localVisibilityLocationResponse, radiusM int32) localVisibilityLocationResponse {
	response.RadiusM = int(radiusM)
	return response
}

// handleListLocalVisibilityRuns answers GET /projects/{projectID}/locations/{locationID}/runs.
// Any project member can read; a foreign location reads as not found.
func (a *App) handleListLocalVisibilityRuns(w http.ResponseWriter, r *http.Request) {
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
	limit, offset, err := parseRunHistoryPagination(r.URL.Query())
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := a.Queries.GetProjectLocationForUser(r.Context(), sqlc.GetProjectLocationForUserParams{
		ID:     locationID,
		ID_2:   projectID,
		UserID: principal.User.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "location not found")
		} else {
			serverError(w, r, err)
		}
		return
	}
	response, err := listLocalVisibilityRunHistory(r.Context(), a.DB, locationID, limit, offset)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// listLocalVisibilityRunHistory reads the total and one page of stored runs.
// It never writes and never calls a provider.
func listLocalVisibilityRunHistory(ctx context.Context, pool *pgxpool.Pool, locationID pgtype.UUID, limit, offset int) (localVisibilityRunHistoryResponse, error) {
	response := localVisibilityRunHistoryResponse{Runs: []localVisibilityRunHistoryItem{}}
	var total int64
	if err := pool.QueryRow(ctx, countLocalVisibilityRunsForHistory, locationID).Scan(&total); err != nil {
		return response, err
	}
	response.Total = int(total)
	rows, err := pool.Query(ctx, selectLocalVisibilityRunsForHistory, locationID, limit, offset)
	if err != nil {
		return response, err
	}
	defer rows.Close()
	for rows.Next() {
		row, err := scanLocalVisibilityRunHistoryRow(rows)
		if err != nil {
			rows.Close()
			return response, err
		}
		response.Runs = append(response.Runs, newLocalVisibilityRunHistoryItem(row))
	}
	if err := rows.Err(); err != nil {
		return response, err
	}
	return response, nil
}
