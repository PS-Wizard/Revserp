package crawler

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Scratch roundtrip for persisted analysis progress: real migrated schema
// only. The URL comes from CRAWLER_SCRATCH_TEST_DATABASE_URL alone; the suite
// refuses any non-local host or non-scratch database name and never touches
// the application database.
const crawlerScratchDatabaseEnv = "CRAWLER_SCRATCH_TEST_DATABASE_URL"

func crawlerScratchPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	raw := os.Getenv(crawlerScratchDatabaseEnv)
	if raw == "" {
		t.Skip(crawlerScratchDatabaseEnv + " is not set")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", crawlerScratchDatabaseEnv, err)
	}
	if name := strings.Trim(parsed.Path, "/"); !strings.HasPrefix(name, "revserp_layer_c_") {
		t.Fatalf("refusing %s database %q: want a revserp_layer_c_* scratch database", crawlerScratchDatabaseEnv, name)
	}
	if host := strings.ToLower(parsed.Hostname()); host != "127.0.0.1" && host != "localhost" {
		t.Fatalf("refusing %s host %q: want 127.0.0.1 or localhost", crawlerScratchDatabaseEnv, host)
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, raw, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("%s is not available: %v", crawlerScratchDatabaseEnv, err)
	}
	t.Cleanup(pool.Close)
	var hasProgressColumn bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'crawls' AND column_name = 'analysis_progress')`).Scan(&hasProgressColumn); err != nil || !hasProgressColumn {
		t.Skip("crawls.analysis_progress is not migrated (needs migration 000114) in the scratch database")
	}
	return pool, ctx
}

func readCrawlPhaseProgress(t *testing.T, ctx context.Context, pool *pgxpool.Pool, crawlID pgtype.UUID) (pgtype.Text, []byte) {
	t.Helper()
	var phase pgtype.Text
	var progress []byte
	if err := pool.QueryRow(ctx, `SELECT phase, analysis_progress FROM crawls WHERE id = $1`,
		crawlID).Scan(&phase, &progress); err != nil {
		t.Fatalf("read crawl progress: %v", err)
	}
	return phase, progress
}

func TestScratchAnalysisProgressRoundtrip(t *testing.T) {
	pool, ctx := crawlerScratchPool(t)
	store := NewStore(pool)
	queries := sqlc.New(pool)
	crawlID, cleanup := createTestCrawl(t, ctx, pool)
	t.Cleanup(cleanup)

	running, err := store.UpdateCrawlAnalysisProgress(ctx, crawlID, CrawlAnalysisProgress{
		Stage: AnalysisStageCheckingLinks, LinksResolved: 400000,
	})
	if err != nil {
		t.Fatalf("update analysis progress: %v", err)
	}
	if !running {
		t.Fatal("running crawl reported stopped")
	}
	phase, progress := readCrawlPhaseProgress(t, ctx, pool, crawlID)
	if !phase.Valid || phase.String != "analyzing" {
		t.Fatalf("phase = %+v, want analyzing", phase)
	}
	var decoded CrawlAnalysisProgress
	if err := json.Unmarshal(progress, &decoded); err != nil {
		t.Fatalf("unmarshal persisted progress %s: %v", progress, err)
	}
	if decoded.Stage != AnalysisStageCheckingLinks || decoded.LinksResolved != 400000 {
		t.Fatalf("persisted progress = %+v, want checking_links/400000", decoded)
	}

	// The durable organization event behind SSE carries the same JSON, so a
	// reload or reconnect sees it without new polling.
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM organization_events
		WHERE resource_id = $1 AND event_type = 'crawl.progress'
		ORDER BY id DESC LIMIT 1`, crawlID).Scan(&payload); err != nil {
		t.Fatalf("read crawl.progress event: %v", err)
	}
	var eventPayload struct {
		AnalysisProgress *CrawlAnalysisProgress `json:"analysis_progress"`
	}
	if err := json.Unmarshal(payload, &eventPayload); err != nil {
		t.Fatalf("unmarshal event payload %s: %v", payload, err)
	}
	if eventPayload.AnalysisProgress == nil || eventPayload.AnalysisProgress.LinksResolved != 400000 {
		t.Fatalf("event payload = %s, want persisted links_resolved 400000", payload)
	}

	var memberID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT om.user_id FROM organization_members AS om
		INNER JOIN projects AS p ON p.organization_id = om.org_id
		INNER JOIN crawls AS c ON c.project_id = p.id
		WHERE c.id = $1 LIMIT 1`, crawlID).Scan(&memberID); err != nil {
		t.Fatalf("read member: %v", err)
	}
	if _, err := queries.CancelCrawlByIDForUser(ctx, sqlc.CancelCrawlByIDForUserParams{
		ID: crawlID, UserID: memberID,
	}); err != nil {
		t.Fatalf("cancel crawl: %v", err)
	}
	if running, err := store.UpdateCrawlAnalysisProgress(ctx, crawlID, CrawlAnalysisProgress{
		Stage: AnalysisStageCheckingLinks, LinksResolved: 640000,
	}); err != nil || running {
		t.Fatalf("cancelled progress update = (%v, %v), want (false, nil)", running, err)
	}
	if phase, progress := readCrawlPhaseProgress(t, ctx, pool, crawlID); phase.Valid || len(progress) != 0 {
		t.Fatalf("cancelled crawl keeps phase %+v progress %s, want both cleared", phase, progress)
	}

	crawlID2, cleanup2 := createTestCrawl(t, ctx, pool)
	t.Cleanup(cleanup2)
	if _, err := store.UpdateCrawlAnalysisProgress(ctx, crawlID2, CrawlAnalysisProgress{
		Stage: AnalysisStageCheckingLinks, LinksResolved: 1,
	}); err != nil {
		t.Fatalf("update analysis progress: %v", err)
	}
	if _, err := queries.MarkCrawlCompleted(ctx, sqlc.MarkCrawlCompletedParams{
		ID: crawlID2, UrlsDiscovered: 1, UrlsCrawled: 1,
	}); err != nil {
		t.Fatalf("mark crawl completed: %v", err)
	}
	if phase, progress := readCrawlPhaseProgress(t, ctx, pool, crawlID2); phase.Valid || len(progress) != 0 {
		t.Fatalf("completed crawl keeps phase %+v progress %s, want both cleared", phase, progress)
	}
}
