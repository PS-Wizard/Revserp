package app

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestDeleteLocationWithAIAuditHistoryReturnsConflict(t *testing.T) {
	fx := newLocationAuditFixture(t)
	locationID := fx.locationID
	var auditID pgtype.UUID
	if err := fx.pool.QueryRow(fx.ctx, `INSERT INTO ai_audits(project_id,location_id,status)
		VALUES($1,$2,'completed') RETURNING id`, fx.projectID, locationID).Scan(&auditID); err != nil {
		t.Fatal(err)
	}
	response := callDeleteProjectLocation(t, fx.app, fx.ownerID, fx.projectID.String(), locationID.String())
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "AI audit history") {
		t.Fatalf("delete returned %d: %s", response.Code, response.Body.String())
	}
	var count int
	if err := fx.pool.QueryRow(fx.ctx, `SELECT count(*) FROM ai_audits WHERE id=$1 AND location_id=$2`, auditID, locationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("location deletion changed AI audit history")
	}
}
