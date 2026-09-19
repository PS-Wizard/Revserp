package projectsetup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// fakeTransitioner models the two database calls the transition makes. The
// advance hook decides whether the conditional UPDATE matched a row, exactly
// like the SQL WHERE clause does in production.
type fakeTransitioner struct {
	advance    func(arg sqlc.AdvanceProjectSetupFromCrawlingParams) (sqlc.ProjectSetup, error)
	advanceIn  []sqlc.AdvanceProjectSetupFromCrawlingParams
	enqueues   []sqlc.EnqueueAIWorkerJobParams
	enqueueErr error
}

func (f *fakeTransitioner) AdvanceProjectSetupFromCrawling(_ context.Context, arg sqlc.AdvanceProjectSetupFromCrawlingParams) (sqlc.ProjectSetup, error) {
	f.advanceIn = append(f.advanceIn, arg)
	return f.advance(arg)
}

func (f *fakeTransitioner) EnqueueAIWorkerJob(_ context.Context, arg sqlc.EnqueueAIWorkerJobParams) (sqlc.EnqueueAIWorkerJobRow, error) {
	f.enqueues = append(f.enqueues, arg)
	return sqlc.EnqueueAIWorkerJobRow{}, f.enqueueErr
}

func testUUID() pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.New(), Valid: true}
}

func TestOnCrawlCompletedEnqueuesOneBootstrapJob(t *testing.T) {
	projectID := testUUID()
	crawlID := testUUID()
	fake := &fakeTransitioner{
		advance: func(sqlc.AdvanceProjectSetupFromCrawlingParams) (sqlc.ProjectSetup, error) {
			return sqlc.ProjectSetup{ProjectID: projectID, Status: StatusProfileGeneration}, nil
		},
	}

	if err := OnCrawlCompleted(context.Background(), fake, crawlID); err != nil {
		t.Fatalf("OnCrawlCompleted: %v", err)
	}

	if len(fake.advanceIn) != 1 || fake.advanceIn[0].CrawlID != crawlID || fake.advanceIn[0].Status != StatusProfileGeneration {
		t.Fatalf("advance params = %+v, want crawl %s status %s", fake.advanceIn, crawlID.String(), StatusProfileGeneration)
	}
	if len(fake.enqueues) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(fake.enqueues))
	}
	if fake.enqueues[0].JobType != BusinessProfileBootstrapJobType || fake.enqueues[0].ProjectID != projectID {
		t.Fatalf("enqueued %+v, want %s for project %s", fake.enqueues[0], BusinessProfileBootstrapJobType, projectID.String())
	}
}

func TestOnCrawlCompletedDuplicateTerminalEnqueuesOnce(t *testing.T) {
	projectID := testUUID()
	crawlID := testUUID()
	transitioned := false
	fake := &fakeTransitioner{
		advance: func(sqlc.AdvanceProjectSetupFromCrawlingParams) (sqlc.ProjectSetup, error) {
			if transitioned {
				return sqlc.ProjectSetup{}, pgx.ErrNoRows
			}
			transitioned = true
			return sqlc.ProjectSetup{ProjectID: projectID}, nil
		},
	}

	if err := OnCrawlCompleted(context.Background(), fake, crawlID); err != nil {
		t.Fatalf("first OnCrawlCompleted: %v", err)
	}
	if err := OnCrawlCompleted(context.Background(), fake, crawlID); err != nil {
		t.Fatalf("duplicate OnCrawlCompleted: %v", err)
	}

	if len(fake.enqueues) != 1 {
		t.Fatalf("enqueued %d jobs after duplicate terminal, want 1", len(fake.enqueues))
	}
}

func TestOnCrawlCompletedUnrelatedCrawlIsNoop(t *testing.T) {
	fake := &fakeTransitioner{
		advance: func(sqlc.AdvanceProjectSetupFromCrawlingParams) (sqlc.ProjectSetup, error) {
			return sqlc.ProjectSetup{}, pgx.ErrNoRows
		},
	}

	if err := OnCrawlCompleted(context.Background(), fake, testUUID()); err != nil {
		t.Fatalf("OnCrawlCompleted: %v", err)
	}
	if len(fake.enqueues) != 0 {
		t.Fatalf("enqueued %d jobs for unrelated crawl, want 0", len(fake.enqueues))
	}
}

