package competitorsetup

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type fakeCompetitorQueries struct {
	setup        sqlc.CompetitorSetup
	setupErr     error
	child        sqlc.GetCompetitorChildCrawlForParentRow
	childErr     error
	updates      []sqlc.UpdateCompetitorSetupParams
	enqueues     []sqlc.EnqueueCompetitorAIWorkerJobParams
	completed    []pgtype.UUID
	failed       []sqlc.MarkAIWorkerJobFailedParams
	createErr    error
	onUpdate     func(sqlc.CompetitorSetup) sqlc.CompetitorSetup
	created      int
	childDeleted bool
}

func testIDs() (org, project, competitor, user, parent pgtype.UUID) {
	mk := func(b byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{b}, Valid: true} }
	return mk(1), mk(2), mk(3), mk(4), mk(5)
}

func (f *fakeCompetitorQueries) GetCompetitorSetupByCompetitorForUpdate(_ context.Context, _ pgtype.UUID) (sqlc.CompetitorSetup, error) {
	if f.setupErr != nil {
		return sqlc.CompetitorSetup{}, f.setupErr
	}
	return f.setup, nil
}

func (f *fakeCompetitorQueries) CreateCompetitorSetup(_ context.Context, arg sqlc.CreateCompetitorSetupParams) (sqlc.CompetitorSetup, error) {
	if f.createErr != nil {
		return sqlc.CompetitorSetup{}, f.createErr
	}
	f.created++
	return sqlc.CompetitorSetup{
		OrganizationID: arg.OrganizationID, ProjectID: arg.ProjectID, CompetitorID: arg.CompetitorID,
		RequestedByUserID: arg.RequestedByUserID, ParentCrawlID: arg.ParentCrawlID,
		Status: arg.Status, CrawlStatus: arg.CrawlStatus,
		KeywordGenerationStatus: arg.KeywordGenerationStatus, KeywordAnalysisStatus: arg.KeywordAnalysisStatus,
	}, nil
}

func (f *fakeCompetitorQueries) UpdateCompetitorSetup(_ context.Context, arg sqlc.UpdateCompetitorSetupParams) (sqlc.CompetitorSetup, error) {
	f.updates = append(f.updates, arg)
	updated := f.setup
	updated.Status = arg.Status
	updated.CrawlStatus = arg.CrawlStatus
	updated.KeywordGenerationStatus = arg.KeywordGenerationStatus
	updated.KeywordAnalysisStatus = arg.KeywordAnalysisStatus
	updated.Error = arg.Error
	updated.FailedStep = arg.FailedStep
	if arg.CrawlID.Valid {
		updated.CrawlID = arg.CrawlID
	}
	if arg.ParentCrawlID.Valid {
		updated.ParentCrawlID = arg.ParentCrawlID
	}
	if f.onUpdate != nil {
		updated = f.onUpdate(updated)
	}
	f.setup = updated
	return updated, nil
}

func (f *fakeCompetitorQueries) GetCompetitorChildCrawlForParent(_ context.Context, _ sqlc.GetCompetitorChildCrawlForParentParams) (sqlc.GetCompetitorChildCrawlForParentRow, error) {
	if f.childErr != nil {
		return sqlc.GetCompetitorChildCrawlForParentRow{}, f.childErr
	}
	return f.child, nil
}

func (f *fakeCompetitorQueries) EnqueueCompetitorAIWorkerJob(_ context.Context, arg sqlc.EnqueueCompetitorAIWorkerJobParams) (sqlc.EnqueueCompetitorAIWorkerJobRow, error) {
	f.enqueues = append(f.enqueues, arg)
	return sqlc.EnqueueCompetitorAIWorkerJobRow{}, nil
}

func (f *fakeCompetitorQueries) MarkAIWorkerJobCompleted(_ context.Context, id pgtype.UUID) error {
	f.completed = append(f.completed, id)
	return nil
}

func (f *fakeCompetitorQueries) MarkAIWorkerJobFailed(_ context.Context, arg sqlc.MarkAIWorkerJobFailedParams) error {
	f.failed = append(f.failed, arg)
	return nil
}

func crawlID(b byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{b}, Valid: true} }

