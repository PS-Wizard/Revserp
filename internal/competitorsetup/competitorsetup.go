// Package competitorsetup owns the durable competitor initialization workflow:
// crawl -> keyword_generation -> keyword_analysis -> completed. A missing row
// reads as ready; the POST creates it in running. The crawl step reconciles
// lazily from the child crawl row, so the crawler never learns about this
// workflow. It is a thin wrapper over conditional queries so transitions can
// be exercised without a database.
package competitorsetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	// StatusReady is the synthesized pre-start status a competitor without a
	// setup row reads as. No worker runs while it is ready.
	StatusReady = "ready"
	// StatusRunning is the stored status while any step is pending or running.
	StatusRunning = "running"
	// StatusCompleted is the terminal success status.
	StatusCompleted = "completed"
	// StatusFailed is the status after a step fails.
	StatusFailed = "failed"

	// StepCrawl waits for the competitor child crawl of the parent crawl.
	StepCrawl = "crawl"
	// StepKeywordGeneration generates brand and non-brand keywords from the
	// competitor crawl.
	StepKeywordGeneration = "keyword_generation"
	// StepKeywordAnalysis estimates rank, traffic, volume, difficulty, CPC and
	// result counts for those keywords.
	StepKeywordAnalysis = "keyword_analysis"

	// StepPending is the initial step status.
	StepPending = "pending"
	// StepRunning is the active step status.
	StepRunning = "running"
	// StepCompleted is the terminal step success status.
	StepCompleted = "completed"
	// StepFailed is the terminal step failure status.
	StepFailed = "failed"

	// CompetitorKeywordGenerationJobType must match the ai_worker_jobs check
	// constraint. The worker claims it to generate competitor keywords.
	CompetitorKeywordGenerationJobType = "competitor_keyword_generation"
	// CompetitorKeywordAnalysisJobType must match the ai_worker_jobs check
	// constraint. The worker claims it to estimate competitor keyword metrics.
	CompetitorKeywordAnalysisJobType = "competitor_keyword_analysis"
)

var (
	// ErrSetupNotFound means no setup row exists for the competitor. Callers
	// render it as ready; StartOrResume never sees it.
	ErrSetupNotFound = errors.New("competitor setup not found")
	// ErrInvalidFailedStep means a failed setup has no resumable step.
	ErrInvalidFailedStep = errors.New("competitor setup failed step is missing or invalid")
)

// StepView is one initialization step in API order.
type StepView struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// View is the initialization screen payload.
type View struct {
	Status string     `json:"status"`
	Steps  []StepView `json:"steps"`
	Error  string     `json:"error,omitempty"`
}

// OrderedSteps is the fixed initialization order.
var OrderedSteps = []string{StepCrawl, StepKeywordGeneration, StepKeywordAnalysis}

// MissingView renders a competitor that never initialized as ready.
func MissingView() View {
	return View{Status: StatusReady, Steps: []StepView{
		{ID: StepCrawl, Status: StepPending},
		{ID: StepKeywordGeneration, Status: StepPending},
		{ID: StepKeywordAnalysis, Status: StepPending},
	}}
}

// RowView maps a stored setup row to the initialization screen. A failed step
// reports its stored reason, falling back to the setup error.
func RowView(setup sqlc.CompetitorSetup) View {
	reasons := decodeStepReasons(setup.StepReasons)
	steps := make([]StepView, 0, len(OrderedSteps))
	for _, id := range OrderedSteps {
		status := StepPending
		switch id {
		case StepCrawl:
			status = setup.CrawlStatus
		case StepKeywordGeneration:
			status = setup.KeywordGenerationStatus
		case StepKeywordAnalysis:
			status = setup.KeywordAnalysisStatus
		}
		step := StepView{ID: id, Status: status}
		if status == StepFailed {
			step.Reason = reasons[id]
			if step.Reason == "" && setup.Error.Valid {
				step.Reason = setup.Error.String
			}
		}
		steps = append(steps, step)
	}
	view := View{Status: setup.Status, Steps: steps}
	if setup.Error.Valid {
		view.Error = setup.Error.String
	}
	return view
}