func TestOnCrawlFailedMarksCrawlingStepWithoutEnqueue(t *testing.T) {
	crawlID := testUUID()
	fake := &fakeTransitioner{
		advance: func(sqlc.AdvanceProjectSetupFromCrawlingParams) (sqlc.ProjectSetup, error) {
			return sqlc.ProjectSetup{Status: StatusFailed}, nil
		},
	}

	if err := OnCrawlFailed(context.Background(), fake, crawlID, "crawl cancelled"); err != nil {
		t.Fatalf("OnCrawlFailed: %v", err)
	}

	if len(fake.advanceIn) != 1 {
		t.Fatalf("advance calls = %d, want 1", len(fake.advanceIn))
	}
	arg := fake.advanceIn[0]
	if arg.CrawlID != crawlID || arg.Status != StatusFailed {
		t.Fatalf("advance params = %+v, want crawl %s status failed", arg, crawlID.String())
	}
	if !arg.FailedStep.Valid || arg.FailedStep.String != FailedStepCrawling {
		t.Fatalf("failed_step = %+v, want %s", arg.FailedStep, FailedStepCrawling)
	}
	if !arg.Error.Valid || arg.Error.String != "crawl cancelled" {
		t.Fatalf("error = %+v, want crawl cancelled", arg.Error)
	}
	if len(fake.enqueues) != 0 {
		t.Fatalf("enqueued %d jobs on failure, want 0", len(fake.enqueues))
	}
}

func TestOnCrawlFailedUnrelatedCrawlIsNoop(t *testing.T) {
	fake := &fakeTransitioner{
		advance: func(sqlc.AdvanceProjectSetupFromCrawlingParams) (sqlc.ProjectSetup, error) {
			return sqlc.ProjectSetup{}, pgx.ErrNoRows
		},
	}

	if err := OnCrawlFailed(context.Background(), fake, testUUID(), "boom"); err != nil {
		t.Fatalf("OnCrawlFailed: %v", err)
	}
	if len(fake.enqueues) != 0 {
		t.Fatalf("enqueued %d jobs for unrelated crawl, want 0", len(fake.enqueues))
	}
}

func TestOnCrawlCompletedPropagatesEnqueueError(t *testing.T) {
	enqueueErr := errors.New("enqueue boom")
	fake := &fakeTransitioner{
		advance: func(sqlc.AdvanceProjectSetupFromCrawlingParams) (sqlc.ProjectSetup, error) {
			return sqlc.ProjectSetup{ProjectID: testUUID()}, nil
		},
		enqueueErr: enqueueErr,
	}

	if err := OnCrawlCompleted(context.Background(), fake, testUUID()); !errors.Is(err, enqueueErr) {
		t.Fatalf("OnCrawlCompleted error = %v, want %v", err, enqueueErr)
	}
}

// fakeStartQueries fakes the start/resume surface. Mutating calls update the
// stored row so a repeated call sees the committed transition, the same way a
// second request would after the first transaction committed.
type fakeStartQueries struct {
	setup    sqlc.ProjectSetup
	setupErr error

	startCalls int
	startArg   sqlc.StartProjectSetupParams
	startErr   error

	updates   []sqlc.UpdateProjectSetupStatusParams
	updateErr error

	enqueues   []sqlc.EnqueueAIWorkerJobParams
	enqueueErr error
}

func (f *fakeStartQueries) GetProjectSetupByProjectIDForUpdate(_ context.Context, _ pgtype.UUID) (sqlc.ProjectSetup, error) {
	if f.setupErr != nil {
		return sqlc.ProjectSetup{}, f.setupErr
	}
	return f.setup, nil
}

func (f *fakeStartQueries) StartProjectSetup(_ context.Context, arg sqlc.StartProjectSetupParams) (sqlc.ProjectSetup, error) {
	f.startCalls++
	f.startArg = arg
	if f.startErr != nil {
		return sqlc.ProjectSetup{}, f.startErr
	}
	updated := f.setup
	updated.Status = StatusCrawling
	updated.CrawlID = arg.CrawlID
	updated.RequestedByUserID = arg.RequestedByUserID
	f.setup = updated
	return updated, nil
}

func (f *fakeStartQueries) UpdateProjectSetupStatus(_ context.Context, arg sqlc.UpdateProjectSetupStatusParams) (sqlc.ProjectSetup, error) {
	f.updates = append(f.updates, arg)
	if f.updateErr != nil {
		return sqlc.ProjectSetup{}, f.updateErr
	}
	updated := f.setup
	updated.Status = arg.Status
	updated.Error = pgtype.Text{}
	updated.FailedStep = pgtype.Text{}
	updated.VisibilitySkipReason = arg.VisibilitySkipReason
	if arg.RequestedByUserID.Valid {
		updated.RequestedByUserID = arg.RequestedByUserID
	}
	if arg.CrawlID.Valid {
		updated.CrawlID = arg.CrawlID
	}
	f.setup = updated
	return updated, nil
}

