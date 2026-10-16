package aiaudit

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func locationGenerationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("LOCAL_SEO_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("LOCAL_SEO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("location generation test database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("location generation test database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type locationGenerationFixture struct {
	orgID      string
	projectID  string
	locationID string
}

func seedLocationGenerationFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tag string) locationGenerationFixture {
	t.Helper()
	var fx locationGenerationFixture
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id::text`,
		"locgen "+tag).Scan(&fx.orgID); err != nil {
		t.Fatalf("insert org: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,$2,$3) RETURNING id::text`,
		fx.orgID, "locgen "+tag, "https://acme.example").Scan(&fx.projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_business_profile
		(project_id, brand_name, website_url, primary_category, primary_location, business_description, product_description, target_audience, business_competitors)
		VALUES ($1,'Acme','https://acme.example','Dentist','United States','Parent clinic.','Parent canonical products.','Parents.','["ParentCorp"]')`,
		fx.projectID); err != nil {
		t.Fatalf("insert parent profile: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO project_locations
		(project_id, name, latitude, longitude, address, locality, localities)
		VALUES ($1,'Acme Thamel',27.71,85.31,'Thamel 1','Kathmandu','["Kathmandu"]') RETURNING id::text`,
		fx.projectID).Scan(&fx.locationID); err != nil {
		t.Fatalf("insert location: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1::uuid`, fx.orgID)
	})
	return fx
}

func locationGenerationWorker(pool *pgxpool.Pool, provider ai.Provider) *Worker {
	return &Worker{
		pool:              pool,
		queries:           sqlc.New(pool),
		cfg:               config.Config{DeepSeekModel: "test-model"},
		newPromptProvider: func() (ai.Provider, error) { return provider, nil },
	}
}

func mustParseUUID(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		t.Fatalf("parse uuid %q: %v", raw, err)
	}
	return id
}

