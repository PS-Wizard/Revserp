package app

// Permanent location deletion tests: the owner-only trash path removes one
// location and its location-only terminal Maps, AI audit, Revbot chat and
// settings history while the parent project, sibling locations, parent
// history and shared Google auth survive. Active Maps runs, unconfirmed
// spend and live AI/chat worker state block with 409 and roll back. Every
// DB test uses the isolated fixture database, never paid calls or real data.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// deleteLocationLeftovers removes NO ACTION children (chat, audits, jobs)
// that would otherwise block the fixture project/org cascade teardown.
func deleteLocationLeftovers(ctx context.Context, pool *pgxpool.Pool, projectID pgtype.UUID) {
	_, _ = pool.Exec(ctx, `DELETE FROM ai_conversations WHERE project_id=$1`, projectID)
	_, _ = pool.Exec(ctx, `DELETE FROM ai_audits WHERE project_id=$1`, projectID)
	_, _ = pool.Exec(ctx, `DELETE FROM ai_worker_jobs WHERE project_id=$1`, projectID)
}

func mustDeleteLocationFixture(t *testing.T, fx localVisibilityFixture) {
	t.Helper()
	t.Cleanup(func() { deleteLocationLeftovers(context.Background(), fx.pool, fx.projectID) })
}

func seedTerminalLocationHistory(t *testing.T, fx localVisibilityFixture, projectID pgtype.UUID, locationID string, userID pgtype.UUID, tag string) {
	t.Helper()
	ctx := fx.ctx
	var loc pgtype.UUID
	if err := fx.pool.QueryRow(ctx, `SELECT id FROM project_locations WHERE id=$1`, locationID).Scan(&loc); err != nil {
		t.Fatalf("location %s missing: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO local_visibility_runs(location_id,status,radius_m,snapshot,expected_credits,reserved_credits,credits_used)
		VALUES($1,'completed',5000,'{}'::jsonb,1,0,1)`, loc); err != nil {
		t.Fatalf("seed %s run: %v", tag, err)
	}
	var runID pgtype.UUID
	if err := fx.pool.QueryRow(ctx, `SELECT id FROM local_visibility_runs WHERE location_id=$1`, loc).Scan(&runID); err != nil {
		t.Fatalf("read %s run: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO local_run_cells(run_id,query_index,point_index) VALUES($1,0,0)`, runID); err != nil {
		t.Fatalf("seed %s cell: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO local_visibility_results(run_id,query_index,point_index,call_status,match_status,rank,credits,credit_known)
		VALUES($1,0,0,'success_nonempty','found',1,1,TRUE)`, runID); err != nil {
		t.Fatalf("seed %s result: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO local_listing_lookups(location_id,query,status,reserved_credits,credits_used,credit_known,raw_response)
		VALUES($1,$2,'completed',0,1,TRUE,'{"places":[]}'::jsonb)`, loc, tag); err != nil {
		t.Fatalf("seed %s lookup: %v", tag, err)
	}
	var auditID pgtype.UUID
	if err := fx.pool.QueryRow(ctx, `INSERT INTO ai_audits(project_id,location_id,status) VALUES($1,$2,'completed') RETURNING id`,
		projectID, loc).Scan(&auditID); err != nil {
		t.Fatalf("seed %s audit: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO ai_audit_runs(audit_id,question_text,display_order,model_name,status)
		VALUES($1,'q',1,'m','success')`, auditID); err != nil {
		t.Fatalf("seed %s audit run: %v", tag, err)
	}
	var conversationID pgtype.UUID
	if err := fx.pool.QueryRow(ctx, `INSERT INTO ai_conversations(project_id,created_by_user_id,title,location_id)
		VALUES($1,$2,$3,$4) RETURNING id`, projectID, userID, tag, loc).Scan(&conversationID); err != nil {
		t.Fatalf("seed %s conversation: %v", tag, err)
	}
	var turnID pgtype.UUID
	if err := fx.pool.QueryRow(ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash)
		VALUES($1,$2,'completed','none','none','m','v',$3,decode(repeat('ab',32),'hex')) RETURNING id`,
		conversationID, userID, tag).Scan(&turnID); err != nil {
		t.Fatalf("seed %s turn: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO ai_messages(turn_id,role,status,content) VALUES($1,'user','complete','hi'),($1,'assistant','complete','hello')`, turnID); err != nil {
		t.Fatalf("seed %s messages: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO ai_worker_jobs(job_type,project_id,location_id,status) VALUES('prompt_generation',$1,$2,'completed')`, projectID, loc); err != nil {
		t.Fatalf("seed %s job: %v", tag, err)
	}
	var serviceID pgtype.UUID
	if err := fx.pool.QueryRow(ctx, `INSERT INTO project_services(project_id,label,normalized_label) VALUES($1,$2,$3) RETURNING id`,
		projectID, tag, tag).Scan(&serviceID); err != nil {
		t.Fatalf("seed %s service: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO project_location_services(location_id,project_id,service_id,mode) VALUES($1,$2,$3,'include')`, loc, projectID, serviceID); err != nil {
		t.Fatalf("seed %s service override: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO location_landmarks(location_id,name,latitude,longitude,straight_line_m,provider,provider_ref,categories,fetched_at)
		VALUES($1,$2,27.7,85.3,100,'test',$3,'{}',now())`, loc, tag, tag); err != nil {
		t.Fatalf("seed %s landmark: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO project_location_queries(location_id,text,normalized,ordinal,enabled,kind,source,origin)
		VALUES($1,$2,$3,0,TRUE,'map','manual','locality')`, loc, tag, tag); err != nil {
		t.Fatalf("seed %s query: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO location_keywords(location_id,keyword,normalized_keyword,kind,source) VALUES($1,$2,$3,'brand','user')`, loc, tag, tag); err != nil {
		t.Fatalf("seed %s keyword: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO location_ai_questions(project_id,location_id) VALUES($1,$2)`, projectID, loc); err != nil {
		t.Fatalf("seed %s ai questions: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO location_business_profiles(project_id,location_id,brand_name,website_url) VALUES($1,$2,$3,'https://example.com')`, projectID, loc, tag); err != nil {
		t.Fatalf("seed %s business profile: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO location_website_scopes(project_id,location_id,revision,url,match) VALUES($1,$2,1,'https://example.com/branch','exact')`, projectID, loc); err != nil {
		t.Fatalf("seed %s website scope: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO location_gsc_connections(location_id) VALUES($1)`, loc); err != nil {
		t.Fatalf("seed %s gsc binding: %v", tag, err)
	}
	if _, err := fx.pool.Exec(ctx, `INSERT INTO location_google_analytics_connections(location_id) VALUES($1)`, loc); err != nil {
		t.Fatalf("seed %s analytics binding: %v", tag, err)
	}
}

// countLocationChildren counts every location-scoped child table for one location.
func countLocationChildren(t *testing.T, fx localVisibilityFixture, projectID pgtype.UUID, locationID string) map[string]int {
	t.Helper()
	tables := []string{
		"local_visibility_runs", "local_listing_lookups", "ai_audits",
		"ai_conversations", "ai_worker_jobs", "project_location_services",
		"location_landmarks", "project_location_queries", "location_keywords",
		"location_ai_questions", "location_business_profiles",
		"location_website_scopes", "location_gsc_connections",
		"location_google_analytics_connections",
	}
	counts := map[string]int{}
	for _, table := range tables {
		column := "location_id"
		if table == "ai_audits" || table == "ai_conversations" || table == "location_ai_questions" || table == "location_business_profiles" {
			// These scope by both project and location.
			var count int
			if err := fx.pool.QueryRow(fx.ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE project_id=$1 AND location_id=$2`, table), projectID, locationID).Scan(&count); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			counts[table] = count
			continue
		}
		if table == "ai_worker_jobs" {
			var count int
			if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM ai_worker_jobs WHERE location_id=$1`, locationID).Scan(&count); err != nil {
				t.Fatalf("count ai_worker_jobs: %v", err)
			}
			counts[table] = count
			continue
		}
		var count int
		if err := fx.pool.QueryRow(fx.ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s=$1`, table, column), locationID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}

func TestDeleteLocationOwnerMemberOutsiderWrongProject(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	mustDeleteLocationFixture(t, fx)
	location := createUnboundLocation(t, fx, "auth-matrix")

	if rr := callDeleteProjectLocation(t, fx.app, fx.memberID, fx.projectID.String(), location.ID); rr.Code != http.StatusForbidden {
		t.Fatalf("member delete status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	if rr := callDeleteProjectLocation(t, fx.app, fx.outsider, fx.projectID.String(), location.ID); rr.Code != http.StatusNotFound {
		t.Fatalf("outsider delete status = %d, want 404", rr.Code)
	}
	var otherProject pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'delete-auth-other','https://other.example') RETURNING id`, fx.orgID).Scan(&otherProject); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	t.Cleanup(func() { _, _ = fx.pool.Exec(context.Background(), `DELETE FROM projects WHERE id=$1`, otherProject) })
	if rr := callDeleteProjectLocation(t, fx.app, fx.ownerID, otherProject.String(), location.ID); rr.Code != http.StatusNotFound {
		t.Fatalf("wrong project delete status = %d, want 404", rr.Code)
	}

	// None of the unauthorized attempts removed the location.
	var locations int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM project_locations WHERE id=$1`, location.ID).Scan(&locations); err != nil || locations != 1 {
		t.Fatalf("location retained = %d, err = %v; want 1", locations, err)
	}
	if rr := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusNoContent {
		t.Fatalf("owner delete status = %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusNotFound {
		t.Fatalf("repeat delete status = %d, want 404", rr.Code)
	}
}

func TestDeleteLocationRemovesOnlyItsTerminalHistory(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	mustDeleteLocationFixture(t, fx)
	fx.fundLocalVisibilityBudgets(t)
	target := createUnboundLocation(t, fx, "delete-target")
	sibling := createUnboundLocation(t, fx, "delete-sibling")
	seedTerminalLocationHistory(t, fx, fx.projectID, target.ID, fx.ownerID, "target")
	seedTerminalLocationHistory(t, fx, fx.projectID, sibling.ID, fx.ownerID, "sibling")

	// Parent-level history and a shared Google account must survive.
	var parentAuditID, parentConversationID, sharedAccountID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO ai_audits(project_id,status) VALUES($1,'completed') RETURNING id`, fx.projectID).Scan(&parentAuditID); err != nil {
		t.Fatalf("seed parent audit: %v", err)
	}
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO ai_conversations(project_id,created_by_user_id,title) VALUES($1,$2,'parent chat') RETURNING id`,
		fx.projectID, fx.ownerID).Scan(&parentConversationID); err != nil {
		t.Fatalf("seed parent conversation: %v", err)
	}
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO google_connections(organization_id,connected_by_user_id,google_account_subject,encrypted_refresh_token,scope)
		VALUES($1,$2,'delete-target-subject','x','s') RETURNING id`, fx.orgID, fx.ownerID).Scan(&sharedAccountID); err != nil {
		t.Fatalf("seed shared google account: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(context.Background(), `DELETE FROM google_connections WHERE id=$1`, sharedAccountID)
	})

	beforePlatform := fx.platformBudget(t)
	beforeOrg := fx.orgBudget(t)
	for table, count := range countLocationChildren(t, fx, fx.projectID, target.ID) {
		if count == 0 {
			t.Fatalf("target child %s = 0, want seeded rows", table)
		}
	}
	siblingBefore := countLocationChildren(t, fx, fx.projectID, sibling.ID)

	if rr := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), target.ID); rr.Code != http.StatusNoContent {
		t.Fatalf("owner delete status = %d body=%s", rr.Code, rr.Body.String())
	}
	for table, count := range countLocationChildren(t, fx, fx.projectID, target.ID) {
		if count != 0 {
			t.Fatalf("target child %s = %d, want 0", table, count)
		}
	}
	var targetTurns, targetMessages, targetAuditRuns int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM ai_turns WHERE conversation_id NOT IN (SELECT id FROM ai_conversations)`).Scan(&targetTurns); err != nil {
		t.Fatalf("count orphan turns: %v", err)
	}
	if targetTurns != 0 {
		t.Fatalf("orphan turns = %d, want 0", targetTurns)
	}
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM ai_messages WHERE turn_id NOT IN (SELECT id FROM ai_turns)`).Scan(&targetMessages); err != nil {
		t.Fatalf("count orphan messages: %v", err)
	}
	if targetMessages != 0 {
		t.Fatalf("orphan messages = %d, want 0", targetMessages)
	}
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM ai_audit_runs WHERE audit_id NOT IN (SELECT id FROM ai_audits)`).Scan(&targetAuditRuns); err != nil {
		t.Fatalf("count orphan audit runs: %v", err)
	}
	if targetAuditRuns != 0 {
		t.Fatalf("orphan audit runs = %d, want 0", targetAuditRuns)
	}

	// Sibling history, parent history, shared auth and settled spend ledgers survive.
	for table, count := range countLocationChildren(t, fx, fx.projectID, sibling.ID) {
		if count != siblingBefore[table] {
			t.Fatalf("sibling child %s = %d, want %d", table, count, siblingBefore[table])
		}
	}
	for _, check := range []struct {
		name  string
		query string
		args  []any
	}{
		{"sibling location", `SELECT count(*) FROM project_locations WHERE id=$1`, []any{sibling.ID}},
		{"parent audit", `SELECT count(*) FROM ai_audits WHERE id=$1 AND location_id IS NULL`, []any{parentAuditID}},
		{"parent conversation", `SELECT count(*) FROM ai_conversations WHERE id=$1 AND location_id IS NULL`, []any{parentConversationID}},
		{"shared google account", `SELECT count(*) FROM google_connections WHERE id=$1`, []any{sharedAccountID}},
		{"parent project", `SELECT count(*) FROM projects WHERE id=$1`, []any{fx.projectID}},
	} {
		var count int
		if err := fx.pool.QueryRow(fx.ctx, check.query, check.args...).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s retained = %d, err = %v; want 1", check.name, count, err)
		}
	}
	if after := fx.platformBudget(t); after != beforePlatform {
		t.Fatalf("platform budget changed from %#v to %#v; deletion must not refund settled spend", beforePlatform, after)
	}
	if after := fx.orgBudget(t); after != beforeOrg {
		t.Fatalf("org budget changed from %#v to %#v; deletion must not refund settled spend", beforeOrg, after)
	}
}

func TestDeleteLocationBlockedByLiveWorkRollsBack(t *testing.T) {
	setupQueuedRun := func(t *testing.T, fx localVisibilityFixture, locationID string) {
		t.Helper()
		if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_visibility_runs(location_id,status,radius_m,snapshot,expected_credits,reserved_credits)
			VALUES($1,'queued',5000,'{}'::jsonb,2,2)`, locationID); err != nil {
			t.Fatalf("seed queued run: %v", err)
		}
	}
	setupRunningLookup := func(t *testing.T, fx localVisibilityFixture, locationID string) {
		t.Helper()
		if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_listing_lookups(location_id,query) VALUES($1,'live')`, locationID); err != nil {
			t.Fatalf("seed running lookup: %v", err)
		}
	}
	setupUnconfirmedLookup := func(t *testing.T, fx localVisibilityFixture, locationID string) {
		t.Helper()
		if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO local_listing_lookups(location_id,query,status,reserved_credits,credits_used,credit_known)
			VALUES($1,'held','uncertain',3,0,FALSE)`, locationID); err != nil {
			t.Fatalf("seed unconfirmed lookup: %v", err)
		}
	}
	setupQueuedAudit := func(t *testing.T, fx localVisibilityFixture, locationID string) {
		t.Helper()
		var loc pgtype.UUID
		if err := fx.pool.QueryRow(fx.ctx, `SELECT id FROM project_locations WHERE id=$1`, locationID).Scan(&loc); err != nil {
			t.Fatalf("read location: %v", err)
		}
		if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_audits(project_id,location_id,status) VALUES($1,$2,'queued')`, fx.projectID, loc); err != nil {
			t.Fatalf("seed queued audit: %v", err)
		}
	}
	setupPendingPromptJob := func(t *testing.T, fx localVisibilityFixture, locationID string) {
		t.Helper()
		var loc pgtype.UUID
		if err := fx.pool.QueryRow(fx.ctx, `SELECT id FROM project_locations WHERE id=$1`, locationID).Scan(&loc); err != nil {
			t.Fatalf("read location: %v", err)
		}
		if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_worker_jobs(job_type,project_id,location_id,status) VALUES('prompt_generation',$1,$2,'pending')`, fx.projectID, loc); err != nil {
			t.Fatalf("seed pending job: %v", err)
		}
	}
	setupRunningTurn := func(t *testing.T, fx localVisibilityFixture, locationID string) {
		t.Helper()
		var loc pgtype.UUID
		if err := fx.pool.QueryRow(fx.ctx, `SELECT id FROM project_locations WHERE id=$1`, locationID).Scan(&loc); err != nil {
			t.Fatalf("read location: %v", err)
		}
		var conversationID pgtype.UUID
		if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO ai_conversations(project_id,created_by_user_id,title,location_id)
			VALUES($1,$2,'live chat',$3) RETURNING id`, fx.projectID, fx.ownerID, loc).Scan(&conversationID); err != nil {
			t.Fatalf("seed live conversation: %v", err)
		}
		if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_turns(conversation_id,created_by_user_id,status,requested_effort,effective_effort,model,prompt_version,client_request_id,request_hash)
			VALUES($1,$2,'running','none','none','m','v','live',decode(repeat('cd',32),'hex'))`, conversationID, fx.ownerID); err != nil {
			t.Fatalf("seed running turn: %v", err)
		}
	}

	for _, tc := range []struct {
		name  string
		setup func(*testing.T, localVisibilityFixture, string)
	}{
		{"queued Maps run", setupQueuedRun},
		{"running listing lookup", setupRunningLookup},
		{"unconfirmed listing spend", setupUnconfirmedLookup},
		{"queued AI audit", setupQueuedAudit},
		{"pending AI prompt job", setupPendingPromptJob},
		{"running chat turn", setupRunningTurn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newLocalVisibilityFixture(t)
			mustDeleteLocationFixture(t, fx)
			location := createUnboundLocation(t, fx, "live-blocked")
			tc.setup(t, fx, location.ID)

			if rr := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), location.ID); rr.Code != http.StatusConflict {
				t.Fatalf("live delete status = %d, want 409; body=%s", rr.Code, rr.Body.String())
			}
			// The blocked delete rolls back: location and live state survive.
			var locations int
			if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM project_locations WHERE id=$1`, location.ID).Scan(&locations); err != nil || locations != 1 {
				t.Fatalf("location retained = %d, err = %v; want 1", locations, err)
			}
			var live int
			liveQuery := map[string]string{
				"queued Maps run":           `SELECT count(*) FROM local_visibility_runs WHERE location_id=$1 AND status='queued'`,
				"running listing lookup":    `SELECT count(*) FROM local_listing_lookups WHERE location_id=$1 AND status='running'`,
				"unconfirmed listing spend": `SELECT count(*) FROM local_listing_lookups WHERE location_id=$1 AND reserved_credits>0`,
				"queued AI audit":           `SELECT count(*) FROM ai_audits WHERE project_id=$1 AND location_id=$2 AND status='queued'`,
				"pending AI prompt job":     `SELECT count(*) FROM ai_worker_jobs WHERE project_id=$1 AND location_id=$2 AND status='pending'`,
				"running chat turn":         `SELECT count(*) FROM ai_turns WHERE status='running' AND conversation_id IN (SELECT id FROM ai_conversations WHERE project_id=$1 AND location_id=$2)`,
			}[tc.name]
			args := map[string][]any{
				"queued Maps run":           {location.ID},
				"running listing lookup":    {location.ID},
				"unconfirmed listing spend": {location.ID},
				"queued AI audit":           {fx.projectID, location.ID},
				"pending AI prompt job":     {fx.projectID, location.ID},
				"running chat turn":         {fx.projectID, location.ID},
			}[tc.name]
			if err := fx.pool.QueryRow(fx.ctx, liveQuery, args...).Scan(&live); err != nil || live != 1 {
				t.Fatalf("live state retained = %d, err = %v; want 1", live, err)
			}
		})
	}
}

func TestDeleteLocationConcurrentDeletesSerialize(t *testing.T) {
	fx := newLocalVisibilityFixture(t)
	mustDeleteLocationFixture(t, fx)
	location := createUnboundLocation(t, fx, "concurrent")

	// Requests are built up front: only the handler runs concurrently, so the
	// test never touches *testing.T from a worker goroutine.
	requests := []*http.Request{
		localVisibilityRequest(t, http.MethodDelete, fx.ownerID, map[string]string{"projectID": fx.projectID.String(), "locationID": location.ID}, ""),
		localVisibilityRequest(t, http.MethodDelete, fx.ownerID, map[string]string{"projectID": fx.projectID.String(), "locationID": location.ID}, ""),
	}

	var wg sync.WaitGroup
	codes := make([]int, 2)
	wg.Add(2)
	for i := range requests {
		go func(i int) {
			defer wg.Done()
			rr := httptest.NewRecorder()
			fx.app.handleDeleteLocationSetup(rr, requests[i])
			codes[i] = rr.Code
		}(i)
	}
	wg.Wait()
	var ok204, notFound404 int
	for _, code := range codes {
		switch code {
		case http.StatusNoContent:
			ok204++
		case http.StatusNotFound:
			notFound404++
		default:
			t.Fatalf("concurrent delete code = %d, want 204+404", code)
		}
	}
	if ok204 != 1 || notFound404 != 1 {
		t.Fatalf("concurrent deletes = %v, want one 204 and one 404", codes)
	}
	var locations int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM project_locations WHERE id=$1`, location.ID).Scan(&locations); err != nil || locations != 0 {
		t.Fatalf("location remaining = %d, err = %v; want 0", locations, err)
	}
}