func decodeStepReasons(raw []byte) map[string]string {
	reasons := map[string]string{}
	if len(raw) == 0 {
		return reasons
	}
	_ = json.Unmarshal(raw, &reasons)
	return reasons
}

func encodeStepReason(step, reason string) []byte {
	raw, err := json.Marshal(map[string]string{step: reason})
	if err != nil {
		return []byte("{}")
	}
	return raw
}

// Queries is the narrow database surface the state machine needs.
// *sqlc.Queries satisfies it, including a transaction-bound copy.
type Queries interface {
	GetCompetitorSetupByCompetitorForUpdate(ctx context.Context, competitorID pgtype.UUID) (sqlc.CompetitorSetup, error)
	CreateCompetitorSetup(ctx context.Context, arg sqlc.CreateCompetitorSetupParams) (sqlc.CompetitorSetup, error)
	UpdateCompetitorSetup(ctx context.Context, arg sqlc.UpdateCompetitorSetupParams) (sqlc.CompetitorSetup, error)
	GetCompetitorChildCrawlForParent(ctx context.Context, arg sqlc.GetCompetitorChildCrawlForParentParams) (sqlc.GetCompetitorChildCrawlForParentRow, error)
	EnqueueCompetitorAIWorkerJob(ctx context.Context, arg sqlc.EnqueueCompetitorAIWorkerJobParams) (sqlc.EnqueueCompetitorAIWorkerJobRow, error)
	MarkAIWorkerJobCompleted(ctx context.Context, id pgtype.UUID) error
	MarkAIWorkerJobFailed(ctx context.Context, arg sqlc.MarkAIWorkerJobFailedParams) error
}

// Outcome reports the persisted row and whether this call created it.
type Outcome struct {
	Setup   sqlc.CompetitorSetup
	Started bool
}

// StartOrResume creates the setup on first start and otherwise reconciles it:
// completed returns unchanged, failed resumes at failed_step, running advances
// from the child crawl state. createCrawl creates the one queued child crawl
// under the given parent and runs only when a (re)start needs one. An existing
// setup always reuses its stored parent: the resolved parentCrawlID only seeds
// a new row or repins a row whose parent went missing, so a retry can never
// create a child the reconciliation (which reads the stored parent) ignores.
func StartOrResume(ctx context.Context, q Queries, organizationID, projectID, competitorID, requestedByUserID, parentCrawlID pgtype.UUID, createCrawl func(ctx context.Context, parentCrawlID pgtype.UUID) (pgtype.UUID, error)) (Outcome, error) {
	setup, err := q.GetCompetitorSetupByCompetitorForUpdate(ctx, competitorID)
	if errors.Is(err, pgx.ErrNoRows) {
		created, err := q.CreateCompetitorSetup(ctx, sqlc.CreateCompetitorSetupParams{
			OrganizationID:          organizationID,
			ProjectID:               projectID,
			CompetitorID:            competitorID,
			RequestedByUserID:       requestedByUserID,
			ParentCrawlID:           parentCrawlID,
			Status:                  StatusRunning,
			CrawlStatus:             StepPending,
			KeywordGenerationStatus: StepPending,
			KeywordAnalysisStatus:   StepPending,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Lost a concurrent create race: the winner owns the row now.
			setup, err = q.GetCompetitorSetupByCompetitorForUpdate(ctx, competitorID)
		} else if err == nil {
			setup = created
		} else {
			return Outcome{}, fmt.Errorf("create competitor setup: %w", err)
		}
		if err != nil {
			return Outcome{}, fmt.Errorf("lock competitor setup: %w", err)
		}
		updated, err := reconcileRunning(ctx, q, setup, requestedByUserID, setup.ParentCrawlID, createCrawl)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Setup: updated, Started: true}, nil
	} else if err != nil {
		return Outcome{}, fmt.Errorf("lock competitor setup: %w", err)
	}

	switch setup.Status {
	case StatusCompleted:
		return Outcome{Setup: setup}, nil
	case StatusFailed:
		updated, err := resumeFailedSetup(ctx, q, setup, requestedByUserID, parentCrawlID, createCrawl)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Setup: updated}, nil
	default:
		updated, err := reconcileRunning(ctx, q, setup, requestedByUserID, parentCrawlID, createCrawl)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Setup: updated}, nil
	}
}

