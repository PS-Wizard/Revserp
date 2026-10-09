package aichattools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/aiskills"
)

func writeSkillFixture(t *testing.T, root, relPath, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillCatalog(t *testing.T, files map[string]string) *aiskills.Catalog {
	t.Helper()
	root := t.TempDir()
	for relPath, body := range files {
		writeSkillFixture(t, root, relPath, body)
	}
	return aiskills.New(root)
}

func skillScope(catalog *aiskills.Catalog, budgetBytes int) Scope {
	return Scope{Skills: catalog, SkillsBudget: NewSkillBudget(budgetBytes)}
}

func TestSkillToolsRegisteredInBothCatalogs(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{listSkillsName, readSkillName} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("NewRegistry() is missing %q", name)
		}
	}
	seen := map[string]bool{}
	for _, def := range CatalogDefs() {
		seen[def.Name] = true
	}
	for _, name := range []string{listSkillsName, readSkillName} {
		if !seen[name] {
			t.Fatalf("CatalogDefs() is missing %q", name)
		}
	}
	if _, ok := NewFilteredRegistry([]string{listSkillsName, readSkillName}).Get(listSkillsName); ok {
		t.Fatal("denylisted list_skills is still served")
	}
	for _, name := range []string{listSkillsName, readSkillName} {
		if _, ok := NewLocationScopedRegistry(nil).Get(name); !ok {
			t.Fatalf("location-scoped registry dropped %q; skills read no project data", name)
		}
	}
}

func TestListSkillsEmptyRoot(t *testing.T) {
	result, err := executeListSkills(context.Background(), json.RawMessage(`{}`), skillScope(aiskills.New(filepath.Join(t.TempDir(), "missing")), 96<<10))
	if err != nil {
		t.Fatal(err)
	}
	var response listSkillsResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("list result is not JSON: %v", err)
	}
	if response.Total != 0 || len(response.Skills) != 0 || response.HasMore || response.NextOffset != nil {
		t.Fatalf("empty list = %+v, want zero page", response)
	}
	if !strings.Contains(result.Content, `"skills":[]`) {
		t.Errorf("empty list content %q must carry an explicit empty array", result.Content)
	}
}

func TestListSkillsPaginationIsHonest(t *testing.T) {
	files := map[string]string{}
	for _, id := range []string{"b", "a", "c"} {
		files[id+"/SKILL.md"] = "---\nname: " + id + "\ndescription: Skill " + id + ".\n---\n\n# " + id + "\n"
	}
	scope := skillScope(skillCatalog(t, files), 96<<10)
	first, err := executeListSkills(context.Background(), json.RawMessage(`{"limit":2}`), scope)
	if err != nil {
		t.Fatal(err)
	}
	var page listSkillsResponse
	if err := json.Unmarshal([]byte(first.Content), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Skills) != 2 || !page.HasMore || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatalf("first page = %+v, want 2 of 3 with next_offset 2", page)
	}
	if page.Skills[0].ID != "a" || page.Skills[1].ID != "b" {
		t.Fatalf("first page ids = %q, %q; want sorted a, b", page.Skills[0].ID, page.Skills[1].ID)
	}
	second, err := executeListSkills(context.Background(), json.RawMessage(`{"offset":2,"limit":2}`), scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(second.Content), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Skills) != 1 || page.Skills[0].ID != "c" || page.HasMore || page.NextOffset != nil {
		t.Fatalf("second page = %+v, want final c", page)
	}
	if strings.Contains(first.Content, "# a") || strings.Contains(second.Content, "# c") {
		t.Error("list results leak skill bodies")
	}
}

func reassembleSkill(t *testing.T, scope Scope, skill, relPath string) string {
	t.Helper()
	var builder strings.Builder
	offset := 0
	revision := ""
	for i := 0; i < 100; i++ {
		args, _ := json.Marshal(map[string]any{"skill": skill, "path": relPath, "offset": offset, "revision": revision})
		result, err := executeReadSkill(context.Background(), args, scope)
		if err != nil {
			t.Fatalf("read(%d) error: %v", offset, err)
		}
		if strings.HasPrefix(result.Content, readSkillName+" error:") {
			t.Fatalf("read(%d) failed: %s", offset, result.Content)
		}
		var response aiskills.ReadResult
		if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
			t.Fatalf("read(%d) is not JSON: %v", offset, err)
		}
		encoded, _ := json.Marshal(response)
		if len(encoded) > skillResultSerializedCap {
			t.Fatalf("read(%d) serialized to %d bytes, over the cap", offset, len(encoded))
		}
		builder.WriteString(response.Content)
		revision = response.Revision
		if !response.HasMore {
			return builder.String()
		}
		offset = *response.NextOffset
	}
	t.Fatal("read did not terminate")
	return ""
}

