package projectkeywords

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func newProjectKeywordsTestQueries(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("PROJECT_KEYWORDS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("PROJECT_KEYWORDS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("project keywords test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	var regclass string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.project_keywords')::text`).Scan(&regclass); err != nil || regclass == "" {
		t.Skip("project_keywords table is not migrated in the test database")
	}
	return sqlc.New(pool), pool, ctx
}

func newProjectKeywordsTestProject(t *testing.T, ctx context.Context, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()
	name := fmt.Sprintf("project-keywords-test-%d", time.Now().UnixNano())
	var orgID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	var projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'kw-test','https://kw-test.example') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	return projectID
}

func TestAddUserProjectKeywordIdempotentAndConflict(t *testing.T) {
	queries, pool, ctx := newProjectKeywordsTestQueries(t)
	projectID := newProjectKeywordsTestProject(t, ctx, pool)

	first, created, err := AddUserProjectKeyword(ctx, queries, projectID, "  ACME Shoes ", ProjectKeywordKindBrand)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !created || first.Keyword != "ACME Shoes" || first.Kind != ProjectKeywordKindBrand {
		t.Fatalf("add = %#v created=%v", first, created)
	}

	second, created, err := AddUserProjectKeyword(ctx, queries, projectID, "acme  shoes", ProjectKeywordKindBrand)
	if err != nil {
		t.Fatalf("idempotent add: %v", err)
	}
	if created || second.ID != first.ID {
		t.Fatalf("idempotent = %#v created=%v, want same row", second, created)
	}

	if _, _, err := AddUserProjectKeyword(ctx, queries, projectID, "ACME SHOES", ProjectKeywordKindNonBrand); !errors.Is(err, ErrProjectKeywordConflict) {
		t.Fatalf("opposite kind err = %v, want conflict", err)
	}
}

