package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type meResponse struct {
	User          userResponse           `json:"user"`
	Organizations []organizationResponse `json:"organizations"`
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

// handleMe returns the current local user and organizations for a valid Supabase identity.
func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	identity, ok := internalauth.IdentityFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback(r.Context())

	queries := a.Queries.WithTx(tx)
	user, organizations, err := a.ensureUserAndOrganizations(r, queries, identity)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, meResponse{
		User:          newUserResponse(user),
		Organizations: newOrganizationResponses(organizations),
	})
}

// ensureUserAndOrganizations maps the auth identity to a local user and default organization.
func (a *App) ensureUserAndOrganizations(r *http.Request, queries *sqlc.Queries, identity internalauth.Identity) (sqlc.User, []sqlc.ListOrganizationsForUserRow, error) {
	user, err := queries.GetUserByAuthSubject(r.Context(), sqlc.GetUserByAuthSubjectParams{
		AuthProvider: identity.Provider,
		AuthSubject:  identity.Subject,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return sqlc.User{}, nil, err
		}

		user, err = queries.CreateUser(r.Context(), sqlc.CreateUserParams{
			AuthProvider: identity.Provider,
			AuthSubject:  identity.Subject,
			Email:        identity.Email,
			Name:         pgText(identity.Name),
		})
		if err != nil {
			return sqlc.User{}, nil, err
		}
	}

	organizations, err := queries.ListOrganizationsForUser(r.Context(), user.ID)
	if err != nil {
		return sqlc.User{}, nil, err
	}
	if len(organizations) > 0 {
		return user, organizations, nil
	}

	workspaceOwnerName := strings.TrimSpace(identity.Name)
	if workspaceOwnerName == "" {
		workspaceOwnerName = strings.TrimSpace(identity.Email)
	}

	organization, err := queries.CreateOrganization(r.Context(), fmt.Sprintf("%s's Workspace", workspaceOwnerName))
	if err != nil {
		return sqlc.User{}, nil, err
	}

	_, err = queries.AddOrganizationMember(r.Context(), sqlc.AddOrganizationMemberParams{
		OrgID:  organization.ID,
		UserID: user.ID,
		Role:   "owner",
	})
	if err != nil {
		return sqlc.User{}, nil, err
	}

	organizations, err = queries.ListOrganizationsForUser(r.Context(), user.ID)
	if err != nil {
		return sqlc.User{}, nil, err
	}

	return user, organizations, nil
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeJSONError writes a simple JSON error response.
func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}

// pgText converts a string into pgtype.Text.
func pgText(value string) pgtype.Text {
	if strings.TrimSpace(value) == "" {
		return pgtype.Text{}
	}

	return pgtype.Text{String: value, Valid: true}
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
