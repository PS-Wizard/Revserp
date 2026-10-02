package aichattools

// Approval policy cases the card depends on: a dry_run the remote ignores, a
// pre-write snapshot that still compares after the post changed, a read that
// fails closed, and a builder edit previewed as the copy it replaces.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// publishedPage is a live page with builder, template and SEO state, the shape
// an approved write has to be re-checked against.
const publishedPage = `{
	"id": 812,
	"post_type": "page",
	"status": "publish",
	"title": {"rendered": "Landing page"},
	"content": {"raw": "<p>Old body</p>"},
	"excerpt": {"raw": "Old summary"},
	"template": "page-templates/landing.php",
	"parent": 0,
	"terms": [],
	"seo": {"title": "Old SEO title", "description": "Old SEO description"},
	"meta": {
		"_edit_lock": "1710000000:1",
		"_transient_elementor_css": "cached at some point",
		"_elementor_data": "[{\"id\":\"a1\",\"settings\":{\"text\":\"Old headline\"}}]",
		"_elementor_edit_mode": "builder",
		"_wp_page_template": "page-templates/landing.php",
		"_yoast_wpseo_title": "Old SEO title"
	}
}`

// builderPage is what get_page_structure returns for a page with a nested
// builder tree.
const builderPage = `{
	"id": 812,
	"builder": "elementor",
	"elements": [
		{"id": "sec1", "elType": "section", "elements": [
			{"id": "a1", "elType": "widget", "settings": {"text": "Old headline"}},
			{"id": "b2", "elType": "widget", "settings": {"html": "<p>Keep this paragraph</p>"}}
		]}
	]
}`

// approvalStateSession answers WordPress reads from canned state and records
// every remote call, so a test can see what the policy read and prove that it
// never wrote.
type approvalStateSession struct {
	*fakeRuneSession
	post         string
	postErr      error
	structure    string
	structureErr error
	calls        []string
}

func (s *approvalStateSession) Call(_ context.Context, name string, _ json.RawMessage) (RuneCallResult, error) {
	s.calls = append(s.calls, name)
	switch name {
	case "get_content":
		if s.postErr != nil {
			return RuneCallResult{}, s.postErr
		}
		return RuneCallResult{Content: s.post}, nil
	case "get_page_structure":
		if s.structureErr != nil {
			return RuneCallResult{}, s.structureErr
		}
		return RuneCallResult{Content: s.structure}, nil
	}
	return RuneCallResult{Content: `{"ok":true}`}, nil
}

func newStateSession(post string) *approvalStateSession {
	return &approvalStateSession{fakeRuneSession: &fakeRuneSession{}, post: post, structure: builderPage}
}

func (s *approvalStateSession) called(name string) bool {
	for _, call := range s.calls {
		if call == name {
			return true
		}
	}
	return false
}

func prepareWP(t *testing.T, session RuneSession, tool, args string) CMSApprovalProposal {
	t.Helper()
	proposal, err := PrepareCMSApproval(context.Background(), session, "wordpress", tool, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s %s: %v", tool, args, err)
	}
	return proposal
}

// TestPrepareCMSApprovalUnknownDryRunNeverBypasses proves an unsupported flag
// cannot be used to skip the gate: those tools ignore the argument remotely and
// would still write.
func TestPrepareCMSApprovalUnknownDryRunNeverBypasses(t *testing.T) {
	session := newStateSession(publishedPage)
	for _, testCase := range []struct{ tool, args string }{
		{"wp__update_content", `{"id":812,"content":"New body","dry_run":true}`},
		{"wp__publish_content", `{"title":"New","status":"publish","dry_run":true}`},
		{"wp__delete_content", `{"id":812,"dry_run":true}`},
		{"wp__set_seo", `{"id":812,"fields":{"title":"x"},"dry_run":true}`},
		{"wp__save_menu", `{"menu":"primary","name":"Primary","dry_run":true}`},
		{"wp__clear_cache", `{"dry_run":true}`},
		{"wp__moderate_comment", `{"comment_id":9,"action":"spam","dry_run":true}`},
		{"wp__upload_media", `{"filename":"hero.png","source_url":"https://cdn.example.com/hero.png","dry_run":true}`},
	} {
		proposal := prepareWP(t, session, testCase.tool, testCase.args)
		if !proposal.Required {
			t.Errorf("%s %s has no dry_run flag, so it must still need approval", testCase.tool, testCase.args)
		}
	}
	// The exempt set only ever names reviewed catalogue tools.
	for name := range wpDryRunTools {
		if _, known := lookupWordPressTool(name); !known {
			t.Errorf("%q is exempt from dry_run but is not a reviewed tool", name)
		}
	}
	for name := range wpDryRunTools {
		session := newStateSession(publishedPage)
		proposal := prepareWP(t, session, "wp__"+name, `{"id":812,"dry_run":true}`)
		if proposal.Required || len(session.calls) != 0 {
			t.Errorf("%s dry_run must need no approval and no read: %+v", name, proposal)
		}
	}
}

