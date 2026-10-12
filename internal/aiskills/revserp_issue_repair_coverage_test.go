package aiskills_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/aiskills"
	"github.com/ps-wizard/revserp/internal/issues/aeo"
	pagespeed "github.com/ps-wizard/revserp/internal/issues/page_speed"
	"github.com/ps-wizard/revserp/internal/issues/seo"
)

func TestRevserpIssueRepairCoversEveryDetectorIssueType(t *testing.T) {
	pillarRefs := map[string]string{
		"seo":       "references/seo.md",
		"aeo":       "references/aeo.md",
		"pagespeed": "references/pagespeed.md",
	}
	pillarTypes := map[string][]string{}
	for issueType := range seo.IssuePenaltyByType {
		pillarTypes["seo"] = append(pillarTypes["seo"], issueType)
	}
	for issueType := range aeo.IssuePenaltyByType {
		pillarTypes["aeo"] = append(pillarTypes["aeo"], issueType)
	}
	for issueType := range pagespeed.IssuePenaltyByType {
		pillarTypes["pagespeed"] = append(pillarTypes["pagespeed"], issueType)
	}
	total := 0
	for _, types := range pillarTypes {
		total += len(types)
	}
	if total == 0 {
		t.Fatal("detector issue catalog is empty")
	}

	skillDir := filepath.Join("..", "..", "skills", "revserp-issue-repair")
	refBodies := map[string]string{}
	for pillar, ref := range pillarRefs {
		body, err := os.ReadFile(filepath.Join(skillDir, filepath.FromSlash(ref)))
		if err != nil {
			t.Fatalf("read %s: %v", ref, err)
		}
		refBodies[pillar] = string(body)
	}

	var missing []string
	for pillar, types := range pillarTypes {
		for _, issueType := range types {
			if !strings.Contains("\n"+refBodies[pillar]+"\n", "\n### "+issueType+"\n") {
				missing = append(missing, pillar+"/"+issueType)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("repair skill lacks a \"### <issue_type>\" section in the correct pillar ref: %s", strings.Join(missing, ", "))
	}

	root, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil || strings.TrimSpace(string(root)) == "" {
		t.Fatal("repair skill SKILL.md is missing or empty")
	}
	if len(root) > aiskills.MaxSkillRootBytes {
		t.Fatalf("repair skill root is %d bytes, over the %d limit", len(root), aiskills.MaxSkillRootBytes)
	}
	if _, err := os.Stat(filepath.Join(skillDir, filepath.FromSlash("references/evidence.md"))); err != nil {
		t.Fatalf("repair skill is missing references/evidence.md: %v", err)
	}
	if !strings.Contains(string(root), "references/seo.md") || !strings.Contains(string(root), "references/aeo.md") || !strings.Contains(string(root), "references/pagespeed.md") {
		t.Fatal("repair skill root does not point at the pillar references")
	}

	catalog := aiskills.New(filepath.Join("..", "..", "skills"))
	if err := catalog.Err(); err != nil {
		t.Fatalf("skills catalog failed with repair skill present: %v", err)
	}
	if _, ok := catalog.Find("revserp-issue-repair"); !ok {
		t.Fatal("skills catalog does not list revserp-issue-repair")
	}
	t.Logf("repair skill documents %d detector issue types in their pillar refs", total)
}
