package aiaudit

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectsetup"
)

// fakeSetupQueries fakes the narrow finalization surface so each decision is
// exercised without a database. Every call is recorded so tests can assert the
// expected-current-status guards and the exact side effects.
type fakeSetupQueries struct {
	setup    sqlc.ProjectSetup
	setupErr error

	features      sqlc.GetOrganizationFeaturesByProjectIDRow
	featuresErr   error
	featureArg    sqlc.GetOrganizationFeaturesByProjectIDParams
	featuresCalls int

	reserveUsed int32
	reserveErr  error
	reserveArg  sqlc.ReserveAIWorkspaceMonthlyAuditParams

	audit    sqlc.AiAudit
	auditErr error
	auditArg sqlc.CreateAIAuditParams

	updateErr error

	updates   []sqlc.UpdateProjectSetupStatusParams
	enqueues  []sqlc.EnqueueAIWorkerJobParams
	completed []pgtype.UUID
	failed    []sqlc.MarkAIWorkerJobFailedParams

	failActiveArg   sqlc.FailActiveAIAuditsForCrawlParams
	failActiveCalls int
	failActiveErr   error
}

func (f *fakeSetupQueries) GetProjectSetupByProjectIDForUpdate(_ context.Context, _ pgtype.UUID) (sqlc.ProjectSetup, error) {
	if f.setupErr != nil {
		return sqlc.ProjectSetup{}, f.setupErr
	}
	return f.setup, nil
}

func (f *fakeSetupQueries) UpdateProjectSetupStatus(_ context.Context, arg sqlc.UpdateProjectSetupStatusParams) (sqlc.ProjectSetup, error) {
	f.updates = append(f.updates, arg)
	if f.updateErr != nil {
		return sqlc.ProjectSetup{}, f.updateErr
	}
	updated := f.setup
	updated.Status = arg.Status
	return updated, nil
}

func (f *fakeSetupQueries) EnqueueAIWorkerJob(_ context.Context, arg sqlc.EnqueueAIWorkerJobParams) (sqlc.EnqueueAIWorkerJobRow, error) {
	f.enqueues = append(f.enqueues, arg)
	return sqlc.EnqueueAIWorkerJobRow{JobType: arg.JobType, ProjectID: arg.ProjectID}, nil
}

func (f *fakeSetupQueries) MarkAIWorkerJobCompleted(_ context.Context, id pgtype.UUID) error {
	f.completed = append(f.completed, id)
	return nil
}

func (f *fakeSetupQueries) MarkAIWorkerJobFailed(_ context.Context, arg sqlc.MarkAIWorkerJobFailedParams) error {
	f.failed = append(f.failed, arg)
	return nil
}

func (f *fakeSetupQueries) GetOrganizationFeaturesByProjectID(_ context.Context, arg sqlc.GetOrganizationFeaturesByProjectIDParams) (sqlc.GetOrganizationFeaturesByProjectIDRow, error) {
	f.featuresCalls++
	f.featureArg = arg
	if f.featuresErr != nil {
		return sqlc.GetOrganizationFeaturesByProjectIDRow{}, f.featuresErr
	}
	return f.features, nil
}

func (f *fakeSetupQueries) ReserveAIWorkspaceMonthlyAudit(_ context.Context, arg sqlc.ReserveAIWorkspaceMonthlyAuditParams) (int32, error) {
	f.reserveArg = arg
	if f.reserveErr != nil {
		return 0, f.reserveErr
	}
	return f.reserveUsed, nil
}

func (f *fakeSetupQueries) CreateAIAudit(_ context.Context, arg sqlc.CreateAIAuditParams) (sqlc.AiAudit, error) {
	f.auditArg = arg
	if f.auditErr != nil {
		return sqlc.AiAudit{}, f.auditErr
	}
	return f.audit, nil
}

func (f *fakeSetupQueries) FailActiveAIAuditsForCrawl(_ context.Context, arg sqlc.FailActiveAIAuditsForCrawlParams) (int64, error) {
	f.failActiveCalls++
	f.failActiveArg = arg
	if f.failActiveErr != nil {
		return 0, f.failActiveErr
	}
	return 0, nil
}

