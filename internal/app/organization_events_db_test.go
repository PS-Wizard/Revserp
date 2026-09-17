package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func newOrgEventsTestDB(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
	t.Helper()

	if _, currentFilePath, _, ok := runtime.Caller(0); ok {
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFilePath), "..", ".."))
		_ = godotenv.Load(filepath.Join(repoRoot, ".env"))
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	t.Cleanup(pool.Close)

	return sqlc.New(pool), pool, ctx
}

func createOrgEventsTestScope(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (orgID, userID, projectID pgtype.UUID) {
	t.Helper()

	if err := pool.QueryRow(ctx,
		`INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', gen_random_uuid()::text, 'org-events@example.com') RETURNING id`).Scan(&userID); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name) VALUES ('org-events-test') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatalf("create test organization: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, userID); err != nil {
		t.Fatalf("add test member: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO projects (organization_id, name, base_url) VALUES ($1, 'org-events-project', 'https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create test project: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return orgID, userID, projectID
}

func listOrganizationEvents(t *testing.T, ctx context.Context, queries *sqlc.Queries, userID, orgID pgtype.UUID, after int64) []sqlc.OrganizationEvent {
	t.Helper()
	events, err := queries.ListOrganizationEventsForUser(ctx, sqlc.ListOrganizationEventsForUserParams{
		UserID:         userID,
		OrganizationID: orgID,
		AfterID:        after,
		MaxRows:        200,
	})
	if err != nil {
		t.Fatalf("list organization events: %v", err)
	}
	return events
}

func findOrganizationEvent(t *testing.T, events []sqlc.OrganizationEvent, eventType string) sqlc.OrganizationEvent {
	t.Helper()
	for _, event := range events {
		if event.EventType == eventType {
			return event
		}
	}
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.EventType)
	}
	t.Fatalf("event %q not found; got %v", eventType, types)
	return sqlc.OrganizationEvent{}
}

func TestOrganizationCrawlEventMapping(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, userID, projectID := createOrgEventsTestScope(t, ctx, pool)

	crawl, err := queries.CreateCrawl(ctx, sqlc.CreateCrawlParams{
		ProjectID:         projectID,
		RequestedByUserID: userID,
		Source:            "mcp",
		Status:            "queued",
		ConfigSnapshot:    []byte("{}"),
	})
	if err != nil {
		t.Fatalf("create crawl: %v", err)
	}

	queued := findOrganizationEvent(t, listOrganizationEvents(t, ctx, queries, userID, orgID, 0), "crawl.queued")
	if queued.ResourceID != crawl.ID {
		t.Errorf("queued resource_id = %v, want crawl id", queued.ResourceID)
	}
	var payload map[string]any
	if err := json.Unmarshal(queued.Payload, &payload); err != nil {
		t.Fatalf("decode queued payload: %v", err)
	}
	if payload["status"] != "queued" || payload["source"] != "mcp" {
		t.Errorf("queued payload = %v", payload)
	}

	if err := queries.MarkCrawlRunning(ctx, crawl.ID); err != nil {
		t.Fatalf("mark crawl running: %v", err)
	}
	if _, err := queries.UpdateCrawlProgress(ctx, sqlc.UpdateCrawlProgressParams{ID: crawl.ID, UrlsCrawled: 5, UrlsDiscovered: 10}); err != nil {
		t.Fatalf("update crawl progress: %v", err)
	}
	if err := queries.MarkCrawlCompleted(ctx, sqlc.MarkCrawlCompletedParams{ID: crawl.ID, UrlsDiscovered: 10, UrlsCrawled: 10}); err != nil {
		t.Fatalf("mark crawl completed: %v", err)
	}

	events := listOrganizationEvents(t, ctx, queries, userID, orgID, queued.ID)
	started := findOrganizationEvent(t, events, "crawl.started")
	progress := findOrganizationEvent(t, events, "crawl.progress")
	completed := findOrganizationEvent(t, events, "crawl.completed")

	if err := json.Unmarshal(progress.Payload, &payload); err != nil {
		t.Fatalf("decode progress payload: %v", err)
	}
	if payload["urls_crawled"] != float64(5) || payload["urls_discovered"] != float64(10) {
		t.Errorf("progress payload = %v", payload)
	}
	if !(started.ID < progress.ID && progress.ID < completed.ID) {
		t.Errorf("crawl events out of order: started=%d progress=%d completed=%d", started.ID, progress.ID, completed.ID)
	}
}

