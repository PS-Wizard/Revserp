package aichatworker

import (
	"context"
	"errors"
	"fmt"
	"os"
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
