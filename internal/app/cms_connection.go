package app

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/runecms"
)

// CMSProvider is the CMS product backing one project connection. One project
// holds at most one active connection (project_cms_connections primary key).
type CMSProvider string

const (
	// CMSProviderRune is the Rune CMS product (cms__ tools).
	CMSProviderRune CMSProvider = "rune"
	// CMSProviderWordPress is the WordPress product (wp__ tools).
	CMSProviderWordPress CMSProvider = "wordpress"
)

// parseCMSProvider validates the connect-form provider value. Selecting a
// form provider alone never switches the saved connection; only a successful
// connect replacement does.
func parseCMSProvider(value string) (CMSProvider, bool) {
	switch CMSProvider(strings.ToLower(strings.TrimSpace(value))) {
	case CMSProviderRune:
		return CMSProviderRune, true
	case CMSProviderWordPress:
		return CMSProviderWordPress, true
	default:
		return "", false
	}
}

// CMSConnectFunc dials one provider session for validation (initialize plus
// tools/list discovery only, never tool execution). It mirrors
// RuneConnectFunc with a provider switch so tests can inject a fake per
// provider and production never bypasses client-side validation.
type CMSConnectFunc func(ctx context.Context, provider CMSProvider, endpoint, token string) (RuneSession, error)

// defaultWordPressConnect adapts runecms.ConnectWordPress to the plain
// connector shape. The transport worker owns the WordPress tool profile;
// this wrapper only erases the concrete return type like defaultRuneConnect.
func defaultWordPressConnect(ctx context.Context, endpoint, token string) (RuneSession, error) {
	session, err := runecms.ConnectWordPress(ctx, endpoint, token)
	if err != nil {
		return nil, err
	}
	return session, nil
}

// DefaultCMSConnect validates through the provider-owned client: Rune uses
// runecms.Connect, WordPress uses runecms.ConnectWordPress. Both validate
// the HTTP/HTTPS public destination and the known tool set inside the
// client; handlers only bound the raw inputs.
func DefaultCMSConnect(ctx context.Context, provider CMSProvider, endpoint, token string) (RuneSession, error) {
	if provider == CMSProviderWordPress {
		return defaultWordPressConnect(ctx, endpoint, token)
	}
	return defaultRuneConnect(ctx, endpoint, token)
}

// cmsConnector resolves the dial function for one provider. Rune keeps
// using the legacy RuneConnect override (nil degrades to 503, never to a
// hidden default); WordPress dials through the provider-aware CMSConnect.
// A nil connector answers 503 so handlers degrade safely.
func (a *App) cmsConnector(provider CMSProvider) func(ctx context.Context, endpoint, token string) (RuneSession, error) {
	if provider == CMSProviderRune {
		if a.RuneConnect == nil {
			return nil
		}
		return a.RuneConnect
	}
	if a.CMSConnect == nil {
		return nil
	}
	connect := a.CMSConnect
	return func(ctx context.Context, endpoint, token string) (RuneSession, error) {
		return connect(ctx, provider, endpoint, token)
	}
}

// requireCMSConnector ensures a discovery client resolves and the shared
// encryption secret is configured for the provider (never encrypt with an
// empty secret). Both providers share the same encryption service.
func (a *App) requireCMSConnector(w http.ResponseWriter, r *http.Request, provider CMSProvider) bool {
	if provider == CMSProviderRune && a.RuneConnect == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "rune integration is unavailable")
		return false
	}
	if provider == CMSProviderWordPress && a.CMSConnect == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "wordpress integration is unavailable")
		return false
	}
	if strings.TrimSpace(a.Config.GoogleTokenEncryptionSecret) == "" {
		serverError(w, r, errors.New("cms: token encryption secret is not configured"))
		return false
	}
	if a.GSCService == nil {
		serverError(w, r, errors.New("cms: credential service is not configured"))
		return false
	}
	return true
}