// TestPrepareCMSApprovalPublishedPostIsSnapshottedAndRevalidated proves a
// published target is approved against the state the write replaces, and that
// a builder-only or status-only change under it produces a different snapshot.
func TestPrepareCMSApprovalPublishedPostIsSnapshottedAndRevalidated(t *testing.T) {
	args := `{"id":812,"content":"New body"}`
	first := prepareWP(t, newStateSession(publishedPage), "wp__update_content", args)
	if !first.Required {
		t.Fatalf("a published post edit must need approval: %+v", first)
	}
	if len(first.Snapshot) == 0 {
		t.Fatalf("a published post edit must carry the state it replaces: %+v", first)
	}
	for _, want := range []string{`"status":"publish"`, `"digest":"sha256:`, `"kind":"wordpress_post"`} {
		if !strings.Contains(string(first.Snapshot), want) {
			t.Errorf("snapshot %s must contain %s", first.Snapshot, want)
		}
	}
	for _, want := range []string{"status: publish", "Landing page", "Old body"} {
		if !strings.Contains(first.Before, want) {
			t.Errorf("before text %q must show %q", first.Before, want)
		}
	}
	if !strings.Contains(first.After, "New body") {
		t.Errorf("after text must disclose the new content: %q", first.After)
	}

	// Resuming the approved call reproduces the same snapshot.
	same := prepareWP(t, newStateSession(publishedPage), "wp__update_content", args)
	if string(same.Snapshot) != string(first.Snapshot) {
		t.Errorf("unchanged post must compare equal: %s vs %s", same.Snapshot, first.Snapshot)
	}

	// A builder-only change under the same post is a different state.
	builderChanged := strings.Replace(publishedPage, "Old headline", "New headline", 1)
	changed := prepareWP(t, newStateSession(builderChanged), "wp__update_content", args)
	if string(changed.Snapshot) == string(first.Snapshot) {
		t.Error("a changed builder data value must not compare equal to the approved snapshot")
	}
	// So is a different status, excerpt, template or SEO field.
	for _, changed := range []string{
		strings.Replace(publishedPage, `"status": "publish"`, `"status": "private"`, 1),
		strings.Replace(publishedPage, "Old summary", "New summary", 1),
		strings.Replace(publishedPage, "page-templates/landing.php", "page-templates/plain.php", 1),
		strings.Replace(publishedPage, "Old SEO description", "New SEO description", 1),
	} {
		proposal := prepareWP(t, newStateSession(changed), "wp__update_content", args)
		if string(proposal.Snapshot) == string(first.Snapshot) {
			t.Errorf("a changed page must not compare equal: %s", changed)
		}
	}
	// A volatile edit lock or cache is not a change worth re-approving.
	volatile := strings.Replace(publishedPage, "1710000000:1", "1710099999:9", 1)
	volatile = strings.Replace(volatile, "cached at some point", "cached later", 1)
	proposal := prepareWP(t, newStateSession(volatile), "wp__update_content", args)
	if string(proposal.Snapshot) != string(first.Snapshot) {
		t.Errorf("an edit lock or cache must not change the comparison: %s", proposal.Snapshot)
	}
}

