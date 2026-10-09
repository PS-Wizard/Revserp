package aiskills_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/aiskills"
)

func TestRepositorySkills(t *testing.T) {
	catalog := aiskills.New("../../skills")
	if err := catalog.Err(); err != nil {
		t.Fatal(err)
	}
	skills := catalog.Skills()
	if len(skills) == 0 {
		t.Fatal("repository skills catalog is empty")
	}
	for _, skill := range skills {
		t.Run(skill.ID, func(t *testing.T) {
			root, err := catalog.Read(skill.ID, "", 0, "", 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if root.Path != "SKILL.md" || !slices.Equal(root.AvailableFiles, skill.Files) {
				t.Fatalf("root read does not match catalog: %+v", root)
			}
			for _, file := range skill.Files {
				if _, err := catalog.Read(skill.ID, file, 0, "", 0, 0); err != nil {
					t.Fatalf("read %s: %v", file, err)
				}
			}
		})
	}
	metadata, err := json.Marshal(skills)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), "# Local SEO review") {
		t.Fatal("skill instructions leaked into catalog metadata")
	}
}