// cmsConnectFailure converts a connector failure into a safe HTTP response
// using the existing error-response style. The rune wording is byte-identical
// to runeConnectFailure; WordPress mirrors it. Raw errors are never surfaced.
func cmsConnectFailure(w http.ResponseWriter, r *http.Request, err error, provider CMSProvider) {
	if provider == CMSProviderRune {
		runeConnectFailure(w, r, err)
		return
	}
	code := runeErrorCode(err)
	log.Printf("wordpress request failed: code=%q", code)
	switch code {
	case "invalid_endpoint":
		writeJSONError(w, http.StatusBadRequest, "invalid wordpress endpoint")
	case "invalid_token", "unauthorized":
		writeJSONError(w, http.StatusBadRequest, "invalid wordpress credentials")
	case "unsupported_transport":
		writeJSONError(w, http.StatusBadRequest, "unsupported wordpress endpoint")
	case "unreachable":
		writeJSONError(w, http.StatusBadGateway, "wordpress endpoint unreachable")
	case "timeout":
		writeJSONError(w, http.StatusGatewayTimeout, "wordpress endpoint timed out")
	case "too_large":
		writeJSONError(w, http.StatusBadGateway, "wordpress response too large")
	case "invalid_tools":
		writeJSONError(w, http.StatusBadGateway, "invalid wordpress tools response")
	default:
		writeJSONError(w, http.StatusBadGateway, "failed to reach wordpress endpoint")
	}
}

type cmsConnectRequest struct {
	Provider    string `json:"provider"`
	EndpointURL string `json:"endpoint_url"`
	BearerToken string `json:"bearer_token"`
}

type cmsToolResponse struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Group       string `json:"group,omitempty"`
	Write       bool   `json:"write,omitempty"`
}

type cmsStatusResponse struct {
	Connected     bool              `json:"connected"`
	Provider      string            `json:"provider,omitempty"`
	EndpointURL   string            `json:"endpoint_url,omitempty"`
	LastCheckedAt string            `json:"last_checked_at,omitempty"`
	Tools         []cmsToolResponse `json:"tools"`
}

// cmsWriteTool reports whether a stored tool name performs a write, for
// status display only. Both providers use their transport-owned write sets,
// which are keyed by namespaced names, so a raw discovered name is
// namespaced before the lookup.
func cmsWriteTool(provider CMSProvider, name string) bool {
	if provider == CMSProviderWordPress {
		if !strings.HasPrefix(name, aichattools.WordPressToolPrefix) {
			name = aichattools.NamespaceWordPressName(name)
		}
		return aichattools.IsWordPressWriteName(name)
	}
	if !strings.HasPrefix(name, aichattools.RuneToolPrefix) {
		name = aichattools.NamespaceRuneName(name)
	}
	return aichattools.IsRuneWriteName(name)
}

// newCMSStatusResponse builds the public status shape. It never includes
// token material. Every accepted discovered tool is reported: catalogued
// names take the reviewed local description, group and write flag, while a
// discovered tool the catalogue does not describe keeps its own name,
// bounded description and optional server group and is never dropped.
func newCMSStatusResponse(connected bool, provider CMSProvider, endpointURL string, checkedAt time.Time, tools []runeToolResponse) cmsStatusResponse {
	public := make([]cmsToolResponse, 0, len(tools))
	overrides := map[string]aichattools.WordPressToolInfo{}
	if provider == CMSProviderWordPress {
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			names = append(names, tool.Name)
		}
		for _, info := range aichattools.WordPressToolsInfo(names) {
			if info.Known {
				overrides[strings.TrimPrefix(info.Name, aichattools.WordPressToolPrefix)] = info
			}
		}
	}
	for _, tool := range tools {
		entry := cmsToolResponse{Name: tool.Name, Description: tool.Description, Group: tool.Group}
		if info, ok := overrides[strings.TrimPrefix(tool.Name, aichattools.WordPressToolPrefix)]; ok {
			entry.Description = info.Description
			if entry.Group == "" {
				entry.Group = info.Group
			}
			entry.Write = info.Write
		} else if provider == CMSProviderRune && entry.Group == "" {
			entry.Group = string(provider)
		}
		if cmsWriteTool(provider, tool.Name) {
			entry.Write = true
		}
		public = append(public, entry)
	}
	response := cmsStatusResponse{Tools: public}
	if connected {
		response.Connected = true
		response.Provider = string(provider)
		response.EndpointURL = endpointURL
		response.LastCheckedAt = checkedAt.UTC().Format(time.RFC3339)
	}
	return response
}