// TestPrepareCMSApprovalUnreadablePostBlocksWrite proves a read that cannot
// produce comparable state blocks the write instead of approving it blind.
func TestPrepareCMSApprovalUnreadablePostBlocksWrite(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		session  *approvalStateSession
		tool     string
		args     string
		wantRead bool
	}{
		{"read error", &approvalStateSession{fakeRuneSession: &fakeRuneSession{}, postErr: errors.New("boom")}, "wp__update_content", `{"id":812,"content":"x"}`, true},
		{"tool error", &approvalStateSession{fakeRuneSession: &fakeRuneSession{}, post: `{"error":"not found"}`}, "wp__update_content", `{"id":812,"content":"x"}`, false},
		{"malformed json", &approvalStateSession{fakeRuneSession: &fakeRuneSession{}, post: `{oops`}, "wp__update_content", `{"id":812,"content":"x"}`, false},
		{"empty object", &approvalStateSession{fakeRuneSession: &fakeRuneSession{}, post: `{}`}, "wp__update_content", `{"id":812,"content":"x"}`, false},
		{"no comparable fields", &approvalStateSession{fakeRuneSession: &fakeRuneSession{}, post: `{"ok":true}`}, "wp__update_content", `{"id":812,"content":"x"}`, false},
		{"delete with no readable post", &approvalStateSession{fakeRuneSession: &fakeRuneSession{}, postErr: errors.New("boom")}, "wp__delete_content", `{"id":812,"force":true}`, true},
	} {
		proposal, err := PrepareCMSApproval(context.Background(), testCase.session, "wordpress", testCase.tool, json.RawMessage(testCase.args))
		if err == nil {
			t.Errorf("%s: an unreadable target must block, got %+v", testCase.name, proposal)
		}
		if proposal.Required || len(proposal.Snapshot) != 0 || proposal.Target != "" {
			t.Errorf("%s: a blocked write must not read as approvable: %+v", testCase.name, proposal)
		}
		if testCase.session.called("update_content") || testCase.session.called("delete_content") {
			t.Errorf("%s: the write must not be attempted", testCase.name)
		}
		if !testCase.session.called("get_content") && testCase.wantRead {
			t.Errorf("%s: the post read must be attempted", testCase.name)
		}
	}
}

// TestPrepareCMSApprovalDraftExemptionStaysNarrow proves only a proven draft
// edit runs free, and that publishing or a shared effect is never excused by
// the read.
func TestPrepareCMSApprovalDraftExemptionStaysNarrow(t *testing.T) {
	draft := strings.Replace(publishedPage, `"status": "publish"`, `"status": "draft"`, 1)
	exempt := prepareWP(t, newStateSession(draft), "wp__update_content", `{"id":812,"content":"New body"}`)
	if exempt.Required {
		t.Fatalf("a draft-only edit must not need approval: %+v", exempt)
	}
	if len(exempt.Snapshot) == 0 {
		t.Errorf("a draft edit still needs its snapshot: %+v", exempt)
	}
	for _, testCase := range []struct {
		name string
		tool string
		args string
	}{
		{"publishing a draft", "wp__update_content", `{"id":812,"status":"publish"}`},
		{"scheduling a draft", "wp__update_content", `{"id":812,"date":"2030-01-01 09:00"}`},
		{"status as a raw value", "wp__update_content", `{"id":812,"status":"publish"}`},
		{"shared term SEO", "wp__set_seo", `{"object_type":"term","taxonomy":"category","id":812,"fields":{"description":"x"}}`},
		{"publishing through a builder write", "wp__edit_page_element", `{"id":812,"element_id":"a1","status":"publish","settings":{"text":"x"}}`},
	} {
		proposal := prepareWP(t, newStateSession(draft), testCase.tool, testCase.args)
		if !proposal.Required {
			t.Errorf("%s: %s must need approval: %+v", testCase.name, testCase.args, proposal)
		}
	}
}

