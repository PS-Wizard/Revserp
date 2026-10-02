package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/runecms"
)

// RuneTool is the storable/discoverable Rune tool shape: name, description,
// and JSON input schema, never secrets.
type RuneTool = runecms.Tool

// RuneSession mirrors *runecms.Session: discovered tools plus Close.
type RuneSession interface {
	Tools() []RuneTool
	Close() error
}

// RuneConnectFunc mirrors runecms.Connect: initialize + tools/list discovery
// only, validating the HTTP/HTTPS public destination and the known tool set inside
// the client. It is an App dependency so tests can inject a fake; production
// must never bypass client-side validation.
type RuneConnectFunc func(ctx context.Context, endpoint, token string) (RuneSession, error)

// defaultRuneConnect adapts runecms.Connect to RuneConnectFunc.
// *runecms.Session already implements RuneSession; the wrapper only erases
// the concrete return type so App.New can wire the real client directly.
func defaultRuneConnect(ctx context.Context, endpoint, token string) (RuneSession, error) {
	session, err := runecms.Connect(ctx, endpoint, token)
	if err != nil {
		return nil, err
	}
	return session, nil
}

// runeCodedError carries a safe local code (invalid_tools from malformed
// remote data) without embedding endpoint or secret details.
type runeCodedError struct {
	code string
}

func (e runeCodedError) Error() string { return "rune: " + e.code }
func (e runeCodedError) Code() string  { return e.code }

// runeErrorCode maps connector failures to safe codes: local
// runeCodedError values first, then *runecms.Error via runecms.ErrorCode.
// Anything else (including context deadlines wrapped in foreign errors)
// maps to "" so callers fall through to the safe default response. Raw
// messages are never surfaced.
func runeErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var local runeCodedError
	if errors.As(err, &local) {
		return local.Code()
	}
	var localPtr *runeCodedError
	if errors.As(err, &localPtr) && localPtr != nil {
		return localPtr.Code()
	}
	var runeErr *runecms.Error
	if errors.As(err, &runeErr) && runeErr != nil {
		return runecms.ErrorCode(err)
	}
	return ""
}

const (
	runeMaxEndpointLen     = 2048
	runeMaxTokenLen        = 8192
	runeMaxTools           = runecms.MaxDiscoveredTools
	runeMaxToolNameLen     = 256
	runeMaxToolDescLen     = 8192
	runeMaxToolGroupLen    = 256
	runeMaxToolSchemaBytes = 64 << 10
)

// runeConnectFailure converts a connector failure into a safe HTTP response.
// It switches on the safe code only and never echoes the raw error, which may
// carry endpoint or credential hints.
func runeConnectFailure(w http.ResponseWriter, r *http.Request, err error) {
	code := runeErrorCode(err)
	log.Printf("rune request failed: code=%q", code)
	switch code {
	case "invalid_endpoint":
		writeJSONError(w, http.StatusBadRequest, "invalid rune endpoint")
	case "invalid_token", "unauthorized":
		writeJSONError(w, http.StatusBadRequest, "invalid rune credentials")
	case "unsupported_transport":
		writeJSONError(w, http.StatusBadRequest, "unsupported rune endpoint")
	case "unreachable":
		writeJSONError(w, http.StatusBadGateway, "rune endpoint unreachable")
	case "timeout":
		writeJSONError(w, http.StatusGatewayTimeout, "rune endpoint timed out")
	case "too_large":
		writeJSONError(w, http.StatusBadGateway, "rune response too large")
	case "invalid_tools":
		writeJSONError(w, http.StatusBadGateway, "invalid rune tools response")
	default:
		writeJSONError(w, http.StatusBadGateway, "failed to reach rune endpoint")
	}
}

type runeConnectRequest struct {
	EndpointURL string `json:"endpoint_url"`
	BearerToken string `json:"bearer_token"`
}

type runeToolResponse struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Group       string `json:"group,omitempty"`
}