// cmsProjectForWrite resolves the project and requires organization
// ownership. Ownership is validated before any outbound CMS request.
func (a *App) cmsProjectForWrite(w http.ResponseWriter, r *http.Request, projectID pgtype.UUID) (sqlc.Project, bool) {
	return a.runeProjectForWrite(w, r, projectID)
}

func (a *App) handleCMSStatus(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	project, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}
	connection, err := a.Queries.GetProjectCMSConnectionByProjectID(r.Context(), project.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, newCMSStatusResponse(false, "", "", time.Time{}, nil))
			return
		}
		serverError(w, r, err)
		return
	}
	tools, err := parseRuneStoredTools(connection.Tools)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newCMSStatusResponse(true, CMSProvider(connection.Provider), connection.EndpointUrl, connection.LastCheckedAt.Time, tools))
}

// handleCMSConnect discovers the endpoint, closes the session, then
// atomically upserts encrypted credentials + tools + a new revision. A failed
// replacement leaves any existing connection unchanged. A successful
// replacement rotates the revision and invalidates stale pending approvals so
// paused turns cannot resume against the old connection.
func (a *App) handleCMSConnect(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var body cmsConnectRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	provider, ok := parseCMSProvider(body.Provider)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid cms provider")
		return
	}
	endpoint, ok := validateRuneEndpoint(body.EndpointURL)
	if !ok {
		cmsConnectFailure(w, r, runeCodedError{code: "invalid_endpoint"}, provider)
		return
	}
	if body.BearerToken == "" || len(body.BearerToken) > runeMaxTokenLen {
		cmsConnectFailure(w, r, runeCodedError{code: "invalid_token"}, provider)
		return
	}
	project, ok := a.cmsProjectForWrite(w, r, projectID)
	if !ok {
		return
	}
	if !a.requireCMSConnector(w, r, provider) {
		return
	}

	session, err := a.cmsConnector(provider)(r.Context(), endpoint, body.BearerToken)
	if err != nil {
		cmsConnectFailure(w, r, err, provider)
		return
	}
	discovered := session.Tools()
	if closeErr := session.Close(); closeErr != nil {
		log.Printf("cms session close failed: code=%q", runeErrorCode(closeErr))
	}
	toolsRaw, tools, err := normalizeRuneTools(discovered)
	if err != nil {
		cmsConnectFailure(w, r, err, provider)
		return
	}
	encrypted, err := a.GSCService.EncryptSecret(body.BearerToken)
	if err != nil {
		serverError(w, r, err)
		return
	}
	connection, err := a.Queries.UpsertProjectCMSConnection(r.Context(), sqlc.UpsertProjectCMSConnectionParams{
		ProjectID:      project.ID,
		Provider:       string(provider),
		EndpointUrl:    endpoint,
		EncryptedToken: encrypted,
		Tools:          toolsRaw,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := a.invalidateProjectCMSApprovals(r.Context(), project.ID, "cms connection replaced"); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newCMSStatusResponse(true, provider, connection.EndpointUrl, connection.LastCheckedAt.Time, tools))
}

