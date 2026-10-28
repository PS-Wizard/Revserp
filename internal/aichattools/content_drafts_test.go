package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/joho/godotenv"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestRenderOutline(t *testing.T) {
	got := renderOutline([]outlineSection{
		{Heading: "Intro", Level: 2},
		{Heading: "Background", Level: 3},
		{Heading: "Conclusion", Level: 2},
	})
	want := "## Intro\n\n### Background\n\n## Conclusion\n"
	if got != want {
		t.Fatalf("renderOutline = %q, want %q", got, want)
	}
}

func TestReplaceSection(t *testing.T) {
	body := "# Title\n\n## Intro\n\nOld intro.\n\n### Detail\n\nDeep dive.\n\n## Next\n\nAfter.\n"
	t.Run("h2 keeps h3 children intact", func(t *testing.T) {
		got, err := replaceSection(body, "intro", "New intro.")
		if err != nil {
			t.Fatalf("replace: %v", err)
		}
		// Only Intro's own direct body is replaced: the ### Detail
		// subsection heading and its body survive for their own call.
		want := "# Title\n\n## Intro\n\nNew intro.\n\n### Detail\n\nDeep dive.\n\n## Next\n\nAfter.\n"
		if got != want {
			t.Fatalf("replace =\n%s\nwant\n%s", got, want)
		}
	})
	t.Run("h2 write then h3 write both succeed", func(t *testing.T) {
		afterH2, err := replaceSection(body, "intro", "New intro.")
		if err != nil {
			t.Fatalf("replace h2: %v", err)
		}
		got, err := replaceSection(afterH2, "detail", "Fresh dive.")
		if err != nil {
			t.Fatalf("replace h3 after h2: %v", err)
		}
		if !strings.Contains(got, "## Intro\n\nNew intro.") || !strings.Contains(got, "### Detail\n\nFresh dive.") {
			t.Fatalf("sequential h2+h3 wrong:\n%s", got)
		}
	})
	t.Run("content with headings rejected", func(t *testing.T) {
		_, err := replaceSection(body, "intro", "New intro.\n\n## Smuggled\n\nOops.")
		if err == nil || !strings.Contains(err.Error(), "content must not contain headings") {
			t.Fatalf("smuggled heading err = %v", err)
		}
		// The body must be unchanged by the rejection.
		if _, err := replaceSection(body, "intro", "New intro."); err != nil {
			t.Fatalf("valid replace after rejection: %v", err)
		}
	})
	t.Run("h3 stops at next h2", func(t *testing.T) {
		got, err := replaceSection(body, "detail", "Fresh.")
		if err != nil {
			t.Fatalf("replace: %v", err)
		}
		if !strings.Contains(got, "### Detail\n\nFresh.\n\n## Next") || strings.Contains(got, "Deep dive") {
			t.Fatalf("h3 replace wrong:\n%s", got)
		}
	})
	t.Run("strips model-included heading line", func(t *testing.T) {
		got, err := replaceSection(body, "Next", "## Next\nBody here.")
		if err != nil {
			t.Fatalf("replace: %v", err)
		}
		if strings.Count(got, "## Next") != 1 || !strings.Contains(got, "Body here.") {
			t.Fatalf("heading not stripped:\n%s", got)
		}
	})
	t.Run("missing lists available", func(t *testing.T) {
		_, err := replaceSection(body, "nope", "x")
		if err == nil || !strings.Contains(err.Error(), `"Intro"`) || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("missing err = %v", err)
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		dup := body + "\n## INTRO\n\nAgain.\n"
		_, err := replaceSection(dup, "intro", "x")
		if err == nil || !strings.Contains(err.Error(), "matches 2") {
			t.Fatalf("ambiguous err = %v", err)
		}
	})
	t.Run("ignores fenced code blocks", func(t *testing.T) {
		fenced := "## Real\n\nBefore.\n\n## Next\n\n```js\n# not a heading\n## also not\n```\n\n~~~\n# tilde fence\n~~~\n\nEnd.\n"
		headings := parseDraftHeadings(fenced)
		if len(headings) != 2 || headings[0].Text != "Real" || headings[1].Text != "Next" {
			t.Fatalf("headings = %+v", headings)
		}
		got, err := replaceSection(fenced, "real", "New before.")
		if err != nil {
			t.Fatalf("replace with fences: %v", err)
		}
		if !strings.Contains(got, "## Real\n\nNew before.") || !strings.Contains(got, "# not a heading") || !strings.Contains(got, "## also not") || !strings.Contains(got, "# tilde fence") {
			t.Fatalf("fence handling wrong:\n%s", got)
		}
	})
	t.Run("no headings", func(t *testing.T) {
		_, err := replaceSection("plain text", "nope", "x")
		if err == nil || !strings.Contains(err.Error(), "no headings") {
			t.Fatalf("no-heading err = %v", err)
		}
	})
}

