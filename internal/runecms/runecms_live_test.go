package runecms

// Opt-in live test against a real Rune MCP endpoint. It runs only when
// RUNE_MCP_TEST_ENV_FILE points at a mode-0600 file with RUNE_MCP_URL and
// RUNE_MCP_TOKEN keys. Only discovery and read-only calls are exercised;
// create/update are never invoked. The token and any record data are never
// logged; results report only tool names and success.

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func loadRuneLiveCreds(t *testing.T) (endpoint, token string) {
	t.Helper()
	path := os.Getenv("RUNE_MCP_TEST_ENV_FILE")
	if path == "" {
		t.Skip("RUNE_MCP_TEST_ENV_FILE not set; skipping live Rune test")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("cannot stat credentials file")
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credentials file must have mode 0600")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read credentials file")
	}
	vals := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		vals[k] = v
	}
	endpoint, token = vals["RUNE_MCP_URL"], vals["RUNE_MCP_TOKEN"]
	if endpoint == "" || token == "" {
		t.Fatal("credentials file must define RUNE_MCP_URL and RUNE_MCP_TOKEN")
	}
	// Only the local loopback test endpoint is allowed.
	endpoint = strings.TrimSpace(endpoint)
	if endpoint != "http://127.0.0.1:8090/mcp" && endpoint != "http://localhost:8090/mcp" {
		t.Fatal("live test endpoint must be the local Rune MCP URL")
	}
	return endpoint, token
}

func TestLiveRuneReadOnly(t *testing.T) {
	endpoint, token := loadRuneLiveCreds(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// Test-only HTTP path (allows loopback http); production SSRF policy
	// in Connect is untouched.
	s, err := connectWithHTTPClient(ctx, endpoint, token, nil)
	if err != nil {
		t.Fatalf("live connect failed: code=%s", ErrorCode(err))
	}
	defer func() { _ = s.Close() }()

	tools := s.Tools()
	names := make([]string, 0, len(tools))
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	t.Logf("live discovery ok: %d tools: %s", len(names), strings.Join(names, ","))

	if len(tools) != len(allowedTools) {
		t.Fatal("live Rune must expose all six supported tools")
	}
	// Read-only calls only. Never create/update. Never log content or token.
	read := func(name string, args any, target any) {
		t.Helper()
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal("cannot encode live read arguments")
		}
		res, err := s.Call(ctx, name, raw)
		if err != nil {
			t.Fatalf("live %s failed: code=%s", name, ErrorCode(err))
		}
		if res.IsError {
			t.Fatalf("live %s returned a tool error", name)
		}
		if err := json.Unmarshal([]byte(res.Content), target); err != nil {
			t.Fatalf("live %s returned invalid JSON", name)
		}
		t.Logf("live %s ok", name)
	}
	var collections struct {
		Collections []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"collections"`
	}
	read("list_collections", map[string]any{}, &collections)
	for _, collection := range collections.Collections {
		if collection.Type != "base" || strings.HasPrefix(collection.Name, "_") {
			continue
		}
		var schema map[string]any
		read("get_collection_schema", map[string]any{"name": collection.Name}, &schema)
		// Rune lists key collections for schema discovery but forbids their records.
		if collection.Name == "mcp_keys" {
			continue
		}
		var records struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		read("list_records", map[string]any{"collection": collection.Name, "per_page": 1, "fields": []string{"id"}}, &records)
		if len(records.Items) > 0 && records.Items[0].ID != "" {
			var record map[string]any
			read("read_record", map[string]any{"collection": collection.Name, "id": records.Items[0].ID, "fields": []string{"id"}}, &record)
		} else {
			t.Log("read_record not exercised: collection has no existing records")
		}
		return
	}
	t.Log("record reads not exercised: no eligible base collection")
}
