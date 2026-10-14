package organizationaccess

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestCanManageOrganization(t *testing.T) {
	tests := []struct {
		name            string
		role            string
		isPlatformAdmin bool
		want            bool
	}{
		{name: "owner manages", role: "owner", want: true},
		{name: "owner manages even without admin flag", role: "owner", isPlatformAdmin: false, want: true},
		{name: "manager manages without admin flag", role: "manager", want: true},
		{name: "manager manages with admin flag", role: "manager", isPlatformAdmin: true, want: true},
		{name: "member is restricted", role: "member", want: false},
		{name: "platform admin member manages", role: "member", isPlatformAdmin: true, want: true},
		{name: "no membership never manages", role: "", want: false},
		{name: "platform admin outsider never manages", role: "", isPlatformAdmin: true, want: false},
		{name: "unknown role fails closed", role: "superowner", isPlatformAdmin: true, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanManageOrganization(tc.role, tc.isPlatformAdmin); got != tc.want {
				t.Fatalf("CanManageOrganization(%q, %v) = %v, want %v", tc.role, tc.isPlatformAdmin, got, tc.want)
			}
		})
	}
}

type stubQuerier struct {
	member    sqlc.OrganizationMember
	memberErr error
	user      sqlc.GetUserByIDRow
	userErr   error
	userCalls int
}

func (s *stubQuerier) GetOrganizationMember(_ context.Context, _ sqlc.GetOrganizationMemberParams) (sqlc.OrganizationMember, error) {
	return s.member, s.memberErr
}

func (s *stubQuerier) GetUserByID(_ context.Context, _ pgtype.UUID) (sqlc.GetUserByIDRow, error) {
	s.userCalls++
	return s.user, s.userErr
}

func TestRequireOrganizationManager(t *testing.T) {
	orgID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	userID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	dbErr := errors.New("db down")

	t.Run("owner succeeds without user lookup", func(t *testing.T) {
		q := &stubQuerier{member: sqlc.OrganizationMember{Role: "owner"}}
		if err := RequireOrganizationManager(context.Background(), q, orgID, userID); err != nil {
			t.Fatalf("owner: %v", err)
		}
		if q.userCalls != 0 {
			t.Fatalf("owner triggered %d user lookups, want 0", q.userCalls)
		}
	})

	t.Run("manager succeeds without admin flag", func(t *testing.T) {
		q := &stubQuerier{member: sqlc.OrganizationMember{Role: "manager"}}
		if err := RequireOrganizationManager(context.Background(), q, orgID, userID); err != nil {
			t.Fatalf("manager: %v", err)
		}
	})

	t.Run("platform admin member succeeds", func(t *testing.T) {
		q := &stubQuerier{
			member: sqlc.OrganizationMember{Role: "member"},
			user:   sqlc.GetUserByIDRow{IsPlatformAdmin: true, Status: "active"},
		}
		if err := RequireOrganizationManager(context.Background(), q, orgID, userID); err != nil {
			t.Fatalf("admin member: %v", err)
		}
	})

	t.Run("member is forbidden", func(t *testing.T) {
		q := &stubQuerier{
			member: sqlc.OrganizationMember{Role: "member"},
			user:   sqlc.GetUserByIDRow{Status: "active"},
		}
		if err := RequireOrganizationManager(context.Background(), q, orgID, userID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("member err = %v, want ErrForbidden", err)
		}
	})

	t.Run("missing membership is forbidden", func(t *testing.T) {
		q := &stubQuerier{memberErr: pgx.ErrNoRows}
		if err := RequireOrganizationManager(context.Background(), q, orgID, userID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("missing member err = %v, want ErrForbidden", err)
		}
	})

	t.Run("platform admin outsider is forbidden", func(t *testing.T) {
		q := &stubQuerier{
			memberErr: pgx.ErrNoRows,
			user:      sqlc.GetUserByIDRow{IsPlatformAdmin: true, Status: "active"},
		}
		if err := RequireOrganizationManager(context.Background(), q, orgID, userID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("outsider err = %v, want ErrForbidden", err)
		}
	})

	t.Run("unknown role admin is forbidden without user lookup", func(t *testing.T) {
		q := &stubQuerier{
			member: sqlc.OrganizationMember{Role: "superowner"},
			user:   sqlc.GetUserByIDRow{IsPlatformAdmin: true, Status: "active"},
		}
		if err := RequireOrganizationManager(context.Background(), q, orgID, userID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("unknown role admin err = %v, want ErrForbidden", err)
		}
		if q.userCalls != 0 {
			t.Fatalf("unknown role triggered %d user lookups, want 0", q.userCalls)
		}
	})

	t.Run("unknown role nonadmin is forbidden", func(t *testing.T) {
		q := &stubQuerier{
			member: sqlc.OrganizationMember{Role: "superowner"},
			user:   sqlc.GetUserByIDRow{Status: "active"},
		}
		if err := RequireOrganizationManager(context.Background(), q, orgID, userID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("unknown role err = %v, want ErrForbidden", err)
		}
	})

	t.Run("suspended admin member is forbidden", func(t *testing.T) {
		q := &stubQuerier{
			member: sqlc.OrganizationMember{Role: "member"},
			user:   sqlc.GetUserByIDRow{IsPlatformAdmin: true, Status: "suspended"},
		}
		if err := RequireOrganizationManager(context.Background(), q, orgID, userID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("suspended err = %v, want ErrForbidden", err)
		}
	})

	t.Run("missing user is forbidden", func(t *testing.T) {
		q := &stubQuerier{
			member:  sqlc.OrganizationMember{Role: "member"},
			userErr: pgx.ErrNoRows,
		}
		if err := RequireOrganizationManager(context.Background(), q, orgID, userID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("missing user err = %v, want ErrForbidden", err)
		}
	})

	t.Run("membership db error propagates", func(t *testing.T) {
		q := &stubQuerier{memberErr: dbErr}
		if err := RequireOrganizationManager(context.Background(), q, orgID, userID); !errors.Is(err, dbErr) {
			t.Fatalf("member db err = %v, want db error", err)
		}
	})

	t.Run("user db error propagates", func(t *testing.T) {
		q := &stubQuerier{
			member:  sqlc.OrganizationMember{Role: "member"},
			userErr: dbErr,
		}
		if err := RequireOrganizationManager(context.Background(), q, orgID, userID); !errors.Is(err, dbErr) {
			t.Fatalf("user db err = %v, want db error", err)
		}
	})
}

func TestInviteRoles(t *testing.T) {
	if got := NormalizeInviteRole(""); got != "member" {
		t.Fatalf("default role = %q, want member", got)
	}
	if got := NormalizeInviteRole("manager"); got != "manager" {
		t.Fatalf("manager role = %q", got)
	}
	for _, role := range []string{"member", "manager", "", " Manager "} {
		if !ValidInviteRole(role) {
			t.Errorf("role %q should be inviteable", role)
		}
	}
	for _, role := range []string{"owner", "admin", "platform_admin", "superowner"} {
		if ValidInviteRole(role) {
			t.Errorf("role %q must never be inviteable", role)
		}
	}
}
