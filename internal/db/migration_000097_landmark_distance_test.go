package db

import (
	"strings"
	"testing"
)

func TestMigration000097DropsOnlyLandmarkRoadDistance(t *testing.T) {
	text := readMigration(t, "../../migrations/000097_drop_landmark_driving_m.sql")
	up := migrationUpSection(t, text)
	if !strings.Contains(up, "ALTER TABLE location_landmarks DROP COLUMN driving_m;") {
		t.Fatal("migration 097 must remove the road-distance column and its dependent constraint")
	}
	for _, forbidden := range []string{"UPDATE ", "INSERT ", "DELETE ", "CASCADE", "local_visibility_runs", "local_listing_lookups"} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("migration 097 changes unrelated data: %q", forbidden)
		}
	}
	down := migrationDownSection(t, text)
	if !strings.Contains(down, "RAISE EXCEPTION") || strings.Contains(down, "ADD COLUMN") {
		t.Fatal("migration 097 rollback must not invent lost road distances")
	}
}
