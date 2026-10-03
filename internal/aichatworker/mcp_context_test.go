package aichatworker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/aichattools"
)

// emptyMCPDecryptor decrypts every stored credential to an explicitly
// unauthenticated plaintext, isolating the no-auth turn path.
type emptyMCPDecryptor struct{ fakeMCPDecryptor }

func (emptyMCPDecryptor) DecryptSecret(string) (string, error) { return "", nil }

var _ aichattools.GSCFetcher = &emptyMCPDecryptor{}

func mcpSessionWith(names ...string) *fakeMCPSession {
	tools := make([]aichattools.MCPToolDef, 0, len(names))
	for _, name := range names {
		tools = append(tools, aichattools.MCPToolDef{
			Name:        name,
			Description: "remote " + name,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		})
	}
	return &fakeMCPSession{tools: tools}
}

func setMCPConnectionEndpoint(t *testing.T, w *Worker, connID, endpoint string) {
	t.Helper()
	if _, err := w.pool.Exec(context.Background(),
		`UPDATE project_mcp_connections SET endpoint_url = $2 WHERE id = $1::uuid`, connID, endpoint); err != nil {
		t.Fatalf("set endpoint: %v", err)
	}
}

func TestSetupMCPDirectoryAndOmissionReasons(t *testing.T) {
	a, _, user, project := testWorker(t)
	const revision = "22222222-2222-2222-2222-222222222222"
	customID := insertMCPConnection(t, a, project, "Main\nBlog", "custom", revision, liveMCPTools())
	wpID := insertMCPConnection(t, a, project, "WP", "custom", revision, liveMCPTools())
	// The WordPress connection advertises file/SQL names alongside a plain
	// search tool; endpoints distinguish the fake sessions.
	setMCPConnectionEndpoint(t, a, customID.String(), "https://custom.example.test/mcp")
	setMCPConnectionEndpoint(t, a, wpID.String(), "https://wp.example.test/mcp")
	if _, err := a.pool.Exec(context.Background(),
		`UPDATE project_mcp_connections SET service = 'wordpress' WHERE id = $1`, wpID); err != nil {
		t.Fatalf("set service: %v", err)
	}
	a.GSC = &fakeMCPDecryptor{}
	a.MCPDial = func(_ context.Context, endpoint, _ string) (aichattools.MCPSession, error) {
		switch endpoint {
		case "https://custom.example.test/mcp":
			return liveMCPSession(), nil
		case "https://wp.example.test/mcp":
			return mcpSessionWith("run_sql", "write_file", "search_docs"), nil
		}
		return nil, errors.New("unexpected endpoint " + endpoint)
	}
	status, set, close := a.setupMCP(context.Background(),
		turnScope{UserID: user, ProjectID: project}, nil, aichattools.NewFilteredRegistry(nil))
	if close != nil {
		defer close()
	}
	if set == nil {
		t.Fatalf("setupMCP served nothing: %q", status)
	}
	if strings.Contains(status, "Main\nBlog") {
		t.Fatalf("status carries a raw newline from a saved name: %q", status)
	}
	if !strings.Contains(status, `"Main Blog"`) {
		t.Fatalf("status has no sanitized saved name: %q", status)
	}
	if !strings.Contains(status, "no connection is a preferred source of truth") {
		t.Fatalf("status grants source precedence: %q", status)
	}
	for _, connID := range []string{customID.String(), wpID.String()} {
		namespace := "mcp_" + strings.ReplaceAll(strings.ToLower(connID), "-", "") + "_*"
		if !strings.Contains(status, namespace) {
			t.Fatalf("status has no alias namespace %s: %q", namespace, status)
		}
	}
	if strings.Contains(status, "platform_restricted") {
		t.Fatalf("status excludes a tool by name: %q", status)
	}
	for _, misleading := range []string{"duplicate or rejected alias", "alias collision", "aliascollision"} {
		if strings.Contains(status, misleading) {
			t.Fatalf("status uses a misleading skip label %q: %q", misleading, status)
		}
	}
	for _, secret := range []string{"secret-token", "example.test/mcp", "remote run_sql"} {
		if strings.Contains(status, secret) {
			t.Fatalf("status leaks connection material %q: %s", secret, status)
		}
	}
	// Every advertised WordPress name resolves through its own connection
	// handle, file and SQL names included.
	for _, remote := range []string{"run_sql", "write_file", "search_docs"} {
		alias := aichattools.MCPModelToolName(wpID.String(), remote)
		handle, resolved, ok := set.toolHandle(alias)
		if !ok || resolved != remote || handle == nil {
			t.Fatalf("alias for %s does not resolve to its connection: %v %q", remote, ok, resolved)
		}
	}
}

