package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/aichattools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestNormalizeDisabledAITools(t *testing.T) {
	tests := []struct {
		name         string
		tools        []string
		gscConnector bool
		want         []string
	}{
		{"nil", nil, true, []string{}},
		{"only empties", []string{"", ""}, true, []string{}},
		{"dupes and empties", []string{"read_issues", "", "read_issues"}, true, []string{"read_issues"}},
		{"unknown names dropped", []string{"bogus"}, true, []string{}},
		{"known and unknown", []string{"bogus", "read_issues"}, true, []string{"read_issues"}},
		{"gsc flag off force-disables gsc tool", []string{}, false, []string{"get_search_console_data"}},
		{"gsc flag off with other tools", []string{"read_issues"}, false, []string{"read_issues", "get_search_console_data"}},
		{"gsc flag on keeps user choice", []string{"get_search_console_data"}, true, []string{"get_search_console_data"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeDisabledAITools(test.tools, test.gscConnector)
			if !slices.Equal(got, test.want) {
				t.Errorf("normalizeDisabledAITools(%v, %v) = %v, want %v", test.tools, test.gscConnector, got, test.want)
			}
		})
	}
}

func TestValidateDisabledAITools(t *testing.T) {
	tests := []struct {
		name         string
		tools        []string
		gscConnector bool
		want         []string
		wantErr      string
	}{
		{"empty allowed", []string{}, true, []string{}, ""},
		{"known tool", []string{"read_issues"}, true, []string{"read_issues"}, ""},
		{"dupes normalized", []string{"read_issues", "read_issues"}, true, []string{"read_issues"}, ""},
		{"gsc flag off force-disables", []string{}, false, []string{"get_search_console_data"}, ""},
		{"unknown tool rejected", []string{"bogus"}, true, nil, `unknown ai tool "bogus"; valid tools: read_issues, get_score_summary, get_search_console_data, get_business_profile, read_issue_work, read_page, render_chart, update_business_profile`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := validateDisabledAITools(test.tools, test.gscConnector)
			if test.wantErr != "" {
				if err == nil || err.Error() != test.wantErr {
					t.Fatalf("validateDisabledAITools(%v) error = %v, want %q", test.tools, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateDisabledAITools(%v) error = %v", test.tools, err)
			}
			if !slices.Equal(got, test.want) {
				t.Errorf("validateDisabledAITools(%v) = %v, want %v", test.tools, got, test.want)
			}
		})
	}
}

// Validation happens before any database access, so this needs no DB.
func TestAdminPutFeaturesRejectsUnknownAITool(t *testing.T) {
	app := &App{}
	body := `{"workspaces":[{"org_id":"00000000-0000-0000-0000-000000000000","auto_crawl":true,"gsc_connector":true,"ai_chat":true,"ai_use_internal_prompt":false,"ai_monthly_message_limit":50,"ai_concurrent_turn_limit_per_user":2,"ai_allowed_reasoning_efforts":["none"],"disabled_ai_tools":["does_not_exist"]}]}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/admin/features", strings.NewReader(body))
	app.handleAdminPutFeatures(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if !strings.Contains(response.Error, `unknown ai tool "does_not_exist"`) {
		t.Errorf("error %q must name the offending tool", response.Error)
	}
	if !strings.Contains(response.Error, "read_issues") {
		t.Errorf("error %q must list the valid tools", response.Error)
	}
}

// adminFeaturesTestApp wires a real DB-backed app plus an org and an editor
// user for handler tests. Skipped when no test database is available.
func adminFeaturesTestApp(t *testing.T) (*App, *sqlc.Queries, context.Context, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	queries, pool, ctx := newFeaturesTestQueries(t)
	orgID := createFeaturesTestOrg(t, ctx, pool)

	name := fmt.Sprintf("admin-features-test-%d", time.Now().UnixNano())
	var userID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email)
		VALUES ('test', $1, $2) RETURNING id`, name, name+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})

	return &App{DB: pool, Queries: queries}, queries, ctx, orgID, userID
}

// adminFeaturesPutRequest builds a PUT request carrying the editor identity in
// the same way requireActiveUser caches it.
func adminFeaturesPutRequest(t *testing.T, userID pgtype.UUID, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/admin/features", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), cachedUserContextKey{}, sqlc.User{ID: userID}))
	return request
}

func findAdminWorkspace(t *testing.T, workspaces []adminWorkspaceFeaturesResponse, orgID string) adminWorkspaceFeaturesResponse {
	t.Helper()
	for _, workspace := range workspaces {
		if workspace.OrgID == orgID {
			return workspace
		}
	}
	t.Fatalf("workspace %s missing from the admin matrix", orgID)
	return adminWorkspaceFeaturesResponse{}
}

func TestAdminListFeaturesIncludesAIToolCatalog(t *testing.T) {
	app, _, _, orgID, _ := adminFeaturesTestApp(t)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/features", nil)
	app.handleAdminListFeatures(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	var response adminFeaturesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode list response: %v", err)
	}

	defs := aichattools.CatalogDefs()
	if len(response.AITools) != len(defs) {
		t.Fatalf("ai_tools has %d entries, want %d", len(response.AITools), len(defs))
	}
	for i, def := range defs {
		info := response.AITools[i]
		if info.Name != def.Name || info.Label != def.Label || info.Description != def.Description {
			t.Errorf("ai_tools[%d] = %+v, want def %+v", i, info, def)
		}
	}

	workspace := findAdminWorkspace(t, response.Workspaces, orgID.String())
	if len(workspace.DisabledAITools) != 0 {
		t.Errorf("unrestricted workspace disabled_ai_tools = %v, want empty", workspace.DisabledAITools)
	}
}

func TestAdminPutFeaturesRoundTripsDisabledAITools(t *testing.T) {
	app, queries, ctx, orgID, userID := adminFeaturesTestApp(t)

	body := fmt.Sprintf(`{"workspaces":[{"org_id":%q,"auto_crawl":true,"gsc_connector":true,"ai_chat":true,"ai_use_internal_prompt":false,"ai_monthly_message_limit":50,"ai_concurrent_turn_limit_per_user":2,"ai_allowed_reasoning_efforts":["none","low","high","max"],"disabled_ai_tools":["","read_issues","read_issues"]}]}`, orgID.String())
	recorder := httptest.NewRecorder()
	app.handleAdminPutFeatures(recorder, adminFeaturesPutRequest(t, userID, body))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	var response adminFeaturesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode put response: %v", err)
	}
	if len(response.AITools) == 0 {
		t.Error("put response must include the ai_tools catalog")
	}
	workspace := findAdminWorkspace(t, response.Workspaces, orgID.String())
	if !slices.Equal(workspace.DisabledAITools, []string{"read_issues"}) {
		t.Errorf("disabled_ai_tools = %v, want [read_issues]", workspace.DisabledAITools)
	}

	rows, err := queries.ListOrganizationFeaturesForAdmin(ctx)
	if err != nil {
		t.Fatalf("ListOrganizationFeaturesForAdmin: %v", err)
	}
	for _, row := range rows {
		if row.OrgID == orgID {
			if !slices.Equal(row.DisabledAiTools, []string{"read_issues"}) {
				t.Errorf("persisted disabled_ai_tools = %v, want [read_issues]", row.DisabledAiTools)
			}
			return
		}
	}
	t.Fatal("test workspace missing from the admin matrix")
}
