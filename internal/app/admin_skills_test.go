package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/aiskills"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func adminSkillsTestCatalog(t *testing.T, files map[string]string) *aiskills.Catalog {
	t.Helper()
	root := t.TempDir()
	for relPath, body := range files {
		full := filepath.Join(root, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return aiskills.New(root)
}

func adminSkillsPrincipal(admin bool) Principal {
	var uid pgtype.UUID
	_ = uid.Scan("00000000-0000-0000-0000-0000000000a1")
	return Principal{User: sqlc.User{ID: uid, Email: "admin@example.com", IsPlatformAdmin: admin}}
}

func TestHandleAdminListSkills(t *testing.T) {
	app := &App{Skills: adminSkillsTestCatalog(t, map[string]string{
		"seo/local-seo/SKILL.md":                "---\nname: local-seo\ndescription: Local SEO review.\n---\n\n# Local SEO review\n",
		"seo/local-seo/references/checklist.md": "# Checklist\n",
		"ads/SKILL.md":                          "---\nname: ads\ndescription: Ads.\n---\n\n# Ads\n",
	})}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/skills", nil)
	app.handleAdminListSkills(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	var response adminSkillsResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(response.Skills) != 2 || response.Skills[0].ID != "ads" || response.Skills[1].ID != "seo/local-seo" {
		t.Fatalf("skills = %+v, want sorted ads, seo/local-seo", response.Skills)
	}
	local := response.Skills[1]
	if local.Name != "local-seo" || local.Description != "Local SEO review." {
		t.Errorf("skill metadata = %+v", local)
	}
	if len(local.Files) != 2 || local.Files[0] != "SKILL.md" || local.Files[1] != "references/checklist.md" {
		t.Errorf("skill files = %v, want sorted relative paths", local.Files)
	}
	for _, file := range local.Files {
		if filepath.IsAbs(file) || strings.Contains(file, "\\") || strings.Contains(file, "..") {
			t.Errorf("file %q is not skill-local", file)
		}
	}
	if strings.Contains(recorder.Body.String(), "# Local SEO review") {
		t.Error("listing leaks the SKILL.md body")
	}
}

func TestHandleAdminListSkillsEmptyRootIsEmptyArray(t *testing.T) {
	for _, app := range []*App{{}, {Skills: aiskills.New(filepath.Join(t.TempDir(), "missing"))}} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/admin/skills", nil)
		app.handleAdminListSkills(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"skills":[]`) {
			t.Errorf("body %q must carry an explicit empty array, never null", recorder.Body.String())
		}
	}
}

func TestHandleAdminListSkillsCatalogErrorIs500(t *testing.T) {
	app := &App{Skills: adminSkillsTestCatalog(t, map[string]string{
		"bad/SKILL.md": "# No frontmatter\n",
	})}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/skills", nil)
	app.handleAdminListSkills(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "bad") {
		t.Errorf("diagnostic %q does not name the offending skill", recorder.Body.String())
	}
}

func TestAdminSkillsRouteRequiresPlatformAdmin(t *testing.T) {
	app := &App{Skills: adminSkillsTestCatalog(t, map[string]string{
		"ads/SKILL.md": "---\nname: ads\ndescription: Ads.\n---\n\n# Ads\n",
	})}
	handler := app.requirePlatformAdmin(http.HandlerFunc(app.handleAdminListSkills))
	admin := adminSkillsPrincipal(true)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/skills", nil)
	request = request.WithContext(withPrincipal(request.Context(), admin))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	nonAdmin := adminSkillsPrincipal(false)
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/admin/skills", nil)
	request = request.WithContext(withPrincipal(request.Context(), nonAdmin))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403; body = %s", recorder.Code, recorder.Body.String())
	}
}
