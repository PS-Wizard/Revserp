package aichatworker

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func mustUUID(t *testing.T) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan("11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatalf("scan uuid: %v", err)
	}
	return id
}

func TestRegistryForTurnScope(t *testing.T) {
	draftTools := []string{"read_draft", "set_outline", "write_section", "edit_draft"}

	t.Run("normal keeps no draft tools and MCP", func(t *testing.T) {
		registry, withMCP := registryForTurnScope(nil, turnScope{})
		if !withMCP {
			t.Fatalf("normal turn must keep MCP")
		}
		for _, name := range draftTools {
			if _, ok := registry.Get(name); ok {
				t.Fatalf("normal registry must not contain %s", name)
			}
		}
	})

	t.Run("draft adds tools and keeps MCP", func(t *testing.T) {
		registry, withMCP := registryForTurnScope(nil, turnScope{ContentDraftID: mustUUID(t)})
		if !withMCP {
			t.Fatalf("draft turn must keep MCP")
		}
		for _, name := range draftTools {
			if _, ok := registry.Get(name); !ok {
				t.Fatalf("draft registry missing %s: %v", name, registry.Names())
			}
		}
		if _, ok := registry.Get("read_issues"); !ok {
			t.Fatalf("draft registry must keep normal tools: %v", registry.Names())
		}
	})

	t.Run("draft honors denylist", func(t *testing.T) {
		registry, _ := registryForTurnScope([]string{"read_draft"}, turnScope{ContentDraftID: mustUUID(t)})
		if _, ok := registry.Get("read_draft"); ok {
			t.Fatalf("denylist must hide read_draft")
		}
	})

	t.Run("location unchanged without MCP", func(t *testing.T) {
		registry, withMCP := registryForTurnScope(nil, turnScope{LocationID: mustUUID(t), ContentDraftID: mustUUID(t)})
		if withMCP {
			t.Fatalf("location turn must not attach MCP")
		}
		for _, name := range draftTools {
			if _, ok := registry.Get(name); ok {
				t.Fatalf("location registry must not contain %s", name)
			}
		}
	})
}

func TestComposeSystemContextContentDraft(t *testing.T) {
	context := composeSystemContext("p", "Example", "https://example.com", pgtype.Timestamptz{}, "", "", &contentDraftPrompt{Title: "Launch", TargetKeyword: "launch tips"})
	for _, want := range []string{"--- Content draft ---", `"Launch"`, `"launch tips"`, "read_draft", "set_outline", "write_section", "edit_draft", "ask_user_question", "never paste the article into chat"} {
		if !strings.Contains(context, want) {
			t.Errorf("draft context missing %q:\n%s", want, context)
		}
	}
	if strings.Contains(context, "Editor links") || strings.Contains(context, "revserp-editor") {
		t.Errorf("editor links must be gone:\n%s", context)
	}
	plain := composeSystemContext("p", "Example", "https://example.com", pgtype.Timestamptz{}, "", "", nil)
	if strings.Contains(plain, "--- Content draft ---") {
		t.Fatalf("normal context must omit the draft section:\n%s", plain)
	}
}
