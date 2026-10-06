package localvisibility

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/serper"
)

var ErrMapsBudgetUnavailable = errors.New("Maps spending allowance is not provisioned or insufficient")
var ErrLocationListingUnbound = errors.New("bind a Google Maps listing before starting a ranking run")
var ErrMapQueriesInvalid = errors.New("ranking runs require between one and five enabled map queries")
var ErrExpectedCreditsMismatch = errors.New("expected credits do not match the current quote")

const LocalVisibilityJobType = "local_visibility"

type LocalVisibilityStore struct {
	Pool         *pgxpool.Pool
	MapsEndpoint string
}

type LocalRunSnapshot struct {
	Queries              []string    `json:"queries"`
	TargetPlaceID        string      `json:"target_place_id"`
	Points               []GridPoint `json:"points"`
	Viewports            []string    `json:"viewports"`
	Provider             string      `json:"provider"`
	Endpoint             string      `json:"endpoint"`
	Zoom                 int         `json:"zoom"`
	Language             string      `json:"language"`
	GridGeometryVersion  int         `json:"grid_geometry_version"`
	ComparisonVersion    int         `json:"comparison_version"`
	RequestedResultLimit int         `json:"requested_result_limit"`
	ViewportToleranceM   float64     `json:"viewport_tolerance_m"`
}

func ValidateMapQueries(queries []string) ([]string, error) {
	if len(queries) < 1 || len(queries) > MapQueryCount {
		return nil, fmt.Errorf("local visibility requires between 1 and %d map queries, got %d", MapQueryCount, len(queries))
	}
	return ValidateEditableMapQueries(queries)
}

// EnqueueRun locks the parent location first, quotes the run from the enabled
// map draft under that lock, and compares the quote against expectedCredits
// before reserving anything: a stale quote answers ErrExpectedCreditsMismatch
// with no spend and no retry.
func (s LocalVisibilityStore) EnqueueRun(ctx context.Context, userID, projectID, locationID pgtype.UUID, radiusM, expectedCredits int) (sqlc.LocalVisibilityRun, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return sqlc.LocalVisibilityRun{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := sqlc.New(tx)
	locked, err := q.LockProjectLocationForQueryDraftForUser(ctx, sqlc.LockProjectLocationForQueryDraftForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID})
	if err != nil {
		return sqlc.LocalVisibilityRun{}, err
	}
	if !locked.PlaceID.Valid || strings.TrimSpace(locked.PlaceID.String) == "" {
		return sqlc.LocalVisibilityRun{}, ErrLocationListingUnbound
	}
	enabled, err := q.ListEnabledMapQueriesForUser(ctx, sqlc.ListEnabledMapQueriesForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID})
	if err != nil {
		return sqlc.LocalVisibilityRun{}, err
	}
	texts := make([]string, 0, len(enabled))
	for _, row := range enabled {
		texts = append(texts, row.Text)
	}
	queries, err := ValidateMapQueries(texts)
	if err != nil {
		return sqlc.LocalVisibilityRun{}, fmt.Errorf("%w: %v", ErrMapQueriesInvalid, err)
	}
	points, err := BuildGeoGrid(locked.Latitude, locked.Longitude, radiusM)
	if err != nil {
		return sqlc.LocalVisibilityRun{}, err
	}
	expected, err := ExpectedRunCredits(len(queries), len(points))
	if err != nil {
		return sqlc.LocalVisibilityRun{}, err
	}
	if expected != expectedCredits {
		return sqlc.LocalVisibilityRun{}, fmt.Errorf("%w: quoted %d credits but the run costs %d", ErrExpectedCreditsMismatch, expectedCredits, expected)
	}
	viewports := make([]string, len(points))
	for i, point := range points {
		viewports[i], err = serper.FormatMapsViewport(point.Latitude, point.Longitude)
		if err != nil {
			return sqlc.LocalVisibilityRun{}, err
		}
	}
	if _, err = q.ReservePlatformMapsCredits(ctx, int64(expected)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.LocalVisibilityRun{}, ErrMapsBudgetUnavailable
		}
		return sqlc.LocalVisibilityRun{}, err
	}
	if _, err = q.ReserveOrganizationMapsCredits(ctx, sqlc.ReserveOrganizationMapsCreditsParams{Credits: int64(expected), OrganizationID: locked.OrganizationID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.LocalVisibilityRun{}, ErrMapsBudgetUnavailable
		}
		return sqlc.LocalVisibilityRun{}, err
	}
	snapshot, err := json.Marshal(LocalRunSnapshot{Queries: queries, TargetPlaceID: locked.PlaceID.String, Points: points, Viewports: viewports, Provider: "serper", Endpoint: s.MapsEndpoint, Zoom: MapsZoom, Language: "en", GridGeometryVersion: 1, ComparisonVersion: 1, RequestedResultLimit: 20, ViewportToleranceM: serper.MapsViewportToleranceM})
	if err != nil {
		return sqlc.LocalVisibilityRun{}, err
	}
	run, err := q.CreateLocalVisibilityRun(ctx, sqlc.CreateLocalVisibilityRunParams{LocationID: locationID, RadiusM: int32(radiusM), Snapshot: snapshot, ExpectedCredits: int32(expected)})
	if err != nil {
		return sqlc.LocalVisibilityRun{}, err
	}
	if err = q.CreateLocalRunCells(ctx, sqlc.CreateLocalRunCellsParams{RunID: run.ID, QueryCount: int32(len(queries)), PointCount: int32(len(points))}); err != nil {
		return sqlc.LocalVisibilityRun{}, err
	}
	if _, err = q.EnqueueLocalVisibilityJob(ctx, sqlc.EnqueueLocalVisibilityJobParams{ProjectID: projectID, LocalRunID: run.ID}); err != nil {
		return sqlc.LocalVisibilityRun{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return sqlc.LocalVisibilityRun{}, err
	}
	return run, nil
}

