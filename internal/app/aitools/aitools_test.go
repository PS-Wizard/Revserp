package aitools

import (
	"strings"
	"testing"
)

func TestNewRegistry_HasAllToolsWithNoTenantParams(t *testing.T) {
	registry := NewRegistry(Deps{})
	defs := registry.Defs()
	if len(defs) != 17 {
		t.Fatalf("expected 17 tools, got %d", len(defs))
	}

	forbidden := []string{"project_id", "crawl_id", "org_id", "user_id", "source"}
	for _, def := range defs {
		if _, ok := registry.Get(def.Name); !ok {
			t.Errorf("registry.Get(%q) should find the tool", def.Name)
		}
		for _, param := range forbidden {
			if strings.Contains(string(def.Schema), param) {
				t.Errorf("tool %q schema must not expose tenant-scope parameter %q: %s", def.Name, param, def.Schema)
			}
		}
	}

	expected := []string{
		"list_projects", "switch_project", "get_business_profile", "update_business_profile",
		"get_score_summary", "list_issues", "get_recommended_fix", "get_page_content", "list_pages",
		"get_search_console_data",
		"start_crawl", "configure_auto_crawl", "export_crawl", "export_audit", "navigate",
		"compare_projects", "render_chart",
	}
	for _, name := range expected {
		if _, ok := registry.Get(name); !ok {
			t.Errorf("registry.Get(%q) should find tool", name)
		}
	}

	// open_url and set_panel were dropped from the tool set.
	for _, name := range []string{"open_url", "set_panel"} {
		if _, ok := registry.Get(name); ok {
			t.Errorf("registry.Get(%q) should not find a dropped tool", name)
		}
	}

	if _, ok := registry.Get("does_not_exist"); ok {
		t.Error("registry.Get should not find an unregistered tool")
	}
}
