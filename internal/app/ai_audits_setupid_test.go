package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func setupMarkerScope(t *testing.T, fixture sessionFixture) (pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	var orgID pgtype.UUID
	if err := fixture.pool.QueryRow(fixture.ctx,
		`INSERT INTO organizations (name) VALUES ('setup-marker-org') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	t.Cleanup(func() {
		_, _ = fixture.pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID)
	})
	if _, err := fixture.pool.Exec(fixture.ctx,
		`INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, fixture.userID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	var projectID pgtype.UUID
	if err := fixture.pool.QueryRow(fixture.ctx,
		`INSERT INTO projects (organization_id, name, base_url) VALUES ($1, 'setup-marker', 'https://93.184.216.34') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	var crawlID pgtype.UUID
	if err := fixture.pool.QueryRow(fixture.ctx,
		`INSERT INTO crawls (project_id, status) VALUES ($1, 'completed') RETURNING id`, projectID).Scan(&crawlID); err != nil {
		t.Fatalf("create crawl: %v", err)
	}
	return orgID, projectID, crawlID
}

func setupMarkerSetupRow(t *testing.T, fixture sessionFixture, orgID, projectID, crawlID pgtype.UUID) pgtype.UUID {
	t.Helper()
	var setupID pgtype.UUID
	if err := fixture.pool.QueryRow(fixture.ctx,
		`INSERT INTO project_setup (organization_id, project_id, crawl_id, status) VALUES ($1, $2, $3, 'visibility') RETURNING id`,
		orgID, projectID, crawlID).Scan(&setupID); err != nil {
		t.Fatalf("create setup: %v", err)
	}
	return setupID
}

func getAuditPath(t *testing.T, fixture sessionFixture, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(&http.Cookie{Name: "revserp_session", Value: fixture.rawCookie})
	response := httptest.NewRecorder()
	fixture.app.Router().ServeHTTP(response, request)
	return response
}

func TestManualAuditMarkerNilAndOmitted(t *testing.T) {
	fixture := newSessionFixture(t)
	_, projectID, crawlID := setupMarkerScope(t, fixture)

	audit, err := fixture.app.Queries.CreateAIAudit(fixture.ctx, sqlc.CreateAIAuditParams{
		ProjectID: projectID,
		CrawlID:   crawlID,
		Status:    "queued",
	})
	if err != nil {
		t.Fatalf("create manual audit: %v", err)
	}
	if audit.ProjectSetupID.Valid {
		t.Fatalf("manual audit marker = %s, want NULL", audit.ProjectSetupID.String())
	}
	body, err := json.Marshal(newAIAuditResponseFromCreateRow(audit))
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if strings.Contains(string(body), "project_setup_id") {
		t.Fatalf("manual audit response includes marker: %s", body)
	}
	var isNull bool
	if err := fixture.pool.QueryRow(fixture.ctx,
		`SELECT project_setup_id IS NULL FROM ai_audits WHERE id = $1`, audit.ID).Scan(&isNull); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if !isNull {
		t.Fatal("manual audit marker column is not NULL")
	}
}

func TestLocationAuditMarkerNil(t *testing.T) {
	fixture := newSessionFixture(t)
	_, projectID, crawlID := setupMarkerScope(t, fixture)
	var locationID pgtype.UUID
	if err := fixture.pool.QueryRow(fixture.ctx,
		`INSERT INTO project_locations (project_id, name, latitude, longitude) VALUES ($1, 'marker-loc', 27.7, 85.3) RETURNING id`, projectID).Scan(&locationID); err != nil {
		t.Fatalf("create location: %v", err)
	}

	audit, err := fixture.app.Queries.CreateAIAudit(fixture.ctx, sqlc.CreateAIAuditParams{
		ProjectID:  projectID,
		CrawlID:    crawlID,
		LocationID: locationID,
		Status:     "queued",
	})
	if err != nil {
		t.Fatalf("create location audit: %v", err)
	}
	if audit.ProjectSetupID.Valid {
		t.Fatalf("location audit marker = %s, want NULL", audit.ProjectSetupID.String())
	}
}

func TestSetupAuditMarkerRoundTripAndAuthz(t *testing.T) {
	fixture := newSessionFixture(t)
	orgID, projectID, crawlID := setupMarkerScope(t, fixture)
	setupID := setupMarkerSetupRow(t, fixture, orgID, projectID, crawlID)

	audit, err := fixture.app.Queries.CreateAIAudit(fixture.ctx, sqlc.CreateAIAuditParams{
		ProjectID:      projectID,
		CrawlID:        crawlID,
		Status:         "queued",
		ProjectSetupID: setupID,
	})
	if err != nil {
		t.Fatalf("create setup audit: %v", err)
	}

	fetched := getAuditPath(t, fixture, "/ai-audits/"+audit.ID.String())
	if fetched.Code != http.StatusOK {
		t.Fatalf("GET audit = %d (%s)", fetched.Code, fetched.Body.String())
	}
	var getBody struct {
		ProjectSetupID string `json:"project_setup_id"`
	}
	if err := json.Unmarshal(fetched.Body.Bytes(), &getBody); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if getBody.ProjectSetupID != setupID.String() {
		t.Fatalf("GET marker = %q, want %s", getBody.ProjectSetupID, setupID.String())
	}

	listed := getAuditPath(t, fixture, fmt.Sprintf("/projects/%s/ai-audits", projectID.String()))
	if listed.Code != http.StatusOK {
		t.Fatalf("list audits = %d (%s)", listed.Code, listed.Body.String())
	}
	var listBody struct {
		Audits []struct {
			ID             string `json:"id"`
			ProjectSetupID string `json:"project_setup_id"`
		} `json:"ai_audits"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, item := range listBody.Audits {
		if item.ID == audit.ID.String() {
			found = true
			if item.ProjectSetupID != setupID.String() {
				t.Fatalf("list marker = %q, want %s", item.ProjectSetupID, setupID.String())
			}
		}
	}
	if !found {
		t.Fatal("setup audit missing from list")
	}

	outsider := newSessionFixture(t)
	if response := getAuditPath(t, outsider, "/ai-audits/"+audit.ID.String()); response.Code != http.StatusNotFound {
		t.Fatalf("outsider GET = %d, want 404", response.Code)
	}
	if response := getAuditPath(t, outsider, fmt.Sprintf("/projects/%s/ai-audits", projectID.String())); response.Code != http.StatusNotFound {
		t.Fatalf("outsider list = %d, want 404", response.Code)
	}
}

func TestTriggerPayloadCarriesMarker(t *testing.T) {
	fixture := newSessionFixture(t)
	orgID, projectID, crawlID := setupMarkerScope(t, fixture)
	setupID := setupMarkerSetupRow(t, fixture, orgID, projectID, crawlID)

	audit, err := fixture.app.Queries.CreateAIAudit(fixture.ctx, sqlc.CreateAIAuditParams{
		ProjectID:      projectID,
		CrawlID:        crawlID,
		Status:         "queued",
		ProjectSetupID: setupID,
	})
	if err != nil {
		t.Fatalf("create setup audit: %v", err)
	}
	payloadMarker := func(eventType string, auditID pgtype.UUID) *string {
		t.Helper()
		var marker *string
		if err := fixture.pool.QueryRow(fixture.ctx,
			`SELECT payload->>'project_setup_id' FROM organization_events
			 WHERE resource_id = $1 AND event_type = $2 ORDER BY id DESC LIMIT 1`, auditID, eventType).Scan(&marker); err != nil {
			t.Fatalf("read %s payload: %v", eventType, err)
		}
		return marker
	}
	if marker := payloadMarker("ai_audit.queued", audit.ID); marker == nil || *marker != setupID.String() {
		t.Fatalf("queued payload marker = %v, want %s", marker, setupID.String())
	}

	if err := fixture.app.Queries.UpdateAIAuditStatus(fixture.ctx, sqlc.UpdateAIAuditStatusParams{
		ID: audit.ID, Status: "running",
	}); err != nil {
		t.Fatalf("start audit: %v", err)
	}
	if marker := payloadMarker("ai_audit.started", audit.ID); marker == nil || *marker != setupID.String() {
		t.Fatalf("started payload marker = %v, want %s", marker, setupID.String())
	}
	if _, err := fixture.pool.Exec(fixture.ctx,
		`UPDATE ai_audits SET status = 'completed' WHERE id = $1`, audit.ID); err != nil {
		t.Fatalf("complete setup audit: %v", err)
	}

	manual, err := fixture.app.Queries.CreateAIAudit(fixture.ctx, sqlc.CreateAIAuditParams{
		ProjectID: projectID, CrawlID: crawlID, Status: "queued",
	})
	if err != nil {
		t.Fatalf("create manual audit: %v", err)
	}
	if marker := payloadMarker("ai_audit.queued", manual.ID); marker != nil {
		t.Fatalf("manual queued payload marker = %q, want null", *marker)
	}
}
