package aiskills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkillFile(t *testing.T, root, relPath, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedCatalog(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for relPath, body := range files {
		writeSkillFile(t, root, relPath, body)
	}
	return root
}

const nestedSkillBody = `---
name: local-seo
description: >-
  Review a location's local SEO
  across multiple lines of YAML.
---

# Local SEO review

Use this skill for a local SEO review.
`

func TestCatalogNestedIDsAndReferences(t *testing.T) {
	root := seedCatalog(t, map[string]string{
		"seo/local-seo/SKILL.md":                nestedSkillBody,
		"seo/local-seo/references/checklist.md": "# Checklist\n",
		"seo/local-seo/notes.txt":               "plain text\n",
		"seo/local-seo/image.png":               "not a readable file",
		"ads/SKILL.md":                          "---\nname: ads\ndescription: Ads skill.\n---\n\n# Ads\n",
	})
	catalog := New(root)
	if err := catalog.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}
	skills := catalog.Skills()
	if len(skills) != 2 {
		t.Fatalf("Skills() = %+v, want 2 skills", skills)
	}
	if skills[0].ID != "ads" || skills[1].ID != "seo/local-seo" {
		t.Fatalf("Skills() ids = %q, %q; want sorted ads, seo/local-seo", skills[0].ID, skills[1].ID)
	}
	local := skills[1]
	if local.Name != "local-seo" {
		t.Errorf("Name = %q, want local-seo", local.Name)
	}
	if !strings.Contains(local.Description, "across multiple lines") {
		t.Errorf("multiline YAML description lost folding: %q", local.Description)
	}
	wantFiles := []string{"SKILL.md", "notes.txt", "references/checklist.md"}
	if strings.Join(local.Files, ",") != strings.Join(wantFiles, ",") {
		t.Errorf("Files = %v, want %v", local.Files, wantFiles)
	}
	for _, file := range local.Files {
		if filepath.IsAbs(file) || strings.Contains(file, "\\") {
			t.Errorf("file %q is not a skill-local relative path", file)
		}
	}
}

func TestCatalogListingCarriesNoBodies(t *testing.T) {
	root := seedCatalog(t, map[string]string{
		"seo/local-seo/SKILL.md": nestedSkillBody,
	})
	encoded, err := json.Marshal(New(root).Skills())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "local SEO review") {
		t.Error("listing leaks the SKILL.md body")
	}
}

func TestCatalogMissingRootIsEmpty(t *testing.T) {
	catalog := New(filepath.Join(t.TempDir(), "no-such-dir"))
	if err := catalog.Err(); err != nil {
		t.Fatalf("missing root Err() = %v, want nil", err)
	}
	if skills := catalog.Skills(); len(skills) != 0 {
		t.Fatalf("missing root Skills() = %+v, want empty", skills)
	}
	if _, ok := catalog.Find("seo/local-seo"); ok {
		t.Error("missing root Find() matched, want false")
	}
	if _, err := catalog.Read("seo/local-seo", "", 0, "", 0, 0); err == nil {
		t.Error("missing root Read() succeeded, want unknown skill error")
	}
}

func TestCatalogMalformedSkillFailsWholeCatalog(t *testing.T) {
	root := seedCatalog(t, map[string]string{
		"good/SKILL.md": "---\nname: good\ndescription: Good skill.\n---\n\n# Good\n",
		"bad/SKILL.md":  "# No frontmatter here\n",
	})
	catalog := New(root)
	err := catalog.Err()
	if err == nil {
		t.Fatal("malformed skill Err() = nil, want a diagnostic")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("diagnostic %q does not name the offending skill", err)
	}
	if skills := catalog.Skills(); len(skills) != 0 {
		t.Errorf("failed catalog Skills() = %+v, want empty", skills)
	}
	if _, err := catalog.Read("good", "", 0, "", 0, 0); err == nil {
		t.Error("failed catalog Read() succeeded, want a catalog error")
	}
}

func TestCatalogFrontmatterValidation(t *testing.T) {
	bodies := map[string]string{
		"missing name":        "---\ndescription: Has no name.\n---\n\n# X\n",
		"missing description": "---\nname: x\n---\n\n# X\n",
		"no fences":           "name: x\ndescription: y\n",
		"unterminated":        "---\nname: x\ndescription: y\n",
		"oversized name":      "---\nname: " + strings.Repeat("n", MaxSkillNameBytes+1) + "\ndescription: y\n---\n\n# X\n",
		"oversized desc":      "---\nname: x\ndescription: " + strings.Repeat("d", MaxSkillDescriptionBytes+1) + "\n---\n\n# X\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			root := seedCatalog(t, map[string]string{"s/SKILL.md": body})
			if err := New(root).Err(); err == nil {
				t.Errorf("body %q loaded without an error", body)
			}
		})
	}
}

