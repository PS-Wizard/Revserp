package aiaudit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type stubVisibilityProvider struct {
	mu      sync.Mutex
	prompts []string
	respond func(prompt string) (string, error)
}

func (s *stubVisibilityProvider) GenerateText(_ context.Context, prompt string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = append(s.prompts, prompt)
	if s.respond != nil {
		return s.respond(prompt)
	}
	return "1. Acme", nil
}

func (s *stubVisibilityProvider) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.prompts)
}

type fakeVisibilityQueries struct {
	audit          sqlc.AiAudit
	auditErr       error
	auditCalls     int
	gotAuditArg    sqlc.GetAIAuditForWorkerParams
	location       sqlc.ProjectLocation
	locationErr    error
	mapQueries     []sqlc.ProjectLocationQuery
	mapErr         error
	questions      []string
	questionsErr   error
	questionsCalls int
	brand          string
	profileErr     error
	inserts        []sqlc.InsertAIAuditRunParams
	statuses       []string
	statusArgs     []sqlc.UpdateAIAuditStatusParams
	failedJobs     []sqlc.MarkAIWorkerJobFailedParams
}

func (f *fakeVisibilityQueries) GetAIAuditForWorker(_ context.Context, arg sqlc.GetAIAuditForWorkerParams) (sqlc.AiAudit, error) {
	f.auditCalls++
	f.gotAuditArg = arg
	if f.auditErr != nil {
		return sqlc.AiAudit{}, f.auditErr
	}
	return f.audit, nil
}

func (f *fakeVisibilityQueries) GetLocationForAIAuditWorker(_ context.Context, arg sqlc.GetLocationForAIAuditWorkerParams) (sqlc.ProjectLocation, error) {
	if f.locationErr != nil {
		return sqlc.ProjectLocation{}, f.locationErr
	}
	return f.location, nil
}

func (f *fakeVisibilityQueries) ListEnabledMapQueriesForLocation(_ context.Context, _ sqlc.ListEnabledMapQueriesForLocationParams) ([]sqlc.ProjectLocationQuery, error) {
	if f.mapErr != nil {
		return nil, f.mapErr
	}
	return f.mapQueries, nil
}

func (f *fakeVisibilityQueries) GetProjectAIQuestions(_ context.Context, _ pgtype.UUID) (sqlc.GetProjectAIQuestionsRow, error) {
	f.questionsCalls++
	if f.questionsErr != nil {
		return sqlc.GetProjectAIQuestionsRow{}, f.questionsErr
	}
	raw, _ := json.Marshal(f.questions)
	return sqlc.GetProjectAIQuestionsRow{Questions: raw}, nil
}

func (f *fakeVisibilityQueries) GetProjectBusinessProfileByProjectID(_ context.Context, _ pgtype.UUID) (sqlc.GetProjectBusinessProfileByProjectIDRow, error) {
	if f.profileErr != nil {
		return sqlc.GetProjectBusinessProfileByProjectIDRow{}, f.profileErr
	}
	return sqlc.GetProjectBusinessProfileByProjectIDRow{BrandName: f.brand}, nil
}

func (f *fakeVisibilityQueries) UpdateAIAuditStatus(_ context.Context, arg sqlc.UpdateAIAuditStatusParams) error {
	f.statuses = append(f.statuses, arg.Status)
	f.statusArgs = append(f.statusArgs, arg)
	return nil
}

func (f *fakeVisibilityQueries) MarkAIWorkerJobFailed(_ context.Context, arg sqlc.MarkAIWorkerJobFailedParams) error {
	f.failedJobs = append(f.failedJobs, arg)
	return nil
}

func (f *fakeVisibilityQueries) InsertAIAuditRun(_ context.Context, arg sqlc.InsertAIAuditRunParams) (sqlc.AiAuditRun, error) {
	f.inserts = append(f.inserts, arg)
	return sqlc.AiAuditRun{}, nil
}

func visibilityTestUUID(b byte) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{b}, Valid: true}
}

