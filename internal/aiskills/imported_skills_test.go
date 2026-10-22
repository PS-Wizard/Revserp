package aiskills_test

import (
	"testing"

	"github.com/ps-wizard/revserp/internal/aiskills"
)

func TestImportedMarketingSkills(t *testing.T) {
	catalog := aiskills.New("../../skills")
	if err := catalog.Err(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"seo-audit", "ai-seo", "content-strategy"} {
		skill, ok := catalog.Find(id)
		if !ok {
			t.Fatalf("imported skill %q not in catalog", id)
		}
		missing := map[string]bool{"SKILL.md": true, "LICENSE.txt": true}
		for _, file := range skill.Files {
			delete(missing, file)
		}
		for file := range missing {
			t.Errorf("imported skill %q is missing %s", id, file)
		}
		if _, err := catalog.Read(id, "", 0, "", 0, 0); err != nil {
			t.Errorf("read %s: %v", id, err)
		}
	}
}
