package db

import (
	"strings"
	"testing"
)

const migration000103Path = "../../migrations/000103_relax_local_run_cells_query_index_cap.sql"

// Migration 103 removes the database half of the 1-5 Maps query product limit:
// query_index widens to INTEGER with only a non-negative floor. This test reads
// the migration file only and never connects to a database.
func TestMigration000103RelaxesQueryIndexCap(t *testing.T) {
	up := migrationUpSection(t, readMigration(t, migration000103Path))
	for _, needle := range []string{
		"ALTER TABLE local_run_cells ALTER COLUMN query_index TYPE INTEGER",
		"ALTER TABLE local_visibility_results ALTER COLUMN query_index TYPE INTEGER",
		"CHECK (query_index >= 0)",
		"FOREIGN KEY (run_id, query_index, point_index) REFERENCES local_run_cells(run_id, query_index, point_index)",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("103 up missing %q", needle)
		}
	}
	if strings.Contains(up, "BETWEEN 0 AND 4") || strings.Contains(up, "TYPE SMALLINT") {
		t.Error("103 up must not keep the smallint/five-query cap")
	}
	for _, forbidden := range []string{"UPDATE ", "INSERT ", "DELETE FROM ", "local_visibility_runs", "project_location_queries"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("103 up touches unrelated data: %q", forbidden)
		}
	}

	down := migrationDownSection(t, readMigration(t, migration000103Path))
	if !strings.Contains(down, "RAISE EXCEPTION") {
		t.Error("103 rollback must refuse to orphan multi-query cells")
	}
	if !strings.Contains(down, "CHECK (query_index BETWEEN 0 AND 4)") {
		t.Error("103 rollback must restore the five-query check")
	}
	if !strings.Contains(down, "ALTER COLUMN query_index TYPE SMALLINT") {
		t.Error("103 rollback must restore the SMALLINT columns")
	}
}