func TestAddUserProjectKeywordEnforcesLimit(t *testing.T) {
	queries, pool, ctx := newProjectKeywordsTestQueries(t)
	projectID := newProjectKeywordsTestProject(t, ctx, pool)

	if MaxProjectKeywordsPerKindPerSource != 10 {
		t.Fatalf("cap = %d, want 10", MaxProjectKeywordsPerKindPerSource)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source)
		SELECT $1, 'revserp brand ' || g, 'revserp brand ' || g, 'brand', 'revserp' FROM generate_series(1, 10) g`, projectID); err != nil {
		t.Fatalf("seed revserp: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, _, err := AddUserProjectKeyword(ctx, queries, projectID, fmt.Sprintf("brand phrase %d", i), ProjectKeywordKindBrand); err != nil {
			t.Fatalf("fill brand %d: %v", i, err)
		}
	}
	if _, _, err := AddUserProjectKeyword(ctx, queries, projectID, "one too many", ProjectKeywordKindBrand); !errors.Is(err, ErrProjectKeywordLimit) {
		t.Fatalf("over-limit brand err = %v, want limit", err)
	}
	for i := 0; i < 10; i++ {
		if _, _, err := AddUserProjectKeyword(ctx, queries, projectID, fmt.Sprintf("need phrase %d", i), ProjectKeywordKindNonBrand); err != nil {
			t.Fatalf("fill non-brand %d: %v", i, err)
		}
	}
	if _, _, err := AddUserProjectKeyword(ctx, queries, projectID, "one need too many", ProjectKeywordKindNonBrand); !errors.Is(err, ErrProjectKeywordLimit) {
		t.Fatalf("over-limit non-brand err = %v, want limit", err)
	}
}

func TestDeleteUserProjectKeywordIsolation(t *testing.T) {
	queries, pool, ctx := newProjectKeywordsTestQueries(t)
	projectID := newProjectKeywordsTestProject(t, ctx, pool)
	otherID := newProjectKeywordsTestProject(t, ctx, pool)

	mine, _, err := AddUserProjectKeyword(ctx, queries, projectID, "mine", ProjectKeywordKindBrand)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	var mineUUID pgtype.UUID
	if err := mineUUID.Scan(mine.ID); err != nil {
		t.Fatalf("scan id: %v", err)
	}

	var revserpID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source)
		VALUES ($1,'suggested','suggested','non_brand','revserp') RETURNING id`, projectID).Scan(&revserpID); err != nil {
		t.Fatalf("seed revserp: %v", err)
	}
	var otherUUID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source)
		VALUES ($1,'other','other','brand','user') RETURNING id`, otherID).Scan(&otherUUID); err != nil {
		t.Fatalf("seed other project: %v", err)
	}

	if err := DeleteUserProjectKeyword(ctx, queries, projectID, revserpID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revserp delete err = %v, want no rows", err)
	}
	if err := DeleteUserProjectKeyword(ctx, queries, projectID, otherUUID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("other project delete err = %v, want no rows", err)
	}
	if err := DeleteUserProjectKeyword(ctx, queries, projectID, mineUUID); err != nil {
		t.Fatalf("own delete: %v", err)
	}
	if err := DeleteUserProjectKeyword(ctx, queries, projectID, mineUUID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("repeat delete err = %v, want no rows", err)
	}
}

func TestReplaceSuggestedKeywordsValidatesBeforeDelete(t *testing.T) {
	queries, pool, ctx := newProjectKeywordsTestQueries(t)
	projectID := newProjectKeywordsTestProject(t, ctx, pool)

	if _, _, err := AddUserProjectKeyword(ctx, queries, projectID, "user brand", ProjectKeywordKindBrand); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := ReplaceSuggestedKeywords(ctx, queries, projectID, []string{"rev brand"}, []string{"rev need"}); err != nil {
		t.Fatalf("seed revserp: %v", err)
	}

	if err := ReplaceSuggestedKeywords(ctx, queries, projectID, nil, []string{"rev need"}); !errors.Is(err, ErrProjectKeywordInvalid) {
		t.Fatalf("empty brand err = %v, want invalid", err)
	}

	lists, err := LoadProjectKeywordLists(ctx, queries, projectID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(lists.UserDefined) != 1 || len(lists.RevserpSuggested) != 2 {
		t.Fatalf("lists after failed replace = %d/%d, want failed call to keep 1/2", len(lists.UserDefined), len(lists.RevserpSuggested))
	}
}

func TestReplaceSuggestedKeywordsNeverMutatesUser(t *testing.T) {
	queries, pool, ctx := newProjectKeywordsTestQueries(t)
	projectID := newProjectKeywordsTestProject(t, ctx, pool)

	if _, _, err := AddUserProjectKeyword(ctx, queries, projectID, "Acme", ProjectKeywordKindBrand); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := ReplaceSuggestedKeywords(ctx, queries, projectID, []string{"acme", "rev brand"}, []string{"rev need"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	lists, err := LoadProjectKeywordLists(ctx, queries, projectID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(lists.UserDefined) != 1 || lists.UserDefined[0].Keyword != "Acme" {
		t.Fatalf("user = %#v, want untouched Acme", lists.UserDefined)
	}
	if len(lists.Combined) != 3 {
		t.Fatalf("combined = %#v, want user Acme plus 2 revserp", lists.Combined)
	}
	// Shared phrase keeps the user text with both source badges.
	if first := lists.Combined[0]; first.Keyword != "Acme" || len(first.Sources) != 2 {
		t.Fatalf("shared = %#v, want Acme with both sources", first)
	}
	if got := CombinedKeywordTexts(lists); len(got) != 3 {
		t.Fatalf("texts = %v", got)
	}
}

func TestOverlongAndUnicodeLegacyRowsStayUsable(t *testing.T) {
	queries, pool, ctx := newProjectKeywordsTestQueries(t)
	projectID := newProjectKeywordsTestProject(t, ctx, pool)

	long := strings.Repeat("ü", MaxProjectKeywordRunes+50)
	var longID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source)
		VALUES ($1, $2, $3, 'non_brand', 'user') RETURNING id`, projectID, long, strings.ToLower(long)).Scan(&longID); err != nil {
		t.Fatalf("seed overlong: %v", err)
	}
	var spacedID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source)
		VALUES ($1, chr(160) || 'padded' || chr(160), chr(160) || 'padded' || chr(160), 'brand', 'user') RETURNING id`, projectID).Scan(&spacedID); err != nil {
		t.Fatalf("seed unicode-spaced: %v", err)
	}

	lists, err := LoadProjectKeywordLists(ctx, queries, projectID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	texts := CombinedKeywordTexts(lists)
	foundLong, foundSpaced := false, false
	for _, text := range texts {
		if text == long {
			foundLong = true
		}
		if text == "\u00a0padded\u00a0" {
			foundSpaced = true
		}
	}
	if !foundLong || !foundSpaced {
		t.Fatalf("combined texts missing legacy rows: foundLong=%v foundSpaced=%v", foundLong, foundSpaced)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO project_keywords (project_id, keyword, normalized_keyword, kind, source)
		VALUES ($1, 'padded', 'padded', 'non_brand', 'revserp')`, projectID); err != nil {
		t.Fatalf("seed revserp overlap: %v", err)
	}
	merged, err := LoadProjectKeywordLists(ctx, queries, projectID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	paddedEntries := 0
	for _, entry := range merged.Combined {
		if NormalizeProjectKeywordKey(entry.Keyword) != "padded" {
			continue
		}
		paddedEntries++
		if entry.Keyword != "\u00a0padded\u00a0" || entry.Kind != ProjectKeywordKindBrand || len(entry.Sources) != 2 {
			t.Fatalf("merged entry = %#v, want user display/classification with both sources", entry)
		}
	}
	if paddedEntries != 1 {
		t.Fatalf("padded combined entries = %d, want 1", paddedEntries)
	}

	if err := DeleteUserProjectKeyword(ctx, queries, projectID, longID); err != nil {
		t.Fatalf("delete overlong: %v", err)
	}
	if err := DeleteUserProjectKeyword(ctx, queries, projectID, spacedID); err != nil {
		t.Fatalf("delete unicode-spaced: %v", err)
	}
}

