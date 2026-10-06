package db

import (
	"strings"
	"testing"
)

const migration000094Path = "../../migrations/000094_location_services_queries_landmarks.sql"

func migration000094Up(t *testing.T) string {
	t.Helper()
	return migrationUpSection(t, readMigration(t, migration000094Path))
}

func TestMigration000094CreatesTablesBeforeQueries(t *testing.T) {
	up := migration000094Up(t)
	for _, table := range []string{
		"CREATE TABLE project_services",
		"CREATE TABLE project_location_services",
		"CREATE TABLE location_landmarks",
		"CREATE TABLE project_location_queries",
	} {
		if !strings.Contains(up, table) {
			t.Fatalf("094 up missing %q", table)
		}
	}
	queriesAt := strings.Index(up, "CREATE TABLE project_location_queries")
	for _, earlier := range []string{
		"CREATE TABLE project_services",
		"CREATE TABLE project_location_services",
		"CREATE TABLE location_landmarks",
	} {
		if at := strings.Index(up, earlier); at > queriesAt {
			t.Errorf("%q must be created before project_location_queries", earlier)
		}
	}
}

func TestMigration000094ServiceTablesContract(t *testing.T) {
	up := migration000094Up(t)
	for _, needle := range []string{
		"UNIQUE (project_id, normalized_label)",
		"UNIQUE (id, project_id)",
		"CHECK (length(btrim(label)) > 0)",
		"CHECK (length(btrim(normalized_label)) > 0)",
		"CHECK (mode IN ('include', 'exclude'))",
		"CHECK ((service_id IS NULL) <> (service_label IS NULL))",
		"CHECK ((service_label IS NULL) = (normalized_service_label IS NULL))",
		"CHECK (service_label IS NULL OR mode = 'include')",
		"FOREIGN KEY (location_id, project_id)",
		"REFERENCES project_locations(id, project_id) ON DELETE CASCADE",
		"FOREIGN KEY (service_id, project_id)",
		"REFERENCES project_services(id, project_id) ON DELETE CASCADE",
		"CREATE UNIQUE INDEX project_location_services_service_key",
		"CREATE UNIQUE INDEX project_location_services_label_key",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("094 up missing service contract %q", needle)
		}
	}
}

func TestMigration000094TextEmptyChecksAreBlunt(t *testing.T) {
	up := migration000094Up(t)
	for _, column := range []string{
		"label", "normalized_label", "name", "provider", "provider_ref", "text", "normalized",
	} {
		needle := "CHECK (length(btrim(" + column + ")) > 0)"
		if !strings.Contains(up, needle) {
			t.Errorf("094 up missing blunt emptiness check for %q", column)
		}
	}
	for _, needle := range []string{
		"length(btrim(service_label)) > 0",
		"length(btrim(normalized_service_label)) > 0",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("094 up missing blunt emptiness check %q", needle)
		}
	}
}

func TestMigration000094QueryTableContract(t *testing.T) {
	up := migration000094Up(t)
	for _, needle := range []string{
		"CHECK (kind IN ('map', 'ai_question'))",
		"CHECK (source IN ('generated', 'manual'))",
		"CHECK (origin IN ('service', 'locality', 'landmark'))",
		"landmark_id UUID REFERENCES location_landmarks(id) ON DELETE SET NULL",
		"UNIQUE (location_id, kind, normalized)",
		"UNIQUE (location_id, kind, ordinal) DEFERRABLE INITIALLY IMMEDIATE",
		"CHECK (ordinal >= 0)",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("094 up missing query contract %q", needle)
		}
	}
	if strings.Contains(up, "jsonb_array_length") {
		t.Error("project_location_queries must not carry a query-count cap")
	}
}

func TestMigration000094LandmarkBoundsContract(t *testing.T) {
	up := migration000094Up(t)
	for _, needle := range []string{
		"CHECK (latitude BETWEEN -90 AND 90)",
		"CHECK (longitude BETWEEN -180 AND 180)",
		"CHECK (straight_line_m >= 0)",
		"CHECK (driving_m BETWEEN 0 AND 2000)",
		"UNIQUE (location_id, provider_ref)",
		"fetched_at TIMESTAMPTZ NOT NULL",
		"selected BOOLEAN NOT NULL DEFAULT FALSE",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("094 up missing landmark contract %q", needle)
		}
	}
}

func TestMigration000094AddsLocalitiesLadderColumn(t *testing.T) {
	up := migration000094Up(t)
	for _, needle := range []string{
		"ADD COLUMN localities JSONB NOT NULL DEFAULT '[]'::jsonb",
		"CHECK (jsonb_typeof(localities) = 'array')",
	} {
		if !strings.Contains(up, needle) {
			t.Errorf("094 up missing localities contract %q", needle)
		}
	}
}

// TestMigration000094UpIsPureAddOnlyDDL asserts the staging migration is schema
// only: it creates the new artifacts and backfills nothing. The legacy
// reconcile belongs to the consumer-switch migration, so no INSERT/UPDATE data
// statements and no SQL-side normalization may appear here.
func TestMigration000094UpIsPureAddOnlyDDL(t *testing.T) {
	up := migration000094Up(t)
	for _, forbidden := range []string{
		"DROP ",
		"INSERT INTO",
		"UPDATE ",
		"regexp_replace",
		"jsonb_array_elements_text",
		"jsonb_build_array",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("094 up must be pure DDL; found %q", forbidden)
		}
	}
}

func TestMigration000094DownDropsNewArtifactsOnly(t *testing.T) {
	sql := readMigration(t, migration000094Path)
	down := migrationDownSection(t, sql)
	for _, needle := range []string{
		"DROP TABLE project_location_queries",
		"DROP TABLE location_landmarks",
		"DROP TABLE project_location_services",
		"DROP TABLE project_services",
		"ALTER TABLE project_locations DROP CONSTRAINT project_locations_id_project_key",
		"ALTER TABLE project_locations DROP COLUMN localities",
	} {
		if !strings.Contains(down, needle) {
			t.Errorf("094 down missing %q", needle)
		}
	}
	for _, forbidden := range []string{"ADD COLUMN", "UPDATE ", "jsonb_build_array", "query_service"} {
		if strings.Contains(down, forbidden) {
			t.Errorf("094 down must not reconstruct retained legacy fields, found %q", forbidden)
		}
	}
}

func TestMigration000094StatementMarkers(t *testing.T) {
	sql := readMigration(t, migration000094Path)
	if got := strings.Count(sql, "-- +goose StatementBegin"); got != 2 {
		t.Errorf("094 StatementBegin count = %d, want 2", got)
	}
	if got := strings.Count(sql, "-- +goose StatementEnd"); got != 2 {
		t.Errorf("094 StatementEnd count = %d, want 2", got)
	}
}
