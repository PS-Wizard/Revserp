package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/locationkeywords"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

func TestLocationKeywordListsResponseShape(t *testing.T) {
	raw, err := json.Marshal(locationKeywordListsResponse{
		UserDefined:       locationKeywordGroup{Branded: []string{}, NonBranded: []string{}},
		RevserpSuggested:  locationKeywordGroup{Branded: []string{}, NonBranded: []string{"Plumber"}},
		Selected:          locationKeywordGroup{Branded: []string{}, NonBranded: []string{}},
		SuggestedOrigins:  map[string][]string{"plumber": {"service"}},
		CanManageKeywords: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"user_defined", "revserp_suggested", "selected", "suggested_origins", "can_manage_keywords"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("response lacks %q: %s", key, raw)
		}
	}
	for _, key := range []string{"user_defined", "revserp_suggested", "selected"} {
		group := decoded[key].(map[string]any)
		if _, ok := group["branded"]; !ok {
			t.Fatalf("%s lacks branded: %s", key, raw)
		}
		if _, ok := group["non_branded"]; !ok {
			t.Fatalf("%s lacks non_branded: %s", key, raw)
		}
	}
}

// newLocationKeywordsTestTx applies migrations 102 and 107 in an isolated
// schema with a stub parent table, so this test never depends on other
// workers' in-flight schema. It never falls back to a default database URL.
func newLocationKeywordsTestTx(t *testing.T) (pgx.Tx, pgtype.UUID) {
	t.Helper()
	databaseURL := os.Getenv("LOCATION_KEYWORDS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LOCATION_KEYWORDS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("location keywords test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	schema := fmt.Sprintf("location_keywords_%d", time.Now().UnixNano())
	if _, err := tx.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE project_locations(id UUID PRIMARY KEY, project_id UUID)`); err != nil {
		t.Fatal(err)
	}
	// The 107 creation-copy trigger reads parent user rows and seeds manual
	// Maps drafts, so the scratch schema stubs both tables it writes through:
	// the parent keywords it selects (with the created_at/id columns the
	// deterministic ordinal orders by) and the draft table with the unique
	// key its ON CONFLICT targets.
	if _, err := tx.Exec(ctx, `CREATE TABLE project_keywords(project_id UUID, keyword TEXT, normalized_keyword TEXT, kind TEXT, source TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), id UUID PRIMARY KEY DEFAULT gen_random_uuid())`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE project_location_queries(location_id UUID, text TEXT, normalized TEXT, ordinal INTEGER, enabled BOOLEAN, kind TEXT, source TEXT, origin TEXT, landmark_id UUID, UNIQUE (location_id, kind, normalized))`); err != nil {
		t.Fatal(err)
	}
	_, file, _, _ := runtime.Caller(0)
	for _, name := range []string{"000102_location_keywords.sql", "000107_location_keywords_revserp.sql"} {
		migration, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		up, _, ok := strings.Cut(string(migration), "-- +goose Down")
		if !ok {
			t.Fatalf("migration %s has no Down section", name)
		}
		if _, err := tx.Exec(ctx, up); err != nil {
			t.Fatalf("migration %s Up failed: %v", name, err)
		}
	}
	var locationID pgtype.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO project_locations(id) VALUES (gen_random_uuid()) RETURNING id`).Scan(&locationID); err != nil {
		t.Skipf("gen_random_uuid is unavailable: %v", err)
	}
	return tx, locationID
}

// TestLocationCreationTriggerSeedsKeywordsAndDrafts proves the 107 trigger
// end to end: one location insert copies parent user rows to the user and
// selected lists, skips the overlong phrase and the unadopted revserp row,
// and seeds one enabled manual Maps draft per copied phrase with
// deterministic ordinals. Runs only against the scratch database.
func TestLocationCreationTriggerSeedsKeywordsAndDrafts(t *testing.T) {
	tx, _ := newLocationKeywordsTestTx(t)
	ctx := context.Background()
	var projectID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&projectID); err != nil {
		t.Skipf("gen_random_uuid is unavailable: %v", err)
	}
	longPhrase := strings.Repeat("\u00e9", 300)
	for _, row := range []struct {
		keyword, normalized, kind, source string
	}{
		{"Acme Plumber", "acme plumber", "non_brand", "user"},
		{longPhrase, strings.ToLower(longPhrase), "non_brand", "user"},
		{"Stale Suggestion", "stale suggestion", "non_brand", "revserp"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source) VALUES ($1,$2,$3,$4,$5)`,
			projectID, row.keyword, row.normalized, row.kind, row.source); err != nil {
			t.Fatalf("seed parent %q: %v", row.keyword, err)
		}
	}
	var locationID pgtype.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO project_locations(id, project_id) VALUES (gen_random_uuid(), $1) RETURNING id`, projectID).Scan(&locationID); err != nil {
		t.Fatalf("insert location: %v", err)
	}
	rows, err := locationkeywords.LoadStoredKeywords(ctx, tx, locationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"user", "selected"} {
		got := locationkeywords.GroupStoredKeywords(rows, source)
		// The keyword copy preserves every parent user phrase verbatim;
		// only the draft seed enforces the Maps byte threshold.
		if len(got.NonBranded) != 2 {
			t.Fatalf("%s = %#v, want both parent user phrases", source, got)
		}
	}
	if got := locationkeywords.GroupStoredKeywords(rows, locationkeywords.SourceRevserp); len(got.Branded)+len(got.NonBranded) != 0 {
		t.Fatalf("trigger must never copy suggested rows: %#v", got)
	}
	var drafts []draftSeedRow
	draftRows, err := tx.Query(ctx, `SELECT text, normalized, kind, source, origin, ordinal, enabled FROM project_location_queries WHERE location_id = $1 ORDER BY ordinal`, locationID)
	if err != nil {
		t.Fatal(err)
	}
	defer draftRows.Close()
	for draftRows.Next() {
		var d draftSeedRow
		if err := draftRows.Scan(&d.text, &d.normalized, &d.kind, &d.source, &d.origin, &d.ordinal, &d.enabled); err != nil {
			t.Fatal(err)
		}
		drafts = append(drafts, d)
	}
	if err := draftRows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 {
		t.Fatalf("drafts = %#v, want one seeded draft", drafts)
	}
	d := drafts[0]
	if d.text != "Acme Plumber" || d.normalized != "acme plumber" || d.kind != "map" || d.source != "manual" || d.origin != "service" || d.ordinal != 0 || !d.enabled {
		t.Fatalf("draft = %+v, want the enabled manual map row at ordinal 0", d)
	}
}

// draftSeedRow is one seeded Maps draft record read back in the trigger test.
type draftSeedRow struct {
	text, normalized, kind, source, origin string
	ordinal                               int32
	enabled                               bool
}

func TestLocationKeywordsStoreRoundTrip(t *testing.T) {
	tx, locationID := newLocationKeywordsTestTx(t)
	ctx := context.Background()
	for _, row := range []struct{ keyword, kind, source string }{
		{"Acme", "brand", "user"},
		{"Plumber", "non_brand", "user"},
		{"Drain Cleaning", "non_brand", "selected"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source) VALUES ($1,$2,$3,$4,$5)`,
			locationID, row.keyword, projectkeywords.NormalizeProjectKeywordKey(row.keyword), row.kind, row.source); err != nil {
			t.Fatalf("insert %q: %v", row.keyword, err)
		}
	}
	rows, err := locationkeywords.LoadStoredKeywords(ctx, tx, locationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %#v", rows)
	}
	if got := locationkeywords.GroupStoredKeywords(rows, "selected"); len(got.NonBranded) != 1 {
		t.Fatalf("selected = %#v", got)
	}
	for i, negative := range []string{
		`INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source) VALUES ($1,'plumber','plumber','non_brand','user')`,
		`INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source) VALUES ($1,'x','x','non_brand','bogus')`,
	} {
		// Each intentional violation rolls back to a savepoint: a bare
		// CHECK failure would abort the whole test transaction (25P02).
		if _, err := tx.Exec(ctx, fmt.Sprintf("SAVEPOINT negative_%d", i)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, negative, locationID); err == nil {
			t.Fatalf("negative case %d must fail", i)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf("ROLLBACK TO SAVEPOINT negative_%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	// Migration 107 permits the revserp source: saved Find-keywords output
	// persists beside user and selected rows.
	if _, err := tx.Exec(ctx, `INSERT INTO location_keywords (location_id, keyword, normalized_keyword, kind, source) VALUES ($1,'Emergency Fix','emergency fix','non_brand','revserp')`, locationID); err != nil {
		t.Fatalf("insert revserp: %v", err)
	}
	if err := locationkeywords.ReplaceSelectedKeywords(ctx, tx, locationID, []string{"Acme"}, []string{"Leak Repair"}); err != nil {
		t.Fatalf("replace selected: %v", err)
	}
	rows, err = locationkeywords.LoadStoredKeywords(ctx, tx, locationID)
	if err != nil {
		t.Fatal(err)
	}
	if got := locationkeywords.GroupStoredKeywords(rows, "user"); len(got.Branded) != 1 || len(got.NonBranded) != 1 {
		t.Fatalf("selected replace must leave user rows alone: %#v", got)
	}
	if got := locationkeywords.GroupStoredKeywords(rows, "selected"); len(got.Branded) != 1 || len(got.NonBranded) != 1 {
		t.Fatalf("selected = %#v", got)
	}
	if got := locationkeywords.GroupStoredKeywords(rows, locationkeywords.SourceRevserp); len(got.NonBranded) != 1 {
		t.Fatalf("selected replace must leave saved suggestions alone: %#v", got)
	}
	if err := locationkeywords.ReplaceRevserpKeywords(ctx, tx, locationID, []string{"Acme"}, []string{"Drain Rescue"}); err != nil {
		t.Fatalf("replace revserp: %v", err)
	}
	rows, err = locationkeywords.LoadStoredKeywords(ctx, tx, locationID)
	if err != nil {
		t.Fatal(err)
	}
	if got := locationkeywords.GroupStoredKeywords(rows, locationkeywords.SourceRevserp); len(got.Branded) != 1 || len(got.NonBranded) != 1 {
		t.Fatalf("revserp = %#v", got)
	}
	if got := locationkeywords.GroupStoredKeywords(rows, "selected"); len(got.Branded) != 1 || len(got.NonBranded) != 1 {
		t.Fatalf("revserp replace must leave selected rows alone: %#v", got)
	}
	if got := locationkeywords.GroupStoredKeywords(rows, "user"); len(got.Branded) != 1 || len(got.NonBranded) != 1 {
		t.Fatalf("revserp replace must leave user rows alone: %#v", got)
	}
}

func TestLocalCoveragePrimaryLocation(t *testing.T) {
	cases := []struct {
		name       string
		locality   string
		localities []byte
		want       string
	}{
		{"chosen locality wins", "Kathmandu", []byte(`["Lalitpur","Kathmandu"]`), "Kathmandu"},
		{"first level is the fallback", "", []byte(`["Lalitpur","Kathmandu"]`), "Lalitpur"},
		{"blank levels are skipped", "  ", []byte(`["","Bagmati"]`), "Bagmati"},
		{"empty ladder stays empty", "", []byte(`[]`), ""},
		{"nil ladder stays empty", "", nil, ""},
		{"invalid json stays empty", "", []byte(`{`), ""},
	}
	for _, testCase := range cases {
		if got := localCoveragePrimaryLocation(testCase.locality, testCase.localities); got != testCase.want {
			t.Fatalf("%s: got %q, want %q", testCase.name, got, testCase.want)
		}
	}
}