func readProjectKeywordsMigration(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(currentFile), "..", "..", "migrations", "000083_project_keywords.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	return string(raw)
}

func migrationNormalizationClass(t *testing.T) string {
	t.Helper()
	migration := readProjectKeywordsMigration(t)
	lines := strings.Split(migration, "\n")
	var pattern []string
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "-- normalize-class-start" {
			inBlock = true
			continue
		}
		if trimmed == "-- normalize-class-end" {
			inBlock = false
			continue
		}
		if inBlock {
			pattern = append(pattern, trimmed)
		}
	}
	class := strings.Join(pattern, " ")
	if !strings.Contains(class, "chr(160)") || !strings.Contains(class, "chr(8232)") {
		t.Fatalf("normalize class markers missing or pattern changed: %q", class)
	}
	return class
}

func TestMigrationBackfillNormalizationParityOnDisposableDB(t *testing.T) {
	_, pool, ctx := newProjectKeywordsTestQueries(t)
	class := migrationNormalizationClass(t)
	displayQuery := "SELECT btrim(regexp_replace($1, " + class + ", ' ', 'g'), ' ')"
	keyQuery := "SELECT lower(btrim(regexp_replace($1, " + class + ", ' ', 'g'), ' '))"
	cases := []struct{ input, display, key string }{
		{"  Emergency   Plumber ", "Emergency Plumber", "emergency plumber"},
		{"a\tb\nc", "a b c", "a b c"},
		{"ACME Shoes", "ACME Shoes", "acme shoes"},
		{"caf\u00e9  M\u00dcNCHEN", "caf\u00e9 M\u00dcNCHEN", "caf\u00e9 m\u00fcnchen"},
		{"\u00a0nbsp\u00a0", "nbsp", "nbsp"},
		{"trail\u2003boots", "trail boots", "trail boots"},
		{"\ta\u00a0 \u202fb\n", "a b", "a b"},
		{strings.Repeat("ü", 250), strings.Repeat("ü", 250), strings.Repeat("ü", 250)},
	}
	for _, tc := range cases {
		var display, key string
		if err := pool.QueryRow(ctx, displayQuery, tc.input).Scan(&display); err != nil {
			t.Fatalf("display(%q): %v", tc.input, err)
		}
		if err := pool.QueryRow(ctx, keyQuery, tc.input).Scan(&key); err != nil {
			t.Fatalf("key(%q): %v", tc.input, err)
		}
		if display != tc.display || display != NormalizeProjectKeywordDisplay(tc.input) {
			t.Errorf("display(%q) sql=%q go=%q want %q", tc.input, display, NormalizeProjectKeywordDisplay(tc.input), tc.display)
		}
		if key != tc.key || key != NormalizeProjectKeywordKey(tc.input) {
			t.Errorf("key(%q) sql=%q go=%q want %q", tc.input, key, NormalizeProjectKeywordKey(tc.input), tc.key)
		}
	}
	var plain, variant string
	if err := pool.QueryRow(ctx, keyQuery, "ACME Shoes").Scan(&plain); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, keyQuery, "acme\u00a0shoes").Scan(&variant); err != nil {
		t.Fatal(err)
	}
	if plain != variant || plain != "acme shoes" {
		t.Fatalf("overlap keys = %q/%q, want equal acme shoes", plain, variant)
	}
}