func TestOrganizationAIAuditAndJobEventMapping(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, userID, projectID := createOrgEventsTestScope(t, ctx, pool)

	audit, err := queries.CreateAIAudit(ctx, sqlc.CreateAIAuditParams{ProjectID: projectID, Status: "queued"})
	if err != nil {
		t.Fatalf("create ai audit: %v", err)
	}
	if err := queries.UpdateAIAuditStatus(ctx, sqlc.UpdateAIAuditStatusParams{ID: audit.ID, Status: "running"}); err != nil {
		t.Fatalf("mark audit running: %v", err)
	}
	if err := queries.UpdateAIAuditStatus(ctx, sqlc.UpdateAIAuditStatusParams{ID: audit.ID, Status: "completed_with_failures"}); err != nil {
		t.Fatalf("complete audit: %v", err)
	}
	if _, err := queries.InsertAIAuditRun(ctx, sqlc.InsertAIAuditRunParams{
		AuditID: audit.ID, QuestionText: "q", DisplayOrder: 1, ModelName: "m", Status: "success",
	}); err != nil {
		t.Fatalf("insert ai audit run: %v", err)
	}

	job, err := queries.EnqueueAIWorkerJob(ctx, sqlc.EnqueueAIWorkerJobParams{JobType: "prompt_generation", ProjectID: projectID})
	if err != nil {
		t.Fatalf("enqueue prompt job: %v", err)
	}
	if err := queries.MarkAIWorkerJobCompleted(ctx, job.ID); err != nil {
		t.Fatalf("complete prompt job: %v", err)
	}
	// visibility_run must not emit lifecycle events (ai_audits is authoritative).
	if _, err := queries.EnqueueAIWorkerJob(ctx, sqlc.EnqueueAIWorkerJobParams{JobType: "visibility_run", ProjectID: projectID, AuditID: audit.ID}); err != nil {
		t.Fatalf("enqueue visibility job: %v", err)
	}

	events := listOrganizationEvents(t, ctx, queries, userID, orgID, 0)
	for _, want := range []string{"ai_audit.queued", "ai_audit.started", "ai_audit.completed_with_failures", "ai_audit.progress", "prompt_generation.queued", "prompt_generation.completed"} {
		findOrganizationEvent(t, events, want)
	}
}

func TestOrganizationMembershipScopedReads(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, userID, projectID := createOrgEventsTestScope(t, ctx, pool)

	if _, err := queries.CreateCrawl(ctx, sqlc.CreateCrawlParams{
		ProjectID: projectID, RequestedByUserID: userID, Source: "manual", Status: "queued", ConfigSnapshot: []byte("{}"),
	}); err != nil {
		t.Fatalf("create crawl: %v", err)
	}

	var outsiderID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', gen_random_uuid()::text, 'org-events-outsider@example.com') RETURNING id`).Scan(&outsiderID); err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, outsiderID)
	})

	if events := listOrganizationEvents(t, ctx, queries, outsiderID, orgID, 0); len(events) != 0 {
		t.Fatalf("non-member read %d events, want 0", len(events))
	}
	head, err := queries.GetOrganizationEventHeadForUser(ctx, sqlc.GetOrganizationEventHeadForUserParams{UserID: outsiderID, OrganizationID: orgID})
	if err != nil {
		t.Fatalf("non-member head: %v", err)
	}
	if head != 0 {
		t.Fatalf("non-member head = %d, want 0", head)
	}

	memberHead, err := queries.GetOrganizationEventHeadForUser(ctx, sqlc.GetOrganizationEventHeadForUserParams{UserID: userID, OrganizationID: orgID})
	if err != nil {
		t.Fatalf("member head: %v", err)
	}
	if memberHead == 0 {
		t.Fatal("member head = 0, want > 0")
	}
}

