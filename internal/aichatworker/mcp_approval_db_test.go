package aichatworker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
)

// newMCPApprovalTestWorker connects to an explicit disposable test database
// only. It never falls back to DATABASE_URL, so these tests cannot mutate a
// development or production database.
func newMCPApprovalTestWorker(t *testing.T) (*Worker, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	databaseURL := os.Getenv("MCP_APPROVAL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MCP_APPROVAL_TEST_DATABASE_URL is not set")
	}
	pool, err := internaldb.Connect(context.Background(), databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("mcp approval test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := internaldb.EnsureMCPMarketplaceTestSchema(context.Background(), pool); err != nil {
		t.Skipf("mcp marketplace test schema not available: %v", err)
	}
	for _, table := range []string{"public.project_mcp_connections", "public.project_mcp_tool_permissions", "public.ai_mcp_approvals"} {
		var regclass string
		if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, table).Scan(&regclass); err != nil || regclass == "" {
			t.Skipf("mcp approval schema is not migrated in the test database (missing %s)", table)
		}
	}
	name := fmt.Sprintf("mcp-approval-%d", time.Now().UnixNano())
	ctx := context.Background()
	var org, user, project pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations(name) VALUES($1) RETURNING id`, name).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(auth_provider,auth_subject,email) VALUES('test',$1,$2) RETURNING id`, name, name+"@x.test").Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE auth_provider='test' AND auth_subject LIKE $1`, name+"%")
	})
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members(org_id,user_id,role) VALUES($1,$2,'owner')`, org, user); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects(organization_id,name,base_url) VALUES($1,$2,'https://x.test') RETURNING id`, org, name).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_features(org_id,ai_concurrent_turn_limit_per_user) VALUES($1,2)`, org); err != nil {
		t.Fatal(err)
	}
	worker := New(pool, &roundProvider{}, Config{ID: "mcp-approval-test", Concurrency: 1, PollInterval: time.Second, TurnTimeout: time.Minute})
	worker.lease = 45 * time.Second
	worker.heartbeat = 10 * time.Second
	worker.GSC = &fakeMCPDecryptor{token: "live-token"}
	return worker, user, project
}

// countingSession logs every remote call: the gate contract is that no
// remote call happens before its exact approval and no approved call runs
// twice.
type countingSession struct {
	*fakeMCPSession
	log []string
}

func (s *countingSession) Call(ctx context.Context, name string, args json.RawMessage) (aichattools.MCPResult, error) {
	s.log = append(s.log, name+":"+string(args))
	return s.fakeMCPSession.Call(ctx, name, args)
}

func mcpWriteEvent(alias, id string) ai.Event {
	return ai.Event{ToolCall: &ai.ToolCall{ID: id, Name: alias, Args: `{"title":"hello"}`}}
}

