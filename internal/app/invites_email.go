package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	internalmail "github.com/ps-wizard/revserp/internal/mail"
)

const maxInviteEmailsPerRequest = 20

type emailInviteRequest struct {
	Emails []string `json:"emails"`
	// Token is the raw token from the create response. Only the hash is
	// stored, so the sender proves possession of the link it is emailing.
	Token string `json:"token"`
}

type emailInviteResult struct {
	Email string `json:"email"`
	Sent  bool   `json:"sent"`
	Error string `json:"error,omitempty"`
}

// handleEmailOrganizationInvite emails an existing active invite link.
func (a *App) handleEmailOrganizationInvite(w http.ResponseWriter, r *http.Request) {
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
	var requestBody emailInviteRequest
	if !readJSONOrRespond(w, r, &requestBody) {
		return
	}
	if len(requestBody.Emails) == 0 {
		writeJSONError(w, http.StatusBadRequest, "emails must not be empty")
		return
	}
	if len(requestBody.Emails) > maxInviteEmailsPerRequest {
		writeJSONError(w, http.StatusBadRequest, "at most 20 emails per request")
		return
	}
	rawToken := strings.TrimSpace(requestBody.Token)
	if rawToken == "" {
		writeJSONError(w, http.StatusBadRequest, "invite token is required")
		return
	}
	if a.Mailer == nil || !a.Mailer.Configured() {
		writeJSONError(w, http.StatusServiceUnavailable, "email is not configured")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User
	if err := requireOrganizationManager(r.Context(), queries, organizationID, user.ID); err != nil {
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
	if hashOrganizationInviteToken(rawToken) != invite.TokenHash {
		writeJSONError(w, http.StatusNotFound, "invite not found")
		return
	}
	if status := resolveInviteStatus(invite.RevokedAt, invite.ExpiresAt.Time, invite.MaxUses, invite.UsedCount); status != "active" {
		writeJSONError(w, http.StatusGone, "invite is "+status)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	orgName := organizationDisplayName(principal, organizationID)
	inviter := strings.TrimSpace(user.Name.String)
	if inviter == "" {
		inviter = strings.TrimSpace(user.Email)
	}
	origin := ResolveEmailOrigin(r, a.Config)
	inviteURL := strings.TrimSuffix(origin, "/") + "/invite/" + rawToken

	results := make([]emailInviteResult, 0, len(requestBody.Emails))
	seen := make(map[string]struct{}, len(requestBody.Emails))
	for _, raw := range requestBody.Emails {
		address := strings.TrimSpace(raw)
		key := strings.ToLower(address)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		parsed, parseErr := mail.ParseAddress(address)
		if parseErr != nil || strings.TrimSpace(parsed.Address) == "" {
			results = append(results, emailInviteResult{Email: address, Error: "invalid email address"})
			continue
		}
		if !domainAcceptsMail(r.Context(), parsed.Address) {
			results = append(results, emailInviteResult{Email: parsed.Address, Error: "domain does not accept email"})
			continue
		}
		content := internalmail.BuildInvite(inviter, orgName, inviteURL)
		if sendErr := a.Mailer.Send(parsed.Address, content.Subject, content.Text, content.HTML); sendErr != nil {
			results = append(results, emailInviteResult{Email: parsed.Address, Error: "failed to send"})
			continue
		}
		results = append(results, emailInviteResult{Email: parsed.Address, Sent: true})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func organizationDisplayName(principal Principal, organizationID pgtype.UUID) string {
	for _, org := range principal.Organizations {
		if org.ID == organizationID {
			if name := strings.TrimSpace(org.Name); name != "" {
				return name
			}
		}
	}
	return "your workspace"
}

// domainAcceptsMail rejects addresses whose domain has no MX record and no
// host record (the RFC 5321 implicit MX). Only a definitive "not found" counts:
// a DNS timeout or server failure lets the send through rather than blocking.
func domainAcceptsMail(ctx context.Context, address string) bool {
	domain := address[strings.LastIndex(address, "@")+1:]
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if mx, err := net.DefaultResolver.LookupMX(ctx, domain); err == nil && len(mx) > 0 {
		return true
	} else if !isDNSNotFound(err) {
		return true
	}
	_, err := net.DefaultResolver.LookupHost(ctx, domain)
	return !isDNSNotFound(err)
}

func isDNSNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) && dnsErr.IsNotFound
}