func TestOrganizationFreshHeadNoReplayAndBacklog(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, userID, projectID := createOrgEventsTestScope(t, ctx, pool)

	createCrawl := func() {
		t.Helper()
		if _, err := queries.CreateCrawl(ctx, sqlc.CreateCrawlParams{
			ProjectID: projectID, RequestedByUserID: userID, Source: "manual", Status: "queued", ConfigSnapshot: []byte("{}"),
		}); err != nil {
			t.Fatalf("create crawl: %v", err)
		}
	}
	createCrawl()
	createCrawl()

	// A fresh request starts at the current head and must not replay.
	head, err := queries.GetOrganizationEventHeadForUser(ctx, sqlc.GetOrganizationEventHeadForUserParams{UserID: userID, OrganizationID: orgID})
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if replay := listOrganizationEvents(t, ctx, queries, userID, orgID, head); len(replay) != 0 {
		t.Fatalf("fresh head replayed %d events, want 0", len(replay))
	}

	// Reconnect drains the backlog after an older cursor.
	backlog := listOrganizationEvents(t, ctx, queries, userID, orgID, 0)
	if len(backlog) < 3 {
		t.Fatalf("backlog = %d events, want >= 3 (project.created + 2 crawls)", len(backlog))
	}
	tail := listOrganizationEvents(t, ctx, queries, userID, orgID, backlog[len(backlog)-1].ID)
	if len(tail) != 0 {
		t.Fatalf("tail after latest = %d, want 0", len(tail))
	}

	// New activity after the cursor is delivered in id order.
	createCrawl()
	tail = listOrganizationEvents(t, ctx, queries, userID, orgID, head)
	if len(tail) != 1 || tail[0].EventType != "crawl.queued" {
		t.Fatalf("post-cursor events = %v, want one crawl.queued", tail)
	}
}

func TestOrganizationProjectDeletedRetainsProjectID(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, userID, projectID := createOrgEventsTestScope(t, ctx, pool)

	if _, err := pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	events := listOrganizationEvents(t, ctx, queries, userID, orgID, 0)
	deleted := findOrganizationEvent(t, events, "project.deleted")
	if !deleted.ProjectID.Valid || deleted.ProjectID != projectID {
		t.Fatalf("project.deleted project_id = %v, want %v", deleted.ProjectID, projectID)
	}
}

func TestOrganizationProfileCompetitorMapsEventMapping(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, userID, projectID := createOrgEventsTestScope(t, ctx, pool)

	if _, err := queries.UpsertProjectBusinessProfile(ctx, sqlc.UpsertProjectBusinessProfileParams{
		ProjectID:           projectID,
		BrandName:           "Acme",
		WebsiteUrl:          "https://example.com",
		BusinessCompetitors: []byte("[]"),
		BrandedKeywords:     []byte("[]"),
		NonBrandedKeywords:  []byte("[]"),
		SeedPrompts:         []byte("[]"),
		TargetKeywords:      []byte("[]"),
	}); err != nil {
		t.Fatalf("upsert business profile: %v", err)
	}

	competitor, err := queries.InsertProjectCompetitor(ctx, sqlc.InsertProjectCompetitorParams{
		ProjectID: projectID, SeedUrl: "https://rival.example.com", Name: "Rival",
	})
	if err != nil {
		t.Fatalf("insert competitor: %v", err)
	}
	if _, err := queries.DeleteProjectCompetitorForUser(ctx, sqlc.DeleteProjectCompetitorForUserParams{
		CompetitorID: competitor.ID, UserID: userID, ProjectID: projectID,
	}); err != nil {
		t.Fatalf("delete competitor: %v", err)
	}

	check, err := queries.CreateMapsVisibilityCheck(ctx, sqlc.CreateMapsVisibilityCheckParams{ProjectID: projectID, Question: "best plumber"})
	if err != nil {
		t.Fatalf("create maps check: %v", err)
	}
	if err := queries.MarkMapsVisibilityCheckRunning(ctx, check.ID); err != nil {
		t.Fatalf("mark maps check running: %v", err)
	}
	if err := queries.MarkMapsVisibilityCheckCompleted(ctx, sqlc.MarkMapsVisibilityCheckCompletedParams{ID: check.ID}); err != nil {
		t.Fatalf("mark maps check completed: %v", err)
	}

	events := listOrganizationEvents(t, ctx, queries, userID, orgID, 0)
	for _, want := range []string{
		"business_profile.updated",
		"project_competitor.created",
		"project_competitor.deleted",
		"maps_visibility.queued",
		"maps_visibility.started",
		"maps_visibility.completed",
	} {
		findOrganizationEvent(t, events, want)
	}
}

