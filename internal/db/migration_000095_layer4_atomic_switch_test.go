package db

import (
	"strings"
	"testing"
)

const migration000095Path = "../../migrations/000095_layer4_atomic_switch.sql"

// Migration 095 owns the atomic switch: legacy query strings move verbatim
// into the draft table, then the legacy columns go. These tests read the
// migration file only and never connect to a database.
func migration000095Up(t *testing.T) string {
	t.Helper()
	return migrationUpSection(t, readMigration(t, migration000095Path))
}

func migration000095Down(t *testing.T) string {
	t.Helper()
	return migrationDownSection(t, readMigration(t, migration000095Path))
}

func TestMigration000095BackfillsDraftVerbatim(t *testing.T) {
	up := migration000095Up(t)
	for _, needle := range []string{
		"INSERT INTO project_location_queries",
		"jsonb_array_elements_text(l.queries) WITH ORDINALITY",
		"(q.ord - 1)",
		"TRUE, 'map', 'manual', 'service'",
		"ON CONFLICT (location_id, kind, normalized) DO NOTHING",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("095 up missing verbatim backfill %q", needle)
		}
	}
}

func TestMigration000095BackfillNeverNormalizes(t *testing.T) {
	up := migration000095Up(t)
	// Both value columns copy the raw element; the only btrim lives in the
	// blank-row guard, never in a copied value.
	if !strings.Contains(up, "SELECT l.id, q.elem, q.elem, (q.ord - 1)") {
		t.Error("095 backfill must copy text and normalized verbatim from q.elem")
	}
	for _, forbidden := range []string{"lower(", "upper(", "regexp_replace", "unaccent", "initcap"} {
		if strings.Contains(strings.ToLower(up), forbidden) {
			t.Errorf("095 backfill must copy text verbatim; found %q", forbidden)
		}
	}
}

func TestMigration000095DropsLegacyColumns(t *testing.T) {
	up := migration000095Up(t)
	for _, needle := range []string{
		"ALTER TABLE project_locations DROP COLUMN queries",
		"ALTER TABLE project_locations DROP COLUMN query_service",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("095 up missing legacy drop %q", needle)
		}
	}
	for _, forbidden := range []string{
		"project_services", "project_location_services", "location_landmarks",
		"local_visibility_runs", "local_visibility_results", "product_description",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("095 up must not touch %q", forbidden)
		}
	}
}

func TestMigration000095DownRestoresEnabledMapRows(t *testing.T) {
	down := migration000095Down(t)
	for _, needle := range []string{
		"ADD COLUMN queries",
		"ADD COLUMN query_service",
		"q.kind = 'map' AND q.enabled",
		"ORDER BY q.ordinal, q.id",
	} {
		if !strings.Contains(down, needle) {
			t.Errorf("095 down missing legacy restore %q", needle)
		}
	}
}
