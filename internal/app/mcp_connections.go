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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/mcpclient"
)

// MCPService is the connection service: the optional local adapter, never a
// transport protocol. Both values speak the same generic MCP path.
type MCPService string

const (
	MCPServiceWordPress MCPService = "wordpress"
	MCPServiceCustom    MCPService = "custom"
)

func parseMCPService(value string) (MCPService, bool) {
	switch MCPService(strings.ToLower(strings.TrimSpace(value))) {
	case MCPServiceWordPress:
		return MCPServiceWordPress, true
	case MCPServiceCustom:
		return MCPServiceCustom, true
	default:
		return "", false
	}
}

// MCPSession is one open MCP connection for validation. *mcpclient.Session
// satisfies it directly; tests substitute a fake.
type MCPSession interface {
	Tools() []mcpclient.Tool
	Close() error
}

// MCPConnectFunc dials one generic session for validation (initialize plus
// tools/list discovery only, never tool execution).
type MCPConnectFunc func(ctx context.Context, endpoint, token string) (MCPSession, error)

// DefaultMCPConnect validates through the generic transport client.
func DefaultMCPConnect(ctx context.Context, endpoint, token string) (MCPSession, error) {
	return mcpclient.Connect(ctx, endpoint, token)
}

func (a *App) mcpConnector() MCPConnectFunc { return a.MCPConnect }

func (a *App) requireMCPConnector(w http.ResponseWriter, r *http.Request) bool {
	if a.MCPConnect == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "mcp integration is unavailable")
		return false
	}
	if strings.TrimSpace(a.Config.GoogleTokenEncryptionSecret) == "" {
		serverError(w, r, errors.New("mcp: token encryption secret is not configured"))
		return false
	}
	if a.GSCService == nil {
		serverError(w, r, errors.New("mcp: credential service is not configured"))
		return false
	}
	return true
}

const (
	mcpMaxEndpointLen     = 2048
	mcpMaxTokenLen        = 8192
	mcpMaxNameLen         = 100
	mcpMaxTools           = mcpclient.MaxDiscoveredTools
	mcpMaxToolDescLen     = 8192
	mcpMaxToolGroupLen    = 256
	mcpMaxToolSchemaBytes = 64 << 10
	mcpMaxPermissionBatch = 512
)

// mcpCodedError carries a safe local code without endpoint or secret details.
type mcpCodedError struct{ code string }

func (e mcpCodedError) Error() string { return "mcp: " + e.code }
func (e mcpCodedError) Code() string  { return e.code }

func mcpErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var local mcpCodedError
	if errors.As(err, &local) {
		return local.Code()
	}
	var localPtr *mcpCodedError
	if errors.As(err, &localPtr) && localPtr != nil {
		return localPtr.Code()
	}
	return mcpclient.ErrorCode(err)
}

// mcpConnectFailure converts a connector failure into a safe HTTP response,
// switching on the safe code only. Raw errors are never surfaced.
func mcpConnectFailure(w http.ResponseWriter, r *http.Request, err error) {
	code := mcpErrorCode(err)
	log.Printf("mcp request failed: code=%q", code)
	switch code {
	case "invalid_endpoint":
		writeJSONError(w, http.StatusBadRequest, "invalid mcp endpoint")
	case "invalid_token", "unauthorized":
		writeJSONError(w, http.StatusBadRequest, "invalid mcp credentials")
	case "unsupported_transport":
		writeJSONError(w, http.StatusBadRequest, "unsupported mcp endpoint")
	case "unreachable":
		writeJSONError(w, http.StatusBadGateway, "mcp endpoint unreachable")
	case "timeout":
		writeJSONError(w, http.StatusGatewayTimeout, "mcp endpoint timed out")
	case "too_large":
		writeJSONError(w, http.StatusBadGateway, "mcp response too large")
	case "invalid_tools":
		writeJSONError(w, http.StatusBadGateway, "invalid mcp tools response")
	default:
		writeJSONError(w, http.StatusBadGateway, "failed to reach mcp endpoint")
	}
}

// validateMCPName trims and bounds a user-selected connection name.
func validateMCPName(value string) (string, bool) {
	name := strings.TrimSpace(value)
	if name == "" || len(name) > mcpMaxNameLen {
		return "", false
	}
	return name, true
}

