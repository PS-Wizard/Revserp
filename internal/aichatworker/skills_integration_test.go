package aichatworker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aiskills"
)

func TestWorkerLoadsSkillAndReferenceOnlyThroughTools(t *testing.T) {
	worker, _, user, project := testWorker(t)
	root := t.TempDir()
	skillDir := filepath.Join(root, "seo", "local-seo")
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	const rootMarker = "ROOT_INSTRUCTIONS_ONLY_FROM_TOOL"
	const referenceMarker = "REFERENCE_ONLY_FROM_TOOL"
	for name, body := range map[string]string{
		"SKILL.md":                "---\nname: local-seo\ndescription: Review local SEO.\n---\n\n" + rootMarker,
		"references/checklist.md": referenceMarker,
	} {
		if err := os.WriteFile(filepath.Join(skillDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	worker.Skills = aiskills.New(root)
	provider := &roundProvider{rounds: [][]ai.Event{
		{{ToolCall: &ai.ToolCall{ID: "list", Name: "list_skills", Args: `{}`}}},
		{{ToolCall: &ai.ToolCall{ID: "root", Name: "read_skill", Args: `{"skill":"seo/local-seo"}`}}},
		{{ToolCall: &ai.ToolCall{ID: "reference", Name: "read_skill", Args: `{"skill":"seo/local-seo","path":"references/checklist.md"}`}}},
		{{Text: "Review complete."}},
	}}
	worker.provider = provider
	id := queued(t, worker, user, project)
	claimed, err := worker.claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != id {
		t.Fatalf("claimed %s, want %s", claimed.ID, id)
	}
	worker.run(context.Background(), claimed)

	if len(provider.requests) != 4 {
		t.Fatalf("provider requests = %d, want 4", len(provider.requests))
	}
	for round, request := range provider.requests {
		rootSeen, referenceSeen := false, false
		for _, definition := range request.Tools {
			if strings.Contains(definition.Description+string(definition.Schema), rootMarker) || strings.Contains(definition.Description+string(definition.Schema), referenceMarker) {
				t.Fatal("skill bodies leaked into tool definitions")
			}
		}
		for _, message := range request.Messages {
			if strings.Contains(message.Content, rootMarker) {
				rootSeen = true
				if round < 2 || message.Role != ai.RoleTool || message.ToolCallID != "root" {
					t.Fatalf("root instructions appeared outside their requested tool result in round %d", round)
				}
			}
			if strings.Contains(message.Content, referenceMarker) {
				referenceSeen = true
				if round < 3 || message.Role != ai.RoleTool || message.ToolCallID != "reference" {
					t.Fatalf("reference appeared outside its requested tool result in round %d", round)
				}
			}
		}
		if rootSeen != (round >= 2) || referenceSeen != (round >= 3) {
			t.Fatalf("round %d: root seen=%v, reference seen=%v", round, rootSeen, referenceSeen)
		}
	}
	var status, answer string
	if err := worker.pool.QueryRow(context.Background(), `SELECT t.status, m.content FROM ai_turns t JOIN ai_messages m ON m.turn_id=t.id AND m.role='assistant' WHERE t.id=$1`, id).Scan(&status, &answer); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || answer != "Review complete." {
		t.Fatalf("status=%q answer=%q", status, answer)
	}
	var completedCalls int
	if err := worker.pool.QueryRow(context.Background(), `SELECT count(*) FROM ai_tool_calls WHERE turn_id=$1 AND status='completed' AND name IN ('list_skills','read_skill')`, id).Scan(&completedCalls); err != nil {
		t.Fatal(err)
	}
	if completedCalls != 3 {
		t.Fatalf("completed skill calls=%d, want 3", completedCalls)
	}
}
