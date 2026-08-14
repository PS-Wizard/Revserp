package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// The enabled default lives in SQL (LEFT JOIN + COALESCE), so it is verified
// against a real Postgres.
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

	features := featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.AiUseInternalPrompt, row.AiMonthlyMessageLimit, row.AiConcurrentTurnLimitPerUser, row.AiAllowedReasoningEfforts)
	for _, feature := range []Feature{FeatureAutoCrawl, FeatureGSCConnector, FeatureAIChat} {
		if !features.Enabled(feature) {
			t.Errorf("unrestricted workspace has %q disabled", feature)
		}
	}
	if row.AiUseInternalPrompt {
		t.Error("workspace without a feature row resolved internal prompt enabled")
	}
	if row.AiMonthlyMessageLimit != 50 {
		t.Errorf("unrestricted workspace limit = %d, want 50", row.AiMonthlyMessageLimit)
	}
	if !slices.Equal(row.AiAllowedReasoningEfforts, canonicalAIReasoningEfforts) {
		t.Errorf("unrestricted workspace efforts = %v, want %v", row.AiAllowedReasoningEfforts, canonicalAIReasoningEfforts)
	}
}

func TestUpsertThenReadRoundTrips(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	if err := queries.UpsertOrganizationFeatures(ctx, sqlc.UpsertOrganizationFeaturesParams{
		OrgID:                        orgID,
		AutoCrawl:                    false,
		GscConnector:                 true,
		AiChat:                       true,
		AiUseInternalPrompt:          true,
		AiMonthlyMessageLimit:        123,
		AiConcurrentTurnLimitPerUser: 2,
		AiAllowedReasoningEfforts:    []string{"none", "high"},
	}); err != nil {
		t.Fatalf("UpsertOrganizationFeatures: %v", err)
	}

	row, err := queries.GetOrganizationFeatures(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrganizationFeatures: %v", err)
	}
	features := featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.AiUseInternalPrompt, row.AiMonthlyMessageLimit, row.AiConcurrentTurnLimitPerUser, row.AiAllowedReasoningEfforts)

	if features.Enabled(FeatureAutoCrawl) {
		t.Error("auto_crawl was saved disabled but read back enabled")
	}
	if !features.Enabled(FeatureGSCConnector) {
		t.Error("gsc_connector was saved enabled but read back disabled")
	}
	if !features.AIUseInternalPrompt {
		t.Error("ai_use_internal_prompt was saved true but read back false")
	}
	if row.AiMonthlyMessageLimit != 123 {
		t.Errorf("limit = %d, want 123", row.AiMonthlyMessageLimit)
	}
	if !slices.Equal(row.AiAllowedReasoningEfforts, []string{"none", "high"}) {
		t.Errorf("efforts = %v, want [none high]", row.AiAllowedReasoningEfforts)
	}
}

// Saving twice must update in place rather than erroring on the primary key,
// since the admin page re-saves the whole matrix on every click of Save.
func TestUpsertIsIdempotentAndOverwrites(t *testing.T) {
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	first := sqlc.UpsertOrganizationFeaturesParams{
		OrgID: orgID, AutoCrawl: false, GscConnector: false, AiChat: false,
		AiMonthlyMessageLimit: 1, AiConcurrentTurnLimitPerUser: 2, AiAllowedReasoningEfforts: []string{"max"},
	}
	if err := queries.UpsertOrganizationFeatures(ctx, first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	second := sqlc.UpsertOrganizationFeaturesParams{
		OrgID: orgID, AutoCrawl: true, GscConnector: true, AiChat: true,
		AiMonthlyMessageLimit: 999, AiConcurrentTurnLimitPerUser: 2, AiAllowedReasoningEfforts: []string{"low", "none"},
	}
	if err := queries.UpsertOrganizationFeatures(ctx, second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	row, err := queries.GetOrganizationFeatures(ctx, orgID)
	if err != nil {
		t.Fatalf("GetOrganizationFeatures: %v", err)
	}
	features := featuresFromRow(row.AutoCrawl, row.GscConnector, row.AiChat, row.AiUseInternalPrompt, row.AiMonthlyMessageLimit, row.AiConcurrentTurnLimitPerUser, row.AiAllowedReasoningEfforts)

	if !features.Enabled(FeatureAutoCrawl) || !features.Enabled(FeatureGSCConnector) || !features.Enabled(FeatureAIChat) {
		t.Error("re-enabling via a second save did not take effect")
	}
	if row.AiMonthlyMessageLimit != 999 || !slices.Equal(row.AiAllowedReasoningEfforts, []string{"none", "low"}) {
		t.Errorf("settings did not round-trip: limit=%d efforts=%v", row.AiMonthlyMessageLimit, row.AiAllowedReasoningEfforts)
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
		if row.AiMonthlyMessageLimit != 50 || !slices.Equal(row.AiAllowedReasoningEfforts, canonicalAIReasoningEfforts) {
			t.Errorf("unrestricted settings = limit %d efforts %v", row.AiMonthlyMessageLimit, row.AiAllowedReasoningEfforts)
		}
		return
	}
	t.Fatal("the unrestricted test workspace is missing from the admin matrix")
}