// decideWhenPending stands in for the user clicking approve or reject: once
// the worker has written the approval it waits `after` (so the test can watch
// the turn while it waits) and then decides. It runs beside the worker,
// because the worker is the one waiting.
func decideWhenPending(t *testing.T, w *Worker, turnID pgtype.UUID, status string, after time.Duration) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for {
			var pending int
			if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_mcp_approvals WHERE turn_id = $1 AND status = 'pending'`, turnID).Scan(&pending); err != nil {
				t.Errorf("poll approval: %v", err)
				return
			}
			if pending == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("approval never became pending within the wait")
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(after)
		if _, err := w.pool.Exec(context.Background(), `UPDATE ai_mcp_approvals SET status = $2, decided_at = now(), updated_at = now() WHERE turn_id = $1 AND status = 'pending'`, turnID, status); err != nil {
			t.Errorf("decide %s: %v", status, err)
		}
	}()
}

// cancelWhenPending stands in for the cancel endpoint hitting a waiting turn:
// it requests the cancel and invalidates the pending approval in one
// transaction, exactly like the app handler does.
func cancelWhenPending(t *testing.T, w *Worker, turnID pgtype.UUID) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for {
			tag, err := w.pool.Exec(context.Background(), `
UPDATE ai_mcp_approvals SET status = 'invalidated', decided_at = now(), summary = 'turn cancelled while running', updated_at = now()
WHERE turn_id = $1 AND status = 'pending'`, turnID)
			if err != nil {
				t.Errorf("invalidate approval: %v", err)
				return
			}
			if tag.RowsAffected() == 1 {
				if _, err := w.pool.Exec(context.Background(), `UPDATE ai_turns SET cancel_requested_at = now(), updated_at = now() WHERE id = $1`, turnID); err != nil {
					t.Errorf("request cancel: %v", err)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Errorf("approval never became pending within the wait")
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
}

func turnStatus(t *testing.T, w *Worker, turnID pgtype.UUID) string {
	t.Helper()
	var status string
	if err := w.pool.QueryRow(context.Background(), `SELECT status FROM ai_turns WHERE id = $1`, turnID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func approvalAndToolStatus(t *testing.T, w *Worker, turnID pgtype.UUID) (string, string, string) {
	t.Helper()
	var approvalStatus, toolStatus, result string
	if err := w.pool.QueryRow(context.Background(), `SELECT a.status, tc.status, tc.result_content FROM ai_mcp_approvals a JOIN ai_tool_calls tc ON tc.turn_id = a.turn_id AND tc.call_id = a.tool_call_id WHERE a.turn_id = $1`, turnID).Scan(&approvalStatus, &toolStatus, &result); err != nil {
		t.Fatal(err)
	}
	return approvalStatus, toolStatus, result
}

// TestApproveExecutesOnceInTheSameRun is the core in-process contract: the
// turn keeps running while it waits, executes the immutable original args
// exactly once after the decision, and finishes in the same run.
func TestApproveExecutesOnceInTheSameRun(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "Main", "custom", "11111111-1111-1111-1111-111111111111", liveMCPTools())
	alias := aichattools.MCPModelToolName(connID.String(), "save_item")
	session := &countingSession{fakeMCPSession: liveMCPSession()}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{mcpWriteEvent(alias, "call-1")},
		{{Text: "done"}},
	}}
	w.provider.(*roundProvider).reasoning = []string{"private-approval-reasoning"}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decideWhenPending(t, w, turnID, "approved", 200*time.Millisecond)
	w.run(context.Background(), claimed)
	requests := w.provider.(*roundProvider).requests
	if len(requests) != 2 {
		t.Fatalf("provider rounds = %d, want 2", len(requests))
	}
	resultCount := 0
	reasoningReplayed := false
	for _, message := range requests[1].Messages {
		if message.Role == ai.RoleTool && message.ToolCallID == "call-1" {
			resultCount++
		}
		if message.Role == ai.RoleAssistant && len(message.ToolCalls) > 0 {
			reasoningReplayed = message.ReasoningContent == "private-approval-reasoning"
		}
	}
	if resultCount != 1 || !reasoningReplayed {
		t.Fatalf("approved tool replay: results=%d reasoning=%v, want 1/true", resultCount, reasoningReplayed)
	}

	if len(session.log) != 1 {
		t.Fatalf("remote calls = %v, want exactly one approved write", session.log)
	}
	if session.log[0] != `save_item:{"title":"hello"}` {
		t.Fatalf("executed call = %q, want the exact remote name with immutable original args", session.log[0])
	}
	approvalStatus, toolStatus, _ := approvalAndToolStatus(t, w, turnID)
	if approvalStatus != "completed" || toolStatus != "completed" {
		t.Fatalf("approval=%q tool=%q, want completed/completed", approvalStatus, toolStatus)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
	var content string
	if err := w.pool.QueryRow(context.Background(), `SELECT content FROM ai_messages WHERE turn_id = $1 AND role = 'assistant'`, turnID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if content != "done" {
		t.Fatalf("assistant content = %q, want the final answer from the same run", content)
	}
	var required, waiting int
	var proposed, remote, connection string
	if err := w.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'approval_required'), (SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'waiting_for_user'), (SELECT proposed_args::text FROM ai_mcp_approvals WHERE turn_id = $1), (SELECT remote_tool_name FROM ai_mcp_approvals WHERE turn_id = $1), (SELECT connection_name FROM ai_mcp_approvals WHERE turn_id = $1)`, turnID).Scan(&required, &waiting, &proposed, &remote, &connection); err != nil {
		t.Fatal(err)
	}
	if required != 1 || waiting != 0 {
		t.Fatalf("approval_required=%d waiting_for_user=%d, want 1/0: a waiting turn must never be persisted", required, waiting)
	}
	if !canonicalArgsEqual([]byte(proposed), `{"title":"hello"}`) {
		t.Fatalf("immutable proposed args = %s", proposed)
	}
	if remote != "save_item" || connection != "Main" {
		t.Fatalf("approval binds remote=%q connection=%q, want the exact pair", remote, connection)
	}
}

