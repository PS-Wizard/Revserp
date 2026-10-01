package aichatworker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/gsc"
)

// fakeRuneDecryptor reuses the existing GSC secret path shape without Google.
type fakeRuneDecryptor struct {
	token string
	err   error
}

func (f *fakeRuneDecryptor) DecryptSecret(encrypted string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.token != "" {
		return f.token, nil
	}
	return "token-for:" + encrypted, nil
}

func (f *fakeRuneDecryptor) EncryptSecret(plain string) (string, error) { return "enc:" + plain, nil }

func (f *fakeRuneDecryptor) RefreshAccessToken(context.Context, string) (gsc.TokenResponse, error) {
	return gsc.TokenResponse{}, errors.New("no google in tests")
}

func (f *fakeRuneDecryptor) FetchQueriesCached(context.Context, string, string, string, gsc.QueryPageOptions) (gsc.QueryPage, error) {
	return gsc.QueryPage{}, errors.New("no google in tests")
}

func (f *fakeRuneDecryptor) FetchOverviewCached(context.Context, string, string, string) (gsc.OverviewPayload, error) {
	return gsc.OverviewPayload{}, errors.New("no google in tests")
}

func (f *fakeRuneDecryptor) FetchSummaryCached(context.Context, string, string, string, int) (gsc.SummaryPayload, error) {
	return gsc.SummaryPayload{}, errors.New("no google in tests")
}

// fakeRuneSession is a local fake connector: no network, injectable in tests
// only. Production dials through the wired connector.
type fakeRuneSession struct {
	mu       sync.Mutex
	tools    []aichattools.RuneToolDef
	calls    int
	endpoint string
	token    string
	closed   bool
	onCall   func(name string, args json.RawMessage) (aichattools.RuneCallResult, error)
}

func (f *fakeRuneSession) Tools() []aichattools.RuneToolDef { return f.tools }

func (f *fakeRuneSession) Call(ctx context.Context, name string, args json.RawMessage) (aichattools.RuneCallResult, error) {
	f.mu.Lock()
	f.calls++
	onCall := f.onCall
	f.mu.Unlock()
	if onCall != nil {
		return onCall(name, args)
	}
	return aichattools.RuneCallResult{Content: `{"ok":true}`}, nil
}

