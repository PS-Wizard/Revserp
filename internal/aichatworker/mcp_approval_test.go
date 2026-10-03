package aichatworker

import (
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/aichattools"
)

func TestProposalSummaryBoundsDisplayText(t *testing.T) {
	summary := proposalSummary(aichattools.MCPApprovalProposal{
		Service: "custom", Tool: "save_item",
		Target: strings.Repeat("t", approvalTargetMax+10),
		After:  strings.Repeat("a", approvalAfterMax+10),
	})
	if len([]rune(summary.target)) != approvalTargetMax || len([]rune(summary.after)) != approvalAfterMax {
		t.Fatalf("summary = %+v, want bounded display text", summary)
	}
}

func TestSnapshotsEqualCanonical(t *testing.T) {
	for _, blank := range []string{"", "  ", "{}", "null"} {
		if !snapshotsEqualCanonical([]byte(blank), []byte("{}")) {
			t.Errorf("blank %q != blank", blank)
		}
	}
	if snapshotsEqualCanonical([]byte(`{"digest":"sha256:a"}`), []byte(`{"digest":"sha256:b"}`)) {
		t.Error("different snapshots compare equal")
	}
	if !snapshotsEqualCanonical([]byte(`{"digest":"sha256:a"}`), []byte("{\n  \"digest\": \"sha256:a\"\n}")) {
		t.Error("semantically equal snapshots compare different")
	}
}

func TestCanonicalArgsEqual(t *testing.T) {
	if !canonicalArgsEqual([]byte(`{"a":1,"b":2}`), `{"b":2,"a":1}`) {
		t.Error("reordered args compare different")
	}
	if canonicalArgsEqual([]byte(`{"a":1}`), `{"a":2}`) {
		t.Error("different args compare equal")
	}
	if canonicalArgsEqual([]byte(`oops`), `{"a":1}`) {
		t.Error("malformed stored args compare equal")
	}
}

func TestBlockedMCPCallResultSurfacesReason(t *testing.T) {
	call := ai.ToolCall{ID: "call-1", Name: "mcp_alias"}
	status, result := blockedMCPCallResult(call, `allow prerequisite read tool "fetch_items" to perform safety check`)
	if status != "failed" {
		t.Fatalf("status = %q, want failed", status)
	}
	if !strings.Contains(result.Content, "allow prerequisite read tool") {
		t.Fatalf("blocked result hides the prerequisite: %q", result.Content)
	}
	if !strings.Contains(result.Content, "was not performed") {
		t.Fatalf("blocked result misses the no-dispatch promise: %q", result.Content)
	}
	longStatus, longResult := blockedMCPCallResult(call, strings.Repeat("x", 600))
	if longStatus != "failed" || !strings.Contains(longResult.Content, "[truncated]") {
		t.Fatal("unbounded helper reason accepted")
	}
}