func TestSetupMCPNoAuthCustomDialsAnonymous(t *testing.T) {
	a, _, user, project := testWorker(t)
	const revision = "33333333-3333-3333-3333-333333333333"
	insertMCPConnection(t, a, project, "Public Docs", "custom", revision, liveMCPTools())
	a.GSC = &emptyMCPDecryptor{}
	var dialed string
	a.MCPDial = func(_ context.Context, _, token string) (aichattools.MCPSession, error) {
		dialed = token
		return liveMCPSession(), nil
	}
	status, set, close := a.setupMCP(context.Background(),
		turnScope{UserID: user, ProjectID: project}, nil, aichattools.NewFilteredRegistry(nil))
	if close != nil {
		defer close()
	}
	if set == nil {
		t.Fatalf("no-auth custom connection served nothing: %q", status)
	}
	if dialed != "" {
		t.Fatalf("no-auth turn dialed with %q, want anonymous discovery", dialed)
	}
	if !strings.Contains(status, `"Public Docs"`) {
		t.Fatalf("status names no served connection: %q", status)
	}
}

func TestSetupMCPWordPressEmptyCredentialFailsClosed(t *testing.T) {
	a, _, user, project := testWorker(t)
	const revision = "44444444-4444-4444-4444-444444444444"
	insertMCPConnection(t, a, project, "WP", "wordpress", revision, liveMCPTools())
	a.GSC = &emptyMCPDecryptor{}
	a.MCPDial = func(context.Context, string, string) (aichattools.MCPSession, error) {
		t.Error("wordpress with an empty credential must fail closed before dialing")
		return nil, errors.New("must not dial")
	}
	status, set, _ := a.setupMCP(context.Background(),
		turnScope{UserID: user, ProjectID: project}, nil, aichattools.NewFilteredRegistry(nil))
	if set != nil {
		t.Fatalf("wordpress with an empty credential served tools: %q", status)
	}
}

func TestMCPDirectoryNameSanitization(t *testing.T) {
	if got := mcpDirectoryName("Main\nBlog\r\n\tX"); got != "Main Blog X" {
		t.Fatalf("directory name = %q, want controls flattened", got)
	}
	if got := mcpDirectoryName("  "); got != "unnamed connection" {
		t.Fatalf("blank name = %q, want a neutral fallback", got)
	}
	long := strings.Repeat("n", 200)
	if got := mcpDirectoryName(long); len([]rune(got)) != 65 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long name = %q, want a marked 64-rune clip", got)
	}
}

func TestFormatMCPOmissionsBoundsAndLabels(t *testing.T) {
	var omissions []mcpServeOmission
	for i := 0; i < 15; i++ {
		omissions = append(omissions, mcpServeOmission{
			connection: "Docs",
			remote:     "tool",
			reason:     string(aichattools.MCPOmitInvalidTool),
		})
	}
	out := formatMCPOmissions(omissions)
	if strings.Count(out, "invalid_tool") != 12 {
		t.Fatalf("omission list is not capped at 12 entries: %q", out)
	}
	if !strings.Contains(out, "3 further omitted tool(s) not listed.") {
		t.Fatalf("omission list drops the remainder silently: %q", out)
	}
	excluded := formatMCPOmissions([]mcpServeOmission{{
		connection: "WP", remote: "run_sql",
		reason: string(aichattools.MCPOmitInvalidTool),
		detail: "no live input schema",
	}})
	if !strings.Contains(excluded, `"WP"/"run_sql": invalid_tool (no live input schema)`) {
		t.Fatalf("invalid advertisement is not attributed exactly: %q", excluded)
	}
	if strings.Contains(excluded, "duplicate") || strings.Contains(excluded, "collision") || strings.Contains(excluded, "platform_restricted") {
		t.Fatalf("invalid advertisement borrows another reason label: %q", excluded)
	}
}