type runeStatusResponse struct {
	Connected     bool               `json:"connected"`
	EndpointURL   string             `json:"endpoint_url,omitempty"`
	LastCheckedAt string             `json:"last_checked_at,omitempty"`
	Tools         []runeToolResponse `json:"tools"`
}

// runeStoredTool is the only shape persisted in project_cms_connections.tools:
// name/description/input_schema, no secrets.
type runeStoredTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Group       string          `json:"group,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// decodeStrictJSON decodes exactly one JSON value with unknown fields
// rejected. A JSON null literal is rejected (decoding null into a struct
// would otherwise succeed silently). Trailing data after the first value is
// rejected. io.EOF signals an empty body.
func decodeStrictJSON(r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(noopWriter{}, r.Body, maxRequestBodySize)
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if string(bytes.TrimSpace(raw)) == "null" {
		return errors.New("invalid json")
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("invalid json: trailing data")
		}
		return err
	}
	return nil
}

// readStrictJSONOrRespond follows the readJSONOrRespond pattern (1 MB bound,
// 413/400 mapping), additionally rejects unknown fields, trailing JSON, and
// null literals, and requires a body (EOF is invalid).
func readStrictJSONOrRespond(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := decodeStrictJSON(r, target); err != nil {
		return respondReadJSONError(w, err)
	}
	return true
}

// normalizeRuneTools validates discovered tools and marshals the storable
// subset. It returns a runeCodedError("invalid_tools") for malformed remote
// data so callers map it through runeConnectFailure.
func normalizeRuneTools(tools []RuneTool) ([]byte, []runeToolResponse, error) {
	invalid := func() ([]byte, []runeToolResponse, error) {
		return nil, nil, runeCodedError{code: "invalid_tools"}
	}
	// An empty accepted list is a valid connected state (every advertised
	// tool can be exposure-excluded); only an oversized one is malformed.
	if len(tools) > runeMaxTools {
		return invalid()
	}
	seen := make(map[string]struct{}, len(tools))
	stored := make([]runeStoredTool, 0, len(tools))
	response := make([]runeToolResponse, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == "" || len(tool.Name) > runeMaxToolNameLen {
			return invalid()
		}
		if len(tool.Description) > runeMaxToolDescLen || len(tool.Group) > runeMaxToolGroupLen {
			return invalid()
		}
		if _, dup := seen[tool.Name]; dup {
			return invalid()
		}
		seen[tool.Name] = struct{}{}
		schema := tool.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{}`)
		}
		if len(schema) > runeMaxToolSchemaBytes || !json.Valid(schema) {
			return invalid()
		}
		stored = append(stored, runeStoredTool{
			Name:        tool.Name,
			Description: tool.Description,
			Group:       tool.Group,
			InputSchema: schema,
		})
		response = append(response, runeToolResponse{Name: tool.Name, Description: tool.Description, Group: tool.Group})
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return invalid()
	}
	return raw, response, nil
}

// parseRuneStoredTools extracts the public name/description view from a tools
// JSONB value. Stored rows are written by normalizeRuneTools, so a decode
// failure indicates corruption.
func parseRuneStoredTools(raw []byte) ([]runeToolResponse, error) {
	if len(raw) == 0 {
		return []runeToolResponse{}, nil
	}
	var stored []runeStoredTool
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	response := make([]runeToolResponse, 0, len(stored))
	for _, tool := range stored {
		response = append(response, runeToolResponse{Name: tool.Name, Description: tool.Description, Group: tool.Group})
	}
	return response, nil
}

func newRuneStatusResponse(connected bool, endpointURL string, checkedAt time.Time, tools []runeToolResponse) runeStatusResponse {
	if tools == nil {
		tools = []runeToolResponse{}
	}
	response := runeStatusResponse{Connected: connected, Tools: tools}
	if connected {
		response.EndpointURL = endpointURL
		response.LastCheckedAt = checkedAt.UTC().Format(time.RFC3339)
	}
	return response
}