// Unknown provider charges keep their reservation until an administrator reconciles evidence.
func (s LocalVisibilityStore) RecordCellOutcome(ctx context.Context, outcome sqlc.InsertLocalVisibilityResultParams) error {
	if outcome.Credits < 0 {
		return errors.New("local visibility negative provider credits")
	}
	q := sqlc.New(s.Pool)
	run, err := q.GetLocalVisibilityRun(ctx, outcome.RunID)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tq := q.WithTx(tx)
	released := int32(0)
	if outcome.CreditKnown {
		released = MapsCreditsPerCall
	}
	if err = tq.SettlePlatformMapsCredits(ctx, sqlc.SettlePlatformMapsCreditsParams{Released: int64(released), Spent: int64(outcome.Credits)}); err != nil {
		return err
	}
	if err = tq.SettleOrganizationMapsCredits(ctx, sqlc.SettleOrganizationMapsCreditsParams{Released: int64(released), Spent: int64(outcome.Credits), OrganizationID: run.OrganizationID}); err != nil {
		return err
	}
	locked, err := tq.LockLocalVisibilityRun(ctx, run.ID)
	if err != nil {
		return err
	}
	if locked.Status != "running" {
		return errors.New("local visibility outcome requires a running run")
	}
	if err = tq.InsertLocalVisibilityResult(ctx, outcome); err != nil {
		return err
	}
	if err = tq.SettleLocalRunCredits(ctx, sqlc.SettleLocalRunCreditsParams{ID: run.ID, Released: released, Spent: outcome.Credits}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type runOutcomeSummary struct {
	status         string
	total          int
	successful     int
	requestFailed  int
	unstarted      int
	startedPending int
	ambiguous      int
	held           int32
}

func summarizeRunOutcome(cells []sqlc.GetLocalRunCellsRow, reservedCredits int32) runOutcomeSummary {
	s := runOutcomeSummary{total: len(cells)}
	for _, cell := range cells {
		if cell.CallStatus == "success_empty" || cell.CallStatus == "success_nonempty" {
			s.successful++
		}
		if cell.CallStatus == "request_failed" {
			s.requestFailed++
		}
		if !cell.StartedAt.Valid {
			s.unstarted++
		} else if cell.CallStatus == "pending" {
			s.startedPending++
		}
		if cell.StartedAt.Valid && !cell.CreditKnown {
			s.ambiguous++
		}
	}
	s.status = "partial"
	if s.successful == len(cells) && reservedCredits == 0 {
		s.status = "completed"
	} else if s.successful == 0 {
		s.status = "failed"
	}
	s.held = reservedCredits - int32(s.unstarted*MapsCreditsPerCall)
	return s
}

func (s runOutcomeSummary) message() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d of %d calls succeeded, %d failed, %d were not attempted, %d started but left no outcome",
		s.successful, s.total, s.requestFailed, s.unstarted, s.startedPending)
	if s.held > 0 {
		fmt.Fprintf(&b, "; %d credits remain held pending reconciliation", s.held)
	} else {
		b.WriteString("; no credits remain held")
	}
	if s.ambiguous > 0 {
		fmt.Fprintf(&b, "; calls with unconfirmed charges: %d. Their cost is not yet confirmed", s.ambiguous)
	}
	return b.String()
}

func (s LocalVisibilityStore) FinishRun(ctx context.Context, runID pgtype.UUID) error {
	q := sqlc.New(s.Pool)
	run, err := q.GetLocalVisibilityRun(ctx, runID)
	if err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tq := q.WithTx(tx)
	if err = tq.SettlePlatformMapsCredits(ctx, sqlc.SettlePlatformMapsCreditsParams{}); err != nil {
		return err
	}
	if err = tq.SettleOrganizationMapsCredits(ctx, sqlc.SettleOrganizationMapsCreditsParams{OrganizationID: run.OrganizationID}); err != nil {
		return err
	}
	locked, err := tq.LockLocalVisibilityRun(ctx, runID)
	if err != nil {
		return err
	}
	if locked.Status != "running" && locked.Status != "queued" {
		return nil
	}
	cells, err := tq.GetLocalRunCells(ctx, runID)
	if err != nil {
		return err
	}
	summary := summarizeRunOutcome(cells, locked.ReservedCredits)
	message := pgtype.Text{}
	if summary.status != "completed" {
		message = pgtype.Text{String: summary.message(), Valid: true}
	}
	released := int32(summary.unstarted * MapsCreditsPerCall)
	if err = tq.SettlePlatformMapsCredits(ctx, sqlc.SettlePlatformMapsCreditsParams{Released: int64(released)}); err != nil {
		return err
	}
	if err = tq.SettleOrganizationMapsCredits(ctx, sqlc.SettleOrganizationMapsCreditsParams{OrganizationID: run.OrganizationID, Released: int64(released)}); err != nil {
		return err
	}
	if err = tq.SettleLocalRunCredits(ctx, sqlc.SettleLocalRunCreditsParams{ID: runID, Released: released}); err != nil {
		return err
	}
	if err = tq.FinishLocalVisibilityRun(ctx, sqlc.FinishLocalVisibilityRunParams{ID: runID, Status: summary.status, Error: message}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