func finalizationJob(projectID pgtype.UUID) sqlc.ClaimNextPendingAIWorkerJobRow {
	return sqlc.ClaimNextPendingAIWorkerJobRow{
		ID:        pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		ProjectID: projectID,
	}
}

func TestFinalizeBootstrapSuccessAdvancesAndEnqueuesOnce(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusProfileGeneration}}
	job := finalizationJob(projectID)

	if err := finalizeBootstrapSuccess(context.Background(), fake, job); err != nil {
		t.Fatalf("finalizeBootstrapSuccess: %v", err)
	}
	if len(fake.enqueues) != 1 || fake.enqueues[0].JobType != promptGenerationJobType || fake.enqueues[0].ProjectID != projectID {
		t.Fatalf("enqueues = %+v, want exactly one prompt_generation", fake.enqueues)
	}
	if len(fake.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(fake.updates))
	}
	if fake.updates[0].Status != projectsetup.StatusPromptGeneration || fake.updates[0].ExpectedStatus != projectsetup.StatusProfileGeneration {
		t.Fatalf("update = %+v, want prompt_generation from profile_generation", fake.updates[0])
	}
	if len(fake.completed) != 1 || fake.completed[0] != job.ID {
		t.Fatalf("completed = %+v, want the bootstrap job", fake.completed)
	}
}

func TestFinalizeBootstrapSuccessAlreadyAdvancedDoesNotEnqueue(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusPromptGeneration}}

	if err := finalizeBootstrapSuccess(context.Background(), fake, finalizationJob(projectID)); err != nil {
		t.Fatalf("finalizeBootstrapSuccess: %v", err)
	}
	if len(fake.enqueues) != 0 || len(fake.updates) != 0 {
		t.Fatalf("advanced setup must be untouched, enqueues=%d updates=%d", len(fake.enqueues), len(fake.updates))
	}
	if len(fake.completed) != 1 {
		t.Fatalf("job must still complete")
	}
}

func TestFinalizeBootstrapSuccessNoSetupCompletes(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setupErr: pgx.ErrNoRows}

	if err := finalizeBootstrapSuccess(context.Background(), fake, finalizationJob(projectID)); err != nil {
		t.Fatalf("finalizeBootstrapSuccess: %v", err)
	}
	if len(fake.enqueues) != 0 || len(fake.updates) != 0 || len(fake.completed) != 1 {
		t.Fatalf("setup-less job must just complete: enqueues=%d updates=%d completed=%d", len(fake.enqueues), len(fake.updates), len(fake.completed))
	}
}

func TestFinalizeBootstrapFailureFailsProfileStep(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusProfileGeneration}}
	job := finalizationJob(projectID)

	if err := finalizeBootstrapFailure(context.Background(), fake, job, "agent blew up"); err != nil {
		t.Fatalf("finalizeBootstrapFailure: %v", err)
	}
	if len(fake.failed) != 1 || fake.failed[0].ID != job.ID {
		t.Fatalf("failed = %+v, want the bootstrap job", fake.failed)
	}
	assertSetupFailedAt(t, fake.updates, projectsetup.StatusProfileGeneration, projectsetup.FailedStepProfileGeneration)
}