func TestLocationProfileGenerationDBReadsAndWritesLocalOnly(t *testing.T) {
	pool := locationGenerationTestPool(t)
	ctx := context.Background()
	fx := seedLocationGenerationFixture(t, ctx, pool, "profile")

	if _, err := pool.Exec(ctx, `UPDATE location_business_profiles
		SET business_description='Old local.', product_description='Old local products.',
		    primary_location='United States', business_competitors='["OldCorp"]'
		WHERE location_id = $1::uuid`, fx.locationID); err != nil {
		t.Fatalf("stage local copy: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO location_setup (organization_id, project_id, location_id, differences)
		VALUES ($1::uuid,$2::uuid,$3::uuid,'Evening hours differ; competes with Rival One.')`,
		fx.orgID, fx.projectID, fx.locationID); err != nil {
		t.Fatalf("insert setup: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO location_website_scopes (project_id, location_id, revision, url, match)
		VALUES ($1::uuid,$2::uuid,1,'https://acme.example/loc','subtree')`,
		fx.projectID, fx.locationID); err != nil {
		t.Fatalf("insert scope: %v", err)
	}
	var crawlID string
	if err := pool.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1::uuid,'completed') RETURNING id::text`,
		fx.projectID).Scan(&crawlID); err != nil {
		t.Fatalf("insert crawl: %v", err)
	}
	for _, page := range []struct{ url, title, meta string }{
		{"https://acme.example/loc/branch", "Branch", "Branch page"},
		{"https://acme.example/other", "Other", "Elsewhere"},
		{"https://acme.example/locksmith", "Decoy", "Prefix decoy"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, title, meta_description, depth)
			VALUES ($1::uuid,$2,$3,$4,1)`, crawlID, page.url, page.title, page.meta); err != nil {
			t.Fatalf("insert page %s: %v", page.url, err)
		}
	}

	provider := &stubVisibilityProvider{respond: func(string) (string, error) {
		return `{"business_description":"Adapted local clinic.","product_description":"","target_audience":"","primary_category":"","primary_location":"","business_competitors":["Rival One"]}`, nil
	}}
	w := locationGenerationWorker(pool, provider)
	job := sqlc.ClaimNextPendingAIWorkerJobRow{
		ProjectID:  mustParseUUID(t, fx.projectID),
		LocationID: mustParseUUID(t, fx.locationID),
	}
	if err := w.handleLocationProfileGeneration(ctx, job); err != nil {
		t.Fatalf("handleLocationProfileGeneration: %v", err)
	}
	if provider.calls() != 1 {
		t.Fatalf("provider calls = %d, want exactly one generation", provider.calls())
	}
	prompt := provider.prompts[0]
	for _, want := range []string{"Evening hours differ", "https://acme.example/loc/branch"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, excluded := range []string{"https://acme.example/other", "https://acme.example/locksmith"} {
		if strings.Contains(prompt, excluded) {
			t.Fatalf("prompt must scope page evidence, found %q:\n%s", excluded, prompt)
		}
	}

	var local struct {
		brand, website, category, location, description, products, audience, competitors, services string
	}
	if err := pool.QueryRow(ctx, `SELECT brand_name, website_url,
		COALESCE(primary_category,''), COALESCE(primary_location,''),
		COALESCE(business_description,''), COALESCE(product_description,''),
		COALESCE(target_audience,''), business_competitors::text, services::text
		FROM location_business_profiles WHERE location_id = $1::uuid`, fx.locationID).Scan(
		&local.brand, &local.website, &local.category, &local.location,
		&local.description, &local.products, &local.audience, &local.competitors, &local.services); err != nil {
		t.Fatalf("read local profile: %v", err)
	}
	if local.description != "Adapted local clinic." {
		t.Fatalf("local description = %q, want adapted output", local.description)
	}
	if local.products != "Old local products." {
		t.Fatalf("blank products output wiped stored description: %q", local.products)
	}
	if local.location != "Thamel 1" {
		t.Fatalf("blank location must take the Maps address, got %q", local.location)
	}
	if local.competitors != `["Rival One"]` {
		t.Fatalf("competitors = %s, want mentioned set", local.competitors)
	}
	if local.website != "https://acme.example" || local.brand != "Acme" {
		t.Fatalf("identity must not change: %+v", local)
	}

	var parent struct {
		brand, website, category, location, description, products, audience, competitors string
	}
	if err := pool.QueryRow(ctx, `SELECT brand_name, website_url,
		COALESCE(primary_category,''), COALESCE(primary_location,''),
		COALESCE(business_description,''), COALESCE(product_description,''),
		COALESCE(target_audience,''), business_competitors::text
		FROM project_business_profile WHERE project_id = $1::uuid`, fx.projectID).Scan(
		&parent.brand, &parent.website, &parent.category, &parent.location,
		&parent.description, &parent.products, &parent.audience, &parent.competitors); err != nil {
		t.Fatalf("read parent profile: %v", err)
	}
	if parent.description != "Parent clinic." || parent.products != "Parent canonical products." ||
		parent.location != "United States" || parent.competitors != `["ParentCorp"]` {
		t.Fatalf("parent profile must be unchanged: %+v", parent)
	}
}