func TestOrganizationEventListenerWakesSubscriber(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, userID, projectID := createOrgEventsTestScope(t, ctx, pool)

	application := &App{DB: pool, Queries: queries, OrgEvents: newOrganizationEventHub()}
	listenCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	application.StartOrganizationEventHub(listenCtx)

	wake, unsubscribe := application.OrgEvents.subscribe(orgID)
	defer unsubscribe()

	insert := func() {
		_, err := queries.CreateCrawl(ctx, sqlc.CreateCrawlParams{
			ProjectID: projectID, RequestedByUserID: userID, Source: "manual", Status: "queued", ConfigSnapshot: []byte("{}"),
		})
		if err != nil {
			t.Fatalf("create crawl: %v", err)
		}
	}
	insert()

	deadline := time.After(8 * time.Second)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-wake:
			return
		case <-ticker.C:
			insert()
		case <-deadline:
			t.Fatal("listener did not wake subscriber")
		}
	}
}

func TestOrganizationAIAuditProgressAdvancesOnRunUpdate(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, userID, projectID := createOrgEventsTestScope(t, ctx, pool)

	audit, err := queries.CreateAIAudit(ctx, sqlc.CreateAIAuditParams{ProjectID: projectID, Status: "queued"})
	if err != nil {
		t.Fatalf("create ai audit: %v", err)
	}
	run, err := queries.InsertAIAuditRun(ctx, sqlc.InsertAIAuditRunParams{
		AuditID: audit.ID, QuestionText: "q", DisplayOrder: 1, ModelName: "m", Status: "pending",
	})
	if err != nil {
		t.Fatalf("insert pending run: %v", err)
	}

	first := findOrganizationEvent(t, listOrganizationEvents(t, ctx, queries, userID, orgID, 0), "ai_audit.progress")
	var payload map[string]any
	if err := json.Unmarshal(first.Payload, &payload); err != nil {
		t.Fatalf("decode progress payload: %v", err)
	}
	if payload["completed"] != float64(0) || payload["total"] != float64(1) {
		t.Fatalf("pending progress = %v, want completed 0 total 1", payload)
	}

	if _, err := pool.Exec(ctx, `UPDATE ai_audit_runs SET status = 'success', updated_at = now() WHERE id = $1`, run.ID); err != nil {
		t.Fatalf("update run status: %v", err)
	}

	after := listOrganizationEvents(t, ctx, queries, userID, orgID, first.ID)
	advanced := findOrganizationEvent(t, after, "ai_audit.progress")
	if err := json.Unmarshal(advanced.Payload, &payload); err != nil {
		t.Fatalf("decode advanced progress payload: %v", err)
	}
	if payload["completed"] != float64(1) || payload["total"] != float64(1) {
		t.Fatalf("advanced progress = %v, want completed 1 total 1", payload)
	}

	// A no-op status write must not emit another progress event.
	if _, err := pool.Exec(ctx, `UPDATE ai_audit_runs SET status = 'success' WHERE id = $1`, run.ID); err != nil {
		t.Fatalf("no-op update: %v", err)
	}
	if extra := listOrganizationEvents(t, ctx, queries, userID, orgID, advanced.ID); len(extra) != 0 {
		t.Fatalf("no-op update emitted %d events, want 0", len(extra))
	}
}