func reassemble(t *testing.T, catalog *Catalog, skill, relPath string) string {
	t.Helper()
	var builder strings.Builder
	offset := 0
	revision := ""
	for i := 0; i < 100; i++ {
		result, err := catalog.Read(skill, relPath, offset, revision, 0, 0)
		if err != nil {
			t.Fatalf("Read(%d) failed: %v", offset, err)
		}
		if result.Revision == "" {
			t.Fatal("read has no revision")
		}
		revision = result.Revision
		builder.WriteString(result.Content)
		if !result.HasMore {
			if result.NextOffset != nil {
				t.Fatal("final read keeps a next_offset")
			}
			return builder.String()
		}
		if result.NextOffset == nil {
			t.Fatal("paged read misses next_offset")
		}
		offset = *result.NextOffset
	}
	t.Fatal("read did not terminate")
	return ""
}

func TestReadPaginationReassemblesMultibyteAndLongLines(t *testing.T) {
	paragraph := strings.Repeat("héllo wörld — one very long single line without breaks. ", 400)
	body := "---\nname: big\ndescription: Big skill.\n---\n\n" + paragraph + "\n"
	root := seedCatalog(t, map[string]string{"big/SKILL.md": body})
	catalog := New(root)
	if err := catalog.Err(); err != nil {
		t.Fatal(err)
	}
	if got := reassemble(t, catalog, "big", "SKILL.md"); got != body {
		t.Errorf("reassembled %d bytes, want %d", len(got), len(body))
	}
	first, err := catalog.Read("big", "SKILL.md", 0, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.AvailableFiles) != 1 || first.AvailableFiles[0] != "SKILL.md" {
		t.Errorf("initial root read available_files = %v", first.AvailableFiles)
	}
	second, err := catalog.Read("big", "SKILL.md", *first.NextOffset, first.Revision, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.AvailableFiles != nil {
		t.Errorf("continued read repeats available_files = %v", second.AvailableFiles)
	}
}

func TestReadResultsStayBelowSerializedCap(t *testing.T) {
	hostile := strings.Repeat("a>b<c&d\"e\\f\n", 3000)
	body := "---\nname: hostile\ndescription: Hostile skill.\n---\n\n" + hostile + "\n"
	root := seedCatalog(t, map[string]string{"h/SKILL.md": body})
	catalog := New(root)
	offset := 0
	revision := ""
	for i := 0; i < 100; i++ {
		result, err := catalog.Read("h", "SKILL.md", offset, revision, 0, DefaultMaxSerializedBytes)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > DefaultMaxSerializedBytes {
			t.Fatalf("serialized read is %d bytes, over the %d cap", len(encoded), DefaultMaxSerializedBytes)
		}
		revision = result.Revision
		if !result.HasMore {
			return
		}
		offset = *result.NextOffset
	}
	t.Fatal("read did not terminate")
}

func TestReadRejectsBadArgsPathsAndSymlinkEscapes(t *testing.T) {
	root := seedCatalog(t, map[string]string{
		"s/SKILL.md":                    "---\nname: s\ndescription: S skill.\n---\n\n# S\n",
		"s/references/checklist.md":     "# Checklist\n",
		"s/notes.txt":                   "text\n",
		"s/run.sh":                      "#!/bin/sh\necho hi\n",
		"other/SKILL.md":                "---\nname: other\ndescription: Other.\n---\n\n# Other\n",
		"other/references/checklist.md": "# Other checklist\n",
	})
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "s", "escape.md")); err != nil {
		t.Fatal(err)
	}
	catalog := New(root)
	if err := catalog.Err(); err != nil {
		t.Fatal(err)
	}
	reads := map[string]struct {
		skill, relPath string
		offset         int
	}{
		"unknown skill":    {"nope", "SKILL.md", 0},
		"unknown file":     {"s", "references/missing.md", 0},
		"dotdot escape":    {"s", "../other/SKILL.md", 0},
		"sibling escape":   {"s", "../other/references/checklist.md", 0},
		"absolute path":    {"s", "/etc/hostname", 0},
		"backslash escape": {"s", `references\checklist.md`, 0},
		"non-text file":    {"s", "run.sh", 0},
		"symlink escape":   {"s", "escape.md", 0},
	}
	for name, item := range reads {
		t.Run(name, func(t *testing.T) {
			if _, err := catalog.Read(item.skill, item.relPath, item.offset, "", 0, 0); err == nil {
				t.Error("read succeeded, want an error")
			}
		})
	}
	if _, err := catalog.Read("s", "SKILL.md", -1, "", 0, 0); err == nil {
		t.Error("negative offset succeeded, want an error")
	}
	if _, err := catalog.Read("s", "SKILL.md", 1<<30, "", 0, 0); err == nil {
		t.Error("past-end offset succeeded, want an error")
	}
	otherRoot, err := catalog.Read("other", "SKILL.md", 0, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Read("s", "SKILL.md", 0, otherRoot.Revision, 0, 0); err == nil {
		t.Error("cross-skill revision succeeded, want a changed-file error")
	}
}

