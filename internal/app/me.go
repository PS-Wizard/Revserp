package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type meResponse struct {
	User              userResponse           `json:"user"`
	Organizations     []organizationResponse `json:"organizations"`
	ActiveOrgID       string                 `json:"active_org_id"`
	IsPlatformAdmin   bool                   `json:"is_platform_admin"`
	Status            string                 `json:"status,omitempty"`
	SessionExpiresAt  string                 `json:"session_expires_at,omitempty"`
	SessionRenewAfter string                 `json:"session_renew_after,omitempty"`
	// Features gates what the UI offers for the active workspace. It is a
	// convenience for the client only: every gated route and AI tool enforces
	// the same state server-side, so a client ignoring this changes nothing.
	Features orgFeaturesResponse `json:"features"`
}

type userResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type organizationResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// handleMe returns the current local user, organizations, and active organization for one backend session.
func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	identity, ok := internalauth.IdentityFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	session, ok := internalauth.SessionFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var (
		user          sqlc.User
		organizations []sqlc.ListOrganizationsForUserRow
		activeOrgID   pgtype.UUID
	)
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		var err error
		user, organizations, err = a.ensureUserAndOrganizations(r, queries, identity)
		if err != nil {
			serverError(w, r, err)
			return err
		}

		activeOrgID = resolveActiveOrganizationID(session.ActiveOrgID, organizations)
		if activeOrgID.Valid && activeOrgID != session.ActiveOrgID {
			if err := a.SessionManager.UpdateActiveOrganization(r.Context(), session.SessionID, activeOrgID); err != nil {
				serverError(w, r, err)
				return err
			}
		}
		return nil
	}) {
		return
	}

	meBody := newMeResponse(user, organizations, activeOrgID)
	meBody.SessionExpiresAt = session.ExpiresAt.Format(time.RFC3339)
	meBody.SessionRenewAfter = a.SessionManager.RenewalStartsAt(session.ExpiresAt).Format(time.RFC3339)
	a.attachActiveOrgFeatures(r.Context(), &meBody, activeOrgID)
	writeJSON(w, http.StatusOK, meBody)
}

// ensureUserAndOrganizations maps the auth identity to a local user and default organization.
func (a *App) ensureUserAndOrganizations(r *http.Request, queries *sqlc.Queries, identity internalauth.Identity) (sqlc.User, []sqlc.ListOrganizationsForUserRow, error) {
	user, needsOrg := resolveUser(r.Context(), queries, identity)
	if !user.ID.Valid {
		return sqlc.User{}, nil, fmt.Errorf("failed to resolve user")
	}

	organizations, err := queries.ListOrganizationsForUser(r.Context(), user.ID)
	if err != nil {
		return sqlc.User{}, nil, err
	}

	if needsOrg && len(organizations) == 0 {
		workspaceOwnerName := strings.TrimSpace(identity.Name)
		if workspaceOwnerName == "" {
			workspaceOwnerName = strings.TrimSpace(identity.Email)
		}

		organization, err := queries.CreateOrganization(r.Context(), fmt.Sprintf("%s's Workspace", workspaceOwnerName))
		if err != nil {
			return sqlc.User{}, nil, err
		}

		if _, err = queries.AddOrganizationMember(r.Context(), sqlc.AddOrganizationMemberParams{
			OrgID:  organization.ID,
			UserID: user.ID,
			Role:   "owner",
		}); err != nil {
			return sqlc.User{}, nil, err
		}

		organizations, err = queries.ListOrganizationsForUser(r.Context(), user.ID)
		if err != nil {
			return sqlc.User{}, nil, err
		}
	}

	return user, organizations, nil
}

// resolveUser loads or creates the local user for an auth identity.
// Returns the user and whether a default org should be created.
func resolveUser(ctx context.Context, queries *sqlc.Queries, identity internalauth.Identity) (sqlc.User, bool) {
	// Use the user already fetched by requireActiveUser middleware if available.
	if cached, ok := ctx.Value(cachedUserContextKey{}).(sqlc.User); ok && cached.ID.Valid {
		return cached, false
	}

	userRow, err := queries.GetUserByAuthSubject(ctx, sqlc.GetUserByAuthSubjectParams{
		AuthProvider: identity.Provider,
		AuthSubject:  identity.Subject,
	})
	if err == nil {
		return userRowToUser(userRow), false
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.User{}, false
	}

	newUserRow, err := queries.CreateUser(ctx, sqlc.CreateUserParams{
		AuthProvider: identity.Provider,
		AuthSubject:  identity.Subject,
		Email:        identity.Email,
		Name:         pgText(identity.Name),
	})
	if err != nil {
		return sqlc.User{}, false
	}

	return userRowToUser(newUserRow), true
}

// newMeResponse converts user, organizations, and active org state into the /me API response.
func newMeResponse(user sqlc.User, organizations []sqlc.ListOrganizationsForUserRow, activeOrgID pgtype.UUID) meResponse {
	response := meResponse{
		User:            newUserResponse(user),
		Organizations:   newOrganizationResponses(organizations),
		IsPlatformAdmin: isPlatformAdmin(user.Email, user.IsPlatformAdmin),
		Status:          user.Status,
	}
	if activeOrgID.Valid {
		response.ActiveOrgID = activeOrgID.String()
	}
	response.Features = newOrgFeaturesResponse(allFeaturesEnabled())
	return response
}

// newUserResponse converts a DB user into an API user.
func newUserResponse(user sqlc.User) userResponse {
	response := userResponse{
		ID:    user.ID.String(),
		Email: user.Email,
	}
	if user.Name.Valid {
		response.Name = user.Name.String
	}

	return response
}

