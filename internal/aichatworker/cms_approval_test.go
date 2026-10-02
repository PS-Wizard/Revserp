package aichatworker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ps-wizard/revserp/internal/aichattools"
)

func TestCMSApprovalRequired(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"cms__create_record", true},
		{"cms__update_record", true},
		{"cms__list_records", false},
		{"cms__read_record", false},
		{"cms__list_collections", false},
		{"cms__get_collection_schema", false},
		{"wp__publish_content", true},
		{"wp__update_content", true},
		{"wp__get_content", false},
		{"wp__list_content", false},
		// A discovered tool outside the catalogue has no approval policy, so it
		// executes directly; its write-uncertainty and no-retry recovery guards
		// still apply.
		{"cms__brand_new_thing", false},
		{"wp__brand_new_thing", false},
		{"read_issues", false},
		{"get_score_summary", false},
		{"unknown_tool", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := cmsApprovalRequired(tc.name); got != tc.want {
			t.Errorf("cmsApprovalRequired(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestToolArgsForLogRedactsWordPress(t *testing.T) {
	if got := toolArgsForLog("wp__update_content", `{"content":"customer secret"}`); got != "[redacted]" {
		t.Errorf("wp args logged as %q, want redaction", got)
	}
	if got := toolArgsForLog("cms__update_record", `{"data":"x"}`); got != "[redacted]" {
		t.Errorf("cms args logged as %q, want redaction", got)
	}
	if got := toolArgsForLog("read_issues", `{"limit":5}`); got != `{"limit":5}` {
		t.Errorf("native args = %q", got)
	}
}

func TestPrepareCMSApprovalRune(t *testing.T) {
	ctx := context.Background()
	session := liveRuneSession()

	// Reads run without approval and without a proposal.
	read, err := aichattools.PrepareCMSApproval(ctx, session, "rune", "cms__read_record", json.RawMessage(`{"collection":"posts","id":"1"}`))
	if err != nil {
		t.Fatalf("read proposal: %v", err)
	}
	if read.Required {
		t.Error("rune read requires approval")
	}

	// Creates require approval with no remote read and no snapshot.
	created, err := aichattools.PrepareCMSApproval(ctx, session, "rune", "cms__create_record", json.RawMessage(`{"collection":"posts","data":{"title":"hi"}}`))
	if err != nil {
		t.Fatalf("create proposal: %v", err)
	}
	if !created.Required || created.Target == "" || created.After == "" {
		t.Errorf("create proposal = %+v", created)
	}
	if len(created.Snapshot) != 0 {
		t.Errorf("create snapshot = %s, want none", created.Snapshot)
	}
	if n := session.calls; n != 0 {
		t.Fatalf("create proposal made %d remote calls, want none", n)
	}

	// Updates preview against the record they replace.
	session.onCall = func(name string, args json.RawMessage) (aichattools.RuneCallResult, error) {
		if name != "read_record" {
			t.Errorf("proposal read %q, want read_record", name)
		}
		return aichattools.RuneCallResult{Content: `{"id":"1","title":"old"}`}, nil
	}
	updated, err := aichattools.PrepareCMSApproval(ctx, session, "rune", "cms__update_record", json.RawMessage(`{"collection":"posts","id":"1","data":{"title":"new"}}`))
	if err != nil {
		t.Fatalf("update proposal: %v", err)
	}
	if !updated.Required || updated.Before == "" || len(updated.Snapshot) == 0 {
		t.Errorf("update proposal = %+v", updated)
	}

	// Malformed arguments and unknown tools block the write.
	if _, err := aichattools.PrepareCMSApproval(ctx, session, "rune", "cms__update_record", json.RawMessage(`{oops`)); err == nil {
		t.Error("malformed args accepted")
	}
	// A discovered name outside the catalogue has no policy: it does not need
	// approval, and it does not error either.
	unlisted, err := aichattools.PrepareCMSApproval(ctx, session, "rune", "cms__brand_new_thing", json.RawMessage(`{}`))
	if err != nil {
		t.Errorf("unlisted rune tool blocked: %v", err)
	}
	if unlisted.Required {
		t.Error("unlisted rune tool requires approval")
	}
	if _, err := aichattools.PrepareCMSApproval(ctx, session, "drupal", "cms__create_record", json.RawMessage(`{}`)); err == nil {
		t.Error("unknown provider accepted")
	}
}

func TestSnapshotsEqualCanonical(t *testing.T) {
	if !snapshotsEqualCanonical(nil, nil) {
		t.Error("two absent snapshots must compare equal")
	}
	if !snapshotsEqualCanonical([]byte(`{}`), []byte(`null`)) {
		t.Error("empty-shaped snapshots must compare equal")
	}
	if !snapshotsEqualCanonical([]byte(`{"b":1,"a":[1,2]}`), []byte(`{"a":[1,2],"b":1}`)) {
		t.Error("key order must not matter")
	}
	if snapshotsEqualCanonical([]byte(`{"digest":"a"}`), []byte(`{"digest":"b"}`)) {
		t.Error("changed digests must compare unequal")
	}
	if snapshotsEqualCanonical(nil, []byte(`{"digest":"a"}`)) {
		t.Error("newly appeared snapshot must compare unequal")
	}
}

func TestCanonicalArgsEqual(t *testing.T) {
	if !canonicalArgsEqual([]byte(`{"a":1,"b":[1,2]}`), `{"b":[1,2],"a":1}`) {
		t.Error("key order must not matter")
	}
	if !canonicalArgsEqual([]byte(`{"n":1}`), `{"n":1.0}`) {
		t.Error("jsonb number normalization must not matter")
	}
	if canonicalArgsEqual([]byte(`{"a":1}`), `{"a":2}`) {
		t.Error("different args must not compare equal")
	}
	if canonicalArgsEqual([]byte(`{oops`), `{"a":1}`) {
		t.Error("malformed proposed args must fail closed")
	}
}