func TestStartOrResumeFirstStartQueuesCrawl(t *testing.T) {
	org, project, competitor, user, parent := testIDs()
	fake := &fakeCompetitorQueries{setupErr: pgx.ErrNoRows}
	crawls := 0

	outcome, err := StartOrResume(context.Background(), fake, org, project, competitor, user, parent, func(_ context.Context, gotParent pgtype.UUID) (pgtype.UUID, error) {
		crawls++
		if gotParent != parent {
			t.Fatalf("createCrawl parent = %s, want new %s", gotParent.String(), parent.String())
		}
		return crawlID(9), nil
	})
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}
	if !outcome.Started || crawls != 1 {
		t.Fatalf("started=%v crawls=%d, want true/1", outcome.Started, crawls)
	}
	if outcome.Setup.CrawlStatus != StepRunning || outcome.Setup.Status != StatusRunning {
		t.Fatalf("setup = %+v, want running/crawl running", outcome.Setup)
	}
	if len(fake.enqueues) != 0 {
		t.Fatalf("enqueues = %d, want 0 before crawl completes", len(fake.enqueues))
	}
}

func TestStartOrResumeReconcilesCompletedChildCrawl(t *testing.T) {
	org, project, competitor, user, parent := testIDs()
	fake := &fakeCompetitorQueries{
		setup: sqlc.CompetitorSetup{
			OrganizationID: org, ProjectID: project, CompetitorID: competitor,
			ParentCrawlID: parent, CrawlID: crawlID(9),
			Status: StatusRunning, CrawlStatus: StepRunning,
			KeywordGenerationStatus: StepPending, KeywordAnalysisStatus: StepPending,
		},
		child: sqlc.GetCompetitorChildCrawlForParentRow{ID: crawlID(9), Status: "completed"},
	}

	outcome, err := StartOrResume(context.Background(), fake, org, project, competitor, user, parent, nil)
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}
	if outcome.Started {
		t.Fatal("reconcile must not report Started")
	}
	if outcome.Setup.CrawlStatus != StepCompleted || outcome.Setup.KeywordGenerationStatus != StepRunning {
		t.Fatalf("setup = %+v, want crawl completed and generation running", outcome.Setup)
	}
	if len(fake.enqueues) != 1 || fake.enqueues[0].JobType != CompetitorKeywordGenerationJobType {
		t.Fatalf("enqueues = %+v, want one competitor_keyword_generation", fake.enqueues)
	}
}

func TestReconcileFailsSetupOnFailedChildCrawl(t *testing.T) {
	_, _, competitor, _, parent := testIDs()
	fake := &fakeCompetitorQueries{
		setup: sqlc.CompetitorSetup{
			CompetitorID: competitor, ParentCrawlID: parent, CrawlID: crawlID(9),
			Status: StatusRunning, CrawlStatus: StepRunning,
		},
		child: sqlc.GetCompetitorChildCrawlForParentRow{ID: crawlID(9), Status: "failed"},
	}

	setup, err := Reconcile(context.Background(), fake, competitor)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if setup.Status != StatusFailed || setup.CrawlStatus != StepFailed {
		t.Fatalf("setup = %+v, want failed at crawl", setup)
	}
	view := RowView(setup)
	if view.Status != StatusFailed || view.Steps[0].ID != StepCrawl || view.Steps[0].Status != StepFailed {
		t.Fatalf("view = %+v, want crawl failed first", view)
	}
	if view.Steps[0].Reason == "" || view.Error == "" {
		t.Fatal("failed step must carry a reason and an error")
	}
	if len(fake.enqueues) != 0 {
		t.Fatalf("enqueues = %d, want 0", len(fake.enqueues))
	}
}

func TestStartOrResumeRetryResumesFailedSteps(t *testing.T) {
	tests := []struct {
		failedStep   string
		wantStatuses []string
		wantJob      string
	}{
		{StepKeywordGeneration, []string{StatusRunning, StepCompleted, StepRunning, StepPending}, CompetitorKeywordGenerationJobType},
		{StepKeywordAnalysis, []string{StatusRunning, StepCompleted, StepCompleted, StepRunning}, CompetitorKeywordAnalysisJobType},
	}
	for _, test := range tests {
		t.Run(test.failedStep, func(t *testing.T) {
			org, project, competitor, user, parent := testIDs()
			fake := &fakeCompetitorQueries{
				setup: sqlc.CompetitorSetup{
					OrganizationID: org, ProjectID: project, CompetitorID: competitor, ParentCrawlID: parent,
					Status:                  StatusFailed,
					CrawlStatus:             StepCompleted,
					KeywordGenerationStatus: test.wantStatuses[2],
					KeywordAnalysisStatus:   StepPending,
					FailedStep:              pgtype.Text{String: test.failedStep, Valid: true},
				},
			}
			if test.failedStep == StepKeywordGeneration {
				fake.setup.KeywordGenerationStatus = StepFailed
			} else {
				fake.setup.KeywordGenerationStatus = StepCompleted
				fake.setup.KeywordAnalysisStatus = StepFailed
			}

			outcome, err := StartOrResume(context.Background(), fake, org, project, competitor, user, parent, nil)
			if err != nil {
				t.Fatalf("StartOrResume: %v", err)
			}
			got := []string{outcome.Setup.Status, outcome.Setup.CrawlStatus, outcome.Setup.KeywordGenerationStatus, outcome.Setup.KeywordAnalysisStatus}
			for i := range got {
				if got[i] != test.wantStatuses[i] {
					t.Fatalf("statuses = %v, want %v", got, test.wantStatuses)
				}
			}
			if len(fake.enqueues) != 1 || fake.enqueues[0].JobType != test.wantJob {
				t.Fatalf("enqueues = %+v, want one %s", fake.enqueues, test.wantJob)
			}
		})
	}
}

