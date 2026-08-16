package aichatworker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/joho/godotenv"
	"github.com/ps-wizard/revserp/internal/ai"
	internaldb "github.com/ps-wizard/revserp/internal/db"
)

func testWorker(t *testing.T) (*Worker, *Worker, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	if err := godotenv.Load("../../.env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	pool, err := internaldb.Connect(context.Background(), url)
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(pool.Close)
	name := fmt.Sprintf("chat-worker-%d", time.Now().UnixNano())
	ctx := context.Background()
	var org, user, project pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations(name) VALUES($1) RETURNING id`, name).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(auth_provider,auth_subject,email) VALUES('test',$1,$2) RETURNING id`, name, name+"@x.test").Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, org); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE auth_provider='test' AND auth_subject LIKE $1`, name+"%"); err != nil {
			t.Error(err)
		}
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
	return New(pool, nil, Config{ID: name + "-a"}), New(pool, nil, Config{ID: name + "-b"}), user, project
}

func queued(t *testing.T, w *Worker, user, project pgtype.UUID) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	var conversation, id pgtype.UUID
	if err := w.pool.QueryRow(ctx, `INSERT INTO ai_conversations(project_id,created_by_user_id,title) VALUES($1,$2,'t') RETURNING id`, project, user).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	if err := w.pool.QueryRow(ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash,queued_at) VALUES($1,$2,'queued','none','none','m','v',$3,decode(repeat('00',32),'hex'),now()-interval '100 years') RETURNING id`, conversation, user, fmt.Sprint(time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, `INSERT INTO ai_messages(turn_id,role,status,content) VALUES($1,'user','complete','x'),($1,'assistant','pending','')`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestClaimExclusivity(t *testing.T) {
	a, b, user, project := testWorker(t)
	queued(t, a, user, project)
	errs := make(chan error, 2)
	go func() { _, err := a.claim(context.Background()); errs <- err }()
	go func() { _, err := b.claim(context.Background()); errs <- err }()
	claimed := 0
	for range 2 {
		if err := <-errs; err == nil {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("claims=%d, want 1", claimed)
	}
}

func TestWorkspaceLimitLeavesThirdQueuedAndClaimsOtherUser(t *testing.T) {
	a, _, user, project := testWorker(t)
	queued(t, a, user, project)
	queued(t, a, user, project)
	third := queued(t, a, user, project)
	for range 2 {
		if _, err := a.claim(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	name := a.cfg.ID + "-other"
	var other pgtype.UUID
	if err := a.pool.QueryRow(context.Background(), `INSERT INTO users(auth_provider,auth_subject,email) VALUES('test',$1,$2) RETURNING id`, name, name+"@x.test").Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := a.pool.Exec(context.Background(), `INSERT INTO organization_members(org_id,user_id,role) SELECT organization_id,$1,'member' FROM projects WHERE id=$2`, other, project); err != nil {
		t.Fatal(err)
	}
	otherTurn := queued(t, a, other, project)
	got, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != otherTurn {
		t.Fatalf("claimed %s, want other %s", got.ID.String(), otherTurn.String())
	}
	var status string
	if err := a.pool.QueryRow(context.Background(), `SELECT status FROM ai_turns WHERE id=$1`, third).Scan(&status); err != nil || status != "queued" {
		t.Fatalf("third status=%q err=%v", status, err)
	}
}

type fakeProvider struct {
	events []ai.Event
	err    error
}

func (f fakeProvider) Stream(_ context.Context, _ ai.Request, emit func(ai.Event) error) error {
	for _, event := range f.events {
		if err := emit(event); err != nil {
			return err
		}
	}
	return f.err
}

func TestSecondTemporaryPreOutputFailureFinalizes(t *testing.T) {
	a, _, user, project := testWorker(t)
	a.provider = fakeProvider{err: &ai.ProviderError{Code: "provider_timeout", Temporary: true}}
	id := queued(t, a, user, project)
	var conversation pgtype.UUID
	if err := a.pool.QueryRow(context.Background(), `SELECT conversation_id FROM ai_turns WHERE id=$1`, id).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []int{1, 2} {
		if _, err := a.pool.Exec(context.Background(), `UPDATE ai_turns SET status='running',claimed_by=$2,attempt_count=$3,lease_expires_at=now()+interval '1 minute' WHERE id=$1`, id, a.cfg.ID, attempt); err != nil {
			t.Fatal(err)
		}
		a.run(context.Background(), turn{ID: id, ConversationID: conversation, Effort: "none", Model: "m", AttemptCount: int32(attempt)})
	}
	var status, code string
	var attempts int
	if err := a.pool.QueryRow(context.Background(), `SELECT status,COALESCE(error_code,''),attempt_count FROM ai_turns WHERE id=$1`, id).Scan(&status, &code, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || code != "provider_timeout" || attempts != 2 {
		t.Fatalf("status=%q code=%q attempts=%d", status, code, attempts)
	}
}

func TestPostOutputFailureKeepsPartial(t *testing.T) {
	a, _, user, project := testWorker(t)
	a.provider = fakeProvider{events: []ai.Event{{Text: "partial"}}, err: &ai.ProviderError{Code: "provider_timeout", Temporary: true}}
	id := queued(t, a, user, project)
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), claimed)
	var status, content string
	if err := a.pool.QueryRow(context.Background(), `SELECT t.status,m.content FROM ai_turns t JOIN ai_messages m ON m.turn_id=t.id AND m.role='assistant' WHERE t.id=$1`, id).Scan(&status, &content); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || content != "partial" {
		t.Fatalf("status=%q content=%q", status, content)
	}
}

type cancellationProvider struct{}

func (cancellationProvider) Stream(ctx context.Context, _ ai.Request, emit func(ai.Event) error) error {
	if err := emit(ai.Event{Text: "partial"}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestCancellationFinalizesPartialWithStoppedEvent(t *testing.T) {
	a, _, user, project := testWorker(t)
	a.provider = cancellationProvider{}
	a.heartbeat = time.Millisecond
	a.flushInterval = time.Hour
	id := queued(t, a, user, project)
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != id {
		t.Fatalf("claimed %s, want %s", claimed.ID.String(), id.String())
	}
	if _, err := a.pool.Exec(context.Background(), `UPDATE ai_turns SET cancel_requested_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), claimed)
	var status, code, messageStatus, content string
	if err := a.pool.QueryRow(context.Background(), `SELECT t.status,COALESCE(t.error_code,''),m.status,m.content FROM ai_turns t JOIN ai_messages m ON m.turn_id=t.id AND m.role='assistant' WHERE t.id=$1`, id).Scan(&status, &code, &messageStatus, &content); err != nil {
		t.Fatal(err)
	}
	if status != "stopped" || code != "cancelled" || messageStatus != "partial" || content != "partial" {
		t.Fatalf("turn=%q/%q message=%q/%q", status, code, messageStatus, content)
	}
	var events int
	if err := a.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_turn_events WHERE turn_id=$1 AND event_type='stopped' AND payload->>'error_code'='cancelled'`, id).Scan(&events); err != nil || events != 1 {
		t.Fatalf("stopped events=%d err=%v", events, err)
	}
}

func TestRecoveryRequeuesOrFailsInterrupted(t *testing.T) {
	a, _, user, project := testWorker(t)
	requeued, partial, exhausted := queued(t, a, user, project), queued(t, a, user, project), queued(t, a, user, project)
	for _, item := range []struct {
		id       pgtype.UUID
		attempts int
		partial  bool
	}{{requeued, 1, false}, {partial, 1, true}, {exhausted, 2, false}} {
		if _, err := a.pool.Exec(context.Background(), `UPDATE ai_turns SET status='running',claimed_by='dead',attempt_count=$2,lease_expires_at=now()-interval '1 second',output_started_at=CASE WHEN $3 THEN now() ELSE NULL END WHERE id=$1`, item.id, item.attempts, item.partial); err != nil {
			t.Fatal(err)
		}
		if item.partial {
			if _, err := a.pool.Exec(context.Background(), `UPDATE ai_messages SET status='partial',content='partial' WHERE turn_id=$1 AND role='assistant'`, item.id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := a.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id, status, messageStatus string
		failed                    int
	}{{requeued.String(), "queued", "pending", 0}, {partial.String(), "failed", "partial", 1}, {exhausted.String(), "failed", "failed", 1}} {
		var status, messageStatus string
		var failed int
		if err := a.pool.QueryRow(context.Background(), `SELECT t.status,m.status,(SELECT count(*) FROM ai_turn_events WHERE turn_id=t.id AND event_type='failed' AND payload->>'error_code'='worker_interrupted') FROM ai_turns t JOIN ai_messages m ON m.turn_id=t.id AND m.role='assistant' WHERE t.id=$1`, item.id).Scan(&status, &messageStatus, &failed); err != nil {
			t.Fatal(err)
		}
		if status != item.status || messageStatus != item.messageStatus || failed != item.failed {
			t.Fatalf("turn=%s status=%q message=%q events=%d", item.id, status, messageStatus, failed)
		}
	}
}

func TestStaleWorkerCannotFlushOrFinalize(t *testing.T) {
	a, _, user, project := testWorker(t)
	id := queued(t, a, user, project)
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.pool.Exec(context.Background(), `UPDATE ai_turns SET claimed_by='live',lease_expires_at=now()+interval '1 minute' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := a.flush(context.Background(), claimed, "nope"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("flush error=%v", err)
	}
	if err := a.finalize(context.Background(), claimed, "failed", "worker_interrupted", "failed", ai.Usage{}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("finalize error=%v", err)
	}
	var status, owner, messageStatus string
	var events int
	if err := a.pool.QueryRow(context.Background(), `SELECT t.status,t.claimed_by,m.status,(SELECT count(*) FROM ai_turn_events WHERE turn_id=t.id) FROM ai_turns t JOIN ai_messages m ON m.turn_id=t.id AND m.role='assistant' WHERE t.id=$1`, id).Scan(&status, &owner, &messageStatus, &events); err != nil {
		t.Fatal(err)
	}
	if status != "running" || owner != "live" || messageStatus != "pending" || events != 0 {
		t.Fatalf("status=%q owner=%q message=%q events=%d", status, owner, messageStatus, events)
	}
}

// roundProvider serves one provider round per Stream call from a script.
type roundProvider struct {
	requests []ai.Request
	rounds   [][]ai.Event
	errs     []error
}

func (p *roundProvider) Stream(_ context.Context, request ai.Request, emit func(ai.Event) error) error {
	p.requests = append(p.requests, request)
	if len(p.rounds) > 0 {
		events := p.rounds[0]
		p.rounds = p.rounds[1:]
		for _, event := range events {
			if err := emit(event); err != nil {
				return err
			}
		}
	}
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		return err
	}
	return nil
}

func toolCallEvent(id string) ai.Event {
	return ai.Event{ToolCall: &ai.ToolCall{ID: id, Name: "read_issues", Args: `{"limit": 5}`}}
}

func TestToolRoundPersistsCallsAndFinalAnswer(t *testing.T) {
	a, _, user, project := testWorker(t)
	provider := &roundProvider{rounds: [][]ai.Event{
		{toolCallEvent("call-1")},
		{{Text: "answer"}},
	}}
	a.provider = provider
	id := queued(t, a, user, project)
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), claimed)

	var status, content string
	if err := a.pool.QueryRow(context.Background(), `SELECT t.status, m.content FROM ai_turns t JOIN ai_messages m ON m.turn_id = t.id AND m.role = 'assistant' WHERE t.id = $1`, id).Scan(&status, &content); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || content != "answer" {
		t.Fatalf("turn=%q content=%q", status, content)
	}

	var calls int
	var name, callStatus, summary string
	if err := a.pool.QueryRow(context.Background(), `SELECT count(*), MIN(name), MIN(status), MIN(summary) FROM ai_tool_calls WHERE turn_id = $1`, id).Scan(&calls, &name, &callStatus, &summary); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || name != "read_issues" || callStatus != "completed" || summary == "" {
		t.Fatalf("tool rows=%d name=%q status=%q summary=%q", calls, name, callStatus, summary)
	}

	for _, want := range []struct {
		eventType string
		payload   string
	}{
		{"phase", `{"phase":"working"}`},
		{"tool_call", `{"id":"call-1","name":"read_issues","args":{"limit": 5}}`},
		{"tool_result", `{"id":"call-1","name":"read_issues","summary":"0 issues shown (0 matching total)"}`},
	} {
		var events int
		if err := a.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type = $2 AND payload = $3::jsonb`, id, want.eventType, want.payload).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if events != 1 {
			t.Fatalf("event %s with payload %s count=%d", want.eventType, want.payload, events)
		}
	}

	if len(provider.requests) != 2 {
		t.Fatalf("streams=%d, want 2", len(provider.requests))
	}
	if len(provider.requests[0].Tools) != 1 || provider.requests[0].Tools[0].Name != "read_issues" {
		t.Fatalf("round 1 tools = %+v", provider.requests[0].Tools)
	}
	foundCall, foundResult := false, false
	for _, message := range provider.requests[1].Messages {
		if message.Role == ai.RoleAssistant && len(message.ToolCalls) == 1 && message.ToolCalls[0].ID == "call-1" {
			foundCall = true
		}
		if message.Role == ai.RoleTool && message.ToolCallID == "call-1" && message.Name == "read_issues" && message.Content != "" {
			foundResult = true
		}
	}
	if !foundCall || !foundResult {
		t.Fatalf("round 2 messages = %+v", provider.requests[1].Messages)
	}
}

func TestDisabledToolsAreNotSentToProvider(t *testing.T) {
	a, _, user, project := testWorker(t)
	provider := &roundProvider{rounds: [][]ai.Event{{{Text: "plain"}}}}
	a.provider = provider
	id := queued(t, a, user, project)
	if _, err := a.pool.Exec(context.Background(), `UPDATE ai_turns SET disabled_ai_tools = '{read_issues}' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed.DisabledTools) != 1 || claimed.DisabledTools[0] != "read_issues" {
		t.Fatalf("claimed disabled tools = %v", claimed.DisabledTools)
	}
	a.run(context.Background(), claimed)

	if len(provider.requests) != 1 {
		t.Fatalf("streams=%d, want 1", len(provider.requests))
	}
	if len(provider.requests[0].Tools) != 0 {
		t.Fatalf("round tools = %+v, want none", provider.requests[0].Tools)
	}
	var status, content string
	if err := a.pool.QueryRow(context.Background(), `SELECT t.status, m.content FROM ai_turns t JOIN ai_messages m ON m.turn_id = t.id AND m.role = 'assistant' WHERE t.id = $1`, id).Scan(&status, &content); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || content != "plain" {
		t.Fatalf("turn=%q content=%q", status, content)
	}
}

func TestToolRoundCapSynthesizesFinalAnswer(t *testing.T) {
	a, _, user, project := testWorker(t)
	provider := &roundProvider{}
	for i := 0; i < maxAgentRounds; i++ {
		provider.rounds = append(provider.rounds, []ai.Event{toolCallEvent(fmt.Sprintf("call-%d", i))})
	}
	provider.rounds = append(provider.rounds, []ai.Event{{Text: "final answer"}})
	a.provider = provider
	id := queued(t, a, user, project)
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), claimed)

	if len(provider.requests) != maxAgentRounds+1 {
		t.Fatalf("streams=%d, want %d", len(provider.requests), maxAgentRounds+1)
	}
	for i := 0; i < maxAgentRounds; i++ {
		if len(provider.requests[i].Tools) != 1 {
			t.Fatalf("round %d tools = %+v, want read_issues", i, provider.requests[i].Tools)
		}
	}
	final := provider.requests[maxAgentRounds]
	if len(final.Tools) != 0 {
		t.Fatalf("synthesis round tools = %+v, want none", final.Tools)
	}
	found := false
	for _, message := range final.Messages {
		if message.Role == ai.RoleUser && strings.Contains(message.Content, "tool-call limit") {
			found = true
		}
	}
	if !found {
		t.Fatalf("synthesis instruction missing from final messages: %+v", final.Messages)
	}
	var status, content string
	var calls int
	if err := a.pool.QueryRow(context.Background(), `SELECT t.status, m.content, (SELECT count(*) FROM ai_tool_calls WHERE turn_id = t.id) FROM ai_turns t JOIN ai_messages m ON m.turn_id = t.id AND m.role = 'assistant' WHERE t.id = $1`, id).Scan(&status, &content, &calls); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || content != "final answer" || calls != maxAgentRounds {
		t.Fatalf("turn=%q content=%q calls=%d", status, content, calls)
	}
}

func TestCancelRequestedBeforeToolExecutionStopsTurn(t *testing.T) {
	a, _, user, project := testWorker(t)
	a.provider = &roundProvider{rounds: [][]ai.Event{{toolCallEvent("call-1")}}}
	a.heartbeat = time.Hour
	a.flushInterval = time.Hour
	id := queued(t, a, user, project)
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.pool.Exec(context.Background(), `UPDATE ai_turns SET cancel_requested_at = now() WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), claimed)

	var status, code, messageStatus string
	if err := a.pool.QueryRow(context.Background(), `SELECT t.status, COALESCE(t.error_code, ''), m.status FROM ai_turns t JOIN ai_messages m ON m.turn_id = t.id AND m.role = 'assistant' WHERE t.id = $1`, id).Scan(&status, &code, &messageStatus); err != nil {
		t.Fatal(err)
	}
	if status != "stopped" || code != "cancelled" || messageStatus != "partial" {
		t.Fatalf("turn=%q/%q message=%q", status, code, messageStatus)
	}
	var calls, toolEvents int
	if err := a.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM ai_tool_calls WHERE turn_id = $1), (SELECT count(*) FROM ai_turn_events WHERE turn_id = $1 AND event_type IN ('tool_call', 'tool_result', 'phase'))`, id).Scan(&calls, &toolEvents); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || toolEvents != 0 {
		t.Fatalf("calls=%d toolEvents=%d, want none", calls, toolEvents)
	}
}

func TestProviderFailureAfterToolRoundDoesNotRetry(t *testing.T) {
	a, _, user, project := testWorker(t)
	provider := &roundProvider{
		rounds: [][]ai.Event{{toolCallEvent("call-1")}},
		errs:   []error{nil, &ai.ProviderError{Code: "provider_timeout", Temporary: true}},
	}
	a.provider = provider
	id := queued(t, a, user, project)
	claimed, err := a.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.run(context.Background(), claimed)

	var status, code string
	if err := a.pool.QueryRow(context.Background(), `SELECT status, COALESCE(error_code, '') FROM ai_turns WHERE id = $1`, id).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || code != "provider_timeout" {
		t.Fatalf("turn=%q/%q, want failed/provider_timeout (no retry after tools)", status, code)
	}
}
