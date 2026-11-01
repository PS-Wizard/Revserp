package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func decodeMeUsage(t *testing.T, recBody []byte) meUsageResponse {
	t.Helper()
	var body meUsageResponse
	if err := json.Unmarshal(recBody, &body); err != nil {
		t.Fatalf("decode /me/usage: %v body=%s", err, recBody)
	}
	return body
}

func expectedUsagePeriod(t *testing.T) (string, string) {
	t.Helper()
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start.Format("2006-01-02"), start.AddDate(0, 1, 0).Format("2006-01-02")
}

// A fresh workspace has no usage row: used counters are zero, limits come
// from the default feature row, and projects.used tracks the project count.
func TestMeUsageFreshWorkspaceReturnsZeros(t *testing.T) {
	f := newSessionFixture(t)
	router := f.app.Router()

	rec := get(t, router, "/me/usage", f.rawCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /me/usage status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeMeUsage(t, rec.Body.Bytes())
	start, end := expectedUsagePeriod(t)

	if body.OrganizationID == "" {
		t.Error("organization_id is empty")
	}
	if body.PeriodStart != start || body.PeriodEnd != end {
		t.Errorf("period = %s/%s, want %s/%s", body.PeriodStart, body.PeriodEnd, start, end)
	}
	if body.AIMessages.Used != 0 || body.AIMessages.Limit != 50 {
		t.Errorf("ai_messages = %+v, want {used 0 limit 50}", body.AIMessages)
	}
	if body.AIVisibilityAudits.Used != 0 || body.AIVisibilityAudits.Limit != 10 {
		t.Errorf("ai_visibility_audits = %+v, want {used 0 limit 10}", body.AIVisibilityAudits)
	}
	if body.Projects.Used != 0 || body.Projects.Limit != 1 {
		t.Errorf("projects = %+v, want {used 0 limit 1}", body.Projects)
	}
	if body.MaxCompetitorsPerProject != 3 {
		t.Errorf("max_competitors_per_project = %d, want 3", body.MaxCompetitorsPerProject)
	}
	// New organizations are provisioned with a 500-credit maps allowance by
	// the organization_maps_allowance_on_create trigger (migration 000090).
	if body.MapsCredits != (mapsCreditUsage{Remaining: 500}) {
		t.Errorf("maps_credits = %+v, want {remaining 500 reserved 0 spent 0}", body.MapsCredits)
	}

	var orgID pgtype.UUID
	if err := orgID.Scan(body.OrganizationID); err != nil {
		t.Fatalf("scan organization_id: %v", err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID) })

	var projectID pgtype.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO projects (organization_id, name, base_url)
		VALUES ($1,'usage-p','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}

	rec = get(t, router, "/me/usage", f.rawCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /me/usage after project status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	body = decodeMeUsage(t, rec.Body.Bytes())
	if body.Projects.Used != 1 {
		t.Errorf("projects.used = %d, want 1", body.Projects.Used)
	}
	if body.AIMessages.Used != 0 || body.AIVisibilityAudits.Used != 0 {
		t.Errorf("usage should still be zero, got %+v / %+v", body.AIMessages, body.AIVisibilityAudits)
	}
}

// After a usage row is written for the current period, the used counters
// surface its values.
func TestMeUsageReflectsStoredMonthlyUsage(t *testing.T) {
	f := newSessionFixture(t)
	router := f.app.Router()

	rec := get(t, router, "/me/usage", f.rawCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /me/usage status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	orgID := decodeMeUsage(t, rec.Body.Bytes()).OrganizationID
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM ai_workspace_monthly_usage WHERE organization_id = $1::uuid`, orgID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1::uuid`, orgID)
	})

	if _, err := f.pool.Exec(f.ctx, `INSERT INTO ai_workspace_monthly_usage (organization_id, period_start, used_messages, used_audits)
		VALUES ($1::uuid, date_trunc('month', now() AT TIME ZONE 'UTC')::date, 12, 1)
		ON CONFLICT (organization_id, period_start)
		DO UPDATE SET used_messages = 12, used_audits = 1, updated_at = now()`, orgID); err != nil {
		t.Fatalf("insert usage row: %v", err)
	}

	rec = get(t, router, "/me/usage", f.rawCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /me/usage after usage insert status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeMeUsage(t, rec.Body.Bytes())
	if body.AIMessages.Used != 12 {
		t.Errorf("ai_messages.used = %d, want 12", body.AIMessages.Used)
	}
	if body.AIVisibilityAudits.Used != 1 {
		t.Errorf("ai_visibility_audits.used = %d, want 1", body.AIVisibilityAudits.Used)
	}
}

func TestMeUsageUnauthenticatedIsRejected(t *testing.T) {
	f := newSessionFixture(t)
	rec := get(t, f.app.Router(), "/me/usage", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /me/usage status = %d, want 401, body=%s", rec.Code, rec.Body.String())
	}
}