func TestOrganizationFailurePayloads(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, userID, projectID := createOrgEventsTestScope(t, ctx, pool)

	audit, err := queries.CreateAIAudit(ctx, sqlc.CreateAIAuditParams{ProjectID: projectID, Status: "queued"})
	if err != nil {
		t.Fatalf("create ai audit: %v", err)
	}
	if err := queries.UpdateAIAuditStatus(ctx, sqlc.UpdateAIAuditStatusParams{
		ID: audit.ID, Status: "failed", ErrorMessage: pgtype.Text{String: "audit boom", Valid: true},
	}); err != nil {
		t.Fatalf("fail ai audit: %v", err)
	}

	job, err := queries.EnqueueAIWorkerJob(ctx, sqlc.EnqueueAIWorkerJobParams{JobType: "prompt_generation", ProjectID: projectID})
	if err != nil {
		t.Fatalf("enqueue prompt job: %v", err)
	}
	if err := queries.MarkAIWorkerJobFailed(ctx, sqlc.MarkAIWorkerJobFailedParams{ID: job.ID, ErrorMessage: pgtype.Text{String: "prompt boom", Valid: true}}); err != nil {
		t.Fatalf("fail prompt job: %v", err)
	}

	check, err := queries.CreateMapsVisibilityCheck(ctx, sqlc.CreateMapsVisibilityCheckParams{ProjectID: projectID, Question: "q"})
	if err != nil {
		t.Fatalf("create maps check: %v", err)
	}
	if err := queries.MarkMapsVisibilityCheckFailed(ctx, sqlc.MarkMapsVisibilityCheckFailedParams{ID: check.ID, Error: pgtype.Text{String: "maps boom", Valid: true}}); err != nil {
		t.Fatalf("fail maps check: %v", err)
	}

	events := listOrganizationEvents(t, ctx, queries, userID, orgID, 0)
	assertError := func(eventType, want string) {
		t.Helper()
		event := findOrganizationEvent(t, events, eventType)
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode %s payload: %v", eventType, err)
		}
		if payload["error"] != want {
			t.Fatalf("%s payload error = %v, want %q", eventType, payload["error"], want)
		}
		if _, ok := payload["status"]; !ok {
			t.Fatalf("%s payload missing status: %v", eventType, payload)
		}
	}
	assertError("ai_audit.failed", "audit boom")
	assertError("prompt_generation.failed", "prompt boom")
	assertError("maps_visibility.failed", "maps boom")
}

func organizationEventsRequest(t *testing.T, userID, orgID pgtype.UUID, query string) *http.Request {
	t.Helper()
	ctx := withPrincipal(context.Background(), Principal{User: sqlc.User{ID: userID, Status: "active"}})
	req := httptest.NewRequest(http.MethodGet, "/organizations/"+orgID.String()+"/events"+query, nil).WithContext(ctx)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("organizationID", orgID.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// lockedEventRecorder lets the test read the SSE body while the handler is
// still streaming, without racing the handler's writes.
type lockedEventRecorder struct {
	rec   *httptest.ResponseRecorder
	mu    sync.Mutex
	wrote chan struct{}
	once  sync.Once
}

func newLockedEventRecorder() *lockedEventRecorder {
	return &lockedEventRecorder{rec: httptest.NewRecorder(), wrote: make(chan struct{})}
}

func (l *lockedEventRecorder) Header() http.Header  { return l.rec.Header() }
func (l *lockedEventRecorder) WriteHeader(code int) { l.rec.WriteHeader(code) }
func (l *lockedEventRecorder) Flush()               { l.rec.Flush() }

func (l *lockedEventRecorder) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.rec.Write(b)
	l.once.Do(func() { close(l.wrote) })
	return n, err
}

func (l *lockedEventRecorder) body() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rec.Body.String()
}

