package db

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/localvisibility"
)

func migration000107Path(t *testing.T) string {
	t.Helper()
	matches, err := filepath.Glob("../../migrations/000107_*.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one 000107 migration, got %v (%v)", matches, err)
	}
	return matches[0]
}

func TestMigration000107PermitsRevserpSource(t *testing.T) {
	up := migrationUpSection(t, readMigration(t, migration000107Path(t)))
	assertContainsAll(t, up, []string{
		"ALTER TABLE location_keywords DROP CONSTRAINT IF EXISTS location_keywords_source_check;",
		"CHECK (source IN ('user', 'selected', 'revserp'))",
	})
	for _, forbidden := range []string{"UPDATE location_keywords", "DELETE FROM location_keywords", "DROP TABLE", "DROP COLUMN"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("000107 up must relax the CHECK without rewriting history; found %q", forbidden)
		}
	}
}

func TestMigration000107CopiesUserKeywordsOnLocationInsert(t *testing.T) {
	up := migrationUpSection(t, readMigration(t, migration000107Path(t)))
	assertContainsAll(t, up, []string{
		"CREATE FUNCTION copy_parent_user_keywords_to_location()",
		"CREATE TRIGGER location_keywords_copy_on_location_insert",
		"AFTER INSERT ON project_locations",
		"FROM project_keywords WHERE project_id = NEW.project_id AND source = 'user'",
		"ON CONFLICT (location_id, source, normalized_keyword) DO NOTHING",
		"kind, 'selected'",
	})
	fnStart := strings.Index(up, "CREATE FUNCTION copy_parent_user_keywords_to_location()")
	fnEnd := strings.Index(up[fnStart:], "$$;")
	if fnStart < 0 || fnEnd < 0 {
		t.Fatal("000107 up must define the creation-copy trigger function")
	}
	if body := up[fnStart : fnStart+fnEnd]; strings.Contains(body, "revserp") {
		t.Errorf("000107 trigger must never copy suggested rows; body:\n%s", body)
	}
	for _, forbidden := range []string{"UPDATE location_keywords", "DELETE FROM location_keywords"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("000107 up must not backfill existing locations; found %q", forbidden)
		}
	}
}

func TestMigration000107SeedsEnabledManualDrafts(t *testing.T) {
	up := migrationUpSection(t, readMigration(t, migration000107Path(t)))
	assertContainsAll(t, up, []string{
		"INSERT INTO project_location_queries (location_id, text, normalized, ordinal, enabled, kind, source, origin, landmark_id)",
		"ROW_NUMBER() OVER (ORDER BY pk.created_at, pk.id) - 1",
		"TRUE, 'map', 'manual', 'service', NULL",
		"ON CONFLICT (location_id, kind, normalized) DO NOTHING",
	})
	// The SQL byte cap must equal the Go Maps threshold exactly: same value,
	// same octet_length semantics as len() on the UTF-8 text.
	if want := fmt.Sprintf("octet_length(pk.keyword) <= %d", localvisibility.MaxMapQueryBytes); !strings.Contains(up, want) {
		t.Errorf("000107 draft seed must enforce %q", want)
	}
	for _, forbidden := range []string{
		"insert_organization_event", "pg_notify", "ai_worker_jobs",
		"local_visibility_runs", "location_ai_questions", "UPDATE ", "DELETE FROM project_location_queries",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("000107 up must seed drafts only, no provider/history touch; found %q", forbidden)
		}
	}
}

func TestMigration000107DownRefusesWhenSuggestionsExist(t *testing.T) {
	down := migrationDownSection(t, readMigration(t, migration000107Path(t)))
	assertContainsAll(t, down, []string{
		"IF EXISTS (SELECT 1 FROM location_keywords WHERE source NOT IN ('user', 'selected'))",
		"RAISE EXCEPTION 'cannot roll back 000107",
		"DROP TRIGGER IF EXISTS location_keywords_copy_on_location_insert ON project_locations;",
		"DROP FUNCTION IF EXISTS copy_parent_user_keywords_to_location();",
		"CHECK (source IN ('user', 'selected'))",
	})
	if strings.Contains(down, "DELETE FROM location_keywords") {
		t.Error("000107 down must refuse while revserp rows exist instead of deleting history")
	}
}
