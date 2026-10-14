package organizationaccess

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Organization manager forbidden: the caller is not an owner, manager, or
// platform admin member of the organization.
var ErrForbidden = errors.New("organization manager forbidden")

// Organization membership roles. Owners and managers can manage workspace
// features; members can only read unless they are platform admins.
const (
	RoleOwner   = "owner"
	RoleManager = "manager"
	RoleMember  = "member"
)

// Invite roles a workspace manager may grant. Invites never create owners.
const (
	InviteRoleMember  = "member"
	InviteRoleManager = "manager"
)

// Querier is the query surface RequireOrganizationManager needs, so callers
// pass *sqlc.Queries or a transaction-scoped queries value.
type Querier interface {
	GetOrganizationMember(context.Context, sqlc.GetOrganizationMemberParams) (sqlc.OrganizationMember, error)
	GetUserByID(context.Context, pgtype.UUID) (sqlc.GetUserByIDRow, error)
}

// CanManageOrganization reports whether a workspace membership may manage
// organization features. Owners and managers always can; members can only
// when they are platform admins. Only owner, manager, and member are
// recognized roles: any other role, including empty (no membership), never
// manages, even for platform admins.
func CanManageOrganization(role string, isPlatformAdmin bool) bool {
	switch role {
	case RoleOwner, RoleManager:
		return true
	case RoleMember:
		return isPlatformAdmin
	default:
		return false
	}
}

// RequireOrganizationManager allows owners, managers, and platform admin
// members of the organization. Membership is checked first: callers outside
// the organization fail even when they are platform admins. Members fall back
// to a fresh user lookup so a stale cached admin flag cannot grant access;
// suspended users never authorize here. Only owner, manager, and member are
// recognized roles; anything else fails closed without a user lookup.
func RequireOrganizationManager(ctx context.Context, q Querier, organizationID, userID pgtype.UUID) error {
	membership, err := q.GetOrganizationMember(ctx, sqlc.GetOrganizationMemberParams{
		OrgID:  organizationID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		return err
	}
	switch membership.Role {
	case RoleOwner, RoleManager:
		return nil
	case RoleMember:
		// Fresh user lookup below.
	default:
		return ErrForbidden
	}
	user, err := q.GetUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		return err
	}
	if user.Status != "active" {
		return ErrForbidden
	}
	if !user.IsPlatformAdmin {
		return ErrForbidden
	}
	return nil
}

// NormalizeInviteRole maps an invite create role to its stored value,
// defaulting to member for old clients that send no role.
func NormalizeInviteRole(role string) string {
	trimmed := strings.ToLower(strings.TrimSpace(role))
	if trimmed == "" {
		return InviteRoleMember
	}
	return trimmed
}

// ValidInviteRole reports whether a role may be granted through an invite.
// Invites never create owners or platform admins.
func ValidInviteRole(role string) bool {
	switch NormalizeInviteRole(role) {
	case InviteRoleMember, InviteRoleManager:
		return true
	default:
		return false
	}
}
