package app

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const agentSetupCodeTTL = 10 * time.Minute

type apiKeyResponse struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"token_prefix"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
	RevokedAt  *string `json:"revoked_at"`
}

type agentSetupCodeResponse struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
}

type redeemAgentSetupRequest struct {
	Code string `json:"code"`
}

func (a *App) handleListAPIKeys(w http.ResponseWriter, r *http.Request) {
	userID, err := a.currentUserID(r)
	if err != nil {
		serverError(w, r, err)
		return
	}

	rows, err := a.Queries.ListAPIKeysForUser(r.Context(), userID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	keys := make([]apiKeyResponse, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, apiKeyResponse{
			ID:         row.ID.String(),
			Name:       row.Name,
			Prefix:     row.TokenPrefix,
			CreatedAt:  formatTimestamp(row.CreatedAt),
			LastUsedAt: optionalTimestamp(row.LastUsedAt),
			RevokedAt:  optionalTimestamp(row.RevokedAt),
		})
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": keys})
}

func (a *App) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	apiKeyID, err := parseUUIDParam(chi.URLParam(r, "apiKeyID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid api key id")
		return
	}
	userID, err := a.currentUserID(r)
	if err != nil {
		serverError(w, r, err)
		return
	}

	rows, err := a.Queries.RevokeAPIKeyForUser(r.Context(), sqlc.RevokeAPIKeyForUserParams{
		ID:     apiKeyID,
		UserID: userID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	if rows == 0 {
		writeJSONError(w, http.StatusNotFound, "api key not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleCreateAgentSetupCode(w http.ResponseWriter, r *http.Request) {
	userID, err := a.currentUserID(r)
	if err != nil {
		serverError(w, r, err)
		return
	}

	raw, hash, err := a.APIKeyManager.GenerateSetupCode()
	if err != nil {
		serverError(w, r, err)
		return
	}
	expiresAt := time.Now().UTC().Add(agentSetupCodeTTL)
	if _, err := a.Queries.CreateAgentSetupCode(r.Context(), sqlc.CreateAgentSetupCodeParams{
		UserID:    userID,
		Name:      "AI agent",
		CodeHash:  hash,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	}); err != nil {
		serverError(w, r, err)
		return
	}

	setNoStore(w)
	writeJSON(w, http.StatusCreated, agentSetupCodeResponse{
		Code:      raw,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	})
}

func (a *App) handleRedeemAgentSetup(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	var body redeemAgentSetupRequest
	if err := readJSON(r, &body); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSONError(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
		writeInvalidSetupCode(w)
		return
	}
	rawCode := strings.TrimSpace(body.Code)
	if !strings.HasPrefix(rawCode, "rvs_setup_") {
		writeInvalidSetupCode(w)
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := a.Queries.WithTx(tx)

	setup, err := queries.GetAgentSetupCodeForUpdate(r.Context(), internalauth.HashCredential(rawCode))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeInvalidSetupCode(w)
			return
		}
		serverError(w, r, err)
		return
	}
	if setup.UserStatus != "active" || !setup.ExpiresAt.Valid || !setup.ExpiresAt.Time.After(time.Now().UTC()) || setup.RedeemedAt.Valid {
		writeInvalidSetupCode(w)
		return
	}

	rawKey, prefix, hash, err := a.APIKeyManager.GenerateAPIKey()
	if err != nil {
		serverError(w, r, err)
		return
	}
	if _, err := queries.CreateAPIKey(r.Context(), sqlc.CreateAPIKeyParams{
		UserID:      setup.UserID,
		Name:        setup.Name,
		TokenPrefix: prefix,
		TokenHash:   hash,
	}); err != nil {
		serverError(w, r, err)
		return
	}
	rows, err := queries.MarkAgentSetupCodeRedeemed(r.Context(), setup.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if rows != 1 {
		writeInvalidSetupCode(w)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "Authorization: Bearer %s\n", rawKey)
}

func (a *App) handleV1Me(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
	organizations := principal.Organizations
	writeJSON(w, http.StatusOK, map[string]any{
		"user":          newUserResponse(user),
		"organizations": newOrganizationResponses(organizations),
	})
}

func writeInvalidSetupCode(w http.ResponseWriter) {
	writeJSONError(w, http.StatusUnauthorized, "invalid or expired setup code")
}

func optionalTimestamp(value pgtype.Timestamptz) *string {
	if !value.Valid {
		return nil
	}
	formatted := formatTimestamp(value)
	return &formatted
}