// validateMCPEndpoint trims and bounds the URL. Scheme/host validation and
// the SSRF policy live in the transport; the handler only rejects empty,
// oversized, or non-HTTP(S) values with safe messages.
func validateMCPEndpoint(value string) (string, bool) {
	endpoint := strings.TrimSpace(value)
	if endpoint == "" || len(endpoint) > mcpMaxEndpointLen {
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

// mcpStoredTool is the only shape persisted in project_mcp_connections.tools:
// the exact remote name, its bounded description, the server group, and the
// live input schema. No secrets.
type mcpStoredTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Group       string          `json:"group,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// normalizeMCPTools validates discovered tools and marshals the storable
// subset, keeping exact remote names. It returns a safe invalid_tools error
// for malformed remote data.
func normalizeMCPTools(tools []mcpclient.Tool) ([]byte, []mcpStoredTool, error) {
	invalid := func() ([]byte, []mcpStoredTool, error) {
		return nil, nil, mcpCodedError{code: "invalid_tools"}
	}
	if len(tools) > mcpMaxTools {
		return invalid()
	}
	seen := make(map[string]struct{}, len(tools))
	stored := make([]mcpStoredTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == "" || !mcpclient.IsValidToolName(tool.Name) {
			return invalid()
		}
		if len(tool.Description) > mcpMaxToolDescLen || len(tool.Group) > mcpMaxToolGroupLen {
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
		if len(schema) > mcpMaxToolSchemaBytes || !json.Valid(schema) {
			return invalid()
		}
		stored = append(stored, mcpStoredTool{
			Name:        tool.Name,
			Description: tool.Description,
			Group:       tool.Group,
			InputSchema: schema,
		})
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return invalid()
	}
	return raw, stored, nil
}

// parseMCPStoredTools extracts the stored discovered tools. Stored rows are
// written by normalizeMCPTools, so a decode failure indicates corruption.
func parseMCPStoredTools(raw []byte) ([]mcpStoredTool, error) {
	if len(raw) == 0 {
		return []mcpStoredTool{}, nil
	}
	var stored []mcpStoredTool
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, err
	}
	return stored, nil
}

// mcpCatalogItemJSON is one marketplace preset. IDs name presets only, never
// a transport adapter or a stored provider identifier. Service selects the
// optional local adapter for a connection created from the preset.
// EndpointURL and AuthRequired are absent when the user supplies them, as
// for the WordPress preset.
type mcpCatalogItemJSON struct {
	ID                  string `json:"id"`
	Title               string `json:"title"`
	Description         string `json:"description"`
	Service             string `json:"service"`
	EndpointURL         string `json:"endpoint_url,omitempty"`
	AuthRequired        *bool  `json:"auth_required,omitempty"`
	ConnectionSupported *bool  `json:"connection_supported,omitempty"`
	SetupNote           string `json:"setup_note,omitempty"`
}

func mcpCatalogFalse() *bool { value := false; return &value }

func mcpCatalogTrue() *bool { value := true; return &value }

type mcpToolJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Group       string `json:"group,omitempty"`
	Permission  string `json:"permission"`
	Available   bool   `json:"available"`
	// UnavailableReason is set only when Available is false, naming a
	// genuine discovery problem such as an invalid schema or a stale
	// connection. No tool is excluded by name or provider: every stored
	// tool is available under the user's Ask/Allow/Deny policy.
	UnavailableReason *string `json:"unavailable_reason,omitempty"`
}

type mcpConnectionJSON struct {
	ID            string        `json:"id"`
	ProjectID     string        `json:"project_id"`
	Name          string        `json:"name"`
	Service       string        `json:"service"`
	EndpointURL   string        `json:"endpoint_url"`
	Revision      string        `json:"revision"`
	LastCheckedAt *string       `json:"last_checked_at,omitempty"`
	Tools         []mcpToolJSON `json:"tools"`
	CreatedAt     string        `json:"created_at"`
	UpdatedAt     string        `json:"updated_at"`
}

// newMCPConnectionJSON builds the public connection shape. It never includes
// token material. Absence of a permission row means Ask. Every stored tool is
// available: Ask/Allow/Deny decides whether it runs.
func newMCPConnectionJSON(conn sqlc.ProjectMcpConnection, stored []mcpStoredTool, permissions map[string]string) mcpConnectionJSON {
	tools := make([]mcpToolJSON, 0, len(stored))
	for _, tool := range stored {
		permission, ok := permissions[tool.Name]
		if !ok {
			permission = "ask"
		}
		entry := mcpToolJSON{
			Name: tool.Name, Description: tool.Description, Group: tool.Group,
			Permission: permission, Available: true,
		}
		tools = append(tools, entry)
	}
	out := mcpConnectionJSON{
		ID: conn.ID.String(), ProjectID: conn.ProjectID.String(),
		Name: conn.Name, Service: conn.Service, EndpointURL: conn.EndpointUrl,
		Revision: conn.Revision.String(), Tools: tools,
		CreatedAt: conn.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt: conn.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}
	if conn.LastCheckedAt.Valid {
		checked := conn.LastCheckedAt.Time.UTC().Format(time.RFC3339)
		out.LastCheckedAt = &checked
	}
	return out
}

func (a *App) mcpConnectionJSONWithPermissions(ctx context.Context, conn sqlc.ProjectMcpConnection) (mcpConnectionJSON, error) {
	stored, err := parseMCPStoredTools(conn.Tools)
	if err != nil {
		return mcpConnectionJSON{}, err
	}
	rows, err := a.Queries.ListMCPToolPermissionsForConnection(ctx, conn.ID)
	if err != nil {
		return mcpConnectionJSON{}, err
	}
	permissions := make(map[string]string, len(rows))
	for _, row := range rows {
		permissions[row.ToolName] = row.Permission
	}
	return newMCPConnectionJSON(conn, stored, permissions), nil
}

// mcpProjectForWrite resolves the project and requires organization
// ownership. Ownership is validated before any outbound MCP request.
func (a *App) mcpProjectForWrite(w http.ResponseWriter, r *http.Request, projectID pgtype.UUID) (sqlc.Project, bool) {
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

// mcpProjectForRead resolves the project for any member. Reads never include
// token material.
func (a *App) mcpProjectForRead(w http.ResponseWriter, r *http.Request, projectID pgtype.UUID) (sqlc.Project, bool) {
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
	return project, true
}

func isMCPNameConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (a *App) handleMCPCatalog(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	if _, ok := a.mcpProjectForRead(w, r, projectID); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": []mcpCatalogItemJSON{
		{ID: "wordpress", Title: "WordPress", Description: "Connect a WordPress site over MCP to read content and draft changes from chat.", Service: string(MCPServiceWordPress), AuthRequired: mcpCatalogTrue()},
		{ID: "deepwiki", Title: "DeepWiki", Description: "Ask questions about any public GitHub repository through DeepWiki.", Service: string(MCPServiceCustom), EndpointURL: "https://mcp.deepwiki.com/mcp", AuthRequired: mcpCatalogFalse()},
		{ID: "cloudflare-docs", Title: "Cloudflare Docs", Description: "Search Cloudflare developer documentation, including code examples.", Service: string(MCPServiceCustom), EndpointURL: "https://docs.mcp.cloudflare.com/mcp", AuthRequired: mcpCatalogFalse()},
		{ID: "microsoft-learn", Title: "Microsoft Learn", Description: "Search Microsoft Learn documentation and code samples.", Service: string(MCPServiceCustom), EndpointURL: "https://learn.microsoft.com/api/mcp", AuthRequired: mcpCatalogFalse()},
		{ID: "se-ranking", Title: "SE Ranking", Description: "Use SE Ranking SEO data over MCP for keyword rankings and site audits.", Service: string(MCPServiceCustom), EndpointURL: "https://api.seranking.com/mcp", AuthRequired: mcpCatalogTrue(), ConnectionSupported: mcpCatalogTrue(), SetupNote: "API access is required. API/MCP trial is available."},
		{ID: "sanity", Title: "Sanity", Description: "Use Sanity structured content over MCP.", Service: string(MCPServiceCustom), EndpointURL: "https://mcp.sanity.io", AuthRequired: mcpCatalogTrue(), ConnectionSupported: mcpCatalogTrue(), SetupNote: "Use a Sanity API token with the required project permissions."},
		{ID: "windsor-ai", Title: "Windsor.ai", Description: "Use Windsor.ai marketing analytics over MCP.", Service: string(MCPServiceCustom), EndpointURL: "https://mcp.windsor.ai/", AuthRequired: mcpCatalogTrue(), ConnectionSupported: mcpCatalogTrue(), SetupNote: "Use your Windsor.ai API key. Source access depends on your plan."},
		{ID: "ahrefs", Title: "Ahrefs", Description: "Use Ahrefs SEO data over MCP for backlinks and keywords.", Service: string(MCPServiceCustom), EndpointURL: "https://api.ahrefs.com/mcp/mcp", AuthRequired: mcpCatalogTrue(), ConnectionSupported: mcpCatalogTrue(), SetupNote: "A paid Ahrefs plan and an MCP key are required."},
		{ID: "storyblok", Title: "Storyblok", Description: "Use Storyblok CMS content over MCP.", Service: string(MCPServiceCustom), EndpointURL: "https://mcp.storyblok.com/mcp", AuthRequired: mcpCatalogTrue(), ConnectionSupported: mcpCatalogTrue(), SetupNote: "Use a personal access token. MCP plan access is not yet verified."},
		{ID: "semrush", Title: "Semrush", Description: "Use Semrush SEO data over MCP.", Service: string(MCPServiceCustom), EndpointURL: "https://mcp.semrush.com/v2/mcp", AuthRequired: mcpCatalogTrue(), ConnectionSupported: mcpCatalogFalse(), SetupNote: "Requires Apikey authentication or OAuth. The current bearer-token connector does not support this setup."},
		{ID: "accuranker", Title: "AccuRanker", Description: "Use AccuRanker rank-tracking data over MCP.", Service: string(MCPServiceCustom), EndpointURL: "https://connect.accuranker.com/mcp", AuthRequired: mcpCatalogTrue(), ConnectionSupported: mcpCatalogFalse(), SetupNote: "Requires OAuth sign-in. The current connector does not support OAuth."},
	}})
}

func (a *App) handleMCPListConnections(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	project, ok := a.mcpProjectForRead(w, r, projectID)
	if !ok {
		return
	}
	connections, err := a.Queries.ListProjectMCPConnectionsByProjectID(r.Context(), project.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	out := make([]mcpConnectionJSON, 0, len(connections))
	for _, conn := range connections {
		rendered, err := a.mcpConnectionJSONWithPermissions(r.Context(), conn)
		if err != nil {
			serverError(w, r, err)
			return
		}
		out = append(out, rendered)
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

type mcpCreateConnectionRequest struct {
	Name        string `json:"name"`
	Service     string `json:"service"`
	EndpointURL string `json:"endpoint_url"`
	BearerToken string `json:"bearer_token"`
}

// handleMCPCreateConnection discovers the endpoint, closes the session, then
// inserts encrypted credentials plus the discovered tools. Discovery runs
// before any row exists, so a failed validation saves nothing.
func (a *App) handleMCPCreateConnection(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var body mcpCreateConnectionRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	name, ok := validateMCPName(body.Name)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid connection name")
		return
	}
	service, ok := parseMCPService(body.Service)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid mcp service")
		return
	}
	endpoint, ok := validateMCPEndpoint(body.EndpointURL)
	if !ok {
		mcpConnectFailure(w, r, mcpCodedError{code: "invalid_endpoint"})
		return
	}
	// A custom connection may omit the bearer token for explicitly
	// unauthenticated servers: no placeholder is fabricated, and the empty
	// plaintext encrypts to opaque stored credentials below. Supplied
	// nonempty tokens validate as before. WordPress keeps its
	// required-token behavior, and a server that requires authentication
	// fails closed at discovery when no token was supplied.
	if len(body.BearerToken) > mcpMaxTokenLen || (body.BearerToken == "" && service != MCPServiceCustom) {
		mcpConnectFailure(w, r, mcpCodedError{code: "invalid_token"})
		return
	}
	project, ok := a.mcpProjectForWrite(w, r, projectID)
	if !ok {
		return
	}
	if !a.requireMCPConnector(w, r) {
		return
	}

	session, err := a.mcpConnector()(r.Context(), endpoint, body.BearerToken)
	if err != nil {
		mcpConnectFailure(w, r, err)
		return
	}
	discovered := session.Tools()
	if closeErr := session.Close(); closeErr != nil {
		log.Printf("mcp session close failed: code=%q", mcpErrorCode(closeErr))
	}
	toolsRaw, _, err := normalizeMCPTools(discovered)
	if err != nil {
		mcpConnectFailure(w, r, err)
		return
	}
	encrypted, err := a.GSCService.EncryptSecret(body.BearerToken)
	if err != nil {
		serverError(w, r, err)
		return
	}
	conn, err := a.Queries.CreateProjectMCPConnection(r.Context(), sqlc.CreateProjectMCPConnectionParams{
		ProjectID:      project.ID,
		Name:           name,
		Service:        string(service),
		EndpointUrl:    endpoint,
		EncryptedToken: encrypted,
		Tools:          toolsRaw,
	})
	if err != nil {
		if isMCPNameConflict(err) {
			writeJSONError(w, http.StatusConflict, "connection name already exists")
			return
		}
		serverError(w, r, err)
		return
	}
	rendered, err := a.mcpConnectionJSONWithPermissions(r.Context(), conn)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": rendered})
}

type mcpPatchConnectionRequest struct {
	Name        *string `json:"name,omitempty"`
	EndpointURL *string `json:"endpoint_url,omitempty"`
	BearerToken *string `json:"bearer_token,omitempty"`
}

// handleMCPPatchConnection renames, or revalidates and replaces the endpoint
// and credentials. Endpoint or credential changes validate through discovery
// before replacing the row and bump the revision; a name-only change does
// neither. An omitted token retains the stored one, never returns it.
func (a *App) handleMCPPatchConnection(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	connectionID, err := parseUUIDParam(chi.URLParam(r, "connectionID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid connection id")
		return
	}
	var body mcpPatchConnectionRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	project, ok := a.mcpProjectForWrite(w, r, projectID)
	if !ok {
		return
	}
	conn, err := a.Queries.GetProjectMCPConnectionByID(r.Context(), sqlc.GetProjectMCPConnectionByIDParams{
		ID:        connectionID,
		ProjectID: project.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "connection not found")
			return
		}
		serverError(w, r, err)
		return
	}

	if body.EndpointURL == nil && body.BearerToken == nil {
		name, ok := validateMCPName(firstNonNilString(body.Name))
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "invalid connection name")
			return
		}
		renamed, err := a.Queries.RenameProjectMCPConnection(r.Context(), sqlc.RenameProjectMCPConnectionParams{
			ID:        conn.ID,
			ProjectID: project.ID,
			Name:      name,
		})
		if err != nil {
			if isMCPNameConflict(err) {
				writeJSONError(w, http.StatusConflict, "connection name already exists")
				return
			}
			serverError(w, r, err)
			return
		}
		rendered, err := a.mcpConnectionJSONWithPermissions(r.Context(), renamed)
		if err != nil {
			serverError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"connection": rendered})
		return
	}

	endpoint := conn.EndpointUrl
	if body.EndpointURL != nil {
		endpoint, ok = validateMCPEndpoint(*body.EndpointURL)
		if !ok {
			mcpConnectFailure(w, r, mcpCodedError{code: "invalid_endpoint"})
			return
		}
	}
	// A blank token field retains the stored credential, exactly like an
	// omitted one: credentials are never silently cleared and no auth
	// exemption is inferred. Only an explicitly supplied nonempty token
	// replaces the stored one.
	token := ""
	tokenReplaced := false
	if body.BearerToken != nil && *body.BearerToken != "" {
		if len(*body.BearerToken) > mcpMaxTokenLen {
			mcpConnectFailure(w, r, mcpCodedError{code: "invalid_token"})
			return
		}
		token = *body.BearerToken
		tokenReplaced = true
	} else {
		if !a.requireMCPConnector(w, r) {
			return
		}
		decrypted, err := a.GSCService.DecryptSecret(conn.EncryptedToken)
		if err != nil || (decrypted == "" && conn.Service != string(MCPServiceCustom)) {
			serverError(w, r, errors.New("mcp: failed to decrypt stored credentials"))
			return
		}
		token = decrypted
	}
	if !a.requireMCPConnector(w, r) {
		return
	}

	session, err := a.mcpConnector()(r.Context(), endpoint, token)
	if err != nil {
		mcpConnectFailure(w, r, err)
		return
	}
	discovered := session.Tools()
	if closeErr := session.Close(); closeErr != nil {
		log.Printf("mcp session close failed: code=%q", mcpErrorCode(closeErr))
	}
	toolsRaw, _, err := normalizeMCPTools(discovered)
	if err != nil {
		mcpConnectFailure(w, r, err)
		return
	}
	encrypted := conn.EncryptedToken
	if tokenReplaced {
		encrypted, err = a.GSCService.EncryptSecret(token)
		if err != nil {
			serverError(w, r, err)
			return
		}
	}
	name := conn.Name
	if body.Name != nil {
		name, ok = validateMCPName(*body.Name)
		if !ok {
			writeJSONError(w, http.StatusBadRequest, "invalid connection name")
			return
		}
	}
	// One short transaction replaces every field under a revision CAS and
	// invalidates the connection's pending approvals with their events. A
	// name conflict, a stale revision, or a failed invalidation rolls all
	// fields and approvals back together. Discovery above ran before any
	// write and never inside the transaction.
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := a.Queries.WithTx(tx)
	updated, err := queries.ReplaceProjectMCPConnection(r.Context(), sqlc.ReplaceProjectMCPConnectionParams{
		ID:             conn.ID,
		ProjectID:      project.ID,
		Name:           name,
		EndpointUrl:    endpoint,
		EncryptedToken: encrypted,
		Tools:          toolsRaw,
		Revision:       conn.Revision,
	})
	if err != nil {
		if isMCPNameConflict(err) {
			writeJSONError(w, http.StatusConflict, "connection name already exists")
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "mcp connection changed, retry")
			return
		}
		serverError(w, r, err)
		return
	}
	if err := invalidateMCPConnectionApprovalsTx(r.Context(), tx, queries, project.ID, conn.ID, "mcp connection replaced"); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	rendered, err := a.mcpConnectionJSONWithPermissions(r.Context(), updated)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": rendered})
}

func firstNonNilString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// handleMCPCheckConnection rediscovers with the stored token and persists
// fresh tools only when the revision is unchanged. Checking never bumps the
// revision and never touches saved permissions.
func (a *App) handleMCPCheckConnection(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	connectionID, err := parseUUIDParam(chi.URLParam(r, "connectionID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid connection id")
		return
	}
	var body struct{}
	if !readOptionalStrictJSONOrRespond(w, r, &body) {
		return
	}
	project, ok := a.mcpProjectForWrite(w, r, projectID)
	if !ok {
		return
	}
	conn, err := a.Queries.GetProjectMCPConnectionByID(r.Context(), sqlc.GetProjectMCPConnectionByIDParams{
		ID:        connectionID,
		ProjectID: project.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "connection not found")
			return
		}
		serverError(w, r, err)
		return
	}
	if !a.requireMCPConnector(w, r) {
		return
	}
	token, err := a.GSCService.DecryptSecret(conn.EncryptedToken)
	// A decrypted empty credential is an explicit no-auth custom
	// connection. WordPress connections fail closed here as before.
	if err != nil || (token == "" && conn.Service != string(MCPServiceCustom)) {
		serverError(w, r, errors.New("mcp: failed to decrypt stored credentials"))
		return
	}

	session, err := a.mcpConnector()(r.Context(), conn.EndpointUrl, token)
	if err != nil {
		mcpConnectFailure(w, r, err)
		return
	}
	discovered := session.Tools()
	if closeErr := session.Close(); closeErr != nil {
		log.Printf("mcp session close failed: code=%q", mcpErrorCode(closeErr))
	}
	toolsRaw, _, err := normalizeMCPTools(discovered)
	if err != nil {
		mcpConnectFailure(w, r, err)
		return
	}
	updated, err := a.Queries.UpdateProjectMCPConnectionChecked(r.Context(), sqlc.UpdateProjectMCPConnectionCheckedParams{
		ID:        conn.ID,
		ProjectID: project.ID,
		Tools:     toolsRaw,
		Revision:  conn.Revision,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "mcp connection changed, retry")
			return
		}
		serverError(w, r, err)
		return
	}
	rendered, err := a.mcpConnectionJSONWithPermissions(r.Context(), updated)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": rendered})
}

// handleMCPDeleteConnection deletes one connection with its preferences and
// invalidates its pending approvals, so no paused turn can resume against a
// removed connection. Other connections are untouched.
func (a *App) handleMCPDeleteConnection(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	connectionID, err := parseUUIDParam(chi.URLParam(r, "connectionID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid connection id")
		return
	}
	var body struct{}
	if !readOptionalStrictJSONOrRespond(w, r, &body) {
		return
	}
	project, ok := a.mcpProjectForWrite(w, r, projectID)
	if !ok {
		return
	}
	// Delete and invalidation share one short transaction, in connection
	// row then approval row order like every other connection writer.
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := a.Queries.WithTx(tx)
	deleted, err := queries.DeleteProjectMCPConnection(r.Context(), sqlc.DeleteProjectMCPConnectionParams{
		ID:        connectionID,
		ProjectID: project.ID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	if deleted == 0 {
		writeJSONError(w, http.StatusNotFound, "connection not found")
		return
	}
	if err := invalidateMCPConnectionApprovalsTx(r.Context(), tx, queries, project.ID, connectionID, "mcp connection removed"); err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

type mcpPermissionEntry struct {
	ToolName   string `json:"tool_name"`
	Permission string `json:"permission"`
}

type mcpPutPermissionsRequest struct {
	Permissions []mcpPermissionEntry `json:"permissions"`
}

// handleMCPPutPermissions replaces the saved policy for the listed tools in
// one atomic batch. Names must be non-empty, permissions ask/allow/deny, and
// every name must belong to the currently discovered available set: saved
// policy is user data, never remote metadata, and a concurrent /check cannot
// first and validated inside the transaction. Ask deletes the row (absence
// means Ask). Every discovered tool accepts ask, allow and deny: the user
// decides what runs. Permission edits never bump the connection
// revision and never cross the network. Deny atomically invalidates matching
// pending approvals.
func (a *App) handleMCPPutPermissions(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	connectionID, err := parseUUIDParam(chi.URLParam(r, "connectionID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid connection id")
		return
	}
	var body mcpPutPermissionsRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	if len(body.Permissions) > mcpMaxPermissionBatch {
		writeJSONError(w, http.StatusBadRequest, "too many permissions")
		return
	}
	project, ok := a.mcpProjectForWrite(w, r, projectID)
	if !ok {
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := a.Queries.WithTx(tx)
	conn, err := queries.LockProjectMCPConnectionByID(r.Context(), sqlc.LockProjectMCPConnectionByIDParams{
		ID:        connectionID,
		ProjectID: project.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "connection not found")
			return
		}
		serverError(w, r, err)
		return
	}
	stored, err := parseMCPStoredTools(conn.Tools)
	if err != nil {
		serverError(w, r, err)
		return
	}
	discovered := make(map[string]bool, len(stored))
	for _, tool := range stored {
		discovered[tool.Name] = true
	}
	seen := make(map[string]bool, len(body.Permissions))
	denied := make([]string, 0)
	for _, entry := range body.Permissions {
		name := strings.TrimSpace(entry.ToolName)
		if name == "" || seen[name] {
			writeJSONError(w, http.StatusBadRequest, "invalid tool name")
			return
		}
		seen[name] = true
		switch entry.Permission {
		case "ask", "allow", "deny":
		default:
			writeJSONError(w, http.StatusBadRequest, "invalid permission")
			return
		}
		if !discovered[name] {
			writeJSONError(w, http.StatusBadRequest, "unknown tool name")
			return
		}
		if entry.Permission == "deny" {
			denied = append(denied, name)
		}
	}
	// Lock order inside this transaction is the connection row, then approval
	// rows, then permission rows: the same order the decision and replace
	// paths use, so concurrent writers serialize instead of deadlocking.
	// Invalidating before writing the rules means a concurrent decision that
	// already passed its compare-and-set keeps its outcome, while this batch
	// never revives a row the decision just consumed.
	for _, name := range denied {
		invalidated, err := queries.InvalidatePendingMCPApprovalsForConnectionTool(r.Context(), sqlc.InvalidatePendingMCPApprovalsForConnectionToolParams{
			ConnectionID:   conn.ID,
			RemoteToolName: name,
			Summary:        "tool permission set to deny",
		})
		if err != nil {
			serverError(w, r, err)
			return
		}
		for _, approval := range invalidated {
			payload, err := mcpApprovalDecidedPayloadFromData(mcpApprovalDataFromInvalidatedConnectionTool(approval))
			if err != nil {
				serverError(w, r, err)
				return
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				serverError(w, r, err)
				return
			}
			if _, err := tx.Exec(r.Context(), `INSERT INTO ai_turn_events(turn_id, event_type, payload) VALUES ($1, 'approval_decided', $2::jsonb)`, approval.TurnID, string(encoded)); err != nil {
				serverError(w, r, err)
				return
			}
		}
	}
	for _, entry := range body.Permissions {
		name := strings.TrimSpace(entry.ToolName)
		if entry.Permission == "ask" {
			if err := queries.DeleteMCPToolPermission(r.Context(), sqlc.DeleteMCPToolPermissionParams{
				ConnectionID: conn.ID,
				ToolName:     name,
			}); err != nil {
				serverError(w, r, err)
				return
			}
			continue
		}
		if err := queries.UpsertMCPToolPermission(r.Context(), sqlc.UpsertMCPToolPermissionParams{
			ConnectionID: conn.ID,
			ToolName:     name,
			Permission:   entry.Permission,
		}); err != nil {
			serverError(w, r, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	// Re-read after commit: a concurrent /check may have moved the tools
	// under the batch, and the response must describe the current row, not
	// the locked one. Permission edits never change tools, so a missing
	// row here only means a concurrent delete won the race.
	current, err := a.Queries.GetProjectMCPConnectionByID(r.Context(), sqlc.GetProjectMCPConnectionByIDParams{
		ID:        conn.ID,
		ProjectID: project.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "connection not found")
			return
		}
		serverError(w, r, err)
		return
	}
	rendered, err := a.mcpConnectionJSONWithPermissions(r.Context(), current)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": rendered})
}

// invalidateMCPConnectionApprovalsTx marks every pending approval of one
// connection invalid and fails their waiting turns, so no paused turn can
// resume against a replaced or removed connection. It runs inside the
// caller's connection write transaction, taking the connection row lock
// first and approval rows after, the same order as every other connection
// writer, so a name conflict, a stale revision, or a failed invalidation
// rolls all fields and approvals back together.
func invalidateMCPConnectionApprovalsTx(ctx context.Context, tx pgx.Tx, queries *sqlc.Queries, projectID, connectionID pgtype.UUID, summary string) error {
	invalidated, err := queries.InvalidatePendingMCPApprovalsForConnection(ctx, sqlc.InvalidatePendingMCPApprovalsForConnectionParams{
		ProjectID:    projectID,
		ConnectionID: connectionID,
		Summary:      summary,
	})
	if err != nil {
		return err
	}
	for _, approval := range invalidated {
		payload, err := mcpApprovalDecidedPayloadFromData(mcpApprovalDataFromInvalidatedConnection(approval))
		if err != nil {
			return err
		}
		if err := insertAITurnEventTx(ctx, tx, approval.TurnID, "approval_decided", payload); err != nil {
			return err
		}
	}
	failed, err := queries.FailWaitingTurnsForProject(ctx, projectID)
	if err != nil {
		return err
	}
	for _, turn := range failed {
		if !turn.IsPartial {
			if _, err := queries.FailPendingAssistantMessageForTurn(ctx, turn.ID); err != nil {
				return err
			}
		}
		if err := insertAITurnEventTx(ctx, tx, turn.ID, "failed", map[string]string{"error_code": "cms_connection_changed"}); err != nil {
			return err
		}
	}
	return nil
}

// insertAITurnEvent appends one SSE-replayed event outside a transaction.
// Payload must be a JSON object; callers pass maps or structs that marshal
// to one.
func (a *App) insertAITurnEvent(ctx context.Context, turnID pgtype.UUID, eventType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = a.DB.Exec(ctx, `INSERT INTO ai_turn_events(turn_id, event_type, payload) VALUES ($1, $2, $3::jsonb)`, turnID, eventType, string(encoded))
	return err
}

// insertAITurnEventTx appends one SSE-replayed event inside the caller's
// transaction, so the event and its approval invalidation commit together.
func insertAITurnEventTx(ctx context.Context, tx pgx.Tx, turnID pgtype.UUID, eventType string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO ai_turn_events(turn_id, event_type, payload) VALUES ($1, $2, $3::jsonb)`, turnID, eventType, string(encoded))
	return err
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