func TestReadRevisionChangeFailsClearly(t *testing.T) {
	root := seedCatalog(t, map[string]string{
		"s/SKILL.md": "---\nname: s\ndescription: S skill.\n---\n\n" + strings.Repeat("x", MaxReadContentBytes+100) + "\n",
	})
	catalog := New(root)
	first, err := catalog.Read("s", "SKILL.md", 0, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !first.HasMore {
		t.Fatal("fixture file should page")
	}
	writeSkillFile(t, root, "s/SKILL.md", "---\nname: s\ndescription: Changed.\n---\n\nchanged\n")
	if _, err := catalog.Read("s", "SKILL.md", *first.NextOffset, first.Revision, 0, 0); err == nil {
		t.Fatal("stale revision read succeeded, want a changed-file error")
	} else if !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale revision error %q does not say the file changed", err)
	}
	fresh, err := catalog.Read("s", "SKILL.md", 0, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fresh.Content, "changed") {
		t.Errorf("fresh read does not show the changed file: %q", fresh.Content)
	}
}

func TestReadDefaultPathAndNilSafety(t *testing.T) {
	root := seedCatalog(t, map[string]string{
		"s/SKILL.md": "---\nname: s\ndescription: S skill.\n---\n\n# S\n",
	})
	catalog := New(root)
	result, err := catalog.Read("s", "", 0, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != "SKILL.md" || !strings.Contains(result.Content, "# S") {
		t.Errorf("default path read = %+v", result)
	}
	var nilCatalog *Catalog
	if err := nilCatalog.Err(); err == nil {
		t.Error("nil catalog Err() = nil")
	}
	if skills := nilCatalog.Skills(); skills == nil || len(skills) != 0 {
		t.Errorf("nil catalog Skills() = %v, want empty non-nil", skills)
	}
	if _, err := nilCatalog.Read("s", "", 0, "", 0, 0); err == nil {
		t.Error("nil catalog Read() succeeded")
	}
	if _, ok := nilCatalog.Find("s"); ok {
		t.Error("nil catalog Find() matched")
	}
}

func TestNestedSkillRootExcludedFromParentManifest(t *testing.T) {
	root := seedCatalog(t, map[string]string{
		"seo/SKILL.md":                          "---\nname: seo\ndescription: SEO parent.\n---\n\n# SEO\n",
		"seo/shared.md":                         "# Shared parent notes\n",
		"seo/local-seo/SKILL.md":                "---\nname: local-seo\ndescription: Local child.\n---\n\n# Local\n",
		"seo/local-seo/references/checklist.md": "# Checklist\n",
	})
	catalog := New(root)
	if err := catalog.Err(); err != nil {
		t.Fatal(err)
	}
	parent, ok := catalog.Find("seo")
	if !ok {
		t.Fatal("parent skill seo missing")
	}
	for _, file := range parent.Files {
		if strings.HasPrefix(file, "local-seo/") {
			t.Errorf("parent manifest lists nested skill file %q", file)
		}
	}
	if len(parent.Files) != 2 {
		t.Errorf("parent files = %v, want SKILL.md and shared.md only", parent.Files)
	}
	child, ok := catalog.Find("seo/local-seo")
	if !ok {
		t.Fatal("nested skill seo/local-seo missing")
	}
	if len(child.Files) != 2 {
		t.Errorf("child files = %v, want SKILL.md and references/checklist.md", child.Files)
	}
}

func TestOversizedReferenceStaysListedWithExplicitReadError(t *testing.T) {
	root := seedCatalog(t, map[string]string{
		"s/SKILL.md": "---\nname: s\ndescription: S skill.\n---\n\n# S\n",
	})
	writeSkillFile(t, root, "s/references/huge.md", "# Huge\n\n"+strings.Repeat("h", MaxSkillFileBytes+1))
	catalog := New(root)
	if err := catalog.Err(); err != nil {
		t.Fatalf("oversized reference broke the catalog: %v", err)
	}
	skill, ok := catalog.Find("s")
	if !ok {
		t.Fatal("skill s missing")
	}
	if len(skill.Files) != 2 {
		t.Fatalf("oversized reference disappeared from the manifest: %v", skill.Files)
	}
	if _, err := catalog.Read("s", "references/huge.md", 0, "", 0, 0); err == nil {
		t.Fatal("oversized reference read succeeded, want an explicit size error")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized reference error %q is not an explicit size failure", err)
	}
}

func TestOversizedRootFailsCatalogWithDiagnostic(t *testing.T) {
	root := seedCatalog(t, map[string]string{
		"s/SKILL.md": "---\nname: s\ndescription: S skill.\n---\n\n# S\n",
	})
	writeSkillFile(t, root, "s/SKILL.md", "---\nname: s\ndescription: S skill.\n---\n\n"+strings.Repeat("x", MaxSkillRootBytes+1))
	catalog := New(root)
	if err := catalog.Err(); err == nil {
		t.Fatal("oversized root loaded, want a whole-catalog diagnostic")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized root error %q is not an explicit size diagnostic", err)
	}
}
