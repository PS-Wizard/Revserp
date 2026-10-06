package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Layer 4 consumer-artifact contract. These tests read the query source files
// only; they never connect to a database. They pin the named operations, the
// deterministic ordering, the values that must stay out of SQL, and the exact
// ordinal deferral so a later handler cannot quietly drift from the contract.

func readLayer4QueryFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("queries", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

func TestLayer4QueryFilesDeclareNamedOperations(t *testing.T) {
	want := map[string][]string{
		"location_services.sql": {
			"ListProjectServicesForUser :many",
			"CreateProjectServiceForUser :one",
			"RenameProjectServiceForUser :one",
			"DeleteProjectServiceForUser :execrows",
			"ListProjectLocationServiceEditorRowsForUser :many",
			"ListProjectLocationOnlyServiceLabelsForUser :many",
			"ListEffectiveProjectLocationServiceLabelsForUser :many",
			"DeleteProjectLocationServiceOverridesForUser :execrows",
			"InsertProjectLocationServiceOverrideForUser :one",
		},
		"location_queries.sql": {
			"ListProjectLocationQueriesForUser :many",
			"LockProjectLocationQueriesForUser :many",
			"LockProjectLocationForQueryDraftForUser :one",
			"ListEnabledMapQueriesForUser :many",
			"InsertProjectLocationQueryForUser :one",
			"UpdateProjectLocationQueryForUser :one",
			"DeleteProjectLocationQueryForUser :execrows",
			"DisableProjectLocationQueryForUser :one",
			"InsertMissingGeneratedProjectLocationQueryForUser :one",
			"DeleteObsoleteGeneratedProjectLocationQueryForUser :execrows",
		},
		"location_landmarks.sql": {
			"ListLocationLandmarksForUser :many",
			"UpsertLocationLandmarkForUser :one",
			"DeleteRemovedLocationLandmarksForUser :execrows",
			"UpdateLocationLandmarkSelectionForUser :one",
			"LockProjectLocationForLandmarkRefreshForUser :one",
		},
	}
	for name, operations := range want {
		sql := readLayer4QueryFile(t, name)
		for _, operation := range operations {
			if !strings.Contains(sql, "-- name: "+operation) {
				t.Errorf("%s missing named operation %q", name, operation)
			}
		}
	}
}

// The SQL never normalizes text: the Go shared helper supplies normalized values
// as parameters, and normalized columns are never recomputed in a query.
func TestLayer4QueryFilesNeverNormalizeText(t *testing.T) {
	for _, name := range []string{
		"location_services.sql",
		"location_queries.sql",
		"location_landmarks.sql",
	} {
		sql := strings.ToLower(readLayer4QueryFile(t, name))
		for _, forbidden := range []string{
			"regexp_replace",
			"unaccent",
			"lower(",
			"lower (",
			"btrim(",
			"btrim (",
		} {
			if strings.Contains(sql, forbidden) {
				t.Errorf("%s must not normalize text in SQL; found %q", name, forbidden)
			}
		}
	}
}

func TestLayer4LocationOperationsResolveMembership(t *testing.T) {
	// Every operation here touches location-scoped data and must go through the
	// project and its organization membership at the boundary.
	for name, queries := range map[string][]string{
		"location_services.sql": {"sqlc.arg(project_id)::uuid", "organization_members"},
		"location_queries.sql":  {"sqlc.arg(project_id)::uuid", "organization_members"},
		"location_landmarks.sql": {
			"sqlc.arg(project_id)::uuid",
			"organization_members",
		},
	} {
		sql := readLayer4QueryFile(t, name)
		for _, needle := range queries {
			if !strings.Contains(sql, needle) {
				t.Errorf("%s missing membership/project tie %q", name, needle)
			}
		}
	}
}

func TestLayer4DraftQueryOrderingIsDeterministic(t *testing.T) {
	sql := readLayer4QueryFile(t, "location_queries.sql")
	for _, needle := range []string{
		"ORDER BY q.kind, q.ordinal, q.id",
		"ORDER BY q.ordinal, q.id",
	} {
		if !strings.Contains(sql, needle) {
			t.Errorf("location_queries.sql missing deterministic ordering %q", needle)
		}
	}
}

func TestLayer4OrdinalDeferralNamesSpecificConstraint(t *testing.T) {
	sql := readLayer4QueryFile(t, "location_queries.sql")
	const stmt = "SET CONSTRAINTS project_location_queries_location_id_kind_ordinal_key DEFERRED;"
	if !strings.Contains(sql, stmt) {
		t.Fatalf("location_queries.sql missing specific ordinal deferral %q", stmt)
	}
	if strings.Contains(sql, "SET CONSTRAINTS ALL") {
		t.Error("ordinal deferral must name the specific constraint, never SET CONSTRAINTS ALL")
	}
}

func TestLayer4GeneratedSurfaceExists(t *testing.T) {
	var q *sqlc.Queries
	_ = q.ListProjectServicesForUser
	_ = q.CreateProjectServiceForUser
	_ = q.RenameProjectServiceForUser
	_ = q.DeleteProjectServiceForUser
	_ = q.ListProjectLocationServiceEditorRowsForUser
	_ = q.ListProjectLocationOnlyServiceLabelsForUser
	_ = q.ListEffectiveProjectLocationServiceLabelsForUser
	_ = q.DeleteProjectLocationServiceOverridesForUser
	_ = q.InsertProjectLocationServiceOverrideForUser
	_ = q.ListProjectLocationQueriesForUser
	_ = q.LockProjectLocationQueriesForUser
	_ = q.LockProjectLocationForQueryDraftForUser
	_ = q.ListEnabledMapQueriesForUser
	_ = q.InsertProjectLocationQueryForUser
	_ = q.UpdateProjectLocationQueryForUser
	_ = q.DeleteProjectLocationQueryForUser
	_ = q.DisableProjectLocationQueryForUser
	_ = q.InsertMissingGeneratedProjectLocationQueryForUser
	_ = q.DeleteObsoleteGeneratedProjectLocationQueryForUser
	_ = q.ListLocationLandmarksForUser
	_ = q.UpsertLocationLandmarkForUser
	_ = q.DeleteRemovedLocationLandmarksForUser
	_ = q.UpdateLocationLandmarkSelectionForUser
	_ = q.LockProjectLocationForLandmarkRefreshForUser
}

// operationBlock returns the text of one named operation, from its -- name marker
// to the next marker, so a guard is asserted per operation rather than per file.
func operationBlock(t *testing.T, file, operation string) string {
	t.Helper()
	sql := readLayer4QueryFile(t, file)
	marker := "-- name: " + operation
	start := strings.Index(sql, marker)
	if start < 0 {
		t.Fatalf("%s missing operation %q", file, operation)
	}
	rest := sql[start+len(marker):]
	if next := strings.Index(rest, "-- name:"); next >= 0 {
		rest = rest[:next]
	}
	return rest
}

func TestLayer4DirectRowMutationsGuardRouteLocation(t *testing.T) {
	for _, operation := range []string{
		"UpdateProjectLocationQueryForUser :one",
		"DeleteProjectLocationQueryForUser :execrows",
		"DisableProjectLocationQueryForUser :one",
	} {
		block := operationBlock(t, "location_queries.sql", operation)
		if !strings.Contains(block, "sqlc.arg(location_id)::uuid") {
			t.Errorf("location_queries.sql %q must guard the route location_id", operation)
		}
	}
	block := operationBlock(t, "location_landmarks.sql", "UpdateLocationLandmarkSelectionForUser :one")
	if !strings.Contains(block, "sqlc.arg(location_id)::uuid") {
		t.Error("UpdateLocationLandmarkSelectionForUser must guard the route location_id")
	}
}

func TestLayer4DeleteQueryIsManualOnly(t *testing.T) {
	block := operationBlock(t, "location_queries.sql", "DeleteProjectLocationQueryForUser :execrows")
	if !strings.Contains(block, "q.source = 'manual'") {
		t.Error("DeleteProjectLocationQueryForUser must delete manual rows only; generated rows are disabled")
	}
}

func TestLayer4DraftLockTargetsParentLocation(t *testing.T) {
	block := operationBlock(t, "location_queries.sql", "LockProjectLocationForQueryDraftForUser :one")
	for _, needle := range []string{
		"FROM project_locations l",
		"sqlc.arg(location_id)::uuid",
		"sqlc.arg(project_id)::uuid",
		"sqlc.arg(user_id)::uuid",
		"FOR NO KEY UPDATE OF l",
	} {
		if !strings.Contains(block, needle) {
			t.Errorf("LockProjectLocationForQueryDraftForUser missing %q", needle)
		}
	}
}