func TestApplyDraftEdits(t *testing.T) {
	body := "The cat sat. The cat slept."
	t.Run("sequential", func(t *testing.T) {
		got, err := applyDraftEdits(body, []draftEdit{{Old: "sat", New: "ran"}, {Old: "ran. The cat slept", New: "ran fast"}})
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if got != "The cat ran fast." {
			t.Fatalf("apply = %q", got)
		}
	})
	t.Run("not found aborts unchanged", func(t *testing.T) {
		_, err := applyDraftEdits(body, []draftEdit{{Old: "sat", New: "ran"}, {Old: "missing", New: "x"}})
		if err == nil || !strings.Contains(err.Error(), "edit 2 failed") || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		_, err := applyDraftEdits(body, []draftEdit{{Old: "The cat", New: "A dog"}})
		if err == nil || !strings.Contains(err.Error(), "matches 2") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("empty old", func(t *testing.T) {
		if _, err := applyDraftEdits(body, []draftEdit{{Old: "", New: "x"}}); err == nil {
			t.Fatalf("empty old accepted")
		}
	})
	t.Run("empty edits identity", func(t *testing.T) {
		got, err := applyDraftEdits(body, nil)
		if err != nil || got != body {
			t.Fatalf("identity = %q, %v", got, err)
		}
	})
}

func TestContentDraftRegistry(t *testing.T) {
	normal := NewRegistry()
	for _, name := range []string{"read_draft", "set_outline", "write_section", "edit_draft"} {
		if _, ok := normal.Get(name); ok {
			t.Fatalf("normal registry must not contain %s", name)
		}
	}
	draft := NewContentDraftRegistry(nil)
	for _, name := range []string{"read_draft", "set_outline", "write_section", "edit_draft", "read_issues", "ask_user_question"} {
		if _, ok := draft.Get(name); !ok {
			t.Fatalf("draft registry missing %s: %v", name, draft.Names())
		}
	}
	blocked := NewContentDraftRegistry([]string{"read_draft", "read_issues"})
	if _, ok := blocked.Get("read_draft"); ok {
		t.Fatalf("denylist must hide read_draft")
	}
	if _, ok := blocked.Get("read_issues"); ok {
		t.Fatalf("denylist must hide read_issues")
	}
	if _, ok := blocked.Get("set_outline"); !ok {
		t.Fatalf("denylist must keep set_outline")
	}
	if len(ContentDraftDefs()) != 4 {
		t.Fatalf("defs = %d, want 4", len(ContentDraftDefs()))
	}
}

func TestParseSetOutlineArgs(t *testing.T) {
	_, err := parseSetOutlineArgs(json.RawMessage(`{"title":"T","sections":[{"heading":"A","level":1}]}`))
	if err == nil || !strings.Contains(err.Error(), "level") {
		t.Fatalf("bad level err = %v", err)
	}
	_, err = parseSetOutlineArgs(json.RawMessage(`{"title":"T","sections":[]}`))
	if err == nil {
		t.Fatalf("empty sections accepted")
	}
	args, err := parseSetOutlineArgs(json.RawMessage(`{"title":" T ","meta_title":"M","sections":[{"heading":" A ","level":2}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if args.Title != "T" || args.MetaTitle == nil || *args.MetaTitle != "M" || len(args.Sections) != 1 {
		t.Fatalf("args = %+v", args)
	}
}

type contentDraftTestSeed struct {
	queries *sqlc.Queries
	scope   Scope
	cleanup func()
}

func newContentDraftTestSeed(t *testing.T) contentDraftTestSeed {
	t.Helper()
	if err := godotenv.Load("../../.env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, url, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	name := fmt.Sprintf("content-draft-tool-%d", time.Now().UnixNano())
	var orgID, userID, projectID, draftID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations(name) VALUES($1) RETURNING id`, name).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(auth_provider,auth_subject,email) VALUES('test',$1,$2) RETURNING id`, name, name+"@x.test").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects(organization_id,name,base_url) VALUES($1,$2,'https://x.test') RETURNING id`, orgID, name).Scan(&projectID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO content_drafts(org_id,project_id,created_by_user_id,title) VALUES($1,$2,$3,'Seed') RETURNING id`, orgID, projectID, userID).Scan(&draftID); err != nil {
		t.Fatalf("seed draft: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE id=$1`, orgID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, userID)
	})
	queries := sqlc.New(pool)
	return contentDraftTestSeed{
		queries: queries,
		scope:   Scope{UserID: userID, ProjectID: projectID, ContentDraftID: draftID, Queries: queries, DB: pool},
		cleanup: func() {},
	}
}

