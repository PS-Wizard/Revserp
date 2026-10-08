package app

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// Permanent location deletion removes terminal location AI audit history
// with the location, while parent and sibling audits survive.
func TestDeleteLocationWithCompletedAIAuditHistoryDeletesLocationOnly(t *testing.T) {
	fx := newLocationAuditFixture(t)
	locationID := fx.locationID
	var auditID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO ai_audits(project_id,location_id,status)
		VALUES($1,$2,'completed') RETURNING id`, fx.projectID, locationID).Scan(&auditID); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(fx.ctx, `INSERT INTO ai_audit_runs(audit_id,question_text,display_order,model_name,status)
		VALUES($1,'q',1,'m','success')`, auditID); err != nil {
		t.Fatal(err)
	}
	var siblingAuditID, parentAuditID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO ai_audits(project_id,location_id,status)
		VALUES($1,$2,'completed') RETURNING id`, fx.projectID, fx.emptyLocationID).Scan(&siblingAuditID); err != nil {
		t.Fatal(err)
	}
	var crawlID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO crawls(project_id,status) VALUES($1,'completed') RETURNING id`, fx.projectID).Scan(&crawlID); err != nil {
		t.Fatal(err)
	}
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO ai_audits(project_id,crawl_id,status)
		VALUES($1,$2,'completed') RETURNING id`, fx.projectID, crawlID).Scan(&parentAuditID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = fx.pool.Exec(fx.ctx, `DELETE FROM ai_audits WHERE id=$1`, siblingAuditID)
		_, _ = fx.pool.Exec(fx.ctx, `DELETE FROM ai_audits WHERE id=$1`, parentAuditID)
	})

	response := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), locationID.String())
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete returned %d: %s", response.Code, response.Body.String())
	}
	var count int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM ai_audits WHERE id=$1`, auditID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("location deletion kept terminal location AI audit history")
	}
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM ai_audit_runs WHERE audit_id=$1`, auditID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("location deletion kept orphaned AI audit runs")
	}
	for name, id := range map[string]pgtype.UUID{"sibling": siblingAuditID, "parent": parentAuditID} {
		if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM ai_audits WHERE id=$1`, id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s audit retained = %d, err = %v; want 1", name, count, err)
		}
	}
}

// A queued or running location audit still blocks permanent deletion and
// rolls back, keeping both the audit and the location.
func TestDeleteLocationWithActiveAIAuditReturnsConflict(t *testing.T) {
	fx := newLocationAuditFixture(t)
	locationID := fx.emptyLocationID
	var auditID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO ai_audits(project_id,location_id,status)
		VALUES($1,$2,'running') RETURNING id`, fx.projectID, locationID).Scan(&auditID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = fx.pool.Exec(fx.ctx, `DELETE FROM ai_audits WHERE id=$1`, auditID) })

	response := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), locationID.String())
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "active AI audit") {
		t.Fatalf("delete returned %d: %s", response.Code, response.Body.String())
	}
	var count int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM ai_audits WHERE id=$1 AND location_id=$2`, auditID, locationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("blocked deletion changed live AI audit history")
	}
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM project_locations WHERE id=$1`, locationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("blocked deletion removed the location")
	}
}
