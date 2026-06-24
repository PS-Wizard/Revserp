package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type meResponse struct {
	User            userResponse           `json:"user"`
	Organizations   []organizationResponse `json:"organizations"`
	ActiveOrgID     string                 `json:"active_org_id"`
	IsPlatformAdmin bool                   `json:"is_platform_admin"`
	Status          string                 `json:"status,omitempty"`
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
		user           sqlc.User
		organizations  []sqlc.ListOrganizationsForUserRow
		activeOrgID    pgtype.UUID
		needsOrgUpdate bool
	)
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		var err error
		user, organizations, err = a.ensureUserAndOrganizations(r, queries, identity)
		if err != nil {
			serverError(w, r, err)
			return err
		}

		resolved := resolveActiveOrganizationID(session.ActiveOrgID, organizations)
		activeOrgID = resolved
		if resolved.Valid && resolved != session.ActiveOrgID {
			needsOrgUpdate = true
		}
		return nil
	}) {
		return
	}

	// Update active org AFTER the transaction commits (M-11).
	if needsOrgUpdate {
		if err := a.SessionManager.UpdateActiveOrganization(r.Context(), session.SessionID, session.UserID, activeOrgID); err != nil {
			log.Printf("app: update active organization after me (non-fatal): session=%s error=%v", session.SessionID.String(), err)
		}
	}

	writeJSON(w, http.StatusOK, newMeResponse(user, organizations, activeOrgID))
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
// On CreateUser failure, the error is logged; the function returns an invalid user.
func resolveUser(ctx context.Context, queries *sqlc.Queries, identity internalauth.Identity) (sqlc.User, bool) {
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
		// M-14: log the real error rather than silently swallowing it.
		log.Printf("app: create user failed: provider=%s subject=%s email=%s error=%v", identity.Provider, identity.Subject, identity.Email, err)
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

		return nil
	}) {
		return
	}

	// Update active org AFTER the transaction commits (M-11).
	if err := a.SessionManager.UpdateActiveOrganization(r.Context(), session.SessionID, session.UserID, organizationID); err != nil {
		log.Printf("app: update active organization after set-active-org (non-fatal): session=%s error=%v", session.SessionID.String(), err)
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

	var needsActiveOrgUpdate bool
	var nextActiveOrgID pgtype.UUID

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
			for _, organization := range organizations {
				if organization.ID != organizationID {
					nextActiveOrgID = organization.ID
					break
				}
			}
			needsActiveOrgUpdate = true
		}
		return nil
	}) {
		return
	}

	// Update active org AFTER the transaction commits (M-11).
	if needsActiveOrgUpdate {
		if err := a.SessionManager.UpdateActiveOrganization(r.Context(), session.SessionID, session.UserID, nextActiveOrgID); err != nil {
			log.Printf("app: update active organization after leave-org (non-fatal): session=%s error=%v", session.SessionID.String(), err)
		}
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