func TestStartOrResumeRetryFailedCrawlRestartsCrawl(t *testing.T) {
	org, project, competitor, user, parent := testIDs()
	fake := &fakeCompetitorQueries{
		setup: sqlc.CompetitorSetup{
			OrganizationID: org, ProjectID: project, CompetitorID: competitor, ParentCrawlID: parent, CrawlID: crawlID(9),
			Status: StatusFailed, CrawlStatus: StepFailed,
			FailedStep: pgtype.Text{String: StepCrawl, Valid: true},
		},
	}
	crawls := 0

	storedParent := parent
	outcome, err := StartOrResume(context.Background(), fake, org, project, competitor, user, parent, func(_ context.Context, gotParent pgtype.UUID) (pgtype.UUID, error) {
		crawls++
		if gotParent != storedParent {
			t.Fatalf("retry createCrawl parent = %s, want stored %s", gotParent.String(), storedParent.String())
		}
		return crawlID(10), nil
	})
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}
	if crawls != 1 || outcome.Setup.CrawlID != crawlID(10) || outcome.Setup.CrawlStatus != StepRunning {
		t.Fatalf("setup = %+v crawls=%d, want fresh crawl running", outcome.Setup, crawls)
	}
	if len(fake.enqueues) != 0 {
		t.Fatalf("enqueues = %d, want 0", len(fake.enqueues))
	}
}

func TestFinalizeGenerationSuccessAdvances(t *testing.T) {
	_, project, competitor, _, _ := testIDs()
	fake := &fakeCompetitorQueries{
		setup: sqlc.CompetitorSetup{
			ProjectID: project, CompetitorID: competitor,
			Status: StatusRunning, CrawlStatus: StepCompleted, KeywordGenerationStatus: StepRunning,
		},
	}
	job := sqlc.ClaimNextPendingAIWorkerJobRow{ProjectID: project, CompetitorID: competitor}
	if err := FinalizeGenerationSuccess(context.Background(), fake, job); err != nil {
		t.Fatalf("FinalizeGenerationSuccess: %v", err)
	}
	if len(fake.enqueues) != 1 || fake.enqueues[0].JobType != CompetitorKeywordAnalysisJobType {
		t.Fatalf("enqueues = %+v, want one competitor_keyword_analysis", fake.enqueues)
	}
	if len(fake.completed) != 1 {
		t.Fatal("generation job must complete")
	}
}

func TestFinalizeAnalysisSuccessCompletes(t *testing.T) {
	_, project, competitor, _, _ := testIDs()
	fake := &fakeCompetitorQueries{
		setup: sqlc.CompetitorSetup{
			ProjectID: project, CompetitorID: competitor,
			Status: StatusRunning, CrawlStatus: StepCompleted,
			KeywordGenerationStatus: StepCompleted, KeywordAnalysisStatus: StepRunning,
		},
	}
	job := sqlc.ClaimNextPendingAIWorkerJobRow{ProjectID: project, CompetitorID: competitor}
	if err := FinalizeAnalysisSuccess(context.Background(), fake, job); err != nil {
		t.Fatalf("FinalizeAnalysisSuccess: %v", err)
	}
	if fake.setup.Status != StatusCompleted {
		t.Fatalf("setup = %+v, want completed", fake.setup)
	}
	if len(fake.enqueues) != 0 || len(fake.completed) != 1 {
		t.Fatalf("enqueues=%d completed=%d, want 0/1", len(fake.enqueues), len(fake.completed))
	}
}