func TestLocationMapQueryGenerationDBStarterAndRegen(t *testing.T) {
	pool := locationGenerationTestPool(t)
	ctx := context.Background()
	fx := seedLocationGenerationFixture(t, ctx, pool, "maps")

	if _, err := pool.Exec(ctx, `INSERT INTO location_landmarks
		(location_id, name, latitude, longitude, straight_line_m, provider, provider_ref, categories, fetched_at)
		VALUES ($1::uuid,'Durbar Marg',27.71,85.31,400,'google_places','ref-1','{shopping}',now())`,
		fx.locationID); err != nil {
		t.Fatalf("insert landmark: %v", err)
	}

	outputs := []string{
		"{\"queries\":[\"dentist in Kathmandu\",\"braces in Kathmandu\",\"dentist near me\"]}",
		"{\"queries\":[\"braces in Kathmandu\",\"teeth whitening in Kathmandu\"]}",
		"{\"queries\":[\"braces in Kathmandu\",\"teeth whitening in Kathmandu\"]}",
	}
	calls := 0
	provider := &stubVisibilityProvider{respond: func(string) (string, error) {
		out := outputs[calls]
		calls++
		return out, nil
	}}
	w := locationGenerationWorker(pool, provider)
	job := sqlc.ClaimNextPendingAIWorkerJobRow{
		ProjectID:  mustParseUUID(t, fx.projectID),
		LocationID: mustParseUUID(t, fx.locationID),
	}

	if err := w.handleLocationMapQueryGeneration(ctx, job); err != nil {
		t.Fatalf("first generation: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT text, enabled, kind, source FROM project_location_queries
		WHERE location_id = $1::uuid ORDER BY ordinal`, fx.locationID)
	if err != nil {
		t.Fatalf("list queries: %v", err)
	}
	var first []struct {
		text, kind, source string
		enabled            bool
	}
	for rows.Next() {
		var row struct {
			text, kind, source string
			enabled            bool
		}
		if err := rows.Scan(&row.text, &row.enabled, &row.kind, &row.source); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		first = append(first, row)
	}
	rows.Close()
	if len(first) != 3 {
		t.Fatalf("starter rows = %+v, want 3", first)
	}
	for _, row := range first {
		if !row.enabled || row.kind != "map" || row.source != "generated" {
			t.Fatalf("starter row must be enabled generated map: %+v", row)
		}
	}

	if _, err := pool.Exec(ctx, `INSERT INTO project_location_queries
    (location_id, text, normalized, ordinal, enabled, kind, source, origin)
    VALUES ($1::uuid,'my custom query','my custom query',50,TRUE,'map','manual','service')`,
		fx.locationID); err != nil {
		t.Fatalf("insert manual query: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE project_location_queries SET text='my edited query', normalized='my edited query', source='manual'
    WHERE location_id=$1::uuid AND text='dentist near me'`, fx.locationID); err != nil {
		t.Fatalf("simulate edited generated row: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE project_location_queries SET enabled=FALSE
    WHERE location_id=$1::uuid AND text='braces in Kathmandu'`, fx.locationID); err != nil {
		t.Fatalf("simulate user deselect: %v", err)
	}
	var bracesID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM project_location_queries WHERE location_id=$1::uuid AND text='braces in Kathmandu'`, fx.locationID).Scan(&bracesID); err != nil {
		t.Fatalf("read braces id: %v", err)
	}

	if err := w.handleLocationMapQueryGeneration(ctx, job); err != nil {
		t.Fatalf("second generation: %v", err)
	}
	type queryState struct {
		id      string
		enabled bool
		source  string
	}
	listStates := func() map[string]queryState {
		states := map[string]queryState{}
		qrows, err := pool.Query(ctx, `SELECT id::text, text, enabled, source FROM project_location_queries WHERE location_id = $1::uuid`,
			fx.locationID)
		if err != nil {
			t.Fatalf("list queries: %v", err)
		}
		for qrows.Next() {
			var row queryState
			var text string
			if err := qrows.Scan(&row.id, &text, &row.enabled, &row.source); err != nil {
				qrows.Close()
				t.Fatalf("scan: %v", err)
			}
			states[text] = row
		}
		qrows.Close()
		return states
	}
	states := listStates()
	if len(states) != 4 {
		t.Fatalf("states = %+v, want kept braces plus teeth plus 2 manual rows", states)
	}
	if _, ok := states["dentist in Kathmandu"]; ok {
		t.Fatalf("stale generated must be replaced: %+v", states)
	}
	if _, ok := states["dentist near me"]; ok {
		t.Fatalf("edited text must stay replaced: %+v", states)
	}
	braces, ok := states["braces in Kathmandu"]
	if !ok || braces.id != bracesID || braces.enabled || braces.source != "generated" {
		t.Fatalf("kept row must hold stable id and deselected flag: %+v", states)
	}
	appended, ok := states["teeth whitening in Kathmandu"]
	if !ok || appended.enabled || appended.source != "generated" {
		t.Fatalf("regen insert must be disabled generated: %+v", states)
	}
	for _, text := range []string{"my custom query", "my edited query"} {
		manual, ok := states[text]
		if !ok || !manual.enabled || manual.source != "manual" {
			t.Fatalf("manual row %q must be preserved: %+v", text, states)
		}
	}

	if err := w.handleLocationMapQueryGeneration(ctx, job); err != nil {
		t.Fatalf("third generation: %v", err)
	}
	if repeated := listStates(); len(repeated) != 4 {
		t.Fatalf("repeated = %+v, want bounded total", repeated)
	}
}