// newOrganizationResponses converts DB organizations into API organizations.
func newOrganizationResponses(organizations []sqlc.ListOrganizationsForUserRow) []organizationResponse {
	items := make([]organizationResponse, 0, len(organizations))
	for _, organization := range organizations {
		items = append(items, organizationResponse{
			ID:   organization.ID.String(),
			Name: organization.Name,
			Role: organization.Role,
		})
	}

	return items
}

func resolveActiveOrganizationID(currentActiveOrgID pgtype.UUID, organizations []sqlc.ListOrganizationsForUserRow) pgtype.UUID {
	for _, organization := range organizations {
		if currentActiveOrgID.Valid && organization.ID == currentActiveOrgID {
			return currentActiveOrgID
		}
	}
	if len(organizations) == 0 {
		return pgtype.UUID{}
	}
	return organizations[0].ID
}

type setActiveOrganizationRequest struct {
	OrganizationID string `json:"organization_id"`
}

// handleSetActiveOrganization switches the current backend session into one organization the user belongs to.
func (a *App) handleSetActiveOrganization(w http.ResponseWriter, r *http.Request) {
	session, ok := internalauth.SessionFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var requestBody setActiveOrganizationRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	organizationID, err := parseUUIDParam(strings.TrimSpace(requestBody.OrganizationID))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}

	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		user, _, err := a.ensureCurrentUser(r, queries)
		if err != nil {
			serverError(w, r, err)
			return err
		}

		if _, err := queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{OrgID: organizationID, UserID: user.ID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusForbidden, "forbidden")
				return err
			}
			serverError(w, r, err)
			return err
		}

		if err := a.SessionManager.UpdateActiveOrganization(r.Context(), session.SessionID, organizationID); err != nil {
			serverError(w, r, err)
			return err
		}
		return nil
	}) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"active_org_id": organizationID.String(),
	})
}

// handleLeaveOrganization removes the current user from one non-owner workspace.
func (a *App) handleLeaveOrganization(w http.ResponseWriter, r *http.Request) {
	organizationID, err := parseUUIDParam(strings.TrimSpace(chi.URLParam(r, "organizationID")))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}

	session, ok := internalauth.SessionFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		user, organizations, err := a.ensureCurrentUser(r, queries)
		if err != nil {
			serverError(w, r, err)
			return err
		}

		membership, err := queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{OrgID: organizationID, UserID: user.ID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusForbidden, "forbidden")
				return err
			}
			serverError(w, r, err)
			return err
		}
		if membership.Role == "owner" {
			writeJSONError(w, http.StatusConflict, "owners cannot leave their own workspace")
			return errors.New("owner cannot leave")
		}

		deletedRows, err := queries.RemoveOrganizationMember(r.Context(), sqlc.RemoveOrganizationMemberParams{OrgID: organizationID, UserID: user.ID})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		if deletedRows == 0 {
			writeJSONError(w, http.StatusNotFound, "workspace membership not found")
			return errors.New("workspace membership not found")
		}

		if session.ActiveOrgID.Valid && session.ActiveOrgID == organizationID {
			nextActiveOrganizationID := pgtype.UUID{}
			for _, organization := range organizations {
				if organization.ID != organizationID {
					nextActiveOrganizationID = organization.ID
					break
				}
			}
			if err := a.SessionManager.UpdateActiveOrganization(r.Context(), session.SessionID, nextActiveOrganizationID); err != nil {
				serverError(w, r, err)
				return err
			}
		}
		return nil
	}) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// userRowToUser converts a user query row to the canonical User model.
func userRowToUser(row any) sqlc.User {
	switch r := row.(type) {
	case sqlc.GetUserByAuthSubjectRow:
		return sqlc.User{
			ID:               r.ID,
			AuthProvider:     r.AuthProvider,
			AuthSubject:      r.AuthSubject,
			Email:            r.Email,
			Name:             r.Name,
			IsPlatformAdmin:  r.IsPlatformAdmin,
			Status:           r.Status,
			SuspendedAt:      r.SuspendedAt,
			SuspensionReason: r.SuspensionReason,
			CreatedAt:        r.CreatedAt,
		}
	case sqlc.CreateUserRow:
		return sqlc.User{
			ID:               r.ID,
			AuthProvider:     r.AuthProvider,
			AuthSubject:      r.AuthSubject,
			Email:            r.Email,
			Name:             r.Name,
			IsPlatformAdmin:  r.IsPlatformAdmin,
			Status:           r.Status,
			SuspendedAt:      r.SuspendedAt,
			SuspensionReason: r.SuspensionReason,
			CreatedAt:        r.CreatedAt,
		}
	default:
		return sqlc.User{}
	}
}

// attachActiveOrgFeatures fills the /me feature block for the caller's active
// workspace. On a lookup error it leaves the all-enabled default in place: the
// gated routes and the AI tool registry enforce the real state regardless, so
// the worst case is the UI offering something that then returns 403 — better
// than stripping features from a user because of a transient query failure.
func (a *App) attachActiveOrgFeatures(ctx context.Context, response *meResponse, activeOrgID pgtype.UUID) {
	if !activeOrgID.Valid {
		return
	}
	features, err := a.OrgFeaturesForOrg(ctx, activeOrgID)
	if err != nil {
		log.Printf("resolve org features for /me failed: org_id=%s error=%v", activeOrgID.String(), err)
		return
	}
	response.Features = newOrgFeaturesResponse(features)
}