func (f *fakeStartQueries) EnqueueAIWorkerJob(_ context.Context, arg sqlc.EnqueueAIWorkerJobParams) (sqlc.EnqueueAIWorkerJobRow, error) {
	f.enqueues = append(f.enqueues, arg)
	if f.enqueueErr != nil {
		return sqlc.EnqueueAIWorkerJobRow{}, f.enqueueErr
	}
	return sqlc.EnqueueAIWorkerJobRow{JobType: arg.JobType, ProjectID: arg.ProjectID}, nil
}

func failingStepSetup(step string) sqlc.ProjectSetup {
	return sqlc.ProjectSetup{
		ProjectID:  testUUID(),
		Status:     StatusFailed,
		FailedStep: pgtype.Text{String: step, Valid: true},
	}
}

func TestStartOrResumeReadyStartsFirstCrawl(t *testing.T) {
	projectID := testUUID()
	ownerID := testUUID()
	crawlID := testUUID()
	fake := &fakeStartQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: StatusReady, RequestedByUserID: testUUID()}}
	starts := 0

	outcome, err := StartOrResume(context.Background(), fake, projectID, ownerID, func(context.Context) (pgtype.UUID, error) {
		starts++
		return crawlID, nil
	})
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}
	if !outcome.Started || outcome.Setup.Status != StatusCrawling {
		t.Fatalf("outcome = %+v, want started crawling", outcome)
	}
	if fake.startArg.RequestedByUserID != ownerID || fake.startArg.CrawlID != crawlID || fake.startArg.ProjectID != projectID {
		t.Fatalf("start arg = %+v, want owner %s crawl %s project %s", fake.startArg, ownerID.String(), crawlID.String(), projectID.String())
	}
	if starts != 1 || len(fake.enqueues) != 0 || len(fake.updates) != 0 {
		t.Fatalf("starts=%d enqueues=%d updates=%d, want 1/0/0", starts, len(fake.enqueues), len(fake.updates))
	}
}

func TestStartOrResumeReadyDuplicateStartsOnce(t *testing.T) {
	projectID := testUUID()
	ownerID := testUUID()
	fake := &fakeStartQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: StatusReady}}
	starts := 0
	start := func(context.Context) (pgtype.UUID, error) { starts++; return testUUID(), nil }

	if _, err := StartOrResume(context.Background(), fake, projectID, ownerID, start); err != nil {
		t.Fatalf("first StartOrResume: %v", err)
	}
	second, err := StartOrResume(context.Background(), fake, projectID, ownerID, start)
	if err != nil {
		t.Fatalf("second StartOrResume: %v", err)
	}
	if second.Started {
		t.Fatalf("duplicate start reported Started=true, want false")
	}
	if starts != 1 || fake.startCalls != 1 {
		t.Fatalf("starts=%d startCalls=%d, want 1/1", starts, fake.startCalls)
	}
}

func TestStartOrResumeFailedCrawlingCreatesCrawl(t *testing.T) {
	setup := failingStepSetup(FailedStepCrawling)
	ownerID := testUUID()
	crawlID := testUUID()
	fake := &fakeStartQueries{setup: setup}
	starts := 0

	outcome, err := StartOrResume(context.Background(), fake, setup.ProjectID, ownerID, func(context.Context) (pgtype.UUID, error) {
		starts++
		return crawlID, nil
	})
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}
	if outcome.Started {
		t.Fatalf("resume reported Started=true, want false")
	}
	if starts != 1 || fake.startCalls != 0 {
		t.Fatalf("starts=%d startCalls=%d, want 1/0", starts, fake.startCalls)
	}
	if len(fake.enqueues) != 0 {
		t.Fatalf("enqueues = %+v, want none for crawling step", fake.enqueues)
	}
	if len(fake.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(fake.updates))
	}
	arg := fake.updates[0]
	if arg.Status != StatusCrawling || arg.ExpectedStatus != StatusFailed || arg.CrawlID != crawlID {
		t.Fatalf("update = %+v, want crawling from failed with new crawl", arg)
	}
	if arg.VisibilitySkipReason.Valid {
		t.Fatalf("visibility_skip_reason = %+v, want cleared", arg.VisibilitySkipReason)
	}
}

