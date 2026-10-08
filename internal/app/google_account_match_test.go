package app

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func testGoogleAccount(id, subject, email string) sqlc.GoogleConnection {
	return sqlc.GoogleConnection{
		ID:                   pgtype.UUID{Bytes: uuid.MustParse(id), Valid: true},
		OrganizationID:       pgtype.UUID{Bytes: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Valid: true},
		GoogleAccountSubject: pgText(subject),
		GoogleAccountEmail:   pgText(email),
		Status:               "active",
	}
}

func TestMatchGoogleAccountForSelect(t *testing.T) {
	first := testGoogleAccount("22222222-2222-2222-2222-222222222222", "sub-1", "one@example.com")
	second := testGoogleAccount("33333333-3333-3333-3333-333333333333", "sub-2", "two@example.com")
	unknown := pgtype.UUID{Bytes: uuid.MustParse("44444444-4444-4444-4444-444444444444"), Valid: true}

	t.Run("explicit request wins over binding", func(t *testing.T) {
		matched, err := matchGoogleAccountForSelect([]sqlc.GoogleConnection{first, second}, &first.ID, &second.ID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !uuidEqual(matched.ID, second.ID) {
			t.Fatalf("matched = %v, want second account", matched.ID.String())
		}
	})

	t.Run("bound account honored without request", func(t *testing.T) {
		matched, err := matchGoogleAccountForSelect([]sqlc.GoogleConnection{first, second}, &second.ID, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !uuidEqual(matched.ID, second.ID) {
			t.Fatalf("matched = %v, want bound second account", matched.ID.String())
		}
	})

	t.Run("single account implicit", func(t *testing.T) {
		matched, err := matchGoogleAccountForSelect([]sqlc.GoogleConnection{first}, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !uuidEqual(matched.ID, first.ID) {
			t.Fatalf("matched = %v, want the only account", matched.ID.String())
		}
	})

	t.Run("multiple accounts require explicit choice", func(t *testing.T) {
		if _, err := matchGoogleAccountForSelect([]sqlc.GoogleConnection{first, second}, nil, nil); err != errGoogleConnectionRequired {
			t.Fatalf("err = %v, want errGoogleConnectionRequired", err)
		}
	})

	t.Run("unknown requested account not found", func(t *testing.T) {
		if _, err := matchGoogleAccountForSelect([]sqlc.GoogleConnection{first}, nil, &unknown); err != errGoogleAccountNotFound {
			t.Fatalf("err = %v, want errGoogleAccountNotFound", err)
		}
	})

	t.Run("stale binding not silently replaced", func(t *testing.T) {
		if _, err := matchGoogleAccountForSelect([]sqlc.GoogleConnection{first}, &unknown, nil); err != errGoogleAccountNotFound {
			t.Fatalf("err = %v, want errGoogleAccountNotFound", err)
		}
	})
}

func TestValidateReconnectSubject(t *testing.T) {
	if err := validateReconnectSubject("sub-1", "sub-1"); err != nil {
		t.Fatalf("matching subject rejected: %v", err)
	}
	if err := validateReconnectSubject("sub-1", "sub-2"); err != errGoogleAccountMismatch {
		t.Fatalf("err = %v, want errGoogleAccountMismatch", err)
	}
	for _, oldSubject := range []string{"", "   "} {
		if err := validateReconnectSubject(oldSubject, "sub-9"); err != errGoogleAccountMismatch {
			t.Fatalf("unverifiable old subject must fail closed, got %v", err)
		}
	}
	if err := validateReconnectSubject("sub-1", ""); err != errGoogleAccountMismatch {
		t.Fatalf("empty verified subject must fail closed, got %v", err)
	}
}

func TestMatchGoogleAccountBySubject(t *testing.T) {
	legacyA := testGoogleAccount("22222222-2222-2222-2222-222222222222", "", "")
	known := testGoogleAccount("33333333-3333-3333-3333-333333333333", "sub-1", "one@example.com")

	t.Run("new subject never matches legacy unknown row", func(t *testing.T) {
		if _, found := matchGoogleAccountBySubject([]sqlc.GoogleConnection{legacyA}, "sub-B"); found {
			t.Fatal("verified new account must not match legacy row; caller creates a new row instead")
		}
	})

	t.Run("exact subject matches", func(t *testing.T) {
		matched, found := matchGoogleAccountBySubject([]sqlc.GoogleConnection{legacyA, known}, "sub-1")
		if !found || !uuidEqual(matched.ID, known.ID) {
			t.Fatalf("found = %v, matched = %v, want known account", found, matched.ID.String())
		}
	})

	t.Run("empty subject never matches", func(t *testing.T) {
		if _, found := matchGoogleAccountBySubject([]sqlc.GoogleConnection{legacyA, known}, ""); found {
			t.Fatal("empty subject must not match any row")
		}
	})
}

func TestParseLocationGoogleBindingMode(t *testing.T) {
	for _, mode := range []string{"inherit", "off", "custom"} {
		if parsed, err := parseLocationGoogleBindingMode(mode); err != nil || parsed != mode {
			t.Fatalf("mode %q parsed as %q, err %v", mode, parsed, err)
		}
	}
	for _, mode := range []string{"", "parent", "INHERIT"} {
		if _, err := parseLocationGoogleBindingMode(mode); err == nil {
			t.Fatalf("mode %q should be rejected", mode)
		}
	}
}

func TestResolveLocationServiceSource(t *testing.T) {
	tests := []struct {
		name               string
		mode               string
		hasProjectBinding  bool
		hasLocationBinding bool
		wantSource         string
		wantOK             bool
	}{
		{"inherit with project binding", "inherit", true, false, "inherit", true},
		{"inherit without project binding", "inherit", false, false, "", false},
		{"inherit ignores location binding", "inherit", true, true, "inherit", true},
		{"off binds nothing", "off", true, true, "", false},
		{"custom with location binding", "custom", true, true, "custom", true},
		{"custom without location binding", "custom", true, false, "", false},
		{"empty mode without project binding", "", false, false, "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, ok := resolveLocationServiceSource(test.mode, test.hasProjectBinding, test.hasLocationBinding)
			if source != test.wantSource || ok != test.wantOK {
				t.Fatalf("got (%q, %v), want (%q, %v)", source, ok, test.wantSource, test.wantOK)
			}
		})
	}
}
