package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func mustAccessUUID(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		t.Fatalf("parse UUID %q: %v", raw, err)
	}
	return id
}

func TestValidateLocalAccessDatabaseURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"ipv4 loopback", "postgres://revserp:revserp@127.0.0.1:55440/local_seo_test?sslmode=disable", false},
		{"localhost alias", "postgresql://localhost/local_seo_test", false},
		{"ipv6 loopback", "postgres://[::1]:55440/local_seo_test", false},
		{"remote host refused", "postgres://db.internal:5432/local_seo_test", true},
		{"shared database refused", "postgres://127.0.0.1:5432/revserp_app", true},
		{"previous local database refused", "postgres://127.0.0.1:5432/local_seo_ui", true},
		{"wrong scheme refused", "mysql://127.0.0.1:3306/local_seo_test", true},
		{"missing database refused", "postgres://127.0.0.1:5432", true},
		{"scheme-less refused", "127.0.0.1:55440/local_seo_test", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := validateLocalAccessDatabaseURL(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateLocalAccessDatabaseURL(%q) = %+v, want error", tc.raw, target)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateLocalAccessDatabaseURL(%q) error: %v", tc.raw, err)
			}
			if target.Database != requiredLocalAccessDatabaseName {
				t.Fatalf("database = %q, want %q", target.Database, requiredLocalAccessDatabaseName)
			}
		})
	}
}

func TestValidateAccessUser(t *testing.T) {
	realID := mustAccessUUID(t, "3f0d9c4e-6a1b-4c2d-9e77-1b2c3d4e5f60")
	for _, tc := range []struct {
		name    string
		user    accessUser
		wantErr bool
	}{
		{"real user accepted", accessUser{ID: realID, AuthProvider: "supabase", Email: "real@example.com"}, false},
		{"test-only provider refused", accessUser{ID: realID, AuthProvider: recordedFixtureAuthProvider, Email: "real@example.com"}, true},
		{"empty provider refused", accessUser{ID: realID, Email: "real@example.com"}, true},
		{"fixture email refused", accessUser{ID: realID, AuthProvider: "supabase", Email: "someone@example.invalid"}, true},
		{"missing id refused", accessUser{AuthProvider: "supabase", Email: "real@example.com"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAccessUser(tc.user)
			if tc.wantErr != (err != nil) {
				t.Fatalf("validateAccessUser(%+v) error = %v, wantErr = %t", tc.user, err, tc.wantErr)
			}
		})
	}
}

func TestParseAccessUserID(t *testing.T) {
	valid := "3f0d9c4e-6a1b-4c2d-9e77-1b2c3d4e5f60"
	if id, err := parseAccessUserID("  " + valid + "  "); err != nil || id.String() != valid {
		t.Fatalf("parseAccessUserID(%q) = %v, %v", valid, id, err)
	}
	for _, raw := range []string{"", "not-a-uuid", "3f0d9c4e-6a1b-4c2d-9e77"} {
		if _, err := parseAccessUserID(raw); err == nil {
			t.Fatalf("parseAccessUserID(%q) = nil error, want error", raw)
		}
	}
}

func TestParseGrantAccessFlags(t *testing.T) {
	validURL := "postgres://revserp:revserp@127.0.0.1:55440/local_seo_test?sslmode=disable"
	validUser := "3f0d9c4e-6a1b-4c2d-9e77-1b2c3d4e5f60"

	opts, err := parseGrantAccessFlags([]string{
		"--database-url", validURL,
		"--user-id", validUser,
		"--allow-local-fixture-access",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseGrantAccessFlags error: %v", err)
	}
	if opts.DatabaseURL != validURL || opts.UserID != validUser || !opts.AllowLocalFixtureAccess {
		t.Fatalf("parsed options = %+v", opts)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing database url", []string{"--user-id", validUser, "--allow-local-fixture-access"}},
		{"missing user id", []string{"--database-url", validURL, "--allow-local-fixture-access"}},
		{"missing safety flag", []string{"--database-url", validURL, "--user-id", validUser}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseGrantAccessFlags(tc.args, &bytes.Buffer{}); err == nil {
				t.Fatalf("parseGrantAccessFlags(%v) = nil error, want error", tc.args)
			}
		})
	}
}

func TestRecordedFixtureRunIDParses(t *testing.T) {
	var id pgtype.UUID
	if err := id.Scan(recordedFixtureRunID); err != nil {
		t.Fatalf("recorded fixture run id %q is not a UUID: %v", recordedFixtureRunID, err)
	}
	if !strings.EqualFold(id.String(), recordedFixtureRunID) {
		t.Fatalf("run id round trip = %q, want %q", id.String(), recordedFixtureRunID)
	}
}

func TestLocalAccessRejectsConnectionOverrides(t *testing.T) {
	for _, databaseURL := range []string{
		"postgres://user:secret@127.0.0.1:55439/local_seo_test?host=remote.example",
		"postgres://user:secret@127.0.0.1:55439/local_seo_test?host=127.0.0.1,remote.example",
		"postgres://user:secret@127.0.0.1:55439/local_seo_test?dbname=production",
		"postgres://user:secret@127.0.0.1:bad-port/local_seo_test",
	} {
		_, err := validateLocalAccessDatabaseURL(databaseURL)
		if err == nil {
			t.Fatalf("unsafe connection override was accepted")
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("connection error exposed a password")
		}
	}
}