func TestStartOrResumeFailedEnqueuesPerStep(t *testing.T) {
	tests := []struct {
		step    string
		status  string
		jobType string
	}{
		{FailedStepProfileGeneration, StatusProfileGeneration, BusinessProfileBootstrapJobType},
		{FailedStepPromptGeneration, StatusPromptGeneration, PromptGenerationJobType},
		{FailedStepVisibility, StatusPromptGeneration, PromptGenerationJobType},
	}
	for _, test := range tests {
		t.Run(test.step, func(t *testing.T) {
			setup := failingStepSetup(test.step)
			fake := &fakeStartQueries{setup: setup}
			ownerID := testUUID()
			crawlCalls := 0

			if _, err := StartOrResume(context.Background(), fake, setup.ProjectID, ownerID, func(context.Context) (pgtype.UUID, error) {
				crawlCalls++
				return testUUID(), nil
			}); err != nil {
				t.Fatalf("StartOrResume: %v", err)
			}
			if crawlCalls != 0 {
				t.Fatalf("startCrawl called %d times, want 0", crawlCalls)
			}
			if len(fake.updates) != 1 || fake.updates[0].Status != test.status || fake.updates[0].ExpectedStatus != StatusFailed {
				t.Fatalf("updates = %+v, want one to %s from failed", fake.updates, test.status)
			}
			if fake.updates[0].RequestedByUserID != ownerID {
				t.Fatalf("retry requester = %s, want the acting owner %s", fake.updates[0].RequestedByUserID.String(), ownerID.String())
			}
			if fake.updates[0].VisibilitySkipReason.Valid {
				t.Fatalf("visibility_skip_reason = %+v, want cleared", fake.updates[0].VisibilitySkipReason)
			}
			if len(fake.enqueues) != 1 || fake.enqueues[0].JobType != test.jobType || fake.enqueues[0].ProjectID != setup.ProjectID {
				t.Fatalf("enqueues = %+v, want one %s", fake.enqueues, test.jobType)
			}
		})
	}
}

func TestStartOrResumeFailedDuplicateEnqueuesOnce(t *testing.T) {
	setup := failingStepSetup(FailedStepProfileGeneration)
	fake := &fakeStartQueries{setup: setup}

	if _, err := StartOrResume(context.Background(), fake, setup.ProjectID, testUUID(), nil); err != nil {
		t.Fatalf("first StartOrResume: %v", err)
	}
	if _, err := StartOrResume(context.Background(), fake, setup.ProjectID, testUUID(), nil); err != nil {
		t.Fatalf("duplicate StartOrResume: %v", err)
	}
	if len(fake.enqueues) != 1 {
		t.Fatalf("enqueued %d jobs after duplicate retry, want 1", len(fake.enqueues))
	}
	if len(fake.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(fake.updates))
	}
}

func TestStartOrResumeActiveOrCompletedIsNoop(t *testing.T) {
	for _, status := range []string{StatusCrawling, StatusProfileGeneration, StatusPromptGeneration, StatusVisibility, StatusCompleted} {
		t.Run(status, func(t *testing.T) {
			setup := sqlc.ProjectSetup{ProjectID: testUUID(), Status: status}
			fake := &fakeStartQueries{setup: setup}
			crawlCalls := 0

			outcome, err := StartOrResume(context.Background(), fake, setup.ProjectID, testUUID(), func(context.Context) (pgtype.UUID, error) {
				crawlCalls++
				return testUUID(), nil
			})
			if err != nil {
				t.Fatalf("StartOrResume: %v", err)
			}
			if outcome.Started || outcome.Setup.Status != status {
				t.Fatalf("outcome = %+v, want unchanged %s", outcome, status)
			}
			if crawlCalls != 0 || len(fake.updates) != 0 || len(fake.enqueues) != 0 {
				t.Fatalf("side effects for %s: starts=%d updates=%d enqueues=%d", status, crawlCalls, len(fake.updates), len(fake.enqueues))
			}
		})
	}
}

func TestStartOrResumeFailedInvalidStepConflicts(t *testing.T) {
	for _, step := range []pgtype.Text{{}, {String: "bogus", Valid: true}} {
		setup := sqlc.ProjectSetup{ProjectID: testUUID(), Status: StatusFailed, FailedStep: step}
		fake := &fakeStartQueries{setup: setup}

		_, err := StartOrResume(context.Background(), fake, setup.ProjectID, testUUID(), func(context.Context) (pgtype.UUID, error) {
			t.Fatal("startCrawl must not run for an invalid failed_step")
			return pgtype.UUID{}, nil
		})
		if !errors.Is(err, ErrInvalidFailedStep) {
			t.Fatalf("error = %v, want ErrInvalidFailedStep", err)
		}
		if len(fake.updates) != 0 || len(fake.enqueues) != 0 {
			t.Fatalf("invalid step had side effects: updates=%d enqueues=%d", len(fake.updates), len(fake.enqueues))
		}
	}
}

