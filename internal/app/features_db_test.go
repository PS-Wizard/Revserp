package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// The denylist default lives in SQL (LEFT JOIN + COALESCE), not in Go, so it can
// only be verified against a real Postgres. If it were wrong, either every
// workspace would be fully disabled or nothing could ever be gated.
func newFeaturesTestQueries(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
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
	pool, err := internaldb.Connect(ctx, databaseURL)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	t.Cleanup(pool.Close)

	return sqlc.New(pool), pool, ctx
}

func createFeaturesTestOrg(t *testing.T, ctx context.Context, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()

	var orgID pgtype.UUID
	err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name) VALUES ('features-test-org') RETURNING id`).Scan(&orgID)
	if err != nil {
		t.Fatalf("create test organization: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return orgID
}

// A workspace with no organization_features row must resolve to all-enabled.
func TestOrgWithNoRowResolvesToEverythingEnabled(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	row, err := queries.GetOrganizationFeatures(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrganizationFeatures: %v", err)
	}

	features := featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.DisabledAiTools)
	for _, feature := range []Feature{FeatureAutoCrawl, FeatureGSCConnector, FeatureAIChat} {
		if !features.Enabled(feature) {
			t.Errorf("unrestricted workspace has %q disabled", feature)
		}
	}
	if len(features.DisabledAITools()) != 0 {
		t.Errorf("unrestricted workspace has disabled tools: %v", features.DisabledAITools())
	}
}

func TestUpsertThenReadRoundTrips(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	if err := queries.UpsertOrganizationFeatures(ctx, sqlc.UpsertOrganizationFeaturesParams{
		OrgID:           orgID,
		AutoCrawl:       false,
		GscConnector:    true,
		AiChat:          true,
		DisabledAiTools: []string{"start_crawl", "export_crawl"},
	}); err != nil {
		t.Fatalf("UpsertOrganizationFeatures: %v", err)
	}

	row, err := queries.GetOrganizationFeatures(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrganizationFeatures: %v", err)
	}
	features := featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.DisabledAiTools)

	if features.Enabled(FeatureAutoCrawl) {
		t.Error("auto_crawl was saved disabled but read back enabled")
	}
	if !features.Enabled(FeatureGSCConnector) {
		t.Error("gsc_connector was saved enabled but read back disabled")
	}
	if features.AIToolEnabled("start_crawl") || features.AIToolEnabled("export_crawl") {
		t.Error("a disabled tool read back enabled")
	}
	if !features.AIToolEnabled("list_issues") {
		t.Error("an untouched tool read back disabled")
	}
}

// Saving twice must update in place rather than erroring on the primary key,
// since the admin page re-saves the whole matrix on every click of Save.
func TestUpsertIsIdempotentAndOverwrites(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	first := sqlc.UpsertOrganizationFeaturesParams{
		OrgID: orgID, AutoCrawl: false, GscConnector: false, AiChat: false,
		DisabledAiTools: []string{"start_crawl"},
	}
	if err := queries.UpsertOrganizationFeatures(ctx, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	second := sqlc.UpsertOrganizationFeaturesParams{
		OrgID: orgID, AutoCrawl: true, GscConnector: true, AiChat: true,
		DisabledAiTools: []string{},
	}
	if err := queries.UpsertOrganizationFeatures(ctx, second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	row, err := queries.GetOrganizationFeatures(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrganizationFeatures: %v", err)
	}
	features := featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.DisabledAiTools)

	if !features.Enabled(FeatureAutoCrawl) || !features.Enabled(FeatureGSCConnector) || !features.Enabled(FeatureAIChat) {
		t.Error("re-enabling via a second save did not take effect")
	}
	if !features.AIToolEnabled("start_crawl") {
		t.Error("clearing the tool denylist did not take effect")
	}
}

// The admin matrix must list every workspace, including ones never restricted —
// otherwise a workspace could not be gated for the first time.
func TestAdminListIncludesUnrestrictedWorkspaces(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	rows, err := queries.ListOrganizationFeaturesForAdmin(ctx)
	if err != nil {
		t.Fatalf("ListOrganizationFeaturesForAdmin: %v", err)
	}

	for _, row := range rows {
		if row.OrgID != orgID {
			continue
		}
		if !row.AutoCrawl || !row.GscConnector || !row.AiChat {
			t.Error("an unrestricted workspace is listed with features disabled")
		}
		return
	}
	t.Fatal("the unrestricted test workspace is missing from the admin matrix")
}
