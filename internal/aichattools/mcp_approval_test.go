package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeSnapshotSession answers the pre-write read the WordPress adapter performs
// and records every call so a test can prove the mutation was never dispatched.
type fakeSnapshotSession struct {
	fakeMCPSession
	content   string
	readErr   error
	structure string
	blocked   map[string]bool
}

func (f *fakeSnapshotSession) Call(ctx context.Context, name string, args json.RawMessage) (MCPResult, error) {
	f.calls = append(f.calls, name)
	f.args = append(f.args, args)
	if f.blocked[name] {
		return MCPResult{}, fmt.Errorf("%s: %w", name, ErrMCPPreflightPermission)
	}
	switch name {
	case "get_content":
		if f.readErr != nil {
			return MCPResult{}, f.readErr
		}
		return MCPResult{Content: f.content}, nil
	case "get_page_structure":
		return MCPResult{Content: f.structure}, nil
	}
	return MCPResult{}, errors.New("unexpected call: " + name)
}

const wordpressPostJSON = `{"id":"12","status":"publish","title":{"raw":"Landing"},"content":{"raw":"old body"},"meta":{"_edit_lock":"1700000000","layout":"hero"}}`

func TestPrepareMCPApprovalDisclosesExactArgumentsWithoutInventingADiff(t *testing.T) {
	session := &fakeSnapshotSession{}
	proposal, err := PrepareMCPApproval(context.Background(), session, MCPServiceCustom, "mystery_write", json.RawMessage(`{"path":"/etc/hosts","mode":"overwrite"}`))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if proposal.Tool != "mystery_write" || proposal.Service != MCPServiceCustom {
		t.Fatalf("proposal %+v does not name the exact server tool", proposal)
	}
	if !strings.Contains(proposal.After, "mode: overwrite") || !strings.Contains(proposal.After, "path: /etc/hosts") {
		t.Fatalf("after %q does not disclose the exact arguments", proposal.After)
	}
	if proposal.Before != "" {
		t.Fatalf("before %q invents a pre-write state for a generic call", proposal.Before)
	}
	if proposal.Snapshot != nil {
		t.Fatalf("snapshot %s was invented for a generic call", proposal.Snapshot)
	}
	if len(session.calls) != 0 {
		t.Fatalf("preparation dispatched %v, want no remote call at all", session.calls)
	}
}

func TestPrepareMCPApprovalSnapshotsWordPressPostStateAndFailsClosed(t *testing.T) {
	session := &fakeSnapshotSession{content: wordpressPostJSON}
	args := json.RawMessage(`{"id":"12","content":"new body"}`)
	proposal, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, "update_content", args)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(session.calls) != 1 || session.calls[0] != "get_content" {
		t.Fatalf("prepared with calls %v, want only the pre-write read", session.calls)
	}
	if !strings.Contains(proposal.Before, "old body") || !strings.Contains(proposal.Target, "update_content #12") {
		t.Fatalf("proposal %+v does not show what the write replaces", proposal)
	}
	if !strings.Contains(string(proposal.Snapshot), "sha256:") {
		t.Fatalf("snapshot %s is not a digest of the full state", proposal.Snapshot)
	}
	if len(proposal.Snapshot) > 512 {
		t.Fatalf("snapshot is %d bytes; it must stay compact whatever the page weighs", len(proposal.Snapshot))
	}
	again, err := PrepareMCPApproval(context.Background(), &fakeSnapshotSession{content: wordpressPostJSON}, MCPServiceWordPress, "update_content", args)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if string(again.Snapshot) != string(proposal.Snapshot) {
		t.Fatal("the same state must produce the same snapshot so an approved call can be re-checked")
	}
	changed, err := PrepareMCPApproval(context.Background(), &fakeSnapshotSession{content: strings.Replace(wordpressPostJSON, "old body", "edited elsewhere", 1)}, MCPServiceWordPress, "update_content", args)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if string(changed.Snapshot) == string(proposal.Snapshot) {
		t.Fatal("a changed page must produce a different snapshot")
	}
	for _, unreadable := range []MCPSession{
		&fakeSnapshotSession{readErr: errors.New("timeout")},
		&fakeSnapshotSession{content: "not json"},
		&fakeSnapshotSession{content: "{}"},
		nil,
	} {
		if _, err := PrepareMCPApproval(context.Background(), unreadable, MCPServiceWordPress, "update_content", args); err == nil {
			t.Fatalf("an unreadable pre-write state must block the call, session %T", unreadable)
		}
	}
}