// TestLeaseStaysValidAcrossWait is the durability that replaces the resume
// feature: the waiting run keeps its lease, so a lease shorter than the wait
// still leaves the turn claimed and unclaimable by a second worker.
func TestLeaseStaysValidAcrossWait(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "Main", "custom", "12121212-1212-1212-1212-121212121212", liveMCPTools())
	alias := aichattools.MCPModelToolName(connID.String(), "save_item")
	session := &countingSession{fakeMCPSession: liveMCPSession()}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{mcpWriteEvent(alias, "call-1")},
		{{Text: "done"}},
	}}
	// A lease that expires well inside the wait: without a refresh inside the
	// wait loop the turn would be lost and never finalize.
	w.lease = 600 * time.Millisecond
	w.approvalWait = 30 * time.Second
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != turnID {
		t.Fatalf("claimed turn %s, want %s", claimed.ID.String(), turnID.String())
	}
	decideWhenPending(t, w, turnID, "approved", 3*w.lease)

	observed := make(chan string, 1)
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			var required int
			if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'approval_required'`, turnID).Scan(&required); err == nil && required == 1 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		// The lease taken at claim time would already be expired here.
		time.Sleep(3 * w.lease)
		var status string
		var live bool
		if err := w.pool.QueryRow(context.Background(), `SELECT status, lease_expires_at > now() FROM ai_turns WHERE id = $1`, turnID).Scan(&status, &live); err != nil {
			observed <- fmt.Sprintf("read the waiting turn: %v", err)
			return
		}
		if !live {
			observed <- "the waiting turn let its lease expire"
			return
		}
		if status != "running" {
			observed <- "waiting turn status " + status
			return
		}
		if _, err := New(w.pool, nil, Config{ID: "second-worker"}).claim(context.Background()); err == nil {
			observed <- "a second worker claimed the waiting turn"
			return
		}
		observed <- ""
	}()
	w.run(context.Background(), claimed)
	if problem := <-observed; problem != "" {
		t.Fatal(problem)
	}

	if len(session.log) != 1 {
		t.Fatalf("remote calls = %v, want the approved write after the wait", session.log)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed: the lease was refreshed throughout the wait", got)
	}
}

// TestCancelDuringWaitStopsTurn is stop during a wait: cancel wins the race,
// the write never runs, and the turn ends stopped.
func TestCancelDuringWaitStopsTurn(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "Main", "custom", "13131313-1313-1313-1313-131313131313", liveMCPTools())
	alias := aichattools.MCPModelToolName(connID.String(), "save_item")
	session := &countingSession{fakeMCPSession: liveMCPSession()}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{{mcpWriteEvent(alias, "call-1")}}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cancelWhenPending(t, w, turnID)
	w.run(context.Background(), claimed)

	if len(session.log) != 0 {
		t.Fatalf("cancelled write ran remotely: %v", session.log)
	}
	var status, errorCode string
	if err := w.pool.QueryRow(context.Background(), `SELECT status, COALESCE(error_code,'') FROM ai_turns WHERE id = $1`, turnID).Scan(&status, &errorCode); err != nil {
		t.Fatal(err)
	}
	if status != "stopped" || errorCode != "cancelled" {
		t.Fatalf("turn=%q code=%q, want stopped/cancelled", status, errorCode)
	}
}

// TestApprovalWaitTimeoutDeniesCall bounds the wait: past the bound nobody
// answered, so the call is denied and the round continues.
func TestApprovalWaitTimeoutDeniesCall(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "Main", "custom", "14141414-1414-1414-1414-141414141414", liveMCPTools())
	alias := aichattools.MCPModelToolName(connID.String(), "save_item")
	session := &countingSession{fakeMCPSession: liveMCPSession()}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{mcpWriteEvent(alias, "call-1")},
		{{Text: "nobody answered"}},
	}}
	w.approvalWait = 400 * time.Millisecond
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w.run(context.Background(), claimed)

	if len(session.log) != 0 {
		t.Fatalf("unanswered write ran remotely: %v", session.log)
	}
	approvalStatus, toolStatus, result := approvalAndToolStatus(t, w, turnID)
	if approvalStatus != "rejected" || toolStatus != "failed" {
		t.Fatalf("approval=%q tool=%q, want rejected/failed", approvalStatus, toolStatus)
	}
	if want := alias + " error: the requested MCP change was not approved and was not performed."; result != want {
		t.Fatalf("denied result = %q, want %q", result, want)
	}
	var decided int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'approval_decided'`, turnID).Scan(&decided); err != nil {
		t.Fatal(err)
	}
	if decided != 1 {
		t.Fatalf("approval_decided events = %d, want the timed-out card cleared", decided)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}

