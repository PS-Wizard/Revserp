package locationsetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Get returns the setup row, or pgx.ErrNoRows when the location never started.
func Get(ctx context.Context, db DB, projectID, locationID pgtype.UUID) (Setup, error) {
	return scanSetupRow(db.QueryRow(ctx, `SELECT id, organization_id, project_id, location_id, requested_by_user_id,
		COALESCE(differences, ''), status, COALESCE(error, ''),
		prepare_status, profile_status, questions_status, map_queries_status,
		ai_visibility_status, maps_visibility_status, COALESCE(step_reasons, '{}'::jsonb),
		ai_audit_id, maps_run_id, estimated_maps_credits
		FROM location_setup WHERE project_id = $1 AND location_id = $2 LIMIT 1`, projectID, locationID))
}

func getSetupTx(ctx context.Context, tx pgx.Tx, projectID, locationID pgtype.UUID) (Setup, error) {
	return scanSetupRow(tx.QueryRow(ctx, `SELECT id, organization_id, project_id, location_id, requested_by_user_id,
		COALESCE(differences, ''), status, COALESCE(error, ''),
		prepare_status, profile_status, questions_status, map_queries_status,
		ai_visibility_status, maps_visibility_status, COALESCE(step_reasons, '{}'::jsonb),
		ai_audit_id, maps_run_id, estimated_maps_credits
		FROM location_setup WHERE project_id = $1 AND location_id = $2 LIMIT 1 FOR UPDATE`, projectID, locationID))
}

func scanSetupRow(row pgx.Row) (Setup, error) {
	var (
		s                                      Setup
		prep, prof, ques, maps, aiVis, mapsVis string
		reasonsRaw                             []byte
		estimated                              pgtype.Int4
	)
	s.Steps = map[string]string{}
	s.Reasons = map[string]string{}
	err := row.Scan(&s.ID, &s.OrganizationID, &s.ProjectID, &s.LocationID,
		&s.RequestedByUserID, &s.Differences, &s.Status, &s.Error,
		&prep, &prof, &ques, &maps, &aiVis, &mapsVis, &reasonsRaw,
		&s.AIAuditID, &s.MapsRunID, &estimated)
	if err != nil {
		return Setup{}, err
	}
	s.Steps[StepPrepare] = prep
	s.Steps[StepProfile] = prof
	s.Steps[StepQuestions] = ques
	s.Steps[StepMapQueries] = maps
	s.Steps[StepAIVisibility] = aiVis
	s.Steps[StepMapsVisibility] = mapsVis
	if len(reasonsRaw) > 0 {
		if err := json.Unmarshal(reasonsRaw, &s.Reasons); err != nil {
			return Setup{}, fmt.Errorf("decode location setup reasons: %w", err)
		}
	}
	if s.Reasons == nil {
		s.Reasons = map[string]string{}
	}
	if estimated.Valid {
		v := estimated.Int32
		s.EstimatedMapsCredits = &v
	}
	return s, nil
}

// CountEnabledMapQueries counts the saved enabled starter queries feeding the quote.
func CountEnabledMapQueries(ctx context.Context, db DB, locationID pgtype.UUID) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT COUNT(*) FROM project_location_queries
		WHERE location_id = $1 AND kind = 'map' AND enabled = TRUE`, locationID).Scan(&n)
	return n, err
}

// GetDifferences exposes the immutable setup input to the AI profile worker.
// Empty means the manager gave none.
func GetDifferences(ctx context.Context, db DB, projectID, locationID pgtype.UUID) (string, error) {
	var differences string
	err := db.QueryRow(ctx, `SELECT COALESCE(differences, '') FROM location_setup
		WHERE project_id = $1 AND location_id = $2`, projectID, locationID).Scan(&differences)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return differences, err
}

// StartOrResume creates the setup row on first start and resumes a failed one.
// Active or completed setups return unchanged with no writes: input is fixed
// at first creation and the requester is never restamped mid-job.
func StartOrResume(ctx context.Context, tx pgx.Tx, projectID, locationID, userID pgtype.UUID, differences string) (Setup, bool, error) {
	var orgID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT p.organization_id FROM project_locations l
		JOIN projects p ON p.id = l.project_id
		JOIN organization_members m ON m.org_id = p.organization_id
		WHERE l.id = $1 AND p.id = $2 AND m.user_id = $3`, locationID, projectID, userID).Scan(&orgID); err != nil {
		return Setup{}, false, err
	}
	setup, err := getSetupTx(ctx, tx, projectID, locationID)
	if errors.Is(err, pgx.ErrNoRows) {
		setup, err = insertSetup(ctx, tx, orgID, projectID, locationID, userID, differences)
		if err != nil {
			return Setup{}, false, err
		}
		return setup, true, nil
	}
	if err != nil {
		return Setup{}, false, err
	}
	if setup.Status != StatusFailed {
		return setup, false, nil
	}
	setup, err = resetUnfinishedSteps(ctx, tx, setup, userID)
	if err != nil {
		return Setup{}, false, err
	}
	return setup, false, nil
}

func insertSetup(ctx context.Context, tx pgx.Tx, orgID, projectID, locationID, userID pgtype.UUID, differences string) (Setup, error) {
	enabled, err := CountEnabledMapQueries(ctx, tx, locationID)
	if err != nil {
		return Setup{}, err
	}
	disclosed := DiscloseMapsCredits(enabled)
	_, err = tx.Exec(ctx, `INSERT INTO location_setup
		(organization_id, project_id, location_id, requested_by_user_id, differences,
		 status, prepare_status, estimated_maps_credits)
		VALUES ($1, $2, $3, $4, $5, 'running', 'completed', $6)
		ON CONFLICT (location_id) DO NOTHING`,
		orgID, projectID, locationID, userID, differences, disclosed)
	if err != nil {
		return Setup{}, err
	}
	return getSetupTx(ctx, tx, projectID, locationID)
}

