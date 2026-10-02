package aichatworker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
)

// newCMSApprovalTestWorker connects to an explicit disposable test database
// only. It never falls back to DATABASE_URL, so these tests cannot mutate a
// development or production database.
func newCMSApprovalTestWorker(t *testing.T) (*Worker, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	databaseURL := os.Getenv("CMS_APPROVAL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("CMS_APPROVAL_TEST_DATABASE_URL is not set")
	}
	pool, err := internaldb.Connect(context.Background(), databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("cms approval test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"public.project_cms_connections", "public.ai_cms_approvals"} {
		var regclass string
		if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1)::text`, table).Scan(&regclass); err != nil || regclass == "" {
			t.Skipf("cms approval schema is not migrated in the test database (missing %s)", table)
		}
	}
	name := fmt.Sprintf("cms-approval-%d", time.Now().UnixNano())
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
	worker := New(pool, &roundProvider{}, Config{ID: "cms-approval-test", Concurrency: 1, PollInterval: time.Second, TurnTimeout: time.Minute})
	worker.lease = 45 * time.Second
	worker.heartbeat = 10 * time.Second
	worker.GSC = &fakeRuneDecryptor{token: "live-token"}
	return worker, user, project
}

func insertCMSConn(t *testing.T, w *Worker, project pgtype.UUID, provider, revision string) {
	t.Helper()
	if _, err := w.pool.Exec(context.Background(), `INSERT INTO project_cms_connections(project_id, provider, endpoint_url, encrypted_token, revision, last_checked_at) VALUES($1, $2, $3, $4, $5::uuid, now())
ON CONFLICT (project_id) DO UPDATE SET provider = EXCLUDED.provider, endpoint_url = EXCLUDED.endpoint_url, encrypted_token = EXCLUDED.encrypted_token, revision = EXCLUDED.revision, updated_at = now()`,
		project, provider, "https://cms.example.test/mcp", "enc:secret-token", revision); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.pool.Exec(context.Background(), `DELETE FROM project_cms_connections WHERE project_id = $1`, project)
	})
}

// countingSession logs every remote call: the gate contract is that no remote
// sensitive call happens before approval and no approved call runs twice.
type countingSession struct {
	*fakeRuneSession
	log []string
}

func (s *countingSession) Call(ctx context.Context, name string, args json.RawMessage) (aichattools.RuneCallResult, error) {
	s.log = append(s.log, name+":"+string(args))
	return s.fakeRuneSession.Call(ctx, name, args)
}

func cmsWriteEvent(id string) ai.Event {
	return ai.Event{ToolCall: &ai.ToolCall{ID: id, Name: "cms__create_record", Args: `{"collection":"posts","data":{"title":"hello"}}`}}
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
			if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_cms_approvals WHERE turn_id = $1 AND status = 'pending'`, turnID).Scan(&pending); err != nil {
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
		if _, err := w.pool.Exec(context.Background(), `UPDATE ai_cms_approvals SET status = $2, decided_at = now(), updated_at = now() WHERE turn_id = $1 AND status = 'pending'`, turnID, status); err != nil {
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
UPDATE ai_cms_approvals SET status = 'invalidated', decided_at = now(), summary = 'turn cancelled while running', updated_at = now()
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
	if err := w.pool.QueryRow(context.Background(), `SELECT a.status, tc.status, tc.result_content FROM ai_cms_approvals a JOIN ai_tool_calls tc ON tc.turn_id = a.turn_id AND tc.call_id = a.tool_call_id WHERE a.turn_id = $1`, turnID).Scan(&approvalStatus, &toolStatus, &result); err != nil {
		t.Fatal(err)
	}
	return approvalStatus, toolStatus, result
}

// TestApproveExecutesOnceInTheSameRun is the core in-process contract: the
// turn keeps running while it waits, executes the immutable original args
// exactly once after the decision, and finishes in the same run.
func TestApproveExecutesOnceInTheSameRun(t *testing.T) {
	w, user, project := newCMSApprovalTestWorker(t)
	insertCMSConn(t, w, project, "rune", "11111111-1111-1111-1111-111111111111")
	session := &countingSession{fakeRuneSession: liveRuneSession()}
	w.RuneDial = func(ctx context.Context, endpoint, token string) (aichattools.RuneSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{cmsWriteEvent("call-1")},
		{{Text: "done"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	decideWhenPending(t, w, turnID, "approved", 200*time.Millisecond)
	w.run(context.Background(), claimed)

	if len(session.log) != 1 {
		t.Fatalf("remote calls = %v, want exactly one approved write", session.log)
	}
	if session.log[0] != `create_record:{"collection":"posts","data":{"title":"hello"}}` {
		t.Fatalf("executed call = %q, want immutable original args", session.log[0])
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
	var proposed string
	if err := w.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'approval_required'), (SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = 'waiting_for_user'), (SELECT proposed_args::text FROM ai_cms_approvals WHERE turn_id = $1)`, turnID).Scan(&required, &waiting, &proposed); err != nil {
		t.Fatal(err)
	}
	if required != 1 || waiting != 0 {
		t.Fatalf("approval_required=%d waiting_for_user=%d, want 1/0: a waiting turn must never be persisted", required, waiting)
	}
	if !canonicalArgsEqual([]byte(proposed), `{"collection":"posts","data":{"title":"hello"}}`) {
		t.Fatalf("immutable proposed args = %s", proposed)
	}
}

// TestLeaseStaysValidAcrossWait is the durability that replaces the resume
// feature: the waiting run keeps its lease, so a lease shorter than the wait
// still leaves the turn claimed and unclaimable by a second worker.
func TestLeaseStaysValidAcrossWait(t *testing.T) {
	w, user, project := newCMSApprovalTestWorker(t)
	insertCMSConn(t, w, project, "rune", "12121212-1212-1212-1212-121212121212")
	session := &countingSession{fakeRuneSession: liveRuneSession()}
	w.RuneDial = func(ctx context.Context, endpoint, token string) (aichattools.RuneSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{cmsWriteEvent("call-1")},
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
	w, user, project := newCMSApprovalTestWorker(t)
	insertCMSConn(t, w, project, "rune", "13131313-1313-1313-1313-131313131313")
	session := &countingSession{fakeRuneSession: liveRuneSession()}
	w.RuneDial = func(ctx context.Context, endpoint, token string) (aichattools.RuneSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{{cmsWriteEvent("call-1")}}}
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
	w, user, project := newCMSApprovalTestWorker(t)
	insertCMSConn(t, w, project, "rune", "14141414-1414-1414-1414-141414141414")
	session := &countingSession{fakeRuneSession: liveRuneSession()}
	w.RuneDial = func(ctx context.Context, endpoint, token string) (aichattools.RuneSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{cmsWriteEvent("call-1")},
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
	if result != "cms__create_record error: the requested CMS change was not approved and was not performed." {
		t.Fatalf("denied result = %q", result)
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
	w, user, project := newCMSApprovalTestWorker(t)
	insertCMSConn(t, w, project, "rune", "22222222-2222-2222-2222-222222222222")
	session := &countingSession{fakeRuneSession: liveRuneSession()}
	w.RuneDial = func(ctx context.Context, endpoint, token string) (aichattools.RuneSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{cmsWriteEvent("call-1")},
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
	if result != "cms__create_record error: the requested CMS change was not approved and was not performed." {
		t.Fatalf("denied result = %q", result)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}

// liveUpdateSession serves a rune update flow: read_record returns the
// current record, update_record applies the write. The log counts remote
// calls for the no-early-write assertion.
type liveUpdateSession struct {
	*fakeRuneSession
	record string
	// mutateAfterProposal replaces the record once the pre-approval snapshot
	// has been read, so the re-read after the decision sees a moved target.
	mutateAfterProposal string
	log                 []string
}

func (s *liveUpdateSession) Call(ctx context.Context, name string, args json.RawMessage) (aichattools.RuneCallResult, error) {
	s.log = append(s.log, name)
	if name == "read_record" {
		content := s.record
		if s.mutateAfterProposal != "" {
			s.record = s.mutateAfterProposal
			s.mutateAfterProposal = ""
		}
		return aichattools.RuneCallResult{Content: content}, nil
	}
	return aichattools.RuneCallResult{Content: `{"updated":true}`}, nil
}

func cmsUpdateEvent(id string) ai.Event {
	return ai.Event{ToolCall: &ai.ToolCall{ID: id, Name: "cms__update_record", Args: `{"collection":"posts","id":"1","data":{"title":"new"}}`}}
}

func updateRuneSession() *fakeRuneSession {
	return &fakeRuneSession{tools: []aichattools.RuneToolDef{
		{Name: "read_record", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "update_record", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}}
}

// TestUpdateSnapshotValidationExecutes approves an update whose record is
// unchanged: the helper snapshot read happens before the wait (not a write),
// and the re-read after approval reproduces it, so the write runs once.
func TestUpdateSnapshotValidationExecutes(t *testing.T) {
	w, user, project := newCMSApprovalTestWorker(t)
	insertCMSConn(t, w, project, "rune", "55555555-5555-5555-5555-555555555555")
	session := &liveUpdateSession{fakeRuneSession: updateRuneSession(), record: `{"id":"1","title":"old"}`}
	w.RuneDial = func(ctx context.Context, endpoint, token string) (aichattools.RuneSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{cmsUpdateEvent("call-1")},
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
	if err := w.pool.QueryRow(context.Background(), `SELECT before_text FROM ai_cms_approvals WHERE turn_id = $1`, turnID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == "" {
		t.Fatal("update approval has no before preview from the read snapshot")
	}
	writes := 0
	for _, entry := range session.log {
		if entry == "update_record" {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("update_record executed %d times (%v), want exactly 1", writes, session.log)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}

// TestUpdateSnapshotChangeBlocksWrite moves the record while the turn waits:
// the re-read after approval produces a different snapshot, so the approved
// write never runs and the turn fails loudly instead.
func TestUpdateSnapshotChangeBlocksWrite(t *testing.T) {
	w, user, project := newCMSApprovalTestWorker(t)
	insertCMSConn(t, w, project, "rune", "66666666-6666-6666-6666-666666666666")
	session := &liveUpdateSession{fakeRuneSession: updateRuneSession(), record: `{"id":"1","title":"old"}`}
	w.RuneDial = func(ctx context.Context, endpoint, token string) (aichattools.RuneSession, error) {
		return session, nil
	}
	// The record moves between the approval snapshot and the check that runs
	// after the decision.
	session.mutateAfterProposal = `{"id":"1","title":"changed underneath"}`
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{cmsUpdateEvent("call-1")},
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
		if entry == "update_record" {
			t.Fatalf("stale approved write executed: %v", session.log)
		}
	}
	if got := turnStatus(t, w, turnID); got != "failed" {
		t.Fatalf("turn status = %q, want failed on snapshot mismatch", got)
	}
	var approvalStatus string
	if err := w.pool.QueryRow(context.Background(), `SELECT status FROM ai_cms_approvals WHERE turn_id = $1`, turnID).Scan(&approvalStatus); err != nil {
		t.Fatal(err)
	}
	if approvalStatus != "invalidated" {
		t.Fatalf("approval = %q, want invalidated", approvalStatus)
	}
}

// TestWordPressReadNeedsNoApproval runs a WordPress read end to end: the
// helper clears it without approval, so no approval row exists and the turn
// completes with the remote read as its only call.
func TestWordPressReadNeedsNoApproval(t *testing.T) {
	w, user, project := newCMSApprovalTestWorker(t)
	insertCMSConn(t, w, project, "wordpress", "77777777-7777-7777-7777-777777777777")
	session := &fakeRuneSession{tools: []aichattools.RuneToolDef{
		{Name: "get_content", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}}
	var calls int
	session.onCall = func(name string, args json.RawMessage) (aichattools.RuneCallResult, error) {
		calls++
		return aichattools.RuneCallResult{Content: `{"ok":true}`}, nil
	}
	w.WordPressDial = func(ctx context.Context, endpoint, token string) (aichattools.RuneSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "call-1", Name: "wp__get_content", Args: `{"id":"9"}`}}},
		{{Text: "read it"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w.run(context.Background(), claimed)

	if calls != 1 {
		t.Fatalf("wordpress read remote calls = %d, want 1", calls)
	}
	var approvals int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_cms_approvals WHERE turn_id = $1`, turnID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if approvals != 0 {
		t.Fatalf("wordpress read created an approval row")
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
	for _, def := range w.provider.(*roundProvider).requests[0].Tools {
		if len(def.Name) >= 5 && def.Name[:5] == "cms__" {
			t.Fatalf("wordpress turn offered rune tool %q", def.Name)
		}
	}
}

// unknownToolSession discovers a catalogued write plus one tool no local
// catalogue describes, so the per-turn registry must serve the unknown one
// exactly like the known ones.
func unknownToolSession() *countingSession {
	return &countingSession{fakeRuneSession: &fakeRuneSession{
		tools: []aichattools.RuneToolDef{
			{Name: "create_record", Description: "remote", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "brand_new_thing", Description: "server supplied", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	}}
}

// TestUnknownDiscoveredToolRunsWithoutApproval is the discovery contract on
// the initial path: a tool with no approval policy executes directly, in the
// same turn, with no approval row.
func TestUnknownDiscoveredToolRunsWithoutApproval(t *testing.T) {
	w, user, project := newCMSApprovalTestWorker(t)
	insertCMSConn(t, w, project, "rune", "88888888-8888-8888-8888-888888888888")
	session := unknownToolSession()
	w.RuneDial = func(ctx context.Context, endpoint, token string) (aichattools.RuneSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "call-1", Name: "cms__brand_new_thing", Args: `{"a":1}`}}},
		{{Text: "done"}},
	}}
	turnID := queued(t, w, user, project)
	claimed, err := w.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w.run(context.Background(), claimed)

	if len(session.log) != 1 || session.log[0] != `brand_new_thing:{"a":1}` {
		t.Fatalf("remote calls = %v, want the unknown tool executed once", session.log)
	}
	var approvals int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_cms_approvals WHERE turn_id = $1`, turnID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if approvals != 0 {
		t.Fatal("unknown tool created an approval row")
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed without a wait", got)
	}
}

// TestApprovedWriteThenUnknownToolInOneRound keeps the rest of the round
// running after the gate: the approved write executes and the following
// ungated call executes directly, both in the same turn.
func TestApprovedWriteThenUnknownToolInOneRound(t *testing.T) {
	w, user, project := newCMSApprovalTestWorker(t)
	insertCMSConn(t, w, project, "rune", "99999999-9999-9999-9999-999999999999")
	session := unknownToolSession()
	w.RuneDial = func(ctx context.Context, endpoint, token string) (aichattools.RuneSession, error) {
		return session, nil
	}
	w.provider = &roundProvider{rounds: [][]ai.Event{
		{
			{ToolCall: &ai.ToolCall{ID: "call-1", Name: "cms__create_record", Args: `{"collection":"posts","data":{"title":"a"}}`}},
			{ToolCall: &ai.ToolCall{ID: "call-2", Name: "cms__brand_new_thing", Args: `{"a":1}`}},
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

	if len(session.log) != 2 || session.log[1] != `brand_new_thing:{"a":1}` {
		t.Fatalf("remote calls = %v, want the approved write then the unknown tool", session.log)
	}
	var approvals int
	if err := w.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_cms_approvals WHERE turn_id = $1`, turnID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if approvals != 1 {
		t.Fatalf("approval rows = %d, want only the known sensitive write", approvals)
	}
	if got := turnStatus(t, w, turnID); got != "completed" {
		t.Fatalf("turn status = %q, want completed", got)
	}
}

// TestRecoverMarksExecutingUnknown covers the worker recovery path: a lease
// lost mid-execution fails the executing approval as unknown without retry,
// so a CMS write that may have applied is never replayed.
func TestRecoverMarksExecutingUnknown(t *testing.T) {
	w, user, project := newCMSApprovalTestWorker(t)
	insertCMSConn(t, w, project, "rune", "44444444-4444-4444-4444-444444444444")
	turnID := queued(t, w, user, project)
	ctx := context.Background()
	if _, err := w.pool.Exec(ctx, `UPDATE ai_turns SET status='running', claimed_by='dead-worker', output_started_at=now(), attempt_count=2, lease_expires_at=now()-interval '1 second' WHERE id = $1`, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, `INSERT INTO ai_cms_approvals(turn_id, tool_call_id, tool_name, provider, status) VALUES($1,'call-1','cms__update_record','rune','executing')`, turnID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, `INSERT INTO ai_tool_calls(turn_id,seq,call_id,name,args,status) VALUES($1,0,'call-1','cms__update_record','{}','running')`, turnID); err != nil {
		t.Fatal(err)
	}

	if err := w.recover(ctx); err != nil {
		t.Fatal(err)
	}
	var approvalStatus string
	if err := w.pool.QueryRow(ctx, `SELECT status FROM ai_cms_approvals WHERE turn_id = $1`, turnID).Scan(&approvalStatus); err != nil {
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