// handleCMSCheck re-discovers with the stored token and persists fresh tools
// only when the revision is unchanged; otherwise it reports a conflict.
func (a *App) handleCMSCheck(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var body struct{}
	if !readOptionalStrictJSONOrRespond(w, r, &body) {
		return
	}
	project, ok := a.cmsProjectForWrite(w, r, projectID)
	if !ok {
		return
	}
	connection, err := a.Queries.GetProjectCMSConnectionByProjectID(r.Context(), project.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "cms is not connected")
			return
		}
		serverError(w, r, err)
		return
	}
	provider := CMSProvider(connection.Provider)
	if !a.requireCMSConnector(w, r, provider) {
		return
	}
	token, err := a.GSCService.DecryptSecret(connection.EncryptedToken)
	if err != nil || token == "" {
		serverError(w, r, errors.New("cms: failed to decrypt stored credentials"))
		return
	}

	session, err := a.cmsConnector(provider)(r.Context(), connection.EndpointUrl, token)
	if err != nil {
		cmsConnectFailure(w, r, err, provider)
		return
	}
	discovered := session.Tools()
	if closeErr := session.Close(); closeErr != nil {
		log.Printf("cms session close failed: code=%q", runeErrorCode(closeErr))
	}
	toolsRaw, tools, err := normalizeRuneTools(discovered)
	if err != nil {
		cmsConnectFailure(w, r, err, provider)
		return
	}
	updated, err := a.Queries.UpdateProjectCMSConnectionChecked(r.Context(), sqlc.UpdateProjectCMSConnectionCheckedParams{
		ProjectID: project.ID,
		Tools:     toolsRaw,
		Revision:  connection.Revision,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "cms connection changed, retry")
			return
		}
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newCMSStatusResponse(true, provider, updated.EndpointUrl, updated.LastCheckedAt.Time, tools))
}

// handleCMSDisconnect deletes the saved connection and invalidates pending
// approvals so paused turns cannot resume against a removed connection.
// Organization owners only.
func (a *App) handleCMSDisconnect(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var body struct{}
	if !readOptionalStrictJSONOrRespond(w, r, &body) {
		return
	}
	project, ok := a.cmsProjectForWrite(w, r, projectID)
	if !ok {
		return
	}
	deleted, err := a.Queries.DeleteProjectCMSConnectionByProjectID(r.Context(), project.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if deleted == 0 {
		writeJSONError(w, http.StatusNotFound, "cms is not connected")
		return
	}
	if err := a.invalidateProjectCMSApprovals(r.Context(), project.ID, "cms connection removed"); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newCMSStatusResponse(false, "", "", time.Time{}, nil))
}

// invalidateProjectCMSApprovals marks every pending approval of the project
// invalid and fails their waiting turns, so no paused turn can resume
// against a replaced or removed connection. Waiting turns fail (not stop:
// the user did not cancel) with a cms_connection_changed code.
func (a *App) invalidateProjectCMSApprovals(ctx context.Context, projectID pgtype.UUID, summary string) error {
	invalidated, err := a.Queries.InvalidatePendingCMSApprovalsForProject(ctx, sqlc.InvalidatePendingCMSApprovalsForProjectParams{
		ProjectID: projectID,
		Summary:   summary,
	})
	if err != nil {
		return err
	}
	for _, approval := range invalidated {
		payload, err := cmsApprovalDecidedPayload(approval)
		if err != nil {
			return err
		}
		if err := a.insertAITurnEvent(ctx, approval.TurnID, "approval_decided", payload); err != nil {
			return err
		}
	}
	failed, err := a.Queries.FailWaitingTurnsForProject(ctx, projectID)
	if err != nil {
		return err
	}
	for _, turn := range failed {
		if !turn.IsPartial {
			if _, err := a.Queries.FailPendingAssistantMessageForTurn(ctx, turn.ID); err != nil {
				return err
			}
		}
		if err := a.insertAITurnEvent(ctx, turn.ID, "failed", map[string]string{"error_code": "cms_connection_changed"}); err != nil {
			return err
		}
	}
	return nil
}

// insertAITurnEvent appends one SSE-replayed event. Payload must be a JSON
// object; callers pass maps or structs that marshal to one.
func (a *App) insertAITurnEvent(ctx context.Context, turnID pgtype.UUID, eventType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = a.DB.Exec(ctx, `INSERT INTO ai_turn_events(turn_id, event_type, payload) VALUES ($1, $2, $3::jsonb)`, turnID, eventType, string(encoded))
	return err
}
