package app

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresUniqueConstraintConflictUsesCallerConstraint(t *testing.T) {
	for _, constraint := range []string{
		"local_visibility_runs_inflight_idx",
		"local_listing_lookups_unsettled_idx",
	} {
		t.Run(constraint, func(t *testing.T) {
			for _, test := range []struct {
				name string
				err  error
				want bool
			}{
				{"matching constraint", &pgconn.PgError{Code: "23505", ConstraintName: constraint}, true},
				{"wrapped matching constraint", fmt.Errorf("reserve: %w", &pgconn.PgError{Code: "23505", ConstraintName: constraint}), true},
				{"other unique constraint", &pgconn.PgError{Code: "23505", ConstraintName: "unrelated_pkey"}, false},
				{"different error code", &pgconn.PgError{Code: "23503", ConstraintName: constraint}, false},
				{"no error", nil, false},
			} {
				t.Run(test.name, func(t *testing.T) {
					if got := isPostgresUniqueConstraintConflict(test.err, constraint); got != test.want {
						t.Fatalf("unique conflict = %t, want %t", got, test.want)
					}
				})
			}
		})
	}
	for _, pair := range [][2]string{
		{"local_visibility_runs_inflight_idx", "local_listing_lookups_unsettled_idx"},
		{"local_listing_lookups_unsettled_idx", "local_visibility_runs_inflight_idx"},
	} {
		if isPostgresUniqueConstraintConflict(&pgconn.PgError{Code: "23505", ConstraintName: pair[0]}, pair[1]) {
			t.Fatalf("constraint %s must not match caller %s", pair[0], pair[1])
		}
	}
}

func TestLocalVisibilityConflictCallSitesReturn409(t *testing.T) {
	for _, test := range []struct {
		file       string
		constraint string
		status     string
	}{
		{"local_visibility.go", "local_visibility_runs_inflight_idx", "http.StatusConflict"},
		{"location_listing.go", "local_listing_lookups_unsettled_idx", "409"},
	} {
		t.Run(test.file, func(t *testing.T) {
			source, err := os.ReadFile(test.file)
			if err != nil {
				t.Fatal(err)
			}
			branch := fmt.Sprintf("case isPostgresUniqueConstraintConflict(err, %q):\n\t\t\twriteJSONError(w, %s,", test.constraint, test.status)
			if !strings.Contains(string(source), branch) {
				t.Fatalf("%s must classify its own constraint and return 409", test.file)
			}
		})
	}
}
