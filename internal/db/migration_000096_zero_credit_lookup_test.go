package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// Migration 096 lets a zero-credit Places lookup share the listing-lookup
// table. These tests read the migration file only and never connect to a
// database.

func migration000096Path(t *testing.T) string {
	t.Helper()
	matches, err := filepath.Glob("../../migrations/000096_*.sql")
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one 000096 migration, got %v (%v)", matches, err)
	}
	return matches[0]
}

func TestMigration000096AllowsZeroCreditsInOneCheck(t *testing.T) {
	up := migrationUpSection(t, readMigration(t, migration000096Path(t)))
	if got := strings.Count(up, "ADD CONSTRAINT"); got != 1 {
		t.Errorf("000096 up changes %d constraints, want exactly one CHECK change", got)
	}
	if got := strings.Count(up, "DROP CONSTRAINT"); got != 1 {
		t.Errorf("000096 up drops %d constraints, want exactly one CHECK change", got)
	}
	for _, needle := range []string{
		"DROP CONSTRAINT local_listing_lookups_expected_credits_check",
		"ADD CONSTRAINT local_listing_lookups_expected_credits_check CHECK (expected_credits IN (0,1,3))",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("000096 up missing %q", needle)
		}
	}
}

func TestMigration000096RewritesNoRowsAndNoDefaults(t *testing.T) {
	up := migrationUpSection(t, readMigration(t, migration000096Path(t)))
	for _, forbidden := range []string{
		"UPDATE ", "INSERT ", "DELETE ", "ALTER COLUMN", "SET DEFAULT",
		"reserved_credits_check", "ADD COLUMN", "DROP COLUMN", "CREATE TABLE",
		"DO $$", "RAISE ",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("000096 up must only change the expected_credits CHECK; found %q", forbidden)
		}
	}
}

func TestMigration000096DownRestoresOldCheckAndGuardsZeroEvidence(t *testing.T) {
	down := migrationDownSection(t, readMigration(t, migration000096Path(t)))
	for _, needle := range []string{
		"expected_credits IN (1,3)",
		"IF EXISTS (SELECT 1 FROM local_listing_lookups WHERE expected_credits = 0)",
		"RAISE EXCEPTION 'zero-credit listing evidence must be preserved'",
	} {
		if !strings.Contains(down, needle) {
			t.Errorf("000096 down missing %q", needle)
		}
	}
	if strings.Contains(down, "SET DEFAULT") {
		t.Error("000096 down must not change the column default")
	}
}