func TestFinalizePromptGenerationSuccessStartsVisibility(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	orgID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	crawlID := pgtype.UUID{Bytes: [16]byte{4}, Valid: true}
	requester := pgtype.UUID{Bytes: [16]byte{5}, Valid: true}
	auditID := pgtype.UUID{Bytes: [16]byte{6}, Valid: true}
	fake := &fakeSetupQueries{
		setup:       sqlc.ProjectSetup{ProjectID: projectID, OrganizationID: orgID, CrawlID: crawlID, RequestedByUserID: requester, Status: projectsetup.StatusPromptGeneration},
		features:    sqlc.GetOrganizationFeaturesByProjectIDRow{AiVisibilityAuditMonthlyLimit: 10},
		reserveUsed: 1,
		audit:       sqlc.AiAudit{ID: auditID, ProjectID: projectID, CrawlID: crawlID, Status: "queued"},
	}
	job := finalizationJob(projectID)

	if err := finalizePromptGenerationSuccess(context.Background(), fake, job); err != nil {
		t.Fatalf("finalizePromptGenerationSuccess: %v", err)
	}
	if fake.featureArg.UserID != requester {
		t.Fatalf("feature authorization user = %s, want setup requester %s", fake.featureArg.UserID.String(), requester.String())
	}
	if fake.failActiveCalls != 1 || fake.failActiveArg.ProjectID != projectID || fake.failActiveArg.CrawlID != crawlID {
		t.Fatalf("failActive = %+v (calls=%d), want one call for project %s crawl %s", fake.failActiveArg, fake.failActiveCalls, projectID.String(), crawlID.String())
	}
	if len(fake.enqueues) != 1 || fake.enqueues[0].JobType != visibilityRunJobType || fake.enqueues[0].AuditID != auditID {
		t.Fatalf("enqueues = %+v, want one visibility_run for the audit", fake.enqueues)
	}
	if fake.auditArg.Status != "queued" || fake.auditArg.CrawlID != crawlID || fake.auditArg.ProjectID != projectID {
		t.Fatalf("audit = %+v, want queued audit for the setup crawl", fake.auditArg)
	}
	if len(fake.updates) != 1 || fake.updates[0].Status != projectsetup.StatusVisibility || fake.updates[0].ExpectedStatus != projectsetup.StatusPromptGeneration {
		t.Fatalf("update = %+v, want visibility from prompt_generation", fake.updates)
	}
	if len(fake.completed) != 1 {
		t.Fatalf("prompt job must complete")
	}
}

func TestFinalizePromptGenerationSuccessDisabledSkips(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{
		setup:    sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusPromptGeneration},
		features: sqlc.GetOrganizationFeaturesByProjectIDRow{AiVisibilityAuditMonthlyLimit: 0},
	}

	if err := finalizePromptGenerationSuccess(context.Background(), fake, finalizationJob(projectID)); err != nil {
		t.Fatalf("finalizePromptGenerationSuccess: %v", err)
	}
	if len(fake.enqueues) != 0 {
		t.Fatalf("disabled visibility must not enqueue, got %+v", fake.enqueues)
	}
	assertSetupCompletedWithSkip(t, fake.updates, visibilityDisabledSkipReason)
	if len(fake.completed) != 1 {
		t.Fatalf("prompt job must complete")
	}
}

func TestFinalizePromptGenerationSuccessQuotaExhaustedSkips(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{
		setup:      sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusPromptGeneration},
		features:   sqlc.GetOrganizationFeaturesByProjectIDRow{AiVisibilityAuditMonthlyLimit: 5},
		reserveErr: pgx.ErrNoRows,
	}

	if err := finalizePromptGenerationSuccess(context.Background(), fake, finalizationJob(projectID)); err != nil {
		t.Fatalf("finalizePromptGenerationSuccess: %v", err)
	}
	if len(fake.enqueues) != 0 {
		t.Fatalf("exhausted quota must not enqueue, got %+v", fake.enqueues)
	}
	assertSetupCompletedWithSkip(t, fake.updates, visibilityQuotaSkipReason)
	if len(fake.completed) != 1 {
		t.Fatalf("prompt job must complete")
	}
}

func TestFinalizePromptGenerationSuccessNormalJobWithoutSetup(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setupErr: pgx.ErrNoRows}

	if err := finalizePromptGenerationSuccess(context.Background(), fake, finalizationJob(projectID)); err != nil {
		t.Fatalf("finalizePromptGenerationSuccess: %v", err)
	}
	if len(fake.updates) != 0 || len(fake.enqueues) != 0 || fake.featuresCalls != 0 {
		t.Fatalf("manual job must not touch setup")
	}
	if len(fake.completed) != 1 {
		t.Fatalf("manual job must complete")
	}
}

func TestFinalizePromptGenerationSuccessSetupNotOnStep(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusVisibility}}

	if err := finalizePromptGenerationSuccess(context.Background(), fake, finalizationJob(projectID)); err != nil {
		t.Fatalf("finalizePromptGenerationSuccess: %v", err)
	}
	if len(fake.updates) != 0 || len(fake.enqueues) != 0 {
		t.Fatalf("setup past prompt_generation must be untouched")
	}
	if len(fake.completed) != 1 {
		t.Fatalf("job must complete")
	}
}