func TestStartOrResumeNoRowNotFound(t *testing.T) {
	fake := &fakeStartQueries{setupErr: pgx.ErrNoRows}
	_, err := StartOrResume(context.Background(), fake, testUUID(), testUUID(), nil)
	if !errors.Is(err, ErrSetupNotFound) {
		t.Fatalf("error = %v, want ErrSetupNotFound", err)
	}
}

func TestStartOrResumeGuardedUpdateConflict(t *testing.T) {
	setup := failingStepSetup(FailedStepPromptGeneration)
	fake := &fakeStartQueries{setup: setup, updateErr: pgx.ErrNoRows}

	_, err := StartOrResume(context.Background(), fake, setup.ProjectID, testUUID(), nil)
	if !errors.Is(err, ErrResumeConflict) {
		t.Fatalf("error = %v, want ErrResumeConflict", err)
	}
	if len(fake.enqueues) != 0 {
		t.Fatalf("conflicting retry enqueued %d jobs, want 0", len(fake.enqueues))
	}
}

func TestStartOrResumeEnqueueErrorPropagates(t *testing.T) {
	enqueueErr := errors.New("enqueue boom")
	setup := failingStepSetup(FailedStepProfileGeneration)
	fake := &fakeStartQueries{setup: setup, enqueueErr: enqueueErr}

	if _, err := StartOrResume(context.Background(), fake, setup.ProjectID, testUUID(), nil); !errors.Is(err, enqueueErr) {
		t.Fatalf("error = %v, want %v", err, enqueueErr)
	}
}

// TestStartOrResumeFailedRestampsStaleRequester covers the case where a
// different current owner retries a failed setup: the durable row must move to
// that owner so later profile and feature checks never run as a deleted user.
func TestStartOrResumeFailedRestampsStaleRequester(t *testing.T) {
	setup := failingStepSetup(FailedStepProfileGeneration)
	staleOwner := testUUID()
	setup.RequestedByUserID = staleOwner
	newOwner := testUUID()
	fake := &fakeStartQueries{setup: setup}

	if _, err := StartOrResume(context.Background(), fake, setup.ProjectID, newOwner, nil); err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}
	if fake.setup.RequestedByUserID != newOwner {
		t.Fatalf("stored requester = %s, want retrying owner %s", fake.setup.RequestedByUserID.String(), newOwner.String())
	}
}

func TestStartOrResumeReadyRestampsRequester(t *testing.T) {
	projectID := testUUID()
	staleOwner := testUUID()
	newOwner := testUUID()
	fake := &fakeStartQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: StatusReady, RequestedByUserID: staleOwner}}

	if _, err := StartOrResume(context.Background(), fake, projectID, newOwner, func(context.Context) (pgtype.UUID, error) {
		return testUUID(), nil
	}); err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}
	if fake.startArg.RequestedByUserID != newOwner {
		t.Fatalf("start requester = %s, want acting owner %s", fake.startArg.RequestedByUserID.String(), newOwner.String())
	}
}

func TestOnCrawlFailedBoundsErrorMessage(t *testing.T) {
	crawlID := testUUID()
	fake := &fakeTransitioner{
		advance: func(sqlc.AdvanceProjectSetupFromCrawlingParams) (sqlc.ProjectSetup, error) {
			return sqlc.ProjectSetup{Status: StatusFailed}, nil
		},
	}
	long := strings.Repeat("é", maxSetupErrorMessageLength)

	if err := OnCrawlFailed(context.Background(), fake, crawlID, long); err != nil {
		t.Fatalf("OnCrawlFailed: %v", err)
	}
	if len(fake.advanceIn) != 1 {
		t.Fatalf("advance calls = %d, want 1", len(fake.advanceIn))
	}
	got := fake.advanceIn[0].Error.String
	if len(got) > maxSetupErrorMessageLength {
		t.Fatalf("stored error length = %d, want <= %d", len(got), maxSetupErrorMessageLength)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("stored error is not valid UTF-8: %q", got)
	}
}

func TestCapSetupErrorMessageKeepsRuneBoundary(t *testing.T) {
	if got := capSetupErrorMessage("short"); got != "short" {
		t.Fatalf("short = %q, want unchanged", got)
	}
	message := strings.Repeat("a", maxSetupErrorMessageLength-1) + "é" + "tail"
	got := capSetupErrorMessage(message)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated message is not valid UTF-8: %q", got)
	}
	if len(got) > maxSetupErrorMessageLength {
		t.Fatalf("length = %d, want <= %d", len(got), maxSetupErrorMessageLength)
	}
}