func TestReadSkillListToReadFlow(t *testing.T) {
	body := "---\nname: local-seo\ndescription: Local SEO review.\n---\n\n# Local SEO\n\n" + strings.Repeat("Do the review. héllo. ", 900) + "\n"
	scope := skillScope(skillCatalog(t, map[string]string{
		"seo/local-seo/SKILL.md":                body,
		"seo/local-seo/references/checklist.md": "# Checklist\n",
	}), 96<<10)
	listed, err := executeListSkills(context.Background(), json.RawMessage(`{}`), scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listed.Content, `"id":"seo/local-seo"`) || strings.Contains(listed.Content, "# Local SEO") {
		t.Fatalf("list result %q must name the skill without its body", listed.Content)
	}
	if got := reassembleSkill(t, scope, "seo/local-seo", ""); got != body {
		t.Errorf("reassembled %d bytes, want %d", len(got), len(body))
	}
	args, _ := json.Marshal(map[string]any{"skill": "seo/local-seo"})
	initial, err := executeReadSkill(context.Background(), args, scope)
	if err != nil {
		t.Fatal(err)
	}
	var response aiskills.ReadResult
	if err := json.Unmarshal([]byte(initial.Content), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.AvailableFiles) != 2 || response.AvailableFiles[0] != "SKILL.md" {
		t.Errorf("initial root read available files = %v", response.AvailableFiles)
	}
}

func TestReadSkillContinuationRequiresRevision(t *testing.T) {
	scope := skillScope(skillCatalog(t, map[string]string{
		"s/SKILL.md": "---\nname: s\ndescription: S.\n---\n\n" + strings.Repeat("x", aiskills.MaxReadContentBytes+10) + "\n",
	}), 96<<10)
	args, _ := json.Marshal(map[string]any{"skill": "s", "offset": 10})
	result, err := executeReadSkill(context.Background(), args, scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Content, readSkillName+" error:") || !strings.Contains(result.Content, "revision") {
		t.Errorf("continuation without revision = %q, want a revision failure", result.Content)
	}
}

func TestReadSkillFailures(t *testing.T) {
	scope := skillScope(skillCatalog(t, map[string]string{
		"s/SKILL.md": "---\nname: s\ndescription: S.\n---\n\n# S\n",
	}), 96<<10)
	cases := map[string]string{
		`{}`:                                    "skill",
		`{"skill":"nope"}`:                      "unknown skill",
		`{"skill":"s","path":"../x.md"}`:        "relative slash-separated",
		`{"skill":"s","path":"missing.md"}`:     "unknown file",
		`{"skill":"s","offset":-1}`:             "offset",
		`{"skill":"s","bogus":true}`:            "unknown argument",
		`{"skill":"s","offset":"zero"}`:         "integer",
		`{"skill":""}`:                          "required",
		`{"skill":"s","revision":"stale-hash"}`: "changed",
	}
	for args, want := range cases {
		result, err := executeReadSkill(context.Background(), json.RawMessage(args), scope)
		if err != nil {
			t.Fatalf("read(%s) error: %v", args, err)
		}
		if !strings.HasPrefix(result.Content, readSkillName+" error:") || !strings.Contains(result.Content, want) {
			t.Errorf("read(%s) = %q, want a failure naming %q", args, result.Content, want)
		}
	}
}

