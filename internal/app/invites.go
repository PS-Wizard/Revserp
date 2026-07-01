package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type createOrganizationInviteRequest struct {
	ExpiresAt string `json:"expires_at"`
	MaxUses   int32  `json:"max_uses"`
}

type organizationInviteResponse struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	MaxUses        int32  `json:"max_uses"`
	UsedCount      int32  `json:"used_count"`
	RemainingUses  int32  `json:"remaining_uses"`
	ExpiresAt      string `json:"expires_at"`
	RevokedAt      string `json:"revoked_at,omitempty"`
	CreatedAt      string `json:"created_at"`
	Status         string `json:"status"`
}

type createOrganizationInviteResponse struct {
	Invite organizationInviteResponse `json:"invite"`
	Token  string                     `json:"token"`
}

type inviteLookupResponse struct {
	ID               string `json:"id"`
	OrganizationID   string `json:"organization_id"`
	OrganizationName string `json:"organization_name"`
	MaxUses          int32  `json:"max_uses"`
	UsedCount        int32  `json:"used_count"`
	RemainingUses    int32  `json:"remaining_uses"`
	ExpiresAt        string `json:"expires_at"`
	CreatedAt        string `json:"created_at"`
	Status           string `json:"status"`
}

// handleCreateOrganizationInvite creates one reusable invite link for an owner-managed organization.
func (a *App) handleCreateOrganizationInvite(w http.ResponseWriter, r *http.Request) {
	organizationID, err := parseUUIDParam(chi.URLParam(r, "organizationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}

	var requestBody createOrganizationInviteRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(requestBody.ExpiresAt))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid expires_at")
		return
	}
	if !expiresAt.After(time.Now().UTC()) {
		writeJSONError(w, http.StatusBadRequest, "expires_at must be in the future")
		return
	}
	if requestBody.MaxUses <= 0 {
		writeJSONError(w, http.StatusBadRequest, "max_uses must be greater than 0")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := requireOrganizationOwner(r.Context(), queries, organizationID, user.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}

	rawToken, err := generateOrganizationInviteToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	invite, err := queries.CreateOrganizationInvite(r.Context(), sqlc.CreateOrganizationInviteParams{
		OrganizationID:  organizationID,
		CreatedByUserID: user.ID,
		TokenHash:       hashOrganizationInviteToken(rawToken),
		MaxUses:         requestBody.MaxUses,
		ExpiresAt:       pgtype.Timestamptz{Time: expiresAt.UTC(), Valid: true},
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, createOrganizationInviteResponse{
		Invite: newOrganizationInviteResponse(invite),
		Token:  rawToken,
	})
}

// handleListOrganizationInvites lists all invite links for one owner-managed organization.
func (a *App) handleListOrganizationInvites(w http.ResponseWriter, r *http.Request) {
	organizationID, err := parseUUIDParam(chi.URLParam(r, "organizationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := requireOrganizationOwner(r.Context(), queries, organizationID, user.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}

	invites, err := queries.ListOrganizationInvites(r.Context(), organizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	responses := make([]organizationInviteResponse, 0, len(invites))
	for _, invite := range invites {
		responses = append(responses, newOrganizationInviteResponse(invite))
	}

	writeJSON(w, http.StatusOK, map[string]any{"invites": responses})
}

// handleRevokeOrganizationInvite revokes one invite link owned by one organization owner.
func (a *App) handleRevokeOrganizationInvite(w http.ResponseWriter, r *http.Request) {
	organizationID, err := parseUUIDParam(chi.URLParam(r, "organizationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}
	inviteID, err := parseUUIDParam(chi.URLParam(r, "inviteID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid invite id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := requireOrganizationOwner(r.Context(), queries, organizationID, user.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}

	invite, err := queries.GetOrganizationInviteByID(r.Context(), inviteID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "invite not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if invite.OrganizationID != organizationID {
		writeJSONError(w, http.StatusNotFound, "invite not found")
		return
	}
	if invite.RevokedAt.Valid {
		writeJSONError(w, http.StatusConflict, "invite already revoked")
		return
	}

	revokedRows, err := queries.RevokeOrganizationInvite(r.Context(), sqlc.RevokeOrganizationInviteParams{
		ID:             inviteID,
		OrganizationID: organizationID,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if revokedRows == 0 {
		writeJSONError(w, http.StatusNotFound, "invite not found")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleGetInvite resolves one invite link for the public invite landing page.
func (a *App) handleGetInvite(w http.ResponseWriter, r *http.Request) {
	tokenHash := hashOrganizationInviteToken(strings.TrimSpace(chi.URLParam(r, "token")))
	invite, err := a.Queries.GetOrganizationInviteByTokenHash(r.Context(), tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "invite not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, inviteLookupResponse{
		ID:               invite.ID.String(),
		OrganizationID:   invite.OrganizationID.String(),
		OrganizationName: invite.OrganizationName,
		MaxUses:          invite.MaxUses,
		UsedCount:        invite.UsedCount,
		RemainingUses:    remainingInviteUses(invite.MaxUses, invite.UsedCount),
		ExpiresAt:        invite.ExpiresAt.Time.UTC().Format(time.RFC3339),
		CreatedAt:        invite.CreatedAt.Time.UTC().Format(time.RFC3339),
		Status:           resolveInviteStatus(invite.RevokedAt, invite.ExpiresAt.Time, invite.MaxUses, invite.UsedCount),
	})
}

// handleAcceptInvite adds the current user to the invite organization when the link is still valid.
func (a *App) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	tokenHash := hashOrganizationInviteToken(strings.TrimSpace(chi.URLParam(r, "token")))
	session, ok := internalauth.SessionFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	invite, err := queries.GetOrganizationInviteByTokenHashForUpdate(r.Context(), tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "invite not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	switch resolveInviteStatus(invite.RevokedAt, invite.ExpiresAt.Time, invite.MaxUses, invite.UsedCount) {
	case "revoked":
		writeJSONError(w, http.StatusGone, "invite has been revoked")
		return
	case "expired":
		writeJSONError(w, http.StatusGone, "invite has expired")
		return
	case "exhausted":
		writeJSONError(w, http.StatusGone, "invite has reached its usage limit")
		return
	}

	_, err = queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{
		OrgID:  invite.OrganizationID,
		UserID: user.ID,
	})
	if err == nil {
		writeJSONError(w, http.StatusConflict, "user is already a member of this organization")
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := queries.AddOrganizationMember(r.Context(), sqlc.AddOrganizationMemberParams{
		OrgID:  invite.OrganizationID,
		UserID: user.ID,
		Role:   "member",
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := queries.IncrementOrganizationInviteUsedCount(r.Context(), invite.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := queries.UpdateSessionActiveOrganization(r.Context(), sqlc.UpdateSessionActiveOrganizationParams{
		ID:          session.SessionID,
		ActiveOrgID: invite.OrganizationID,
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"organization_id": invite.OrganizationID.String(),
	})
}

func requireOrganizationOwner(ctx context.Context, queries *sqlc.Queries, organizationID, userID pgtype.UUID) error {
	membership, err := queries.GetOrganizationMember(ctx, sqlc.GetOrganizationMemberParams{
		OrgID:  organizationID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errInviteForbidden
		}
		return err
	}
	if membership.Role != "owner" {
		return errInviteForbidden
	}
	return nil
}

func writeInvitePermissionError(w http.ResponseWriter, err error) {
	if errors.Is(err, errInviteForbidden) {
		writeJSONError(w, http.StatusForbidden, "forbidden")
		return
	}
	writeJSONError(w, http.StatusInternalServerError, "internal server error")
}

func newOrganizationInviteResponse(invite sqlc.OrganizationInvite) organizationInviteResponse {
	response := organizationInviteResponse{
		ID:             invite.ID.String(),
		OrganizationID: invite.OrganizationID.String(),
		MaxUses:        invite.MaxUses,
		UsedCount:      invite.UsedCount,
		RemainingUses:  remainingInviteUses(invite.MaxUses, invite.UsedCount),
		ExpiresAt:      invite.ExpiresAt.Time.UTC().Format(time.RFC3339),
		CreatedAt:      invite.CreatedAt.Time.UTC().Format(time.RFC3339),
		Status:         resolveInviteStatus(invite.RevokedAt, invite.ExpiresAt.Time, invite.MaxUses, invite.UsedCount),
	}
	if invite.RevokedAt.Valid {
		response.RevokedAt = invite.RevokedAt.Time.UTC().Format(time.RFC3339)
	}
	return response
}

func resolveInviteStatus(revokedAt pgtype.Timestamptz, expiresAt time.Time, maxUses, usedCount int32) string {
	if revokedAt.Valid {
		return "revoked"
	}
	if !expiresAt.UTC().After(time.Now().UTC()) {
		return "expired"
	}
	if usedCount >= maxUses {
		return "exhausted"
	}
	return "active"
}

func remainingInviteUses(maxUses, usedCount int32) int32 {
	remainingUses := maxUses - usedCount
	if remainingUses < 0 {
		return 0
	}
	return remainingUses
}

var errInviteForbidden = errors.New("invite forbidden")