func TestFinalizePromptGenerationFailureFailsPromptStep(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusPromptGeneration}}

	if err := finalizePromptGenerationFailure(context.Background(), fake, finalizationJob(projectID), "no questions"); err != nil {
		t.Fatalf("finalizePromptGenerationFailure: %v", err)
	}
	if len(fake.failed) != 1 {
		t.Fatalf("failed = %+v, want the prompt job", fake.failed)
	}
	assertSetupFailedAt(t, fake.updates, projectsetup.StatusPromptGeneration, projectsetup.FailedStepPromptGeneration)
}

func TestFinalizeVisibilitySuccessCompletesOnFinishedAudit(t *testing.T) {
	for _, auditStatus := range []string{"completed", "completed_with_failures"} {
		t.Run(auditStatus, func(t *testing.T) {
			projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
			fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusVisibility}}

			if err := finalizeVisibilitySuccess(context.Background(), fake, finalizationJob(projectID), auditStatus); err != nil {
				t.Fatalf("finalizeVisibilitySuccess: %v", err)
			}
			if len(fake.updates) != 1 || fake.updates[0].Status != projectsetup.StatusCompleted || fake.updates[0].ExpectedStatus != projectsetup.StatusVisibility {
				t.Fatalf("update = %+v, want completed from visibility", fake.updates)
			}
			if len(fake.completed) != 1 || len(fake.failed) != 0 {
				t.Fatalf("visibility job must complete")
			}
		})
	}
}

func TestFinalizeVisibilitySuccessAuditFailedFailsSetup(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusVisibility}}

	if err := finalizeVisibilitySuccess(context.Background(), fake, finalizationJob(projectID), "failed"); err != nil {
		t.Fatalf("finalizeVisibilitySuccess: %v", err)
	}
	assertSetupFailedAt(t, fake.updates, projectsetup.StatusVisibility, projectsetup.FailedStepVisibility)
	if len(fake.completed) != 1 {
		t.Fatalf("visibility job itself still completes")
	}
}

func TestFinalizeVisibilitySuccessNormalJobWithoutSetup(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setupErr: pgx.ErrNoRows}

	if err := finalizeVisibilitySuccess(context.Background(), fake, finalizationJob(projectID), "completed"); err != nil {
		t.Fatalf("finalizeVisibilitySuccess: %v", err)
	}
	if len(fake.updates) != 0 || len(fake.completed) != 1 {
		t.Fatalf("manual visibility job must just complete")
	}
}

func TestFinalizeVisibilityFailureFailsVisibilityStep(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusVisibility}}

	if err := finalizeVisibilityFailure(context.Background(), fake, finalizationJob(projectID), "provider down"); err != nil {
		t.Fatalf("finalizeVisibilityFailure: %v", err)
	}
	if len(fake.failed) != 1 {
		t.Fatalf("failed = %+v, want the visibility job", fake.failed)
	}
	assertSetupFailedAt(t, fake.updates, projectsetup.StatusVisibility, projectsetup.FailedStepVisibility)
}

func TestFailSetupAtStepIgnoresAlreadyAdvancedSetup(t *testing.T) {
	fake := &fakeSetupQueries{updateErr: pgx.ErrNoRows}
	if err := failSetupAtStep(context.Background(), fake, pgtype.UUID{Valid: true}, projectsetup.StatusPromptGeneration, projectsetup.FailedStepPromptGeneration, "boom"); err != nil {
		t.Fatalf("guard miss must not be an error: %v", err)
	}
}

func assertSetupCompletedWithSkip(t *testing.T, updates []sqlc.UpdateProjectSetupStatusParams, reason string) {
	t.Helper()
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(updates))
	}
	if updates[0].Status != projectsetup.StatusCompleted || updates[0].ExpectedStatus != projectsetup.StatusPromptGeneration {
		t.Fatalf("update = %+v, want completed from prompt_generation", updates[0])
	}
	if !updates[0].VisibilitySkipReason.Valid || updates[0].VisibilitySkipReason.String != reason {
		t.Fatalf("skip reason = %+v, want %q", updates[0].VisibilitySkipReason, reason)
	}
}