// TestPrepareCMSApprovalBuilderEditPreviewsTargetText proves a builder write
// shows the element copy it replaces and the change it makes, and that a failed
// structure read only costs the preview.
func TestPrepareCMSApprovalBuilderEditPreviewsTargetText(t *testing.T) {
	session := newStateSession(publishedPage)
	edit := prepareWP(t, session, "wp__edit_page_element", `{"id":812,"element_id":"a1","settings":{"text":"New headline"}}`)
	if !edit.Required {
		t.Fatalf("a builder edit on a published page must need approval: %+v", edit)
	}
	if !strings.Contains(edit.Before, "element a1 currently: Old headline") {
		t.Errorf("before text must show the element it replaces: %q", edit.Before)
	}
	if !strings.Contains(edit.Before, "Landing page") {
		t.Errorf("before text must still show the page: %q", edit.Before)
	}
	if !strings.Contains(edit.After, "New headline") {
		t.Errorf("after text must show the new copy: %q", edit.After)
	}
	if strings.Contains(edit.After, "element_id") {
		t.Errorf("after text must carry the change, not the raw arguments: %q", edit.After)
	}
	if !session.called("get_page_structure") {
		t.Error("a builder edit must read the page structure for its element")
	}

	remove := prepareWP(t, session, "wp__delete_page_element", `{"id":812,"element_id":"a1"}`)
	if !strings.Contains(remove.Before, "Old headline") {
		t.Errorf("before text must show the element being removed: %q", remove.Before)
	}
	if !strings.Contains(remove.After, "removed from the page") {
		t.Errorf("after text must say what happens: %q", remove.After)
	}
	if strings.Contains(strings.ToLower(remove.After), "undo") {
		t.Errorf("a removal must not invent an undo: %q", remove.After)
	}

	// A structure read that fails must not block a snapshotted write.
	broken := &approvalStateSession{fakeRuneSession: &fakeRuneSession{}, post: publishedPage, structureErr: errors.New("boom")}
	fallback := prepareWP(t, broken, "wp__edit_page_element", `{"id":812,"element_id":"a1","settings":{"text":"New headline"}}`)
	if !fallback.Required || len(fallback.Snapshot) == 0 {
		t.Errorf("a failed structure read must still snapshot the post: %+v", fallback)
	}
	if strings.Contains(fallback.Before, "element a1 currently") {
		t.Errorf("no element text may be invented from a failed read: %q", fallback.Before)
	}

	// An element id the page does not have is not a reason to invent text.
	missing := prepareWP(t, session, "wp__edit_page_element", `{"id":812,"element_id":"zz9","settings":{"text":"x"}}`)
	if !missing.Required || len(missing.Snapshot) == 0 {
		t.Errorf("an unknown element must still be approved and snapshotted: %+v", missing)
	}
}

// TestPrepareCMSApprovalGlobalActionsDiscloseExactArgs keeps a call with no
// readable pre-write state honest: exact arguments, no invented state.
func TestPrepareCMSApprovalGlobalActionsDiscloseExactArgs(t *testing.T) {
	session := newStateSession(publishedPage)
	proposal := prepareWP(t, session, "wp__save_menu", `{"menu":"primary","name":"Primary menu","locations":["main"]}`)
	if !proposal.Required {
		t.Fatalf("a menu write must need approval: %+v", proposal)
	}
	if proposal.Before != "" || len(proposal.Snapshot) != 0 {
		t.Errorf("a global action has no pre-write state to show: %+v", proposal)
	}
	for _, want := range []string{"menu: primary", "Primary menu", "main"} {
		if !strings.Contains(proposal.After, want) {
			t.Errorf("after text must disclose %q exactly: %q", want, proposal.After)
		}
	}
	// Deleting content names a post, so it is approved against that post.
	remove := prepareWP(t, session, "wp__delete_content", `{"id":812,"force":true}`)
	if !remove.Required || len(remove.Snapshot) == 0 {
		t.Errorf("a delete must be approved against the post it removes: %+v", remove)
	}
	if !strings.Contains(remove.Before, "Landing page") {
		t.Errorf("a delete must show the content it removes: %q", remove.Before)
	}
	// A draft-post write that names no post has no state to read either.
	unaddressed := prepareWP(t, session, "wp__update_content", `{"content":"New body"}`)
	if !unaddressed.Required || len(unaddressed.Snapshot) != 0 || unaddressed.Before != "" {
		t.Errorf("a write naming no post must disclose its arguments only: %+v", unaddressed)
	}
	if !strings.Contains(unaddressed.After, "New body") {
		t.Errorf("the arguments must still be disclosed: %q", unaddressed.After)
	}
	// post_id names the same post as id, so it is read and snapshotted too.
	byPostID := prepareWP(t, session, "wp__update_content", `{"post_id":812,"content":"New body"}`)
	if len(byPostID.Snapshot) == 0 {
		t.Errorf("a post_id must be snapshotted like an id: %+v", byPostID)
	}
}