func executeDraftTool(t *testing.T, tool Tool, scope Scope, args string) Result {
	t.Helper()
	result, err := tool.Execute(context.Background(), json.RawMessage(args), scope)
	if err != nil {
		t.Fatalf("%s execute error: %v", tool.Def.Name, err)
	}
	return result
}

func TestContentDraftToolsDB(t *testing.T) {
	seed := newContentDraftTestSeed(t)
	ctx := context.Background()

	outline := executeDraftTool(t, setOutlineTool(), seed.scope, `{"title":"Launch post","meta_title":"Launch","meta_description":"Desc","target_keyword":"launch","sections":[{"heading":"Intro","level":2},{"heading":"Steps","level":2},{"heading":"Tip","level":3}]}`)
	if outline.Content != "Outline set: 3 sections." {
		t.Fatalf("outline content = %q", outline.Content)
	}

	read := executeDraftTool(t, readDraftTool(), seed.scope, `{}`)
	for _, want := range []string{"Title: Launch post", "Meta title: Launch", "Target keyword: launch", "## Intro", "### Tip"} {
		if !strings.Contains(read.Content, want) {
			t.Fatalf("read missing %q:\n%s", want, read.Content)
		}
	}

	wrote := executeDraftTool(t, writeSectionTool(), seed.scope, `{"heading":"steps","content":"## Steps\nFirst do this."}`)
	if wrote.Content != `Wrote section "steps" (3 words).` {
		t.Fatalf("write content = %q", wrote.Content)
	}
	after, err := seed.queries.GetContentDraftForProject(ctx, sqlc.GetContentDraftForProjectParams{ID: seed.scope.ContentDraftID, ProjectID: seed.scope.ProjectID})
	if err != nil {
		t.Fatalf("read after write: %v", err)
	}
	if !strings.Contains(after.Body, "## Steps\n\nFirst do this.") || strings.Count(after.Body, "## Steps") != 1 {
		t.Fatalf("body after write:\n%s", after.Body)
	}

	missing := executeDraftTool(t, writeSectionTool(), seed.scope, `{"heading":"nope","content":"x"}`)
	if !strings.Contains(missing.Content, "write_section error:") || !strings.Contains(missing.Content, `"Intro"`) {
		t.Fatalf("missing heading content = %q", missing.Content)
	}

	edited := executeDraftTool(t, editDraftTool(), seed.scope, `{"edits":[{"old":"First do this.","new":"First do that."}],"meta_title":"Launch v2"}`)
	if edited.Content != "Applied 1 edit; updated meta title." {
		t.Fatalf("edit content = %q", edited.Content)
	}
	afterEdit, err := seed.queries.GetContentDraftForProject(ctx, sqlc.GetContentDraftForProjectParams{ID: seed.scope.ContentDraftID, ProjectID: seed.scope.ProjectID})
	if err != nil {
		t.Fatalf("read after edit: %v", err)
	}
	if !strings.Contains(afterEdit.Body, "First do that.") || afterEdit.MetaTitle != "Launch v2" {
		t.Fatalf("body after edit = %+v", afterEdit)
	}

	failed := executeDraftTool(t, editDraftTool(), seed.scope, `{"edits":[{"old":"absent","new":"x"}]}`)
	if !strings.Contains(failed.Content, "edit_draft error: edit 1 failed") {
		t.Fatalf("failed edit content = %q", failed.Content)
	}
	unchanged, err := seed.queries.GetContentDraftForProject(ctx, sqlc.GetContentDraftForProjectParams{ID: seed.scope.ContentDraftID, ProjectID: seed.scope.ProjectID})
	if err != nil {
		t.Fatalf("read after failed edit: %v", err)
	}
	if unchanged.Body != afterEdit.Body {
		t.Fatalf("failed edit changed the body")
	}

	// No draft scope: clear model-facing error, no Go error.
	bare := seed.scope
	bare.ContentDraftID = pgtype.UUID{}
	for _, tool := range []Tool{readDraftTool(), setOutlineTool(), writeSectionTool(), editDraftTool()} {
		result, err := tool.Execute(ctx, json.RawMessage(`{}`), bare)
		if err != nil {
			t.Fatalf("%s bare-scope go error: %v", tool.Def.Name, err)
		}
		if !strings.Contains(result.Content, tool.Def.Name+" error:") || !strings.Contains(result.Content, "no content draft") {
			t.Fatalf("%s bare-scope content = %q", tool.Def.Name, result.Content)
		}
	}
}