// runeProjectForWrite resolves the project and requires organization ownership.
// Ownership is validated before any outbound RuneCMS request.
func (a *App) runeProjectForWrite(w http.ResponseWriter, r *http.Request, projectID pgtype.UUID) (sqlc.Project, bool) {
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return sqlc.Project{}, false
	}
	project, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return sqlc.Project{}, false
		}
		serverError(w, r, err)
		return sqlc.Project{}, false
	}
	if err := requireOrganizationOwner(r.Context(), a.Queries, project.OrganizationID, principal.User.ID); err != nil {
		writeInvitePermissionError(w, err)
		return sqlc.Project{}, false
	}
	return project, true
}

// requireRuneConnector ensures a discovery client is wired and the shared
// encryption secret is configured (never encrypt with an empty secret).
func (a *App) requireRuneConnector(w http.ResponseWriter, r *http.Request) bool {
	if a.RuneConnect == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "rune integration is unavailable")
		return false
	}
	if strings.TrimSpace(a.Config.GoogleTokenEncryptionSecret) == "" {
		serverError(w, r, errors.New("rune: token encryption secret is not configured"))
		return false
	}
	if a.GSCService == nil {
		serverError(w, r, errors.New("rune: credential service is not configured"))
		return false
	}
	return true
}

// validateRuneEndpoint trims the URL and bounds it. HTTP/HTTPS scheme,
// public-destination and known-tool validation live in the client; the
// handler only rejects empty, oversized, unparsable, or non-HTTP(S) values
// with safe messages.
func validateRuneEndpoint(value string) (string, bool) {
	endpoint := strings.TrimSpace(value)
	if endpoint == "" || len(endpoint) > runeMaxEndpointLen {
		return "", false
	}
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", false
	}
	return endpoint, true
}

// handleRuneStatus returns the saved connection. Any member may read it; it
// is never a fresh health assertion and never includes token material.
func (a *App) handleRuneStatus(w http.ResponseWriter, r *http.Request) {
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
			writeJSON(w, http.StatusOK, newRuneStatusResponse(false, "", time.Time{}, nil))
			return
		}
		serverError(w, r, err)
		return
	}
	// The legacy rune surface only sees rune connections; a WordPress
	// connection reports disconnected here by design (one active CMS).
	if connection.Provider != string(CMSProviderRune) {
		writeJSON(w, http.StatusOK, newRuneStatusResponse(false, "", time.Time{}, nil))
		return
	}
	tools, err := parseRuneStoredTools(connection.Tools)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newRuneStatusResponse(true, connection.EndpointUrl, connection.LastCheckedAt.Time, tools))
}

