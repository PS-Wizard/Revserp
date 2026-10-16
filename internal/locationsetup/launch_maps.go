package locationsetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
	"github.com/ps-wizard/revserp/internal/serper"
)

// LaunchMapsVisibility quotes the enabled starter queries and starts the
// Maps test. The reservation never exceeds the disclosed estimate; budget
// exhaustion, an unbound listing, or missing queries skips the step.
func LaunchMapsVisibility(ctx context.Context, pool *pgxpool.Pool, projectID, locationID pgtype.UUID, mapsEndpoint string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	setup, err := getSetupTx(ctx, tx, projectID, locationID)
	if err != nil {
		return err
	}
	if err := launchMapsVisibilityTx(ctx, tx, setup, mapsEndpoint); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func launchMapsVisibilityTx(ctx context.Context, tx pgx.Tx, setup Setup, mapsEndpoint string) error {
	if setup.Steps[StepMapsVisibility] != StepPending || setup.Steps[StepMapQueries] != StepCompleted {
		return nil
	}
	if setup.MapsRunID.Valid {
		var (
			runStatus   string
			runExpected int32
		)
		if err := tx.QueryRow(ctx, `SELECT status, expected_credits FROM local_visibility_runs WHERE id = $1`,
			setup.MapsRunID).Scan(&runStatus, &runExpected); err == nil {
			switch runStatus {
			case "completed", "partial":
				if _, err := tx.Exec(ctx, `UPDATE location_setup
					SET maps_visibility_status = 'completed', estimated_maps_credits = $3, updated_at = now()
					WHERE project_id = $1 AND location_id = $2`,
					setup.ProjectID, setup.LocationID, runExpected); err != nil {
					return err
				}
				setup.Steps[StepMapsVisibility] = StepCompleted
				return refreshOverall(ctx, tx, setup)
			case "queued", "running":
				if err := ensureLocalRunJob(ctx, tx, setup, setup.MapsRunID); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `UPDATE location_setup
					SET maps_visibility_status = 'running', estimated_maps_credits = $3, updated_at = now()
					WHERE project_id = $1 AND location_id = $2`,
					setup.ProjectID, setup.LocationID, runExpected)
				return err
			}
		}
	}
	var foreignActive bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM local_visibility_runs
		WHERE location_id = $1 AND status IN ('queued', 'running'))`, setup.LocationID).Scan(&foreignActive); err != nil {
		return err
	}
	if foreignActive {
		return deferStep(ctx, tx, setup, StepMapsVisibility, "a maps run is already in progress for this location")
	}
	var (
		placeID   pgtype.Text
		radiusM   int32
		latitude  float64
		longitude float64
	)
	if err := tx.QueryRow(ctx, `SELECT place_id, radius_m, latitude, longitude FROM project_locations
		WHERE id = $1 AND project_id = $2 FOR NO KEY UPDATE`, setup.LocationID, setup.ProjectID).Scan(
		&placeID, &radiusM, &latitude, &longitude); err != nil {
		return err
	}
	if !placeID.Valid || placeID.String == "" {
		return skipStep(ctx, tx, setup, StepMapsVisibility, SkipMapsUnbound)
	}
	if radiusM < 1000 || radiusM > 25000 {
		radiusM = DefaultMapsRadiusM
	}
	rows, err := tx.Query(ctx, `SELECT text FROM project_location_queries
		WHERE location_id = $1 AND kind = 'map' AND enabled = TRUE ORDER BY ordinal, id`, setup.LocationID)
	if err != nil {
		return err
	}
	var texts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			rows.Close()
			return err
		}
		texts = append(texts, text)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	queries, err := localvisibility.ValidateMapQueries(texts)
	if err != nil {
		return skipStep(ctx, tx, setup, StepMapsVisibility, SkipMapsNoQueries)
	}
	expected, err := localvisibility.ExpectedRunCredits(len(queries), localvisibility.GridPointCount)
	if err != nil {
		return err
	}
	disclosed := int32(MaxDisclosedMapsCredits)
	if setup.EstimatedMapsCredits != nil {
		disclosed = *setup.EstimatedMapsCredits
	}
	if int32(expected) > disclosed {
		return skipStep(ctx, tx, setup, StepMapsVisibility,
			fmt.Sprintf("maps cost %d exceeds disclosed estimate %d", expected, disclosed))
	}
	points, err := localvisibility.BuildGeoGrid(latitude, longitude, int(radiusM))
	if err != nil {
		return err
	}
	viewports := make([]string, len(points))
	for i, point := range points {
		viewports[i], err = serper.FormatMapsViewport(point.Latitude, point.Longitude)
		if err != nil {
			return err
		}
	}
	q := sqlc.New(tx)
	if _, err := q.ReservePlatformMapsCredits(ctx, int64(expected)); errors.Is(err, pgx.ErrNoRows) {
		return skipStep(ctx, tx, setup, StepMapsVisibility, SkipMapsBudget)
	} else if err != nil {
		return err
	}
	if _, err := q.ReserveOrganizationMapsCredits(ctx, sqlc.ReserveOrganizationMapsCreditsParams{
		Credits:        int64(expected),
		OrganizationID: setup.OrganizationID,
	}); errors.Is(err, pgx.ErrNoRows) {
		if serr := q.SettlePlatformMapsCredits(ctx, sqlc.SettlePlatformMapsCreditsParams{
			Released: int64(expected),
		}); serr != nil {
			return serr
		}
		return skipStep(ctx, tx, setup, StepMapsVisibility, SkipMapsBudget)
	} else if err != nil {
		return err
	}
	snapshot, err := json.Marshal(localvisibility.LocalRunSnapshot{
		Queries: queries, TargetPlaceID: placeID.String, Points: points, Viewports: viewports,
		Provider: "serper", Endpoint: mapsEndpoint, Zoom: localvisibility.MapsZoom, Language: "en",
		GridGeometryVersion: 1, ComparisonVersion: 1, RequestedResultLimit: 20,
		ViewportToleranceM: serper.MapsViewportToleranceM,
	})
	if err != nil {
		return err
	}
	run, err := q.CreateLocalVisibilityRun(ctx, sqlc.CreateLocalVisibilityRunParams{
		LocationID:      setup.LocationID,
		RadiusM:         radiusM,
		Snapshot:        snapshot,
		ExpectedCredits: int32(expected),
	})
	if err != nil {
		return err
	}
	if err := q.CreateLocalRunCells(ctx, sqlc.CreateLocalRunCellsParams{
		RunID:      run.ID,
		QueryCount: int32(len(queries)),
		PointCount: int32(len(points)),
	}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, local_run_id, status)
		VALUES ('local_visibility', $1, $2, $3, 'pending')`, setup.ProjectID, setup.LocationID, run.ID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE location_setup
		SET maps_run_id = $3, estimated_maps_credits = $4, maps_visibility_status = 'running', updated_at = now()
		WHERE project_id = $1 AND location_id = $2`,
		setup.ProjectID, setup.LocationID, run.ID, int32(expected))
	return err
}

func ensureLocalRunJob(ctx context.Context, tx pgx.Tx, setup Setup, runID pgtype.UUID) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ai_worker_jobs
		WHERE local_run_id = $1 AND status IN ('pending', 'running'))`, runID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, local_run_id, status)
		VALUES ('local_visibility', $1, $2, $3, 'pending')`, setup.ProjectID, setup.LocationID, runID)
	return err
}

// FinalizeMapQueriesSuccess completes the map-query step and launches the
// Maps test in the same transaction, so no crash gap can strand it.
func FinalizeMapQueriesSuccess(ctx context.Context, pool *pgxpool.Pool, projectID, locationID, jobID pgtype.UUID, mapsEndpoint string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := markJobCompleted(ctx, tx, jobID); err != nil {
		return err
	}
	setup, err := getSetupTx(ctx, tx, projectID, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if setup.Steps[StepMapQueries] != StepRunning {
		return tx.Commit(ctx)
	}
	if err := completeStep(ctx, tx, setup, StepMapQueries); err != nil {
		return err
	}
	if err := launchMapsVisibilityTx(ctx, tx, setup, mapsEndpoint); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FinalizeMapQueriesFailure fails the map-query step. Other branches still
// complete on their own; the POST resume relaunches only unfinished work.
func FinalizeMapQueriesFailure(ctx context.Context, pool *pgxpool.Pool, projectID, locationID, jobID pgtype.UUID, message string) error {
	return failOwnedJob(ctx, pool, projectID, locationID, jobID, StepMapQueries, message)
}

// FinalizeLocalRun records the Maps test outcome. Only the run the setup
// launched advances the step; manual runs only settle the job.
func FinalizeLocalRun(ctx context.Context, pool *pgxpool.Pool, jobID, runID pgtype.UUID, jobErr error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if jobErr != nil {
		if err := markJobFailed(ctx, tx, jobID, jobErr.Error()); err != nil {
			return err
		}
	} else if err := markJobCompleted(ctx, tx, jobID); err != nil {
		return err
	}
	var (
		locationID pgtype.UUID
		projectID  pgtype.UUID
	)
	if err := tx.QueryRow(ctx, `SELECT l.id, l.project_id FROM local_visibility_runs r
		JOIN project_locations l ON l.id = r.location_id WHERE r.id = $1`, runID).Scan(&locationID, &projectID); err != nil {
		return tx.Commit(ctx)
	}
	setup, err := getSetupTx(ctx, tx, projectID, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if setup.Steps[StepMapsVisibility] != StepRunning || !setup.MapsRunID.Valid || setup.MapsRunID != runID {
		return tx.Commit(ctx)
	}
	if jobErr != nil {
		if err := failStep(ctx, tx, setup, StepMapsVisibility, jobErr.Error()); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err := completeStep(ctx, tx, setup, StepMapsVisibility); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RegenerateMapsQueries enqueues one map generation job. An active job makes
// it a no-op; history is never rewritten.
func RegenerateMapsQueries(ctx context.Context, pool *pgxpool.Pool, projectID, locationID pgtype.UUID) error {
	_, err := pool.Exec(ctx, `INSERT INTO ai_worker_jobs (job_type, project_id, location_id, status)
		VALUES ($1, $2, $3, 'pending') ON CONFLICT DO NOTHING`,
		JobMapQueryGeneration, projectID, locationID)
	return err
}

// GetMapsQueriesStatus derives idle/pending/running/completed/failed from the
// latest map generation job; queries live in project_location_queries.
func GetMapsQueriesStatus(ctx context.Context, db DB, projectID, locationID pgtype.UUID) (MapsQueriesJobStatus, error) {
	var (
		status string
		errMsg pgtype.Text
	)
	err := db.QueryRow(ctx, `SELECT status, error_message FROM ai_worker_jobs
		WHERE project_id = $1 AND location_id = $2 AND job_type = $3
		ORDER BY created_at DESC, id DESC LIMIT 1`,
		projectID, locationID, JobMapQueryGeneration).Scan(&status, &errMsg)
	if errors.Is(err, pgx.ErrNoRows) {
		return MapsQueriesJobStatus{Status: "idle"}, nil
	}
	if err != nil {
		return MapsQueriesJobStatus{}, err
	}
	out := MapsQueriesJobStatus{Status: status}
	if errMsg.Valid {
		out.Error = errMsg.String
	}
	return out, nil
}
