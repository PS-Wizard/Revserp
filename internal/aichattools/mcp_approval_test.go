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

func TestPrepareMCPApprovalSnapshotsWordPressPostState(t *testing.T) {
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
}

func TestPrepareMCPApprovalFallsBackToExactArgumentsWhenStateIsUnreadable(t *testing.T) {
	args := json.RawMessage(`{"id":"12","content":"new body"}`)
	for _, session := range []MCPSession{
		&fakeSnapshotSession{readErr: errors.New("timeout")},
		&fakeSnapshotSession{content: "not json"},
		&fakeSnapshotSession{content: "{}"},
		nil,
	} {
		proposal, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, "update_content", args)
		if err != nil {
			t.Fatalf("an unreadable pre-write state must not block the proposal, session %T: %v", session, err)
		}
		if proposal.Tool != "update_content" || !strings.Contains(proposal.After, "new body") {
			t.Fatalf("proposal %+v does not disclose the exact arguments", proposal)
		}
		if proposal.Snapshot != nil || proposal.Before != "" {
			t.Fatalf("proposal %+v invents a before view without a readable state", proposal)
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

func TestPrepareMCPApprovalKeepsExactArgumentsWithoutRefusal(t *testing.T) {
	for _, args := range []json.RawMessage{
		json.RawMessage(`{"id":"12","content":"<!-- wp:button -->\n<a class=\"wp-block-button__link\">Go</a>\n<!-- /wp:button -->"}`),
		json.RawMessage(`{"id":"12","content":"<!-- wp:buttons -->\n<!-- wp:button -->x<!-- /wp:button -->\n<!-- /wp:buttons -->"}`),
	} {
		session := &fakeSnapshotSession{content: wordpressPostJSON}
		proposal, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, "update_content", args)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if !strings.Contains(proposal.After, "wp:button") {
			t.Fatalf("after %q rewrote the supplied markup", proposal.After)
		}
	}
	section, err := PrepareMCPApproval(context.Background(), &fakeSnapshotSession{}, MCPServiceWordPress, "insert_page_section", json.RawMessage(`{"id":"12","section":{"type":"button"}}`))
	if err != nil {
		t.Fatalf("the section shortcut must be proposed, not refused: %v", err)
	}
	if !strings.Contains(section.After, "button") {
		t.Fatalf("after %q lost the requested shortcut", section.After)
	}
	custom, err := PrepareMCPApproval(context.Background(), &fakeSnapshotSession{}, MCPServiceCustom, "update_content", json.RawMessage(`{"content":"<!-- wp:button -->"}`))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if custom.Tool != "update_content" {
		t.Fatalf("proposal %+v does not name the exact server tool", custom)
	}
}

func TestPreparedWordPressCallDispatchesExactArguments(t *testing.T) {
	session := &fakeMCPSession{}
	tools := BuildMCPTools(testToolSpecs("update_content"), session, MCPToolOptions{
		ConnectionID: testConnectionID,
		Service:      MCPServiceWordPress,
	})
	args := json.RawMessage(`{"id":"12","content":"<!-- wp:button -->x<!-- /wp:button -->"}`)
	if _, err := tools[0].Execute(context.Background(), args, Scope{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(session.calls) != 1 || session.calls[0] != "update_content" {
		t.Fatalf("dispatched %v, want the exact remote name", session.calls)
	}
	if string(session.args[0]) != string(args) {
		t.Fatalf("dispatched args %s, want the call arguments unchanged", session.args[0])
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

func TestPreflightReadDenialFallsBackToExactArgsProposal(t *testing.T) {
	// A denied post read leaves no snapshot: the card still proposes the
	// exact arguments, and the write still needs its own Ask or Allow.
	session := &fakeSnapshotSession{content: wordpressPostJSON, blocked: map[string]bool{"get_content": true}}
	proposal, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, "update_content", json.RawMessage(`{"id":"12","content":"new body"}`))
	if err != nil {
		t.Fatalf("a denied get_content read must not refuse the approved write: %v", err)
	}
	if proposal.Tool != "update_content" || !strings.Contains(proposal.After, "new body") {
		t.Fatalf("proposal %+v does not disclose the exact arguments", proposal)
	}
	if proposal.Snapshot != nil || proposal.Before != "" {
		t.Fatalf("proposal %+v invents a before view without a permitted read", proposal)
	}
	for _, called := range session.calls {
		if called == "update_content" {
			t.Fatalf("preparation dispatched the mutation %q", called)
		}
	}

	// A denied builder-structure read only costs the element line: the
	// permitted post read still guards the write.
	builder := &fakeSnapshotSession{content: wordpressPostJSON, structure: `{"children":[{"id":"el-1","settings":{"text":"Old"}}]}`, blocked: map[string]bool{"get_page_structure": true}}
	edited, err := PrepareMCPApproval(context.Background(), builder, MCPServiceWordPress, "edit_page_element", json.RawMessage(`{"id":"12","element_id":"el-1"}`))
	if err != nil {
		t.Fatalf("a denied get_page_structure read must not refuse the approved write: %v", err)
	}
	if edited.Snapshot == nil || !strings.Contains(edited.Before, "status: publish") {
		t.Fatalf("proposal %+v lost the permitted post preview", edited)
	}
	if strings.Contains(edited.Before, "element el-1 currently") {
		t.Fatalf("before %q shows an element preview without a permitted read", edited.Before)
	}
}

func TestPreflightReadDenialStillObeysItsOwnPermission(t *testing.T) {
	session := &fakeSnapshotSession{content: wordpressPostJSON, blocked: map[string]bool{"get_content": true}}
	if _, err := session.Call(context.Background(), "get_content", json.RawMessage(`{"id":"12"}`)); err == nil {
		t.Fatal("a denied preflight read must stay denied")
	} else if !errors.Is(err, ErrMCPPreflightPermission) {
		t.Fatalf("error %v does not report the permission prerequisite", err)
	}
}

func TestPreflightReadFailureFallsBackToExactArgsProposal(t *testing.T) {
	session := &fakeSnapshotSession{readErr: errors.New("connection reset")}
	proposal, err := PrepareMCPApproval(context.Background(), session, MCPServiceWordPress, "update_content", json.RawMessage(`{"id":"12","content":"new body"}`))
	if err != nil {
		t.Fatalf("an unreadable pre-write state must not block the proposal: %v", err)
	}
	if proposal.Tool != "update_content" || !strings.Contains(proposal.After, "new body") {
		t.Fatalf("proposal %+v does not disclose the exact arguments", proposal)
	}
	if len(session.calls) != 1 || session.calls[0] != "get_content" {
		t.Fatalf("preparation calls = %v, want only the attempted pre-write read", session.calls)
	}
}