func resetUnfinishedSteps(ctx context.Context, tx pgx.Tx, setup Setup, userID pgtype.UUID) (Setup, error) {
	next := map[string]string{}
	for _, id := range OrderedSteps {
		if s := setup.Steps[id]; s == StepCompleted || s == StepSkipped || s == StepRunning {
			next[id] = s
		} else {
			next[id] = StepPending
		}
	}
	kept := map[string]string{}
	for id, reason := range setup.Reasons {
		if next[id] != StepPending {
			kept[id] = reason
		}
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return Setup{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE location_setup
		SET status = 'running', error = NULL, requested_by_user_id = $3,
		    prepare_status = $4, profile_status = $5, questions_status = $6,
		    map_queries_status = $7, ai_visibility_status = $8, maps_visibility_status = $9,
		    step_reasons = $10, completed_at = NULL, updated_at = now()
		WHERE project_id = $1 AND location_id = $2`,
		setup.ProjectID, setup.LocationID, userID,
		next[StepPrepare], next[StepProfile], next[StepQuestions],
		next[StepMapQueries], next[StepAIVisibility], next[StepMapsVisibility], encoded)
	if err != nil {
		return Setup{}, err
	}
	if !setup.MapsRunID.Valid {
		enabled, err := CountEnabledMapQueries(ctx, tx, setup.LocationID)
		if err != nil {
			return Setup{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE location_setup SET estimated_maps_credits = $3, updated_at = now()
			WHERE project_id = $1 AND location_id = $2`,
			setup.ProjectID, setup.LocationID, DiscloseMapsCredits(enabled)); err != nil {
			return Setup{}, err
		}
	}
	return getSetupTx(ctx, tx, setup.ProjectID, setup.LocationID)
}

func completeStep(ctx context.Context, tx pgx.Tx, setup Setup, step string) error {
	if _, err := tx.Exec(ctx, `UPDATE location_setup SET `+step+`_status = 'completed', updated_at = now()
		WHERE project_id = $1 AND location_id = $2`, setup.ProjectID, setup.LocationID); err != nil {
		return err
	}
	setup.Steps[step] = StepCompleted
	return refreshOverall(ctx, tx, setup)
}

func skipStep(ctx context.Context, tx pgx.Tx, setup Setup, step, reason string) error {
	reasons := map[string]string{}
	for id, r := range setup.Reasons {
		reasons[id] = r
	}
	reasons[step] = reason
	encoded, err := json.Marshal(reasons)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE location_setup SET `+step+`_status = 'skipped',
		step_reasons = $3, updated_at = now() WHERE project_id = $1 AND location_id = $2`,
		setup.ProjectID, setup.LocationID, encoded); err != nil {
		return err
	}
	setup.Steps[step] = StepSkipped
	return refreshOverall(ctx, tx, setup)
}

func failStep(ctx context.Context, tx pgx.Tx, setup Setup, step, message string) error {
	reasons := map[string]string{}
	for id, r := range setup.Reasons {
		reasons[id] = r
	}
	reasons[step] = cutError(message)
	encoded, err := json.Marshal(reasons)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE location_setup SET status = 'failed', error = $3,
		`+step+`_status = 'failed', step_reasons = $4, completed_at = now(), updated_at = now()
		WHERE project_id = $1 AND location_id = $2`,
		setup.ProjectID, setup.LocationID, cutError(message), encoded)
	return err
}

func deferStep(ctx context.Context, tx pgx.Tx, setup Setup, step, reason string) error {
	reasons := map[string]string{}
	for id, r := range setup.Reasons {
		reasons[id] = r
	}
	reasons[step] = reason
	encoded, err := json.Marshal(reasons)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE location_setup SET step_reasons = $3, updated_at = now()
		WHERE project_id = $1 AND location_id = $2`,
		setup.ProjectID, setup.LocationID, encoded)
	return err
}

func refreshOverall(ctx context.Context, tx pgx.Tx, setup Setup) error {
	current, err := getSetupTx(ctx, tx, setup.ProjectID, setup.LocationID)
	if err != nil {
		return err
	}
	overall := OverallStatus(current.Steps)
	completed := "NULL"
	if overall != StatusRunning {
		completed = "now()"
	}
	_, err = tx.Exec(ctx, `UPDATE location_setup SET status = $3, completed_at = `+completed+`,
		updated_at = now() WHERE project_id = $1 AND location_id = $2`,
		setup.ProjectID, setup.LocationID, overall)
	return err
}

func cutError(message string) string {
	if len(message) > 500 {
		cut := 500
		for cut > 0 && message[cut]&0xC0 == 0x80 {
			cut--
		}
		return message[:cut]
	}
	return message
}

func markJobCompleted(ctx context.Context, tx pgx.Tx, jobID pgtype.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE ai_worker_jobs SET status = 'completed',
		completed_at = NOW(), updated_at = NOW() WHERE id = $1`, jobID)
	return err
}

func markJobFailed(ctx context.Context, tx pgx.Tx, jobID pgtype.UUID, message string) error {
	_, err := tx.Exec(ctx, `UPDATE ai_worker_jobs SET status = 'failed', error_message = $2,
		completed_at = NOW(), updated_at = NOW() WHERE id = $1`, jobID, cutError(message))
	return err
}