func (f *fakeRuneSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func liveRuneSession() *fakeRuneSession {
	return &fakeRuneSession{tools: []aichattools.RuneToolDef{
		{Name: "list_records", Description: "remote", InputSchema: json.RawMessage(`{"type":"object","properties":{"collection":{"type":"string"}},"live":true}`)},
		{Name: "create_record", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}}
}

// ensureRuneTable creates the pending contract table only when migration 82
// has not landed yet, and drops it afterwards only in that case. Rows for the
// test project are always removed.
func ensureRuneTable(t *testing.T, w *Worker, project pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	var existed bool
	if err := w.pool.QueryRow(ctx, `SELECT to_regclass('project_rune_connections') IS NOT NULL`).Scan(&existed); err != nil {
		t.Fatal(err)
	}
	if !existed {
		if _, err := w.pool.Exec(ctx, `CREATE TABLE project_rune_connections (
	project_id UUID PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
	endpoint_url TEXT NOT NULL,
	encrypted_token TEXT NOT NULL,
	revision UUID NOT NULL DEFAULT gen_random_uuid(),
	tools JSONB NOT NULL DEFAULT '[]',
	last_checked_at TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = w.pool.Exec(ctx, `DELETE FROM project_rune_connections WHERE project_id = $1`, project)
		if !existed {
			_, _ = w.pool.Exec(ctx, `DROP TABLE project_rune_connections`)
		}
	})
}

func insertRuneConnection(t *testing.T, w *Worker, project pgtype.UUID, revision string) {
	t.Helper()
	if _, err := w.pool.Exec(context.Background(), `INSERT INTO project_rune_connections(project_id, endpoint_url, encrypted_token, revision, last_checked_at) VALUES($1, $2, $3, $4::uuid, now())
ON CONFLICT (project_id) DO UPDATE SET endpoint_url = EXCLUDED.endpoint_url, encrypted_token = EXCLUDED.encrypted_token, revision = EXCLUDED.revision, updated_at = now()`,
		project, "https://cms.example.test/mcp", "enc:secret-token", revision); err != nil {
		t.Fatal(err)
	}
}

func TestFilterRuneSpecs(t *testing.T) {
	specs := []aichattools.RuneToolDef{{Name: "list_records"}, {Name: "read_record"}, {Name: "create_record"}}
	filtered := filterRuneSpecs(specs, []string{"cms__read_record"})
	if len(filtered) != 2 {
		t.Fatalf("filtered = %v, want list_records and create_record", filtered)
	}
	for _, spec := range filtered {
		if spec.Name == "read_record" {
			t.Fatalf("denied tool survived filtering: %v", filtered)
		}
	}
	if got := filterRuneSpecs(specs, []string{"read_issues"}); len(got) != 3 {
		t.Fatalf("unrelated denylist filtered rune specs: %v", got)
	}
	if got := filterRuneSpecs(specs, nil); len(got) != 3 {
		t.Fatalf("empty denylist filtered rune specs: %v", got)
	}
}

func TestAllowedToolsFromSameRegistry(t *testing.T) {
	registry := aichattools.NewFilteredRegistry([]string{"read_issues"})
	if _, ok := registry.Get("read_issues"); ok {
		t.Fatal("executing registry serves a disabled tool: the model could run it by guessing its name")
	}
	allowed := allowedToolsFromRegistry(registry)
	for _, def := range allowed {
		if def.Name == "read_issues" {
			t.Fatal("provider defs offer a disabled tool")
		}
	}
	if len(allowed) != len(registry.Defs()) {
		t.Fatalf("allowed defs = %d, registry defs = %d: both must derive from the same registry", len(allowed), len(registry.Defs()))
	}
	if err := registry.Add(aichattools.Tool{Def: aichattools.Def{Name: "cms__list_records", Description: "d", Schema: json.RawMessage(`{"type":"object"}`)}}); err != nil {
		t.Fatal(err)
	}
	allowed = allowedToolsFromRegistry(registry)
	found := false
	for _, def := range allowed {
		if def.Name == "cms__list_records" {
			found = true
		}
	}
	if !found {
		t.Fatal("per-turn CMS tool missing from the same-registry defs")
	}
}

func TestNormalizeCMSIsError(t *testing.T) {
	status, result := normalizeToolCallResult("cms__read_record", "completed", aichattools.Result{Content: "cms__read_record error: no such collection"})
	if status != "failed" {
		t.Fatalf("status = %q, want failed for an isError tool failure", status)
	}
	if result.Summary != "no such collection" {
		t.Fatalf("summary = %q", result.Summary)
	}
}

func TestToolArgsForLog(t *testing.T) {
	if got := toolArgsForLog("cms__create_record", `{"collection":"posts","data":{"title":"secret"}}`); got != "[redacted]" {
		t.Fatalf("cms args logged as %q, want redaction", got)
	}
	if got := toolArgsForLog("read_issues", `{"limit":5}`); got != `{"limit":5}` {
		t.Fatalf("native args = %q", got)
	}
}

func TestRuneNoConnectionKeepsNativeChat(t *testing.T) {
	a, _, user, project := testWorker(t)
	ensureRuneTable(t, a, project)
	a.GSC = &fakeRuneDecryptor{}
	a.RuneDial = func(context.Context, string, string) (aichattools.RuneSession, error) {
		t.Error("connector must not be dialed without a saved connection")
		return nil, errors.New("must not dial")
	}
	provider := &roundProvider{rounds: [][]ai.Event{{{Text: "native answer"}}}}
	a.provider = provider
	id := queued(t, a, user, project)
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), claimed)

	if len(provider.requests) != 1 {
		t.Fatalf("streams = %d, want 1", len(provider.requests))
	}
	for _, def := range provider.requests[0].Tools {
		if strings.HasPrefix(def.Name, "cms__") {
			t.Fatalf("provider offered %q without a connection", def.Name)
		}
	}
	system := provider.requests[0].Messages[0].Content
	if !strings.Contains(system, "not connected") {
		t.Fatalf("system status missing a brief not-connected note: %q", system[len(system)-500:])
	}
	for _, secret := range []string{"secret-token", "cms.example"} {
		if strings.Contains(system, secret) {
			t.Fatalf("system leaks connection secret %q", secret)
		}
	}
	var status, content string
	if err := a.pool.QueryRow(context.Background(), `SELECT t.status, m.content FROM ai_turns t JOIN ai_messages m ON m.turn_id = t.id AND m.role = 'assistant' WHERE t.id = $1`, id).Scan(&status, &content); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || content != "native answer" {
		t.Fatalf("status=%q content=%q, want native chat to work", status, content)
	}
}

func TestRuneConnectionIsPerProject(t *testing.T) {
	a, _, user, project := testWorker(t)
	ensureRuneTable(t, a, project)
	const revision = "11111111-1111-1111-1111-111111111111"
	insertRuneConnection(t, a, project, revision)

	if conn := a.loadRuneConnection(context.Background(), user, project); conn == nil {
		t.Fatal("connection for the owning project did not load")
	}
	var otherProject pgtype.UUID
	if err := a.pool.QueryRow(context.Background(), `INSERT INTO projects(organization_id,name,base_url) SELECT organization_id,'other','https://other.test' FROM projects WHERE id=$1 RETURNING id`, project).Scan(&otherProject); err != nil {
		t.Fatal(err)
	}
	ensureRuneTable(t, a, otherProject)
	if conn := a.loadRuneConnection(context.Background(), user, otherProject); conn != nil {
		t.Fatal("project B loaded project A's connection")
	}
	var outsider pgtype.UUID
	if err := a.pool.QueryRow(context.Background(), `INSERT INTO users(auth_provider,auth_subject,email) VALUES('test','rune-outsider','rune-outsider@x.test') RETURNING id`).Scan(&outsider); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = a.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, outsider) })
	if conn := a.loadRuneConnection(context.Background(), outsider, project); conn != nil {
		t.Fatal("a non-member loaded the project connection")
	}
}

func TestRuneGuardRejectsReplacementAndDisconnect(t *testing.T) {
	a, _, user, project := testWorker(t)
	ensureRuneTable(t, a, project)
	const first = "11111111-1111-1111-1111-111111111111"
	const second = "22222222-2222-2222-2222-222222222222"
	insertRuneConnection(t, a, project, first)
	guard := a.runeGuard(user, project, first)
	if err := guard(context.Background()); err != nil {
		t.Fatalf("fresh guard = %v, want nil", err)
	}
	insertRuneConnection(t, a, project, second)
	if err := guard(context.Background()); err == nil {
		t.Fatal("replaced revision still accepted: stale calls must be rejected")
	}
	if _, err := a.pool.Exec(context.Background(), `DELETE FROM project_rune_connections WHERE project_id = $1`, project); err != nil {
		t.Fatal(err)
	}
	if err := guard(context.Background()); err == nil {
		t.Fatal("disconnected connection still accepted")
	}
}

func TestRuneWritePersistsGuardBeforeDispatch(t *testing.T) {
	a, _, user, project := testWorker(t)
	ensureRuneTable(t, a, project)
	insertRuneConnection(t, a, project, "33333333-3333-3333-3333-333333333333")
	a.GSC = &fakeRuneDecryptor{token: "live-token"}

	session := liveRuneSession()
	var dialEndpoint, dialToken string
	session.onCall = func(name string, args json.RawMessage) (aichattools.RuneCallResult, error) {
		// The write guard must already be persisted before the write is
		// sent: a crash from here on must recover as failed, never requeue.
		var started bool
		// The persisted row carries the namespaced model-facing name while
		// the session sees the original name.
		if err := a.pool.QueryRow(context.Background(), `SELECT output_started_at IS NOT NULL FROM ai_turns AS t WHERE output_started_at IS NOT NULL AND EXISTS (SELECT 1 FROM ai_tool_calls AS c WHERE c.turn_id = t.id AND c.name = 'cms__create_record' AND c.status = 'running' AND c.args = $1::jsonb)`, string(args)).Scan(&started); err != nil {
			return aichattools.RuneCallResult{}, err
		}
		if !started {
			return aichattools.RuneCallResult{}, errors.New("write dispatched before the running guard was persisted")
		}
		return aichattools.RuneCallResult{Content: `{"record":{"id":"r1"}}`}, nil
	}
	a.RuneDial = func(_ context.Context, endpoint, token string) (aichattools.RuneSession, error) {
		dialEndpoint, dialToken = endpoint, token
		return session, nil
	}
	provider := &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "cms-1", Name: "cms__create_record", Args: `{"collection":"posts","data":{"title":"hello"}}`}}},
		{{Text: "created"}},
	}}
	a.provider = provider
	id := queued(t, a, user, project)
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), claimed)

	if dialEndpoint != "https://cms.example.test/mcp" || dialToken != "live-token" {
		t.Fatalf("dialed endpoint=%q token=%q", dialEndpoint, dialToken)
	}
	if !session.closed {
		t.Error("the turn session was not closed at the end of the turn")
	}
	var offered []string
	for _, def := range provider.requests[0].Tools {
		offered = append(offered, def.Name)
		if def.Name == "cms__list_records" && !strings.Contains(string(def.Schema), `"live":true`) {
			t.Fatalf("cms__list_records schema is not the discovered live schema: %s", def.Schema)
		}
	}
	for _, want := range []string{"cms__list_records", "cms__create_record"} {
		found := false
		for _, name := range offered {
			if name == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("provider tools = %v, want %q", offered, want)
		}
	}
	system := provider.requests[0].Messages[0].Content
	for _, secret := range []string{"live-token", "secret-token", "cms.example.test", `"live":true`, "remote"} {
		if strings.Contains(system, secret) {
			t.Fatalf("system leaks %q", secret)
		}
	}
	var name, callStatus, args, summary string
	if err := a.pool.QueryRow(context.Background(), `SELECT name, status, args::text, summary FROM ai_tool_calls WHERE turn_id = $1`, id).Scan(&name, &callStatus, &args, &summary); err != nil {
		t.Fatal(err)
	}
	if name != "cms__create_record" || callStatus != "completed" {
		t.Fatalf("tool row = %q/%q", name, callStatus)
	}
	if !strings.Contains(args, `"posts"`) {
		t.Fatalf("tool args were not persisted for user activity: %q", args)
	}
	if !strings.Contains(summary, "published site unchanged until explicit static build") {
		t.Fatalf("write summary must state the published site is unchanged until an explicit static build, got %q", summary)
	}
	var status, content string
	if err := a.pool.QueryRow(context.Background(), `SELECT t.status, m.content FROM ai_turns t JOIN ai_messages m ON m.turn_id = t.id AND m.role = 'assistant' WHERE t.id = $1`, id).Scan(&status, &content); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || content != "created" {
		t.Fatalf("status=%q content=%q", status, content)
	}
}

func TestRuneDisabledToolCannotRunByGuessing(t *testing.T) {
	a, _, user, project := testWorker(t)
	ensureRuneTable(t, a, project)
	insertRuneConnection(t, a, project, "44444444-4444-4444-4444-444444444444")
	a.GSC = &fakeRuneDecryptor{token: "live-token"}
	session := liveRuneSession()
	a.RuneDial = func(context.Context, string, string) (aichattools.RuneSession, error) { return session, nil }
	provider := &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "cms-guess", Name: "cms__create_record", Args: `{}`}}},
		{{Text: "answered without the write"}},
	}}
	a.provider = provider
	id := queued(t, a, user, project)
	if _, err := a.pool.Exec(context.Background(), `UPDATE ai_turns SET disabled_ai_tools = '{cms__create_record}' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), claimed)

	for _, def := range provider.requests[0].Tools {
		if def.Name == "cms__create_record" {
			t.Fatalf("disabled CMS tool offered to the provider: %v", provider.requests[0].Tools)
		}
	}
	var callStatus, result string
	if err := a.pool.QueryRow(context.Background(), `SELECT status, result_content FROM ai_tool_calls WHERE turn_id = $1`, id).Scan(&callStatus, &result); err != nil {
		t.Fatal(err)
	}
	if callStatus != "failed" || !strings.Contains(result, "unknown tool") {
		t.Fatalf("guessed disabled call = %q/%q, want failed unknown tool", callStatus, result)
	}
	if session.calls != 0 {
		t.Fatal("disabled tool reached the CMS session")
	}
}

func TestRecoveredCMSWriteNeverRequeues(t *testing.T) {
	a, _, user, project := testWorker(t)
	id := queued(t, a, user, project)
	if _, err := a.pool.Exec(context.Background(), `INSERT INTO ai_tool_calls(turn_id, seq, call_id, name, args, status) VALUES($1, 0, 'cms-crash', 'cms__create_record', '{}', 'running')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pool.Exec(context.Background(), `UPDATE ai_turns SET status='running', claimed_by='dead', attempt_count=1, lease_expires_at=now()-interval '1 second', output_started_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := a.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := a.pool.QueryRow(context.Background(), `SELECT status FROM ai_turns WHERE id=$1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("recovered write turn = %q, want failed (a recovered write must never requeue and run twice)", status)
	}
}

// A turn that already started output must never requeue: requeueing would run
// the dispatch a second time, so a CMS write could apply twice.
func TestRequeueRefusesAfterOutputStarted(t *testing.T) {
	a, _, user, project := testWorker(t)
	id := queued(t, a, user, project)
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.pool.Exec(context.Background(), `UPDATE ai_turns SET output_started_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := a.requeue(claimed); err == nil {
		t.Fatal("requeue after output_started_at = nil, want a refusal")
	}
	var status string
	if err := a.pool.QueryRow(context.Background(), `SELECT status FROM ai_turns WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("status = %q, want running (no state change on refusal)", status)
	}
}