// TestRejectDeniesWithoutExecution records a paired denied tool result and
// never executes the remote call.
func TestRejectDeniesWithoutExecution(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "Main", "custom", "22222222-2222-2222-2222-222222222222", liveMCPTools())
	alias := aichattools.MCPModelToolName(connID.String(), "save_item")
	session := &countingSession{fakeMCPSession: liveMCPSession()}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{mcpWriteEvent(alias, "call-1")},
		{{Text: "understood"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decideWhenPending(t, w, turnID, "rejected", 100*time.Millisecond)
	w.run(context.Background(), claimed)

	if len(session.log) != 0 {
		t.Fatalf("rejected call executed remotely: %v", session.log)
	}
	approvalStatus, toolStatus, result := approvalAndToolStatus(t, w, turnID)
	if approvalStatus != "rejected" || toolStatus != "failed" {
		t.Fatalf("approval=%q tool=%q, want rejected/failed", approvalStatus, toolStatus)
	}
	if want := alias + " error: the requested MCP change was not approved and was not performed."; result != want {
		t.Fatalf("denied result = %q, want %q", result, want)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}

// wordPressUpdateSession serves a WordPress update flow: get_content returns
// the current post, update_content applies the write. The log counts remote
// calls for the no-early-write assertion.
type wordPressUpdateSession struct {
	*fakeMCPSession
	record string
	// mutateAfterProposal replaces the record once the pre-approval snapshot
	// has been read, so the re-read after the decision sees a moved target.
	mutateAfterProposal string
	log                 []string
	// hook runs before every session call for tests that must change saved
	// rows mid-turn; a non-nil error replaces the call outcome.
	hook func(name string, args json.RawMessage) error
}

func (s *wordPressUpdateSession) Call(ctx context.Context, name string, args json.RawMessage) (aichattools.MCPResult, error) {
	s.log = append(s.log, name)
	if s.hook != nil {
		if err := s.hook(name, args); err != nil {
			return aichattools.MCPResult{}, err
		}
	}
	if name == "get_content" {
		content := s.record
		if s.mutateAfterProposal != "" {
			s.record = s.mutateAfterProposal
			s.mutateAfterProposal = ""
		}
		return aichattools.MCPResult{Content: content}, nil
	}
	return aichattools.MCPResult{Content: `{"updated":true}`}, nil
}

func getContentMCPTools() []aichattools.MCPToolDef {
	return []aichattools.MCPToolDef{
		{Name: "get_content", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
}

func wordPressUpdateTools() []aichattools.MCPToolDef {
	object := json.RawMessage(`{"type":"object"}`)
	return []aichattools.MCPToolDef{
		{Name: "get_content", Description: "remote", InputSchema: object},
		{Name: "update_content", Description: "remote", InputSchema: object},
	}
}

// TestUpdateSnapshotValidationExecutes approves an update whose post is
// unchanged: the helper snapshot read happens before the wait (a read, not a
// write), and the re-read after approval reproduces it, so the write runs
// once. The snapshot read itself needs get_content on Allow: safety reads
// never bypass saved policy.
func TestUpdateSnapshotValidationExecutes(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "WordPress", "wordpress", "55555555-5555-5555-5555-555555555555", wordPressUpdateTools())
	alias := aichattools.MCPModelToolName(connID.String(), "update_content")
	session := &wordPressUpdateSession{fakeMCPSession: &fakeMCPSession{tools: wordPressUpdateTools()}, record: `{"id":"1","status":"draft","title":"old"}`}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	if _, err := w.pool.Exec(context.Background(), `INSERT INTO project_mcp_tool_permissions(connection_id, tool_name, permission) VALUES($1, 'get_content', 'allow')`, connID); err != nil {
		t.Fatal(err)
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "call-1", Name: alias, Args: `{"id":"1","title":"new"}`}}},
		{{Text: "updated"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decideWhenPending(t, w, turnID, "approved", 100*time.Millisecond)
	w.run(context.Background(), claimed)

	var before string
	if err := w.pool.QueryRow(context.Background(), `SELECT before_text FROM ai_mcp_approvals WHERE turn_id = $1`, turnID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == "" {
		t.Fatal("update approval has no before preview from the read snapshot")
	}
	writes := 0
	for _, entry := range session.log {
		if entry == "update_content" {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("update_content executed %d times (%v), want exactly 1", writes, session.log)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}

// TestUpdateSnapshotChangeBlocksWrite moves the post while the turn waits:
// the re-read after approval produces a different snapshot, so the approved
// write never runs and the turn fails loudly instead.
func TestUpdateSnapshotChangeBlocksWrite(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "WordPress", "wordpress", "66666666-6666-6666-6666-666666666666", wordPressUpdateTools())
	alias := aichattools.MCPModelToolName(connID.String(), "update_content")
	session := &wordPressUpdateSession{fakeMCPSession: &fakeMCPSession{tools: wordPressUpdateTools()}, record: `{"id":"1","status":"draft","title":"old"}`}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	if _, err := w.pool.Exec(context.Background(), `INSERT INTO project_mcp_tool_permissions(connection_id, tool_name, permission) VALUES($1, 'get_content', 'allow')`, connID); err != nil {
		t.Fatal(err)
	}
	// The post moves between the approval snapshot and the check that runs
	// after the decision.
	session.mutateAfterProposal = `{"id":"1","status":"draft","title":"changed underneath"}`
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "call-1", Name: alias, Args: `{"id":"1","title":"new"}`}}},
		{{Text: "never"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decideWhenPending(t, w, turnID, "approved", 0)
	w.run(context.Background(), claimed)

	for _, entry := range session.log {
		if entry == "update_content" {
			t.Fatalf("stale approved write executed: %v", session.log)
		}
	}
	if got := turnStatus(t, w, turnID); got != "failed" {
		t.Fatalf("turn status = %q, want failed on snapshot mismatch", got)
	}
	var approvalStatus string
	if err := w.pool.QueryRow(context.Background(), `SELECT status FROM ai_mcp_approvals WHERE turn_id = $1`, turnID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "invalidated" {
		t.Fatalf("approval = %q, want invalidated", approvalStatus)
	}
}

// TestSnapshotReadWithoutAllowIsBlocked is the preflight policy: the safety
// read runs only while get_content is on Allow. With the default Ask the
// preparation fails closed with the prerequisite message and the write never
// runs, instead of reading past the saved policy or approving blind.
func TestSnapshotReadWithoutAllowIsBlocked(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "WordPress", "wordpress", "99998888-7777-7777-7777-777777777777", wordPressUpdateTools())
	alias := aichattools.MCPModelToolName(connID.String(), "update_content")
	session := &wordPressUpdateSession{fakeMCPSession: &fakeMCPSession{tools: wordPressUpdateTools()}, record: `{"id":"1","status":"draft","title":"old"}`}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "call-1", Name: alias, Args: `{"id":"1","title":"new"}`}}},
		{{Text: "blocked"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w.run(context.Background(), claimed)

	for _, entry := range session.log {
		if entry == "get_content" {
			t.Fatalf("preflight read bypassed saved policy: %v", session.log)
		}
		if entry == "update_content" {
			t.Fatalf("write ran without its safety check: %v", session.log)
		}
	}
	// Blocked preparation stores no tool-call row; the failure reaches the
	// model transcript only.
	var toolRows int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_tool_calls WHERE turn_id = $1`, turnID).Scan(&toolRows); err != nil {
		t.Fatal(err)
	}
	if toolRows != 0 {
		t.Fatalf("blocked preparation stored %d tool rows, want none", toolRows)
	}
	blocked := ""
	for _, message := range w.provider.(*roundProvider).requests[1].Messages {
		if message.Role == ai.RoleTool && message.ToolCallID == "call-1" {
			blocked = message.Content
		}
	}
	if !strings.Contains(blocked, "allow prerequisite read tool") {
		t.Fatalf("blocked result = %q, want the specific prerequisite message preserved", blocked)
	}
	var approvals int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_mcp_approvals WHERE turn_id = $1`, turnID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if approvals != 0 {
		t.Fatal("blocked preparation created an approval row")
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}

// TestAskDefaultRequiresApprovalForReads is the no-exemption rule: even a
// read waits for an exact approval while its policy is Ask.
func TestAskDefaultRequiresApprovalForReads(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "WordPress", "wordpress", "77777777-7777-7777-7777-777777777777", getContentMCPTools())
	alias := aichattools.MCPModelToolName(connID.String(), "get_content")
	session := &fakeMCPSession{tools: []aichattools.MCPToolDef{
		{Name: "get_content", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}}
	var calls int
	session.onCall = func(name string, args json.RawMessage) (aichattools.MCPResult, error) {
		calls++
		return aichattools.MCPResult{Content: `{"ok":true}`}, nil
	}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "call-1", Name: alias, Args: `{"id":"9"}`}}},
		{{Text: "read it"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decideWhenPending(t, w, turnID, "approved", 100*time.Millisecond)
	w.run(context.Background(), claimed)

	if calls != 1 {
		t.Fatalf("ask read remote calls = %d, want 1 after approval", calls)
	}
	var approvals int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_mcp_approvals WHERE turn_id = $1`, turnID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if approvals != 1 {
		t.Fatalf("ask read approvals = %d, want the exact decision", approvals)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}

// TestAllowBypassesPromptButNotGuards runs an allowed call with no approval
// row, while a denied call on the same turn never dispatches.
func TestAllowBypassesPromptButNotGuards(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "Main", "custom", "99999999-9999-9999-9999-999999999999", liveMCPTools())
	allowedAlias := aichattools.MCPModelToolName(connID.String(), "save_item")
	deniedAlias := aichattools.MCPModelToolName(connID.String(), "fetch_items")
	session := &countingSession{fakeMCPSession: liveMCPSession()}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	if _, err := w.pool.Exec(context.Background(), `INSERT INTO project_mcp_tool_permissions(connection_id, tool_name, permission) VALUES($1, 'save_item', 'allow'), ($1, 'fetch_items', 'deny')`, connID); err != nil {
		t.Fatal(err)
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{
			{ToolCall: &ai.ToolCall{ID: "call-1", Name: allowedAlias, Args: `{"title":"a"}`}},
			{ToolCall: &ai.ToolCall{ID: "call-2", Name: deniedAlias, Args: `{}`}},
		},
		{{Text: "done"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w.run(context.Background(), claimed)

	if len(session.log) != 1 || session.log[0] != `save_item:{"title":"a"}` {
		t.Fatalf("remote calls = %v, want only the allowed call", session.log)
	}
	var approvals int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_mcp_approvals WHERE turn_id = $1`, turnID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if approvals != 0 {
		t.Fatalf("approval rows = %d, want none for allow/deny", approvals)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}

// TestDuplicateRemoteNamesStaySeparate gives two connections the same remote
// tool: each gets its own alias and its own saved policy.
func TestDuplicateRemoteNamesStaySeparate(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	firstID := insertMCPConnection(t, w, project, "First", "custom", "aaaaaaaa-1111-1111-1111-111111111111", liveMCPTools())
	secondID := insertMCPConnection(t, w, project, "Second", "custom", "bbbbbbbb-2222-2222-2222-222222222222", liveMCPTools())
	firstAlias := aichattools.MCPModelToolName(firstID.String(), "save_item")
	secondAlias := aichattools.MCPModelToolName(secondID.String(), "save_item")
	if firstAlias == secondAlias {
		t.Fatal("identical remote names share an alias")
	}
	session := &countingSession{fakeMCPSession: liveMCPSession()}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	if _, err := w.pool.Exec(context.Background(), `INSERT INTO project_mcp_tool_permissions(connection_id, tool_name, permission) VALUES($1, 'save_item', 'allow')`, firstID); err != nil {
		t.Fatal(err)
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{
			{ToolCall: &ai.ToolCall{ID: "call-1", Name: firstAlias, Args: `{"title":"a"}`}},
			{ToolCall: &ai.ToolCall{ID: "call-2", Name: secondAlias, Args: `{"title":"b"}`}},
		},
		{{Text: "done"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decideWhenPending(t, w, turnID, "approved", 100*time.Millisecond)
	w.run(context.Background(), claimed)

	// The allowed connection dispatches at once; the Ask connection waits for
	// its own exact approval, which the decider grants.
	if len(session.log) != 2 {
		t.Fatalf("remote calls = %v, want both connections dispatched under the exact remote name", session.log)
	}
	var approvals int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_mcp_approvals WHERE turn_id = $1`, turnID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if approvals != 1 {
		t.Fatalf("approval rows = %d, want only the Ask connection's", approvals)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}

// TestRecoverMarksExecutingUnknown covers the worker recovery path: a lease
// lost mid-execution fails the executing approval as unknown without retry,
// so a call that may have applied is never replayed.
func TestRecoverMarksExecutingUnknown(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	turnID := queued(t, w, user, project)
	ctx := context.Background()
	if _, err := w.pool.Exec(ctx, `UPDATE ai_turns SET status='running', claimed_by='dead-worker', output_started_at=now(), attempt_count=2, lease_expires_at=now()-interval '1 second' WHERE id = $1`, turnID); err != nil {
		t.Fatal(err)
	}
	alias := aichattools.MCPModelToolName("11111111-1111-1111-1111-111111111111", "save_item")
	if _, err := w.pool.Exec(ctx, `INSERT INTO ai_mcp_approvals(turn_id, tool_call_id, tool_name, provider, service, remote_tool_name, status) VALUES($1,'call-1',$2,'custom','custom','save_item','executing')`, turnID, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, `INSERT INTO ai_tool_calls(turn_id,seq,call_id,name,args,status) VALUES($1,0,'call-1',$2,'{}','running')`, turnID, alias); err != nil {
		t.Fatal(err)
	}

	if err := w.recover(ctx); err != nil {
		t.Fatal(err)
	}
	var approvalStatus string
	if err := w.pool.QueryRow(ctx, `SELECT status FROM ai_mcp_approvals WHERE turn_id = $1`, turnID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "failed" {
		t.Fatalf("recovered approval = %q, want failed", approvalStatus)
	}
	if got := turnStatus(t, w, turnID); got != "failed" {
		t.Fatalf("recovered turn = %q, want failed", got)
	}
	// The failed turn is never reclaimed, so the uncertain write is never
	// retried.
	if _, err := w.claim(ctx); err == nil {
		t.Fatal("failed turn was reclaimable")
	}
}

// denyWhilePending stands in for the permissions endpoint denying a tool
// while its approval waits: the exact rule is saved and the matching pending
// approval is invalidated in one transaction, exactly like the app handler.
func denyWhilePending(t *testing.T, w *Worker, turnID, connID pgtype.UUID, remote string) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(30 * time.Second)
		for {
			var pending int
			if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_mcp_approvals WHERE turn_id = $1 AND status = 'pending'`, turnID).Scan(&pending); err != nil {
				t.Errorf("poll approval: %v", err)
				return
			}
			if pending == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("approval never became pending within the wait")
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		tx, err := w.pool.Begin(context.Background())
		if err != nil {
			t.Errorf("begin deny: %v", err)
			return
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		ctx := context.Background()
		if _, err := tx.Exec(ctx, `INSERT INTO project_mcp_tool_permissions(connection_id, tool_name, permission) VALUES($1, $2, 'deny') ON CONFLICT (connection_id, tool_name) DO UPDATE SET permission = 'deny'`, connID, remote); err != nil {
			t.Errorf("deny rule: %v", err)
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE ai_mcp_approvals SET status = 'invalidated', decided_at = now(), summary = 'tool permission set to deny', updated_at = now() WHERE connection_id = $1 AND remote_tool_name = $2 AND status = 'pending'`, connID, remote); err != nil {
			t.Errorf("invalidate: %v", err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("commit deny: %v", err)
			return
		}
	}()
}

// TestDenyOverridesStaleApproval is the mid-turn permission edit: the tool
// is Ask while the call waits, then Deny lands with invalidation. The
// stale approval never dispatches.
func TestDenyOverridesStaleApproval(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "Main", "custom", "aaaaaaaa-4444-4444-4444-444444444444", liveMCPTools())
	alias := aichattools.MCPModelToolName(connID.String(), "save_item")
	session := &countingSession{fakeMCPSession: liveMCPSession()}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{mcpWriteEvent(alias, "call-1")},
		{{Text: "denied mid-turn"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	denyWhilePending(t, w, turnID, connID, "save_item")
	w.run(context.Background(), claimed)

	if len(session.log) != 0 {
		t.Fatalf("denied-after-ask call executed remotely: %v", session.log)
	}
	approvalStatus, toolStatus, _ := approvalAndToolStatus(t, w, turnID)
	if approvalStatus != "invalidated" || toolStatus != "failed" {
		t.Fatalf("approval=%q tool=%q, want invalidated/failed", approvalStatus, toolStatus)
	}
	var permission string
	if err := w.pool.QueryRow(context.Background(), `SELECT permission FROM project_mcp_tool_permissions WHERE connection_id = $1 AND tool_name = 'save_item'`, connID).Scan(&permission); err != nil {
		t.Fatal(err)
	}
	if permission != "deny" {
		t.Fatalf("saved rule = %q, want deny", permission)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}

// TestApprovedProofNeverAuthorizesAnotherCall is the call-scoped proof rule:
// call1 is approved and dispatches, then the rule flips to Ask while call2
// (same alias) prepares. call2 must not dispatch on call1's old proof,
// even though the alias matches.
func TestApprovedProofNeverAuthorizesAnotherCall(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "WordPress", "wordpress", "aaaaaaaa-1111-1111-1111-111111111111", wordPressUpdateTools())
	alias := aichattools.MCPModelToolName(connID.String(), "update_content")
	session := &wordPressUpdateSession{fakeMCPSession: &fakeMCPSession{tools: wordPressUpdateTools()}, record: `{"id":"1","status":"draft","title":"old"}`}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	if _, err := w.pool.Exec(context.Background(), `INSERT INTO project_mcp_tool_permissions(connection_id, tool_name, permission) VALUES($1, 'get_content', 'allow')`, connID); err != nil {
		t.Fatal(err)
	}
	armed := false
	session.hook = func(name string, args json.RawMessage) error {
		ctx := context.Background()
		if name == "update_content" && !armed {
			// call1 dispatches: open call2's gate, then arm the flip for
			// call2's own preparation.
			_, err := w.pool.Exec(ctx, `INSERT INTO project_mcp_tool_permissions(connection_id, tool_name, permission) VALUES($1, 'update_content', 'allow')`, connID)
			if err != nil {
				return err
			}
			armed = true
		}
		if name == "get_content" && armed {
			armed = false
			_, err := w.pool.Exec(ctx, `UPDATE project_mcp_tool_permissions SET permission = 'ask' WHERE connection_id = $1 AND tool_name = 'update_content'`, connID)
			return err
		}
		return nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "call-1", Name: alias, Args: `{"id":"1","title":"one"}`}}},
		{{ToolCall: &ai.ToolCall{ID: "call-2", Name: alias, Args: `{"id":"1","title":"two"}`}}},
		{{Text: "done"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decideWhenPending(t, w, turnID, "approved", 100*time.Millisecond)
	w.run(context.Background(), claimed)

	writes := 0
	for _, entry := range session.log {
		if entry == "update_content" {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("update_content dispatched %d times (%v): call2 reused call1's approval proof", writes, session.log)
	}
	var call2Status, call2Result string
	if err := w.pool.QueryRow(context.Background(), `SELECT status, result_content FROM ai_tool_calls WHERE turn_id = $1 AND call_id = 'call-2'`, turnID).Scan(&call2Status, &call2Result); err != nil {
		t.Fatal(err)
	}
	if call2Status != "failed" {
		t.Fatalf("call2 = %q/%q, want failed without dispatch", call2Status, call2Result)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}

// TestGuardBlocksSchemaChangedAfterGate proves the execution-start guard
// compares the live session schema to the current saved digest: the schema
// moves after the gate verified it, so the stale session must not dispatch.
func TestGuardBlocksSchemaChangedAfterGate(t *testing.T) {
	w, user, project := newMCPApprovalTestWorker(t)
	connID := insertMCPConnection(t, w, project, "WordPress", "wordpress", "bbbbbbbb-2222-2222-2222-222222222222", wordPressUpdateTools())
	alias := aichattools.MCPModelToolName(connID.String(), "update_content")
	session := &wordPressUpdateSession{fakeMCPSession: &fakeMCPSession{tools: wordPressUpdateTools()}, record: `{"id":"1","status":"draft","title":"old"}`}
	w.MCPDial = func(ctx context.Context, endpoint, token string) (aichattools.MCPSession, error) {
		return session, nil
	}
	if _, err := w.pool.Exec(context.Background(), `INSERT INTO project_mcp_tool_permissions(connection_id, tool_name, permission) VALUES($1, 'get_content', 'allow'), ($1, 'update_content', 'allow')`, connID); err != nil {
		t.Fatal(err)
	}
	// The schema moves while the call prepares, after the gate verified it.
	session.hook = func(name string, args json.RawMessage) error {
		if name != "get_content" {
			return nil
		}
		_, err := w.pool.Exec(context.Background(), `UPDATE project_mcp_connections SET tools = '[{"name":"get_content","description":"d","input_schema":{"type":"object"}},{"name":"update_content","description":"d","input_schema":{"type":"object","properties":{"changed":{"type":"string"}}}}]' WHERE id = $1`, connID)
		return err
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "call-1", Name: alias, Args: `{"id":"1","title":"new"}`}}},
		{{Text: "done"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w.run(context.Background(), claimed)

	for _, entry := range session.log {
		if entry == "update_content" {
			t.Fatalf("stale-schema write dispatched: %v", session.log)
		}
	}
	if session.log[0] != "get_content" {
		t.Fatalf("preflight read did not run: %v", session.log)
	}
	var callStatus string
	if err := w.pool.QueryRow(context.Background(), `SELECT status FROM ai_tool_calls WHERE turn_id = $1`, turnID).Scan(&callStatus); err != nil {
		t.Fatal(err)
	}
	if callStatus != "failed" {
		t.Fatalf("stale call = %q, want failed", callStatus)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}