func TestPrepareMCPApprovalAppliesToReadsDraftsAndDryRuns(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		args   string
	}{
		{name: "read", remote: "get_content", args: `{"id":"12"}`},
		{name: "draft create", remote: "publish_content", args: `{"content":"draft body","status":"draft"}`},
		{name: "dry run", remote: "bulk_update_content", args: `{"dry_run":true}`},
		{name: "custom read", remote: "list_things", args: `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := MCPServiceWordPress
			if tc.remote == "list_things" {
				service = MCPServiceCustom
			}
			proposal, err := PrepareMCPApproval(context.Background(), &fakeSnapshotSession{content: wordpressPostJSON}, service, tc.remote, json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			if proposal.Tool != tc.remote || proposal.After == "" {
				t.Fatalf("proposal %+v does not describe the exact call", proposal)
			}
		})
	}
}

func TestPrepareMCPApprovalRefusesBrokenMarkupBeforeAnyDispatch(t *testing.T) {
	session := &fakeSnapshotSession{content: wordpressPostJSON}
	args := json.RawMessage(`{"id":"12","content":"<!-- wp:button -->\n<a class=\"wp-block-button__link\">Go</a>\n<!-- /wp:button -->"}`)
	if _, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, "update_content", args); err == nil {
		t.Fatal("standalone wp:button markup must be refused")
	}
	if len(session.calls) != 0 {
		t.Fatalf("a refused call dispatched %v", session.calls)
	}
	session = &fakeSnapshotSession{content: wordpressPostJSON}
	if _, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, "update_content", json.RawMessage(`{"id":"12","content":"<!-- wp:buttons -->\n<!-- wp:button -->x<!-- /wp:button -->\n<!-- /wp:buttons -->"}`)); err != nil {
		t.Fatalf("valid nested button markup must not be refused: %v", err)
	}
	session = &fakeSnapshotSession{}
	if _, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, "insert_page_section", json.RawMessage(`{"id":"12","section":{"type":"button"}}`)); err == nil {
		t.Fatal("the button section shortcut must be refused")
	}
	if _, err := PrepareMCPApproval(context.Background(), &fakeSnapshotSession{}, MCPServiceCustom, "update_content", json.RawMessage(`{"content":"<!-- wp:button -->"}`)); err != nil {
		t.Fatalf("a custom server's own content rules are not ours to refuse: %v", err)
	}
}

func TestPreparedWordPressCallIsRefusedBeforeDispatch(t *testing.T) {
	session := &fakeMCPSession{}
	tools := BuildMCPTools(testToolSpecs("update_content"), session, MCPToolOptions{
		ConnectionID: testConnectionID,
		Service:      MCPServiceWordPress,
		Writes:       &MCPWriteState{},
	})
	result, err := tools[0].Execute(context.Background(), json.RawMessage(`{"id":"12","content":"<!-- wp:button -->x<!-- /wp:button -->"}`), Scope{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(session.calls) != 0 {
		t.Fatalf("a refused call dispatched %v", session.calls)
	}
	if !strings.Contains(result.Content, "wp:buttons") || result.Summary != "refused before the call" {
		t.Fatalf("refusal result %+v does not explain the fix", result)
	}
}

func TestPrepareMCPApprovalRejectsMalformedArgumentsAndEmptyName(t *testing.T) {
	if _, err := PrepareMCPApproval(context.Background(), &fakeSnapshotSession{}, MCPServiceWordPress, "update_content", json.RawMessage(`{oops`)); err == nil {
		t.Fatal("malformed arguments must block the call")
	}
	if _, err := PrepareMCPApproval(context.Background(), &fakeSnapshotSession{}, MCPServiceWordPress, "  ", json.RawMessage(`{}`)); err == nil {
		t.Fatal("an empty remote tool name must block the call")
	}
}

func TestBuilderPreviewShowsTheElementCopyItReplaces(t *testing.T) {
	session := &fakeSnapshotSession{
		content:   wordpressPostJSON,
		structure: `{"children":[{"id":"el-1","settings":{"text":"Old headline"}}]}`,
	}
	proposal, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, "edit_page_element", json.RawMessage(`{"id":"12","element_id":"el-1","settings":{"text":"New headline"}}`))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !strings.Contains(proposal.Before, "Old headline") {
		t.Fatalf("before %q does not show the element text being replaced", proposal.Before)
	}
	if !strings.Contains(proposal.After, "New headline") || !strings.Contains(proposal.After, "edit_page_element element el-1") {
		t.Fatalf("after %q does not describe the change plainly", proposal.After)
	}
}

func TestBuilderDeleteShowsNoInventedUndo(t *testing.T) {
	proposal, err := PrepareMCPApproval(context.Background(), &fakeSnapshotSession{content: wordpressPostJSON}, MCPServiceWordPress, "delete_page_element", json.RawMessage(`{"id":"12","element_id":"el-1"}`))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !strings.Contains(proposal.After, "this element is removed from the page") {
		t.Fatalf("after %q does not say what is removed", proposal.After)
	}
}

func TestPreparationNeverDispatchesTheRequestedMutation(t *testing.T) {
	session := &fakeSnapshotSession{content: wordpressPostJSON}
	if _, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, "update_content", json.RawMessage(`{"id":"12","content":"new body"}`)); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, "delete_content", json.RawMessage(`{"id":"12"}`)); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for _, called := range session.calls {
		if called == "update_content" || called == "delete_content" {
			t.Fatalf("preparation dispatched the mutation %q", called)
		}
	}
}

func TestPreflightReadRefusalByPermissionBlocksInsteadOfHidingTheBeforeView(t *testing.T) {
	for _, blocked := range []string{"get_content", "get_page_structure"} {
		session := &fakeSnapshotSession{content: wordpressPostJSON, structure: `{"children":[{"id":"el-1","settings":{"text":"Old"}}]}`, blocked: map[string]bool{blocked: true}}
		remote, args := "update_content", json.RawMessage(`{"id":"12","content":"new body"}`)
		if blocked == "get_page_structure" {
			remote, args = "edit_page_element", json.RawMessage(`{"id":"12","element_id":"el-1"}`)
		}
		_, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, remote, args)
		if err == nil {
			t.Fatalf("a refused %s preflight read must block the call, not hide the before view", blocked)
		}
		if !errors.Is(err, ErrMCPPreflightPermission) {
			t.Fatalf("error %v does not report the permission prerequisite", err)
		}
		if !strings.Contains(err.Error(), "not permitted") {
			t.Fatalf("error %q does not explain the permission prerequisite", err)
		}
	}
}

func TestPreflightReadFailureDoesNotFallThroughToAnUnreviewedCall(t *testing.T) {
	session := &fakeSnapshotSession{readErr: errors.New("connection reset")}
	proposal, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, "update_content", json.RawMessage(`{"id":"12","content":"new body"}`))
	if err == nil {
		t.Fatal("an unreadable pre-write state must block the call")
	}
	if len(proposal.After) == 0 && len(session.calls) == 0 {
		t.Fatal("the refused preparation disclosed nothing and read nothing")
	}
}
