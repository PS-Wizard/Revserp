package localvisibility

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/serper"
)

const localRunFinalizeTimeout = 30 * time.Second

var localRunRings = map[string]bool{"centre": true, "edge": true, "corner": true}

var localRunSectors = map[string]bool{
	"centre": true, "N": true, "NE": true, "E": true, "SE": true, "S": true, "SW": true, "W": true, "NW": true,
}

// ExecuteLocalVisibilityRun executes one queued run with one to five frozen
// queries across nine frozen points, settles each call, then finalizes the run.
//
// The frozen snapshot is fully validated before any provider call is issued,
// including the exact stored viewport strings. The run status row is
// authoritative: a nil return means completed, any other return means the
// dispatcher should mark its queue job failed while the run row itself says
// partial or failed. Cells are never retried.
func ExecuteLocalVisibilityRun(ctx context.Context, pool *pgxpool.Pool, cfg config.Config, runID, projectID pgtype.UUID) error {
	queries := sqlc.New(pool)
	run, err := queries.GetLocalVisibilityRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("local visibility load run: %w", err)
	}
	if run.ProjectID != projectID {
		return fmt.Errorf("local visibility run %s does not belong to project %s", runID.String(), projectID.String())
	}
	var snapshot LocalRunSnapshot
	if err := json.Unmarshal(run.Snapshot, &snapshot); err != nil {
		return fmt.Errorf("local visibility run snapshot: %w", err)
	}
	if err := validateLocalRunSnapshot(snapshot, run.ExpectedCredits); err != nil {
		return err
	}
	if _, err := queries.StartLocalVisibilityRun(ctx, runID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("local visibility run %s is not queued", runID.String())
		}
		return fmt.Errorf("local visibility start run: %w", err)
	}

	store := LocalVisibilityStore{Pool: pool}
	client := serper.NewClient(cfg.SerperAPIKey, snapshot.Endpoint, cfg.SerperPlacesEndpoint, cfg.SerperReviewsEndpoint)

	var execErr error
loop:
	for queryIndex, query := range snapshot.Queries {
		for pointIndex, point := range snapshot.Points {
			if ctx.Err() != nil {
				break loop
			}
			if _, err := queries.StartLocalRunCell(ctx, sqlc.StartLocalRunCellParams{
				RunID:      runID,
				QueryIndex: int16(queryIndex),
				PointIndex: int16(pointIndex),
			}); err != nil {
				// A claimed or already-recorded cell must not be called again:
				// the first attempt may already have been billed remotely.
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				execErr = fmt.Errorf("local visibility start cell q%d p%d: %w", queryIndex, pointIndex, err)
				break loop
			}
			// No retry, ever: the remote side bills per call and cannot
			// guarantee a replay is free, so each cell gets one attempt.
			response, providerErr := client.MapsAt(ctx, query, point.Latitude, point.Longitude)
			outcome := localCellOutcome(runID, queryIndex, pointIndex, snapshot.TargetPlaceID, response, providerErr)
			if err := recordLocalCellSettlement(ctx, store, outcome); err != nil {
				execErr = fmt.Errorf("local visibility record cell q%d p%d: %w", queryIndex, pointIndex, err)
				break loop
			}
			if ctx.Err() != nil {
				break loop
			}
			if outcome.CreditKnown && outcome.Credits != MapsCreditsPerCall {
				break loop
			}
		}
	}

	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), localRunFinalizeTimeout)
	defer cancel()
	if err := store.FinishRun(finalCtx, runID); err != nil {
		if execErr != nil {
			return execErr
		}
		return fmt.Errorf("local visibility finish run: %w", err)
	}
	if execErr != nil {
		return execErr
	}
	finished, err := sqlc.New(pool).GetLocalVisibilityRun(finalCtx, runID)
	if err != nil {
		return fmt.Errorf("local visibility reload finished run: %w", err)
	}
	if finished.Status != "completed" {
		return fmt.Errorf("local visibility run %s %s", runID.String(), finished.Status)
	}
	return nil
}