func TestMissingViewIsReady(t *testing.T) {
	view := MissingView()
	if view.Status != StatusReady {
		t.Fatalf("status = %s, want ready", view.Status)
	}
	if len(view.Steps) != 3 || view.Steps[0].ID != StepCrawl {
		t.Fatalf("steps = %+v, want the three ordered steps", view.Steps)
	}
	for _, step := range view.Steps {
		if step.Status != StepPending {
			t.Fatalf("step %+v, want pending", step)
		}
	}
}

func TestFinalizeStepFailureIsIdempotent(t *testing.T) {
	_, _, competitor, _, _ := testIDs()
	fake := &fakeCompetitorQueries{
		setup: sqlc.CompetitorSetup{CompetitorID: competitor, Status: StatusCompleted},
	}
	if err := FinalizeStepFailure(context.Background(), fake, competitor, StepKeywordGeneration, "boom"); err != nil {
		t.Fatalf("FinalizeStepFailure: %v", err)
	}
	if len(fake.updates) != 0 {
		t.Fatal("completed setup must stay untouched")
	}
}

func TestReconcileMissingSetup(t *testing.T) {
	_, _, competitor, _, _ := testIDs()
	fake := &fakeCompetitorQueries{setupErr: pgx.ErrNoRows}
	_, err := Reconcile(context.Background(), fake, competitor)
	if !errors.Is(err, ErrSetupNotFound) {
		t.Fatalf("err = %v, want ErrSetupNotFound", err)
	}
}

func TestStartOrResumeRetryUsesStoredParent(t *testing.T) {
	org, project, competitor, user, stored := testIDs()
	fresh := crawlID(7)
	fake := &fakeCompetitorQueries{
		setup: sqlc.CompetitorSetup{
			OrganizationID: org, ProjectID: project, CompetitorID: competitor,
			ParentCrawlID: stored, CrawlID: crawlID(9),
			Status: StatusFailed, CrawlStatus: StepFailed,
			FailedStep: pgtype.Text{String: StepCrawl, Valid: true},
		},
	}
	outcome, err := StartOrResume(context.Background(), fake, org, project, competitor, user, fresh, func(_ context.Context, gotParent pgtype.UUID) (pgtype.UUID, error) {
		if gotParent != stored {
			t.Fatalf("createCrawl parent = %s, want stored %s", gotParent.String(), stored.String())
		}
		if gotParent == fresh {
			t.Fatal("retry must not create the child under the newly resolved parent")
		}
		return crawlID(10), nil
	})
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}
	if outcome.Setup.ParentCrawlID != stored {
		t.Fatalf("parent = %s, want stored %s", outcome.Setup.ParentCrawlID.String(), stored.String())
	}
}

func TestStartOrResumeRepinsMissingParent(t *testing.T) {
	org, project, competitor, user, fresh := testIDs()
	fake := &fakeCompetitorQueries{
		setup: sqlc.CompetitorSetup{
			OrganizationID: org, ProjectID: project, CompetitorID: competitor,
			Status: StatusFailed, CrawlStatus: StepFailed,
			FailedStep: pgtype.Text{String: StepCrawl, Valid: true},
		},
	}
	outcome, err := StartOrResume(context.Background(), fake, org, project, competitor, user, fresh, func(_ context.Context, gotParent pgtype.UUID) (pgtype.UUID, error) {
		if gotParent != fresh {
			t.Fatalf("createCrawl parent = %s, want resolved %s", gotParent.String(), fresh.String())
		}
		return crawlID(10), nil
	})
	if err != nil {
		t.Fatalf("StartOrResume: %v", err)
	}
	if outcome.Setup.ParentCrawlID != fresh {
		t.Fatalf("parent = %s, want repinned %s", outcome.Setup.ParentCrawlID.String(), fresh.String())
	}
}

func TestFailSetupAtStepBoundsErrorMessage(t *testing.T) {
	_, _, competitor, _, _ := testIDs()
	fake := &fakeCompetitorQueries{setup: sqlc.CompetitorSetup{CompetitorID: competitor, Status: StatusRunning, CrawlStatus: StepCompleted, KeywordGenerationStatus: StepRunning}}
	long := make([]byte, 0, 600)
	for len(long) < 600 {
		long = append(long, "x"...)
	}
	setup, err := failSetupAtStep(context.Background(), fake, fake.setup, StepKeywordGeneration, string(long))
	if err != nil {
		t.Fatalf("failSetupAtStep: %v", err)
	}
	if !setup.Error.Valid || len(setup.Error.String) > 500 {
		t.Fatalf("error length = %d, want bounded to 500", len(setup.Error.String))
	}
}
