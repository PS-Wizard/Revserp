package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type adminUserResponse struct {
	ID              string `json:"id"`
	Email           string `json:"email"`
	Name            string `json:"name,omitempty"`
	IsPlatformAdmin bool   `json:"is_platform_admin"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
}

type adminUsersListResponse struct {
	Users []adminUserResponse `json:"users"`
}

// handleAdminListUsers returns all users in the system.
func (a *App) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.Queries.ListAllUsers(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}

	items := make([]adminUserResponse, 0, len(users))
	for _, u := range users {
		item := adminUserResponse{
			ID:              u.ID.String(),
			Email:           u.Email,
			IsPlatformAdmin: u.IsPlatformAdmin,
			Status:          u.Status,
			CreatedAt:       u.CreatedAt.Time.Format("2006-01-02T15:04:05Z"),
		}
		if u.Name.Valid {
			item.Name = u.Name.String
		}
		items = append(items, item)
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, adminUsersListResponse{Users: items})
}

// handleAdminMakeAdmin grants platform admin to a user.
func (a *App) handleAdminMakeAdmin(w http.ResponseWriter, r *http.Request) {
	userID, err := parseUUIDParam(chi.URLParam(r, "userID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	user, err := a.Queries.GetUserByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "user not found")
			return
		}
		serverError(w, r, err)
		return
	}

	if user.IsPlatformAdmin {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}

	if err := a.Queries.UpdateUserPlatformAdmin(r.Context(), sqlc.UpdateUserPlatformAdminParams{
		ID:              userID,
		IsPlatformAdmin: true,
	}); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAdminRemoveAdmin revokes platform admin from a user.
func (a *App) handleAdminRemoveAdmin(w http.ResponseWriter, r *http.Request) {
	userID, err := parseUUIDParam(chi.URLParam(r, "userID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	user, err := a.Queries.GetUserByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "user not found")
			return
		}
		serverError(w, r, err)
		return
	}

	if !user.IsPlatformAdmin {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}

	if err := a.Queries.UpdateUserPlatformAdmin(r.Context(), sqlc.UpdateUserPlatformAdminParams{
		ID:              userID,
		IsPlatformAdmin: false,
	}); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAdminSuspendUser suspends a non-admin user.
func (a *App) handleAdminSuspendUser(w http.ResponseWriter, r *http.Request) {
	userID, err := parseUUIDParam(chi.URLParam(r, "userID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	user, err := a.Queries.GetUserByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "user not found")
			return
		}
		serverError(w, r, err)
		return
	}

	if isPlatformAdmin(user.Email, user.IsPlatformAdmin) {
		writeJSONError(w, http.StatusBadRequest, "demote admin before suspending")
		return
	}

	if user.Status == "suspended" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}

	if err := a.Queries.UpdateUserStatus(r.Context(), sqlc.UpdateUserStatusParams{
		ID:               userID,
		Status:           "suspended",
		SuspensionReason: pgtype.Text{String: "", Valid: true},
	}); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAdminUnsuspendUser reactivates a suspended user.
func (a *App) handleAdminUnsuspendUser(w http.ResponseWriter, r *http.Request) {
	userID, err := parseUUIDParam(chi.URLParam(r, "userID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	user, err := a.Queries.GetUserByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "user not found")
			return
		}
		serverError(w, r, err)
		return
	}

	if user.Status != "suspended" {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}

	if err := a.Queries.UpdateUserStatus(r.Context(), sqlc.UpdateUserStatusParams{
		ID:     userID,
		Status: "active",
	}); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAdminDeleteUser soft-deletes (locks out) a non-admin user by setting status to 'deleted'.
func (a *App) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	userID, err := parseUUIDParam(chi.URLParam(r, "userID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	user, err := a.Queries.GetUserByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "user not found")
			return
		}
		serverError(w, r, err)
		return
	}

	if isPlatformAdmin(user.Email, user.IsPlatformAdmin) {
		writeJSONError(w, http.StatusBadRequest, "demote admin before disabling")
		return
	}

	if err := a.Queries.UpdateUserStatus(r.Context(), sqlc.UpdateUserStatusParams{
		ID:     userID,
		Status: "deleted",
	}); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