func validateLocalRunSnapshot(snapshot LocalRunSnapshot, expectedCredits int32) error {
	if len(snapshot.Queries) < 1 || len(snapshot.Queries) > MapQueryCount {
		return fmt.Errorf("local visibility snapshot queries = %d, want between 1 and %d", len(snapshot.Queries), MapQueryCount)
	}
	seenQueries := make(map[string]bool, len(snapshot.Queries))
	for i, query := range snapshot.Queries {
		if query == "" || len(query) > 500 {
			return fmt.Errorf("local visibility snapshot query %d must contain 1–500 bytes", i)
		}
		if query != strings.TrimSpace(query) {
			return fmt.Errorf("local visibility snapshot query %d is not normalized", i)
		}
		key := strings.ToLower(query)
		if seenQueries[key] {
			return errors.New("local visibility snapshot queries must be distinct")
		}
		seenQueries[key] = true
	}
	if len(snapshot.Points) != GridPointCount {
		return fmt.Errorf("local visibility snapshot points = %d, want %d", len(snapshot.Points), GridPointCount)
	}
	if len(snapshot.Viewports) != GridPointCount {
		return fmt.Errorf("local visibility snapshot viewports = %d, want %d", len(snapshot.Viewports), GridPointCount)
	}
	seenPoints := make(map[int]bool, len(snapshot.Points))
	for i, point := range snapshot.Points {
		if point.PointIndex < 0 || point.PointIndex >= GridPointCount {
			return fmt.Errorf("local visibility snapshot point %d index = %d, want 0–%d", i, point.PointIndex, GridPointCount-1)
		}
		if seenPoints[point.PointIndex] {
			return fmt.Errorf("local visibility snapshot duplicate point index %d", point.PointIndex)
		}
		seenPoints[point.PointIndex] = true
		if !localRunRings[point.Ring] {
			return fmt.Errorf("local visibility snapshot point %d ring = %q", i, point.Ring)
		}
		if !localRunSectors[point.Sector] {
			return fmt.Errorf("local visibility snapshot point %d sector = %q", i, point.Sector)
		}
		viewport, err := serper.FormatMapsViewport(point.Latitude, point.Longitude)
		if err != nil {
			return fmt.Errorf("local visibility snapshot point %d: %w", i, err)
		}
		if viewport != snapshot.Viewports[i] {
			return fmt.Errorf("local visibility snapshot viewport %d = %q, want %q", i, snapshot.Viewports[i], viewport)
		}
	}
	if snapshot.Zoom != MapsZoom {
		return fmt.Errorf("local visibility snapshot zoom = %d, want %d", snapshot.Zoom, MapsZoom)
	}
	if snapshot.ViewportToleranceM != serper.MapsViewportToleranceM {
		return fmt.Errorf("local visibility snapshot viewport tolerance = %g, want %g metres", snapshot.ViewportToleranceM, serper.MapsViewportToleranceM)
	}
	if snapshot.Provider != "serper" {
		return fmt.Errorf("local visibility snapshot provider = %q, want %q", snapshot.Provider, "serper")
	}
	if snapshot.Endpoint == "" {
		return errors.New("local visibility snapshot endpoint is empty")
	}
	if snapshot.Language != "en" {
		return fmt.Errorf("local visibility snapshot language = %q, want %q", snapshot.Language, "en")
	}
	if snapshot.GridGeometryVersion != 1 {
		return fmt.Errorf("local visibility snapshot grid geometry version = %d, want 1", snapshot.GridGeometryVersion)
	}
	if snapshot.ComparisonVersion != 1 {
		return fmt.Errorf("local visibility snapshot comparison version = %d, want 1", snapshot.ComparisonVersion)
	}
	if snapshot.RequestedResultLimit != 20 {
		return fmt.Errorf("local visibility snapshot requested result limit = %d, want 20", snapshot.RequestedResultLimit)
	}
	expected, err := ExpectedRunCredits(len(snapshot.Queries), len(snapshot.Points))
	if err != nil {
		return err
	}
	if int32(expected) != expectedCredits {
		return fmt.Errorf("local visibility snapshot cost = %d, run expected %d", expected, expectedCredits)
	}
	return nil
}

func recordLocalCellSettlement(ctx context.Context, store LocalVisibilityStore, outcome sqlc.InsertLocalVisibilityResultParams) error {
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), localRunFinalizeTimeout)
	defer cancel()
	return store.RecordCellOutcome(settleCtx, outcome)
}

func localCellOutcome(runID pgtype.UUID, queryIndex, pointIndex int, targetPlaceID string, response serper.MapsResponse, providerErr error) sqlc.InsertLocalVisibilityResultParams {
	raw, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		raw = nil
	}
	outcome := sqlc.InsertLocalVisibilityResultParams{
		RunID:       runID,
		QueryIndex:  int16(queryIndex),
		PointIndex:  int16(pointIndex),
		RawResponse: raw,
	}
	if response.Credits > 0 {
		outcome.Credits = int32(response.Credits)
		outcome.CreditKnown = true
	}
	if providerErr != nil {
		// Echo mismatch included: the decoded response (and its credits) is
		// preserved above, but the ranks belong to another viewport.
		outcome.CallStatus = "request_failed"
		outcome.MatchStatus = "unknown"
		outcome.Error = pgtype.Text{String: providerErr.Error(), Valid: true}
		return outcome
	}
	if len(response.Places) == 0 {
		outcome.CallStatus = "success_empty"
	} else {
		outcome.CallStatus = "success_nonempty"
	}
	if rank, found := resolveLocalPlaceRank(response.Places, targetPlaceID); found {
		outcome.MatchStatus = "found"
		outcome.Rank = pgtype.Int4{Int32: int32(rank), Valid: true}
	} else {
		outcome.MatchStatus = "absent"
	}
	return outcome
}

func resolveLocalPlaceRank(places []serper.Place, targetPlaceID string) (int, bool) {
	if targetPlaceID == "" {
		return 0, false
	}
	best := 0
	for _, place := range places {
		if place.PlaceID != targetPlaceID || place.Position <= 0 {
			continue
		}
		if best == 0 || place.Position < best {
			best = place.Position
		}
	}
	return best, best > 0
}