// runOrganizationEventsStream invokes the real handler and cancels it once the
// body contains waitFor (or fails on timeout).
func runOrganizationEventsStream(t *testing.T, application *App, userID, orgID pgtype.UUID, query, waitFor string) *lockedEventRecorder {
	t.Helper()
	rec := newLockedEventRecorder()
	base := organizationEventsRequest(t, userID, orgID, query)
	reqCtx, cancel := context.WithCancel(base.Context())
	defer cancel()
	req := base.WithContext(reqCtx)

	done := make(chan struct{})
	go func() {
		application.handleListOrganizationEvents(rec, req)
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	for !strings.Contains(rec.body(), waitFor) {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("handler never emitted %q; body=%q", waitFor, rec.body())
		case <-time.After(20 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not return after cancel")
	}
	return rec
}

func TestHandleListOrganizationEventsNonMember404(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, _, _ := createOrgEventsTestScope(t, ctx, pool)

	var outsiderID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', gen_random_uuid()::text, 'org-events-handler-outsider@example.com') RETURNING id`).Scan(&outsiderID); err != nil {
		t.Fatalf("create outsider: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, outsiderID)
	})

	application := &App{DB: pool, Queries: queries, OrgEvents: newOrganizationEventHub()}
	rec := httptest.NewRecorder()
	application.handleListOrganizationEvents(rec, organizationEventsRequest(t, outsiderID, orgID, ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-member status = %d, want 404", rec.Code)
	}
}

func TestHandleListOrganizationEventsFreshReadyNoReplay(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, userID, projectID := createOrgEventsTestScope(t, ctx, pool)
	if _, err := queries.CreateCrawl(ctx, sqlc.CreateCrawlParams{
		ProjectID: projectID, RequestedByUserID: userID, Source: "manual", Status: "queued", ConfigSnapshot: []byte("{}"),
	}); err != nil {
		t.Fatalf("create crawl: %v", err)
	}

	application := &App{DB: pool, Queries: queries, OrgEvents: newOrganizationEventHub()}
	rec := runOrganizationEventsStream(t, application, userID, orgID, "", "event: ready")

	body := rec.body()
	if !strings.Contains(body, "data: {\"cursor\":\"") {
		t.Fatalf("ready frame is not a string cursor: %q", body)
	}
	if strings.Contains(body, "event: crawl.queued") {
		t.Fatalf("fresh stream replayed events: %q", body)
	}
}

func TestHandleListOrganizationEventsReconnectBacklog(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, userID, projectID := createOrgEventsTestScope(t, ctx, pool)
	if _, err := queries.CreateCrawl(ctx, sqlc.CreateCrawlParams{
		ProjectID: projectID, RequestedByUserID: userID, Source: "mcp", Status: "queued", ConfigSnapshot: []byte("{}"),
	}); err != nil {
		t.Fatalf("create crawl: %v", err)
	}

	application := &App{DB: pool, Queries: queries, OrgEvents: newOrganizationEventHub()}
	rec := runOrganizationEventsStream(t, application, userID, orgID, "?after=0", "event: crawl.queued")

	body := rec.body()
	if !strings.Contains(body, "event: ready") || !strings.Contains(body, "data: {\"cursor\":\"0\"}") {
		t.Fatalf("ready frame = %q, want cursor 0", body)
	}
	if !strings.Contains(body, "event: crawl.queued") {
		t.Fatalf("reconnect backlog missing: %q", body)
	}
}

// createOrgEventsCrawl inserts a crawl (and therefore an organization_events
// row via trigger) inside the given queries handle.
func createOrgEventsCrawl(ctx context.Context, queries *sqlc.Queries, projectID, userID pgtype.UUID, source string) (sqlc.CreateCrawlRow, error) {
	return queries.CreateCrawl(ctx, sqlc.CreateCrawlParams{
		ProjectID: projectID, RequestedByUserID: userID, Source: source, Status: "queued", ConfigSnapshot: []byte("{}"),
	})
}

// TestOrganizationEventInsertSerializesPerOrg proves two concurrent inserts
// for the same organization cannot commit out of id order. Without the
// per-organization advisory lock, B would complete while A is still open.
func TestOrganizationEventInsertSerializesPerOrg(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	orgID, userID, projectID := createOrgEventsTestScope(t, ctx, pool)

	txA, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txA: %v", err)
	}
	defer func() { _ = txA.Rollback(ctx) }()
	crawlA, err := createOrgEventsCrawl(ctx, queries.WithTx(txA), projectID, userID, "manual")
	if err != nil {
		t.Fatalf("txA create crawl: %v", err)
	}

	txB, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txB: %v", err)
	}
	defer func() { _ = txB.Rollback(ctx) }()

	type crawlResult struct {
		crawl sqlc.CreateCrawlRow
		err   error
	}
	bDone := make(chan crawlResult, 1)
	go func() {
		crawl, err := createOrgEventsCrawl(ctx, queries.WithTx(txB), projectID, userID, "mcp")
		bDone <- crawlResult{crawl: crawl, err: err}
	}()

	// B must stay blocked behind A's transaction-scoped lock. Confirm causally
	// that B is waiting on an advisory lock rather than sleeping blindly.
	pidB := txB.Conn().PgConn().PID()
	deadline := time.Now().Add(5 * time.Second)
	blockedOnAdvisory := false
	for time.Now().Before(deadline) {
		var waitEventType, waitEvent pgtype.Text
		if err := pool.QueryRow(ctx, `SELECT wait_event_type, wait_event FROM pg_stat_activity WHERE pid = $1`, int64(pidB)).Scan(&waitEventType, &waitEvent); err != nil {
			t.Fatalf("read pg_stat_activity: %v", err)
		}
		if waitEventType.String == "Lock" && waitEvent.String == "advisory" {
			blockedOnAdvisory = true
			break
		}
		select {
		case r := <-bDone:
			t.Fatalf("txB completed before txA committed (err=%v)", r.err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blockedOnAdvisory {
		t.Fatal("txB never waited on the organization advisory lock")
	}

	if err := txA.Commit(ctx); err != nil {
		t.Fatalf("commit txA: %v", err)
	}

	var crawlB sqlc.CreateCrawlRow
	select {
	case r := <-bDone:
		if r.err != nil {
			t.Fatalf("txB create crawl: %v", r.err)
		}
		crawlB = r.crawl
	case <-time.After(5 * time.Second):
		t.Fatal("txB did not complete after txA committed")
	}
	if err := txB.Commit(ctx); err != nil {
		t.Fatalf("commit txB: %v", err)
	}

	events := listOrganizationEvents(t, ctx, queries, userID, orgID, 0)
	var eventA, eventB sqlc.OrganizationEvent
	for _, event := range events {
		if event.EventType != "crawl.queued" {
			continue
		}
		switch event.ResourceID {
		case crawlA.ID:
			eventA = event
		case crawlB.ID:
			eventB = event
		}
	}
	if !eventA.ResourceID.Valid || !eventB.ResourceID.Valid {
		t.Fatalf("missing crawl events: A=%v B=%v", eventA.ResourceID.Valid, eventB.ResourceID.Valid)
	}
	if eventA.ID >= eventB.ID {
		t.Fatalf("event ids out of commit order: A=%d B=%d", eventA.ID, eventB.ID)
	}

	// A cursor advanced to A's id still sees B's later event.
	afterA := listOrganizationEvents(t, ctx, queries, userID, orgID, eventA.ID)
	foundB := false
	for _, event := range afterA {
		if event.EventType == "crawl.queued" && event.ResourceID == crawlB.ID {
			foundB = true
		}
	}
	if !foundB {
		t.Fatal("cursor after A's event did not see B's event")
	}
}

// TestOrganizationEventInsertDoesNotBlockOtherOrg confirms the lock is keyed
// by organization: a held lock on one org must not stall another.
func TestOrganizationEventInsertDoesNotBlockOtherOrg(t *testing.T) {
	queries, pool, ctx := newOrgEventsTestDB(t)
	_, userA, projectA := createOrgEventsTestScope(t, ctx, pool)
	_, userB, projectB := createOrgEventsTestScope(t, ctx, pool)

	txA, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txA: %v", err)
	}
	defer func() { _ = txA.Rollback(ctx) }()
	if _, err := createOrgEventsCrawl(ctx, queries.WithTx(txA), projectA, userA, "manual"); err != nil {
		t.Fatalf("txA create crawl: %v", err)
	}

	txB, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txB: %v", err)
	}
	defer func() { _ = txB.Rollback(ctx) }()
	type crawlResult struct {
		crawl sqlc.CreateCrawlRow
		err   error
	}
	bDone := make(chan crawlResult, 1)
	go func() {
		crawl, err := createOrgEventsCrawl(ctx, queries.WithTx(txB), projectB, userB, "mcp")
		bDone <- crawlResult{crawl: crawl, err: err}
	}()

	select {
	case r := <-bDone:
		if r.err != nil {
			t.Fatalf("txB create crawl: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("txB blocked on a different organization's lock")
	}
}