func visibilityTestWorker(store visibilityQueries, provider *stubVisibilityProvider) *Worker {
	return &Worker{
		cfg:               config.Config{AIVisibilityModels: []string{"test-model"}},
		visibilityQueries: store,
		newVisibilityProvider: func(string) (ai.Provider, error) {
			return provider, nil
		},
	}
}

func visibilityTestJob(projectID, auditID pgtype.UUID) sqlc.ClaimNextPendingAIWorkerJobRow {
	return sqlc.ClaimNextPendingAIWorkerJobRow{
		ID:        visibilityTestUUID(9),
		JobType:   visibilityRunJobType,
		ProjectID: projectID,
		AuditID:   auditID,
	}
}

func TestDetectMentionedBranch(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		office string
		brand  string
		query  string
		want   bool
	}{
		{"full office in item", "1. Acme Downtown\n2. Other Corp", "Acme Downtown", "Acme", "best dentist downtown", true},
		{"office equal brand", "1. Acme\n2. Other Corp", "Acme", "Acme", "best dentist", false},
		{"office prefix shortening of brand", "1. Springfield Dental\n2. Other", "Springfield Dental", "Springfield Dental Care", "dentist near me", false},
		{"other office only", "1. Acme Uptown\n2. Other Corp", "Acme Downtown", "Acme", "best dentist downtown", false},
		{"locality alone", "1. Downtown Dental\n2. Other Corp", "Acme Downtown", "Acme", "best dentist downtown", false},
		{"partial brand", "1. Acme\n2. Other Corp", "Acme Downtown", "Acme", "best dentist downtown", false},
		{"query echo alone", "Results for Acme Downtown dentist:\nSorry, no list available.", "Acme Downtown", "Acme", "Acme Downtown dentist", false},
		{"unnumbered prose mention", "Acme Downtown is the top pick for this.", "Acme Downtown", "Acme", "best dentist", true},
		{"tokens across sentences", "Visit Acme. Downtown is nice.", "Acme Downtown", "Acme", "best dentist", false},
		{"cross item boundary", "1. Best Acme\n2. Downtown Dental", "Acme Downtown", "Acme", "best dentist", false},
		{"tokens across sentences in item", "1. Visit Acme. Downtown is nice.\n2. Other", "Acme Downtown", "Acme", "best dentist", false},
		{"office sentence in item", "1. Visit Acme Downtown. Best pick.\n2. Other", "Acme Downtown", "Acme", "best dentist", true},
		{"case and punctuation insensitive", "1. ACME-Downtown!\n2. Other", "acme downtown", "ACME", "dentist", true},
		{"empty office", "1. Acme Downtown", "", "Acme", "dentist", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectMentionedBranch(tc.answer, tc.office, tc.brand, tc.query); got != tc.want {
				t.Errorf("detectMentionedBranch = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseVisibilityResponseUnchanged(t *testing.T) {
	mentioned, rank, score := parseVisibilityResponse("1. Other Corp\n2. Acme\n3. Third", "Acme")
	if !mentioned || rank != 2 || score != 90 {
		t.Fatalf("numbered = (%v,%d,%d), want (true,2,90)", mentioned, rank, score)
	}
	mentioned, rank, score = parseVisibilityResponse("1. Acme First", "Acme")
	if !mentioned || rank != 1 || score != 100 {
		t.Fatalf("first = (%v,%d,%d), want (true,1,100)", mentioned, rank, score)
	}
	mentioned, rank, score = parseVisibilityResponse("Acme is great", "Acme")
	if !mentioned || rank != 0 || score != 10 {
		t.Fatalf("fallback = (%v,%d,%d), want (true,0,10)", mentioned, rank, score)
	}
	if mentioned, _, _ := parseVisibilityResponse("1. Other\n2. Third", "Acme"); mentioned {
		t.Fatal("miss must not mention target")
	}
}

func TestLocationRunsUseDistinctLocationQueries(t *testing.T) {
	projectID := visibilityTestUUID(1)
	mkWorker := func(auditByte byte, office string, texts []string, answer string) (*Worker, *stubVisibilityProvider, *fakeVisibilityQueries) {
		store := &fakeVisibilityQueries{
			audit:    sqlc.AiAudit{ID: visibilityTestUUID(auditByte), ProjectID: projectID, LocationID: visibilityTestUUID(auditByte + 10)},
			location: sqlc.ProjectLocation{ID: visibilityTestUUID(auditByte + 10), ProjectID: projectID, Name: office},
			brand:    "Acme",
		}
		for i, text := range texts {
			store.mapQueries = append(store.mapQueries, sqlc.ProjectLocationQuery{ID: visibilityTestUUID(byte(i + 20)), Text: text})
		}
		provider := &stubVisibilityProvider{respond: func(string) (string, error) { return answer, nil }}
		return visibilityTestWorker(store, provider), provider, store
	}

	wA, pA, sA := mkWorker(2, "Acme Downtown", []string{"downtown dentist map query", "downtown braces map query"}, "1. Acme Downtown\n2. Other")
	wB, pB, sB := mkWorker(3, "Acme Uptown", []string{"uptown dentist map query"}, "1. Acme Uptown\n2. Other")

	jobA := visibilityTestJob(projectID, visibilityTestUUID(2))
	if status, err := wA.handleVisibilityRun(context.Background(), jobA); err != nil || status != "completed" {
		t.Fatalf("location A run = (%q,%v), want (completed,nil)", status, err)
	}
	jobB := visibilityTestJob(projectID, visibilityTestUUID(3))
	if status, err := wB.handleVisibilityRun(context.Background(), jobB); err != nil || status != "completed" {
		t.Fatalf("location B run = (%q,%v), want (completed,nil)", status, err)
	}

	for _, text := range []string{"downtown dentist map query", "downtown braces map query"} {
		found := false
		for _, prompt := range pA.prompts {
			if strings.Contains(prompt, "Question: "+text) {
				found = true
			}
			if strings.Contains(prompt, "uptown dentist map query") {
				t.Fatal("location A provider saw location B query text")
			}
		}
		if !found {
			t.Fatalf("location A provider never got verbatim query %q", text)
		}
	}
	if len(pB.prompts) != 1 || !strings.Contains(pB.prompts[0], "Question: uptown dentist map query") {
		t.Fatalf("location B prompts = %v, want exactly its verbatim query", pB.prompts)
	}
	if sA.questionsCalls != 0 || sB.questionsCalls != 0 {
		t.Fatal("location runs must not fall back to project questions")
	}
	if len(sA.inserts) != 2 || sA.inserts[0].QuestionText != "downtown dentist map query" {
		t.Fatalf("location A inserts = %+v, want verbatim query texts", sA.inserts)
	}
	for _, in := range append(sA.inserts, sB.inserts...) {
		if !in.MentionedBranch.Valid || !in.MentionedBranch.Bool {
			t.Fatalf("location insert %+v must carry mentioned_branch true", in)
		}
	}
}

func TestLocationRunValidatesQueryCountBeforeProvider(t *testing.T) {
	for _, n := range []int{0, 6} {
		store := &fakeVisibilityQueries{
			audit:    sqlc.AiAudit{ID: visibilityTestUUID(2), LocationID: visibilityTestUUID(12)},
			location: sqlc.ProjectLocation{Name: "Acme Downtown"},
			brand:    "Acme",
		}
		for i := 0; i < n; i++ {
			store.mapQueries = append(store.mapQueries, sqlc.ProjectLocationQuery{Text: "q"})
		}
		provider := &stubVisibilityProvider{}
		w := visibilityTestWorker(store, provider)
		if _, err := w.handleVisibilityRun(context.Background(), visibilityTestJob(visibilityTestUUID(1), visibilityTestUUID(2))); err == nil {
			t.Fatalf("n=%d: want validation error", n)
		}
		if provider.calls() != 0 || len(store.inserts) != 0 {
			t.Fatalf("n=%d: provider calls=%d inserts=%d, want none before validation", n, provider.calls(), len(store.inserts))
		}
	}
}

func TestVisibilityRunLoadsAuditBeforeProvider(t *testing.T) {
	store := &fakeVisibilityQueries{auditErr: pgx.ErrNoRows}
	provider := &stubVisibilityProvider{}
	w := visibilityTestWorker(store, provider)
	if _, err := w.handleVisibilityRun(context.Background(), visibilityTestJob(visibilityTestUUID(1), visibilityTestUUID(2))); err == nil {
		t.Fatal("missing audit must fail")
	}
	if provider.calls() != 0 {
		t.Fatal("no provider call may precede the audit load")
	}
	if arg := store.gotAuditArg; arg.ID != visibilityTestUUID(2) || arg.ProjectID != visibilityTestUUID(1) {
		t.Fatalf("audit load arg = %+v, want audit+project scope", arg)
	}
}

func TestMentionedBranchNullForProjectAndFailedRuns(t *testing.T) {
	projectID := visibilityTestUUID(1)
	store := &fakeVisibilityQueries{
		audit:     sqlc.AiAudit{ID: visibilityTestUUID(2), ProjectID: projectID},
		questions: []string{"best widgets?"},
		brand:     "Acme",
	}
	provider := &stubVisibilityProvider{respond: func(string) (string, error) { return "1. Acme\n2. Other", nil }}
	w := visibilityTestWorker(store, provider)
	if _, err := w.handleVisibilityRun(context.Background(), visibilityTestJob(projectID, visibilityTestUUID(2))); err != nil {
		t.Fatalf("project run: %v", err)
	}
	if len(store.inserts) != 1 || store.inserts[0].MentionedBranch.Valid {
		t.Fatalf("project insert = %+v, want mentioned_branch NULL", store.inserts)
	}

	failStore := &fakeVisibilityQueries{
		audit:    sqlc.AiAudit{ID: visibilityTestUUID(2), ProjectID: projectID, LocationID: visibilityTestUUID(12)},
		location: sqlc.ProjectLocation{Name: "Acme Downtown"},
		brand:    "Acme",
		mapQueries: []sqlc.ProjectLocationQuery{
			{Text: "downtown dentist map query"},
		},
	}
	failProvider := &stubVisibilityProvider{respond: func(string) (string, error) { return "", errors.New("boom") }}
	wFail := visibilityTestWorker(failStore, failProvider)
	if status, err := wFail.handleVisibilityRun(context.Background(), visibilityTestJob(projectID, visibilityTestUUID(2))); err != nil || status != "failed" {
		t.Fatalf("failed run = (%q,%v), want (failed,nil)", status, err)
	}
	if len(failStore.inserts) != 1 || failStore.inserts[0].MentionedBranch.Valid {
		t.Fatalf("failed insert = %+v, want mentioned_branch NULL", failStore.inserts)
	}
	if failStore.inserts[0].Status != "failed" {
		t.Fatalf("failed insert status = %q, want failed", failStore.inserts[0].Status)
	}
}

func TestIsLocationVisibilityJobFailClosed(t *testing.T) {
	projectID := visibilityTestUUID(1)
	locationStore := &fakeVisibilityQueries{audit: sqlc.AiAudit{LocationID: visibilityTestUUID(12)}}
	w := visibilityTestWorker(locationStore, &stubVisibilityProvider{})
	isLoc, err := w.isLocationVisibilityJob(context.Background(), visibilityTestJob(projectID, visibilityTestUUID(2)))
	if err != nil || !isLoc {
		t.Fatalf("location audit = (%v,%v), want (true,nil)", isLoc, err)
	}

	projectStore := &fakeVisibilityQueries{audit: sqlc.AiAudit{}}
	wProject := visibilityTestWorker(projectStore, &stubVisibilityProvider{})
	isLoc, err = wProject.isLocationVisibilityJob(context.Background(), visibilityTestJob(projectID, visibilityTestUUID(2)))
	if err != nil || isLoc {
		t.Fatalf("project audit = (%v,%v), want (false,nil)", isLoc, err)
	}

	errStore := &fakeVisibilityQueries{auditErr: errors.New("transient read failure")}
	wErr := visibilityTestWorker(errStore, &stubVisibilityProvider{})
	if _, err := wErr.isLocationVisibilityJob(context.Background(), visibilityTestJob(projectID, visibilityTestUUID(2))); err == nil {
		t.Fatal("scope lookup failure must propagate, never route to setup")
	}

	legacy := sqlc.ClaimNextPendingAIWorkerJobRow{JobType: visibilityRunJobType, ProjectID: projectID}
	isLoc, err = wErr.isLocationVisibilityJob(context.Background(), legacy)
	if err != nil || isLoc {
		t.Fatalf("job without audit id = (%v,%v), want legacy (false,nil)", isLoc, err)
	}
}

func TestLocationFailureFinalizationFailsQueuedAudit(t *testing.T) {
	projectID := visibilityTestUUID(1)
	auditID := visibilityTestUUID(2)
	locationID := visibilityTestUUID(12)
	started := pgtype.Timestamptz{Time: time.Now().Add(-time.Minute).UTC(), Valid: true}
	store := &fakeVisibilityQueries{
		audit:    sqlc.AiAudit{ID: auditID, ProjectID: projectID, LocationID: locationID, Status: "queued", StartedAt: started},
		location: sqlc.ProjectLocation{ID: locationID, ProjectID: projectID, Name: "Acme Downtown"},
		brand:    "Acme",
	}
	provider := &stubVisibilityProvider{}
	w := visibilityTestWorker(store, provider)
	job := visibilityTestJob(projectID, auditID)
	if _, err := w.handleVisibilityRun(context.Background(), job); err == nil {
		t.Fatal("zero map queries must fail the run")
	}
	if err := failLocationVisibilityAuditTx(context.Background(), store, job, "no queries"); err != nil {
		t.Fatalf("failLocationVisibilityAuditTx: %v", err)
	}
	if len(store.statusArgs) != 1 {
		t.Fatalf("status updates = %d, want exactly the terminal audit failure", len(store.statusArgs))
	}
	got := store.statusArgs[0]
	if got.ID != auditID || got.Status != "failed" {
		t.Fatalf("audit update = %+v, want failed for the location audit", got)
	}
	if got.StartedAt != started {
		t.Fatalf("audit started_at = %+v, want preserved %+v", got.StartedAt, started)
	}
	if !got.CompletedAt.Valid || !got.ErrorMessage.Valid || got.ErrorMessage.String == "" {
		t.Fatalf("audit update = %+v, want completed_at and error", got)
	}
	if len(store.failedJobs) != 1 || store.failedJobs[0].ID != job.ID {
		t.Fatalf("failed jobs = %+v, want the visibility job", store.failedJobs)
	}
	if got.Status == "queued" || got.Status == "running" {
		t.Fatal("audit must leave the active states so a later rerun is not blocked")
	}
}

func TestLocationFailureFinalizationSkipsTerminalAudit(t *testing.T) {
	projectID := visibilityTestUUID(1)
	auditID := visibilityTestUUID(2)
	store := &fakeVisibilityQueries{
		audit: sqlc.AiAudit{ID: auditID, ProjectID: projectID, LocationID: visibilityTestUUID(12), Status: "completed"},
	}
	job := visibilityTestJob(projectID, auditID)
	if err := failLocationVisibilityAuditTx(context.Background(), store, job, "late failure"); err != nil {
		t.Fatalf("failLocationVisibilityAuditTx: %v", err)
	}
	if len(store.statusArgs) != 0 {
		t.Fatalf("terminal audit must not regress, updates = %+v", store.statusArgs)
	}
	if len(store.failedJobs) != 1 {
		t.Fatalf("job must still be marked failed, got %+v", store.failedJobs)
	}
}

func TestLocationFailureFinalizationWithoutAudit(t *testing.T) {
	store := &fakeVisibilityQueries{auditErr: pgx.ErrNoRows}
	job := visibilityTestJob(visibilityTestUUID(1), visibilityTestUUID(2))
	if err := failLocationVisibilityAuditTx(context.Background(), store, job, "gone"); err != nil {
		t.Fatalf("missing audit must still fail the job: %v", err)
	}
	if len(store.statusArgs) != 0 || len(store.failedJobs) != 1 {
		t.Fatalf("want job-only failure, updates=%d failed=%d", len(store.statusArgs), len(store.failedJobs))
	}
}