func assertSetupFailedAt(t *testing.T, updates []sqlc.UpdateProjectSetupStatusParams, expectedStatus, failedStep string) {
	t.Helper()
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(updates))
	}
	if updates[0].Status != projectsetup.StatusFailed || updates[0].ExpectedStatus != expectedStatus {
		t.Fatalf("update = %+v, want failed from %s", updates[0], expectedStatus)
	}
	if !updates[0].FailedStep.Valid || updates[0].FailedStep.String != failedStep {
		t.Fatalf("failed_step = %+v, want %s", updates[0].FailedStep, failedStep)
	}
	if !updates[0].Error.Valid || updates[0].Error.String == "" {
		t.Fatalf("error = %+v, want a useful message", updates[0].Error)
	}
}

// TestFinalizePromptGenerationSuccessNoAuditWhenSkipped pins that the audit
// cleanup and creation are skipped entirely when visibility is disabled or the
// quota is exhausted, so a skip never fails an unrelated active audit.
func TestFinalizePromptGenerationSuccessNoAuditWhenSkipped(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{
		setup:    sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusPromptGeneration},
		features: sqlc.GetOrganizationFeaturesByProjectIDRow{AiVisibilityAuditMonthlyLimit: 0},
	}

	if err := finalizePromptGenerationSuccess(context.Background(), fake, finalizationJob(projectID)); err != nil {
		t.Fatalf("finalizePromptGenerationSuccess: %v", err)
	}
	if fake.failActiveCalls != 0 || len(fake.enqueues) != 0 {
		t.Fatalf("skip touched audits: failActive=%d enqueues=%d", fake.failActiveCalls, len(fake.enqueues))
	}
}

// TestFailSetupAtStepBoundsErrorMessage keeps the UI-facing failure text short
// and valid UTF-8 even when a provider or database error is huge.
func TestFailSetupAtStepBoundsErrorMessage(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusPromptGeneration}}

	long := strings.Repeat("a", maxSetupErrorMessageLength-1) + "é" + "tail"
	if err := failSetupAtStep(context.Background(), fake, projectID, projectsetup.StatusPromptGeneration, projectsetup.FailedStepPromptGeneration, long); err != nil {
		t.Fatalf("failSetupAtStep: %v", err)
	}
	if len(fake.updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(fake.updates))
	}
	got := fake.updates[0].Error.String
	if len(got) > maxSetupErrorMessageLength {
		t.Fatalf("stored error length = %d, want <= %d", len(got), maxSetupErrorMessageLength)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("stored error is not valid UTF-8: %q", got)
	}
}

func TestCapSetupErrorMessage(t *testing.T) {
	if got := capSetupErrorMessage("short"); got != "short" {
		t.Fatalf("short message = %q, want unchanged", got)
	}
	// A multi-byte rune straddling the cut must not leave invalid UTF-8.
	message := strings.Repeat("a", maxSetupErrorMessageLength-1) + "é" + "tail"
	got := capSetupErrorMessage(message)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated message is not valid UTF-8: %q", got)
	}
	if len(got) > maxSetupErrorMessageLength {
		t.Fatalf("length = %d, want <= %d", len(got), maxSetupErrorMessageLength)
	}
}

// TestFinalizePromptGenerationFailureBoundsJobError keeps the job row the
// status endpoint reads from bounded as well.
func TestFinalizePromptGenerationFailureBoundsJobError(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	fake := &fakeSetupQueries{setup: sqlc.ProjectSetup{ProjectID: projectID, Status: projectsetup.StatusPromptGeneration}}
	long := strings.Repeat("a", maxSetupErrorMessageLength+50)

	if err := finalizePromptGenerationFailure(context.Background(), fake, finalizationJob(projectID), long); err != nil {
		t.Fatalf("finalizePromptGenerationFailure: %v", err)
	}
	if len(fake.failed) != 1 {
		t.Fatalf("failed = %+v, want one job", fake.failed)
	}
	if got := fake.failed[0].ErrorMessage.String; len(got) > maxSetupErrorMessageLength {
		t.Fatalf("job error length = %d, want <= %d", len(got), maxSetupErrorMessageLength)
	}
}
