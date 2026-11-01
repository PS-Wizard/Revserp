package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func migration000123Path(t *testing.T) string {
	t.Helper()
	matches, err := filepath.Glob("../../migrations/000123_*.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one 000123 migration, got %v (%v)", matches, err)
	}
	return matches[0]
}

func TestMigration000123CreatesCompetitorKeywordSelections(t *testing.T) {
	up := migrationUpSection(t, readMigration(t, migration000123Path(t)))
	assertContainsAll(t, up, []string{
		"CREATE TABLE project_competitor_keyword_selections",
		"project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE",
		"competitor_id UUID NOT NULL REFERENCES project_competitors(id) ON DELETE CASCADE",
		"keyword TEXT NOT NULL",
		"normalized_keyword TEXT NOT NULL",
		"kind TEXT NOT NULL DEFAULT 'non_brand'",
		"CHECK (kind IN ('non_brand'))",
		"UNIQUE (competitor_id, normalized_keyword)",
		"CREATE INDEX project_competitor_keyword_selections_project_idx",
		"CREATE INDEX project_competitor_keyword_selections_competitor_idx",
		"created_at TIMESTAMPTZ NOT NULL DEFAULT now()",
		"updated_at TIMESTAMPTZ NOT NULL DEFAULT now()",
	})
	for _, forbidden := range []string{
		"UPDATE project_competitor_keyword_selections",
		"DELETE FROM project_competitor_keyword_selections",
		"DROP TABLE",
		"TRUNCATE",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("000123 up must only create the selections table; found %q", forbidden)
		}
	}
}

func TestMigration000123DownDropsSelectionsTable(t *testing.T) {
	down := migrationDownSection(t, readMigration(t, migration000123Path(t)))
	assertContainsAll(t, down, []string{
		"DROP TABLE IF EXISTS project_competitor_keyword_selections",
	})
}

func TestMigration000123OrderingMarker(t *testing.T) {
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	count := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		if strings.HasPrefix(name, "000123") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("want exactly one 000123 migration, found %d", count)
	}
}