// Reconcile advances a running setup from the child crawl state without
// starting or retrying anything: a completed child moves crawl to completed
// and enqueues keyword generation, a failed child fails the setup at crawl.
func Reconcile(ctx context.Context, q Queries, competitorID pgtype.UUID) (sqlc.CompetitorSetup, error) {
	setup, err := q.GetCompetitorSetupByCompetitorForUpdate(ctx, competitorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.CompetitorSetup{}, ErrSetupNotFound
	}
	if err != nil {
		return sqlc.CompetitorSetup{}, fmt.Errorf("lock competitor setup: %w", err)
	}
	if setup.Status != StatusRunning {
		return setup, nil
	}
	return reconcileRunning(ctx, q, setup, pgtype.UUID{}, setup.ParentCrawlID, nil)
}

func reconcileRunning(ctx context.Context, q Queries, setup sqlc.CompetitorSetup, requestedByUserID, parentCrawlID pgtype.UUID, createCrawl func(ctx context.Context, parentCrawlID pgtype.UUID) (pgtype.UUID, error)) (sqlc.CompetitorSetup, error) {
	if setup.CrawlStatus != StepRunning {
		if setup.CrawlStatus == StepPending && createCrawl != nil {
			return startCrawlStep(ctx, q, setup, requestedByUserID, parentCrawlID, createCrawl)
		}
		return setup, nil
	}
	child, err := q.GetCompetitorChildCrawlForParent(ctx, sqlc.GetCompetitorChildCrawlForParentParams{
		ParentCrawlID: setup.ParentCrawlID,
		CompetitorID:  setup.CompetitorID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if createCrawl == nil {
			return setup, nil
		}
		return startCrawlStep(ctx, q, setup, requestedByUserID, parentCrawlID, createCrawl)
	}
	if err != nil {
		return sqlc.CompetitorSetup{}, fmt.Errorf("load competitor child crawl: %w", err)
	}
	switch child.Status {
	case "completed":
		return updateSetup(ctx, q, setup, requestedByUserID, StatusRunning, StepCompleted, StepRunning, StepPending, "", "", pgtype.UUID{}, func(arg *sqlc.EnqueueCompetitorAIWorkerJobParams) {
			*arg = sqlc.EnqueueCompetitorAIWorkerJobParams{JobType: CompetitorKeywordGenerationJobType, ProjectID: setup.ProjectID, CompetitorID: setup.CompetitorID}
		})
	case "failed", "cancelled":
		return failSetupAtStep(ctx, q, setup, StepCrawl, "competitor crawl "+child.Status)
	default:
		return setup, nil
	}
}

// effectiveParent prefers the stored parent so retries create the child the
// reconciliation reads. A row whose parent went missing (crawl deleted)
// repins to the resolved parent, persisted atomically with the start.
func effectiveParent(setup sqlc.CompetitorSetup, parentCrawlID pgtype.UUID) (pgtype.UUID, pgtype.UUID) {
	if setup.ParentCrawlID.Valid {
		return setup.ParentCrawlID, pgtype.UUID{}
	}
	return parentCrawlID, parentCrawlID
}

func startCrawlStep(ctx context.Context, q Queries, setup sqlc.CompetitorSetup, requestedByUserID, parentCrawlID pgtype.UUID, createCrawl func(ctx context.Context, parentCrawlID pgtype.UUID) (pgtype.UUID, error)) (sqlc.CompetitorSetup, error) {
	parent, repin := effectiveParent(setup, parentCrawlID)
	crawlID, err := createCrawl(ctx, parent)
	if err != nil {
		return sqlc.CompetitorSetup{}, fmt.Errorf("start competitor crawl: %w", err)
	}
	return updateSetup(ctx, q, setup, requestedByUserID, StatusRunning, StepRunning, StepPending, StepPending, "", "", repin, nil, crawlID)
}

func resumeFailedSetup(ctx context.Context, q Queries, setup sqlc.CompetitorSetup, requestedByUserID, parentCrawlID pgtype.UUID, createCrawl func(ctx context.Context, parentCrawlID pgtype.UUID) (pgtype.UUID, error)) (sqlc.CompetitorSetup, error) {
	failedStep := ""
	if setup.FailedStep.Valid {
		failedStep = setup.FailedStep.String
	}
	switch failedStep {
	case StepCrawl:
		if createCrawl == nil {
			return sqlc.CompetitorSetup{}, ErrInvalidFailedStep
		}
		parent, repin := effectiveParent(setup, parentCrawlID)
		crawlID, err := createCrawl(ctx, parent)
		if err != nil {
			return sqlc.CompetitorSetup{}, fmt.Errorf("restart competitor crawl: %w", err)
		}
		return updateSetup(ctx, q, setup, requestedByUserID, StatusRunning, StepRunning, StepPending, StepPending, "", "", repin, nil, crawlID)
	case StepKeywordGeneration:
		return updateSetup(ctx, q, setup, requestedByUserID, StatusRunning, StepCompleted, StepRunning, StepPending, "", "", pgtype.UUID{}, func(arg *sqlc.EnqueueCompetitorAIWorkerJobParams) {
			*arg = sqlc.EnqueueCompetitorAIWorkerJobParams{JobType: CompetitorKeywordGenerationJobType, ProjectID: setup.ProjectID, CompetitorID: setup.CompetitorID}
		})
	case StepKeywordAnalysis:
		return updateSetup(ctx, q, setup, requestedByUserID, StatusRunning, StepCompleted, StepCompleted, StepRunning, "", "", pgtype.UUID{}, func(arg *sqlc.EnqueueCompetitorAIWorkerJobParams) {
			*arg = sqlc.EnqueueCompetitorAIWorkerJobParams{JobType: CompetitorKeywordAnalysisJobType, ProjectID: setup.ProjectID, CompetitorID: setup.CompetitorID}
		})
	default:
		return sqlc.CompetitorSetup{}, ErrInvalidFailedStep
	}
}

// FinalizeGenerationSuccess moves keyword_generation to completed, starts
// keyword_analysis and enqueues its job. It is a no-op when the setup already
// moved on.
func FinalizeGenerationSuccess(ctx context.Context, q Queries, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	fresh, err := q.GetCompetitorSetupByCompetitorForUpdate(ctx, job.CompetitorID)
	if err != nil {
		return fmt.Errorf("lock competitor setup: %w", err)
	}
	if fresh.Status != StatusRunning || fresh.KeywordGenerationStatus != StepRunning {
		return q.MarkAIWorkerJobCompleted(ctx, job.ID)
	}
	if _, err := updateSetup(ctx, q, fresh, pgtype.UUID{}, StatusRunning, StepCompleted, StepCompleted, StepRunning, "", "", pgtype.UUID{}, func(arg *sqlc.EnqueueCompetitorAIWorkerJobParams) {
		*arg = sqlc.EnqueueCompetitorAIWorkerJobParams{JobType: CompetitorKeywordAnalysisJobType, ProjectID: fresh.ProjectID, CompetitorID: fresh.CompetitorID}
	}); err != nil {
		return err
	}
	return q.MarkAIWorkerJobCompleted(ctx, job.ID)
}

// FinalizeAnalysisSuccess completes the setup. It is a no-op when the setup
// already moved on.
func FinalizeAnalysisSuccess(ctx context.Context, q Queries, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	fresh, err := q.GetCompetitorSetupByCompetitorForUpdate(ctx, job.CompetitorID)
	if err != nil {
		return fmt.Errorf("lock competitor setup: %w", err)
	}
	if fresh.Status != StatusRunning || fresh.KeywordAnalysisStatus != StepRunning {
		return q.MarkAIWorkerJobCompleted(ctx, job.ID)
	}
	if _, err := updateSetup(ctx, q, fresh, pgtype.UUID{}, StatusCompleted, StepCompleted, StepCompleted, StepCompleted, "", "", pgtype.UUID{}, nil); err != nil {
		return err
	}
	return q.MarkAIWorkerJobCompleted(ctx, job.ID)
}

// FinalizeStepFailure fails the setup at the given step. It cannot move an
// already-advanced setup backward.
func FinalizeStepFailure(ctx context.Context, q Queries, competitorID pgtype.UUID, step, message string) error {
	setup, err := q.GetCompetitorSetupByCompetitorForUpdate(ctx, competitorID)
	if err != nil {
		return fmt.Errorf("lock competitor setup: %w", err)
	}
	if setup.Status != StatusRunning {
		return nil
	}
	var expected string
	switch step {
	case StepKeywordGeneration:
		expected = setup.KeywordGenerationStatus
	case StepKeywordAnalysis:
		expected = setup.KeywordAnalysisStatus
	default:
		return fmt.Errorf("%w: %s", ErrInvalidFailedStep, step)
	}
	if expected != StepRunning {
		return nil
	}
	_, err = failSetupAtStep(ctx, q, setup, step, message)
	return err
}

func failSetupAtStep(ctx context.Context, q Queries, setup sqlc.CompetitorSetup, step, message string) (sqlc.CompetitorSetup, error) {
	crawlStatus := setup.CrawlStatus
	generationStatus := setup.KeywordGenerationStatus
	analysisStatus := setup.KeywordAnalysisStatus
	switch step {
	case StepCrawl:
		crawlStatus = StepFailed
	case StepKeywordGeneration:
		generationStatus = StepFailed
	case StepKeywordAnalysis:
		analysisStatus = StepFailed
	}
	message = capSetupErrorMessage(message)
	updated, err := q.UpdateCompetitorSetup(ctx, sqlc.UpdateCompetitorSetupParams{
		Status:                  StatusFailed,
		CrawlStatus:             crawlStatus,
		KeywordGenerationStatus: generationStatus,
		KeywordAnalysisStatus:   analysisStatus,
		Error:                   pgText(message),
		FailedStep:              pgText(step),
		StepReasons:             encodeStepReason(step, message),
		CompetitorID:            setup.CompetitorID,
	})
	if err != nil {
		return sqlc.CompetitorSetup{}, fmt.Errorf("fail competitor setup: %w", err)
	}
	return updated, nil
}

type enqueueFn func(*sqlc.EnqueueCompetitorAIWorkerJobParams)

func updateSetup(ctx context.Context, q Queries, setup sqlc.CompetitorSetup, requestedByUserID pgtype.UUID, status, crawlStatus, generationStatus, analysisStatus, failedStep, message string, parent pgtype.UUID, enqueue enqueueFn, crawlIDs ...pgtype.UUID) (sqlc.CompetitorSetup, error) {
	arg := sqlc.UpdateCompetitorSetupParams{
		Status:                  status,
		CrawlStatus:             crawlStatus,
		KeywordGenerationStatus: generationStatus,
		KeywordAnalysisStatus:   analysisStatus,
		StepReasons:             setup.StepReasons,
		CompetitorID:            setup.CompetitorID,
	}
	if requestedByUserID.Valid {
		arg.RequestedByUserID = requestedByUserID
	}
	if parent.Valid {
		arg.ParentCrawlID = parent
	}
	if len(crawlIDs) > 0 && crawlIDs[0].Valid {
		arg.CrawlID = crawlIDs[0]
	}
	if status == StatusFailed {
		arg.Error = pgText(message)
		arg.FailedStep = pgText(failedStep)
		arg.StepReasons = encodeStepReason(failedStep, message)
	}
	updated, err := q.UpdateCompetitorSetup(ctx, arg)
	if err != nil {
		return sqlc.CompetitorSetup{}, fmt.Errorf("update competitor setup: %w", err)
	}
	if enqueue != nil {
		var job sqlc.EnqueueCompetitorAIWorkerJobParams
		enqueue(&job)
		if _, err := q.EnqueueCompetitorAIWorkerJob(ctx, job); err != nil {
			return sqlc.CompetitorSetup{}, fmt.Errorf("enqueue %s job: %w", job.JobType, err)
		}
	}
	return updated, nil
}

func pgText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

// maxSetupErrorMessageLength bounds the failure text persisted on the setup
// row. A mid-rune cut is avoided so the stored text stays valid UTF-8.
const maxSetupErrorMessageLength = 500

func capSetupErrorMessage(message string) string {
	if len(message) <= maxSetupErrorMessageLength {
		return message
	}
	cut := maxSetupErrorMessageLength
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut]
}