func TestReadSkillNilCatalogNeverPanics(t *testing.T) {
	if _, err := executeListSkills(context.Background(), json.RawMessage(`{}`), Scope{}); err == nil {
		t.Error("list with nil catalog succeeded, want an error")
	}
	if _, err := executeReadSkill(context.Background(), json.RawMessage(`{"skill":"s"}`), Scope{}); err == nil {
		t.Error("read with nil catalog succeeded, want an error")
	}
	if _, err := executeReadSkill(context.Background(), json.RawMessage(`{"skill":"s"}`), skillScope(nil, 96<<10)); err == nil {
		t.Error("read with nil catalog pointer succeeded, want an error")
	}
}

func TestReadSkillMissingRootFailsClearly(t *testing.T) {
	scope := skillScope(aiskills.New(filepath.Join(t.TempDir(), "missing")), 96<<10)
	result, err := executeReadSkill(context.Background(), json.RawMessage(`{"skill":"s"}`), scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Content, readSkillName+" error:") || !strings.Contains(result.Content, "unknown skill") {
		t.Errorf("missing root read = %q, want unknown skill", result.Content)
	}
}

func TestReadSkillBudgetLimitNamesPartialRoot(t *testing.T) {
	body := "---\nname: s\ndescription: S.\n---\n\n# S\n"
	scope := skillScope(skillCatalog(t, map[string]string{"s/SKILL.md": body}), 0)
	result, err := executeReadSkill(context.Background(), json.RawMessage(`{"skill":"s"}`), scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, `"status":"limit_reached"`) || !strings.Contains(result.Content, "Do not apply a partially read SKILL.md") {
		t.Errorf("spent budget read = %q, want a partial-root limit", result.Content)
	}
}

func TestReadSkillTinyBudgetRefusesRuneOvershoot(t *testing.T) {
	scope := skillScope(skillCatalog(t, map[string]string{
		"s/SKILL.md":            "---\nname: s\ndescription: S.\n---\n\n# S\n",
		"s/references/emoji.md": "\U0001F389 party\n",
	}), 1)
	args, _ := json.Marshal(map[string]any{"skill": "s", "path": "references/emoji.md"})
	result, err := executeReadSkill(context.Background(), args, scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, `"status":"limit_reached"`) || !strings.Contains(result.Content, "Do not apply a partially read SKILL.md") {
		t.Errorf("1-byte budget 4-byte rune read = %q, want a limit instead of a 4-byte spend", result.Content)
	}
	if got := scope.SkillsBudget.Remaining(); got != 1 {
		t.Errorf("refused read spent the budget: remaining = %d, want 1", got)
	}
	fitting := skillScope(scope.Skills, 4)
	served, err := executeReadSkill(context.Background(), args, fitting)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(served.Content, `"status":"limit_reached"`) {
		t.Errorf("4-byte budget 4-byte rune read = %q, want the rune served", served.Content)
	}
	if got := fitting.SkillsBudget.Remaining(); got != 0 {
		t.Errorf("served read remaining = %d, want 0", got)
	}
}

func TestReadSkillRootSizeBound(t *testing.T) {
	big := "---\nname: big\ndescription: Big.\n---\n\n" + strings.Repeat("x", aiskills.MaxSkillRootBytes+1) + "\n"
	catalog := skillCatalog(t, map[string]string{"big/SKILL.md": big})
	if err := catalog.Err(); err == nil {
		t.Fatal("oversized root loaded, want a whole-catalog diagnostic")
	}
	scope := skillScope(catalog, 96<<10)
	result, err := executeReadSkill(context.Background(), json.RawMessage(`{"skill":"big"}`), scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Content, readSkillName+" error:") {
		t.Errorf("oversized root read = %q, want a catalog failure", result.Content)
	}
	huge := strings.Repeat("r", 100<<10)
	refCatalog := skillCatalog(t, map[string]string{
		"s/SKILL.md":          "---\nname: s\ndescription: S.\n---\n\n# S\n",
		"s/references/big.md": "# Big\n\n" + huge + "\n",
	})
	if err := refCatalog.Err(); err != nil {
		t.Fatalf("100KiB reference broke the catalog: %v", err)
	}
	refScope := skillScope(refCatalog, 96<<10)
	args, _ := json.Marshal(map[string]any{"skill": "s", "path": "references/big.md"})
	head, err := executeReadSkill(context.Background(), args, refScope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(head.Content, readSkillName+" error:") {
		t.Fatalf("large reference head read failed: %s", head.Content)
	}
}