// handleRuneConnect discovers the endpoint, closes the session, then
// atomically upserts encrypted credentials + tools + a new revision. A failed
// replacement leaves any existing connection unchanged. Kept as a
// backward-compatible wrapper: it always stores provider rune.
func (a *App) handleRuneConnect(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var body runeConnectRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	endpoint, ok := validateRuneEndpoint(body.EndpointURL)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid rune endpoint")
		return
	}
	if body.BearerToken == "" || len(body.BearerToken) > runeMaxTokenLen {
		writeJSONError(w, http.StatusBadRequest, "invalid rune credentials")
		return
	}
	project, ok := a.runeProjectForWrite(w, r, projectID)
	if !ok {
		return
	}
	if !a.requireRuneConnector(w, r) {
		return
	}

	session, err := a.RuneConnect(r.Context(), endpoint, body.BearerToken)
	if err != nil {
		runeConnectFailure(w, r, err)
		return
	}
	discovered := session.Tools()
	if closeErr := session.Close(); closeErr != nil {
		log.Printf("rune session close failed: code=%q", runeErrorCode(closeErr))
	}
	toolsRaw, tools, err := normalizeRuneTools(discovered)
	if err != nil {
		runeConnectFailure(w, r, err)
		return
	}
	encrypted, err := a.GSCService.EncryptSecret(body.BearerToken)
	if err != nil {
		serverError(w, r, err)
		return
	}
	connection, err := a.Queries.UpsertProjectCMSConnection(r.Context(), sqlc.UpsertProjectCMSConnectionParams{
		ProjectID:      project.ID,
		Provider:       string(CMSProviderRune),
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
	writeJSON(w, http.StatusOK, newRuneStatusResponse(true, connection.EndpointUrl, connection.LastCheckedAt.Time, tools))
}

// handleRuneCheck re-discovers with the stored token and persists fresh tools
// only when the revision is unchanged; otherwise it reports a conflict.
func (a *App) handleRuneCheck(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var body struct{}
	if !readOptionalStrictJSONOrRespond(w, r, &body) {
		return
	}
	project, ok := a.runeProjectForWrite(w, r, projectID)
	if !ok {
		return
	}
	if !a.requireRuneConnector(w, r) {
		return
	}
	connection, err := a.Queries.GetProjectCMSConnectionByProjectID(r.Context(), project.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "rune is not connected")
			return
		}
		serverError(w, r, err)
		return
	}
	if connection.Provider != string(CMSProviderRune) {
		writeJSONError(w, http.StatusNotFound, "rune is not connected")
		return
	}
	token, err := a.GSCService.DecryptSecret(connection.EncryptedToken)
	if err != nil || token == "" {
		serverError(w, r, errors.New("rune: failed to decrypt stored credentials"))
		return
	}

	session, err := a.RuneConnect(r.Context(), connection.EndpointUrl, token)
	if err != nil {
		runeConnectFailure(w, r, err)
		return
	}
	discovered := session.Tools()
	if closeErr := session.Close(); closeErr != nil {
		log.Printf("rune session close failed: code=%q", runeErrorCode(closeErr))
	}
	toolsRaw, tools, err := normalizeRuneTools(discovered)
	if err != nil {
		runeConnectFailure(w, r, err)
		return
	}
	updated, err := a.Queries.UpdateProjectCMSConnectionChecked(r.Context(), sqlc.UpdateProjectCMSConnectionCheckedParams{
		ProjectID: project.ID,
		Tools:     toolsRaw,
		Revision:  connection.Revision,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "rune connection changed, retry")
			return
		}
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newRuneStatusResponse(true, updated.EndpointUrl, updated.LastCheckedAt.Time, tools))
}

// handleRuneDisconnect deletes the saved connection. Organization owners only.
func (a *App) handleRuneDisconnect(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var body struct{}
	if !readOptionalStrictJSONOrRespond(w, r, &body) {
		return
	}
	project, ok := a.runeProjectForWrite(w, r, projectID)
	if !ok {
		return
	}
	connection, err := a.Queries.GetProjectCMSConnectionByProjectID(r.Context(), project.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		serverError(w, r, err)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) || connection.Provider != string(CMSProviderRune) {
		writeJSONError(w, http.StatusNotFound, "rune is not connected")
		return
	}
	deleted, err := a.Queries.DeleteProjectCMSConnectionByProjectID(r.Context(), project.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if deleted == 0 {
		writeJSONError(w, http.StatusNotFound, "rune is not connected")
		return
	}
	if err := a.invalidateProjectCMSApprovals(r.Context(), project.ID, "cms connection removed"); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newRuneStatusResponse(false, "", time.Time{}, nil))
}

// readOptionalStrictJSONOrRespond accepts an empty body or a strict-decoded
// JSON object, rejecting malformed, null, non-object, unknown-field, or
// trailing-JSON payloads.
func readOptionalStrictJSONOrRespond(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Body == nil {
		return true
	}
	if err := decodeStrictJSON(r, target); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		return respondReadJSONError(w, err)
	}
	return true
}
