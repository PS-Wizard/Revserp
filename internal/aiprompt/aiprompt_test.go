package aiprompt

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ps-wizard/revserp/internal/aichattools"
)

func TestComposeSystemPromptBlankDeltaReturnsBaseExactly(t *testing.T) {
	tests := []struct {
		name           string
		useInternal    bool
		internalPrompt string
		externalPrompt string
	}{
		{"both empty", false, "", ""},
		{"whitespace external", false, "", " \n\t "},
		{"whitespace internal", true, "  \n ", ""},
		{"whitespace both", true, "\t", "  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComposeSystemPrompt(tt.useInternal, tt.internalPrompt, tt.externalPrompt); got != DefaultSystemPrompt {
				t.Errorf("ComposeSystemPrompt() = %q, want exactly the base prompt", got)
			}
		})
	}
}

// The delta must never be able to replace the code-owned base. If this fails,
// an admin value can silently delete the accuracy, safety, and citation rules.
func TestComposeSystemPromptAppendsDeltaAndKeepsBase(t *testing.T) {
	tests := []struct {
		name           string
		useInternal    bool
		internalPrompt string
		externalPrompt string
		wantDelta      string
	}{
		{"external appended", false, "internal delta", "external delta", "external delta"},
		{"internal appended", true, "internal delta", "external delta", "internal delta"},
		{"internal wins when selected", true, "internal delta", "", "internal delta"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComposeSystemPrompt(tt.useInternal, tt.internalPrompt, tt.externalPrompt)
			if !strings.HasPrefix(got, DefaultSystemPrompt) {
				t.Fatal("composed prompt does not start with the base prompt")
			}
			if !strings.Contains(got, AudienceDeltaHeader) {
				t.Fatalf("composed prompt missing the audience header: %q", got)
			}
			if !strings.HasSuffix(got, tt.wantDelta) {
				t.Errorf("composed prompt delta = %q, want %q", got, tt.wantDelta)
			}
			if strings.Contains(got, "internal delta") && tt.wantDelta == "external delta" {
				t.Error("the unselected audience delta leaked into the prompt")
			}
		})
	}
}

// Per-tool behaviour belongs in the tool's own description, which the provider
// filters to the tools a workspace may call. A tool named here would be
// documented even for a workspace that has that tool disabled.
func TestDefaultSystemPromptNamesNoTool(t *testing.T) {
	for _, def := range aichattools.CatalogDefs() {
		if strings.Contains(DefaultSystemPrompt, def.Name) {
			t.Errorf("base prompt names the tool %q; move that text into its Def.Description", def.Name)
		}
	}
}

// A hand-maintained count is the drift this architecture exists to prevent.
func TestDefaultSystemPromptStatesNoToolCount(t *testing.T) {
	countWord := regexp.MustCompile(`(?i)\b(one|two|three|four|five|six|seven|eight|nine|ten|\d+)\s+(data\s+)?tools?\b`)
	if match := countWord.FindString(DefaultSystemPrompt); match != "" {
		t.Errorf("base prompt states a tool count (%q); the enabled set is per workspace and changes at runtime", match)
	}
}

func TestUserControlPolicyVersion(t *testing.T) {
	if Version != "chat-v7" {
		t.Fatalf("Version = %q, want chat-v7", Version)
	}
	if !strings.Contains(DefaultSystemPrompt, "## User control") {
		t.Fatal("base prompt missing the user control section")
	}
	for _, def := range aichattools.CatalogDefs() {
		if strings.Contains(DefaultSystemPrompt, def.Label) {
			t.Errorf("base prompt inventories tool label %q; per-tool text belongs in Def.Description", def.Label)
		}
	}
}

func TestBasePromptEvidenceContract(t *testing.T) {
	wants := []string{
		"Answer the latest user message first.",
		"already returned in this conversation when they cover it",
		"only when the evidence is missing or a refresh is needed",
		"Never guess.",
		"reuse actual results",
		"history text alone is background, not a fresh fact",
		"explicitly asks",
		"scope changed",
		"state changed",
		"a write needs a precondition",
		"not yet fetched",
		"For a recap, do not re-verify everything",
	}
	for _, want := range wants {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("base prompt missing evidence phrase %q", want)
		}
	}
	if strings.Contains(DefaultSystemPrompt, "call the correct data tool instead of guessing") {
		t.Error("base prompt must not order a fresh call where conversation evidence answers")
	}
}

func TestBasePromptVerificationStates(t *testing.T) {
	wants := []string{
		"what was changed or applied",
		"what is verified in live output",
		"what a newer crawl cleared",
		"a successful write never means the underlying problem is cleared",
		"Name a blocker once, with its next action",
		"without forced headings or expanding it sitewide",
		"only when a tool result shows it",
		"otherwise say not verified yet",
	}
	for _, want := range wants {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("base prompt missing verification phrase %q", want)
		}
	}
	if strings.Contains(DefaultSystemPrompt, "changed since the earlier result") {
		t.Error("verification must state what changed, not freshness against an earlier result")
	}
}

func TestBasePromptDraftReviewNeverAuthorizesPublishedChange(t *testing.T) {
	wants := []string{
		"never authorizes changing published content",
		"ask before writing anything",
		"Never unpublish a live original",
		"not permission to ignore a draft-only intent",
	}
	for _, want := range wants {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("base prompt missing draft-only phrase %q", want)
		}
	}
	for _, want := range []string{"guarantee", "enforce"} {
		if strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("base prompt must not promise hard draft %s; enforcement is deferred", want)
		}
	}
}

func TestBasePromptPrefersFindingSpecificGuidanceOverGenericAdvice(t *testing.T) {
	wants := []string{
		"Revserp-specific steps",
		"exact evidence its detector returned",
		"not generic advice",
		"never demand repeat checks for a recap",
	}
	for _, want := range wants {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("base prompt missing repair-guidance phrase %q", want)
		}
	}
}

func TestComposeSystemPromptKeepsContractForBothAudiences(t *testing.T) {
	tests := []struct {
		name           string
		useInternal    bool
		internalPrompt string
		externalPrompt string
		wantDelta      string
	}{
		{"external keeps contract", false, "internal delta", "external delta", "external delta"},
		{"internal keeps contract", true, "internal delta", "external delta", "internal delta"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComposeSystemPrompt(tt.useInternal, tt.internalPrompt, tt.externalPrompt)
			if !strings.HasPrefix(got, DefaultSystemPrompt) {
				t.Fatal("composed prompt does not start with the base prompt")
			}
			for _, want := range []string{"## Evidence and verification", "## Drafts and published content", "## User control"} {
				if !strings.Contains(got, want) {
					t.Errorf("composed prompt lost base section %q", want)
				}
			}
			if !strings.HasSuffix(got, tt.wantDelta) {
				t.Errorf("composed prompt delta suffix = %q, want %q", got, tt.wantDelta)
			}
		})
	}
}

func TestUserControlAsksOnlyForMaterialDecisions(t *testing.T) {
	wants := []string{
		"sensible reversible or read-only choices",
		"Infer the selected scope from the provided context",
		"only when a missing material preference or decision",
		"cannot be resolved safely",
		"changes the outcome",
		"a meaningful decision still requires asking",
		"never re-ask",
		"never consent",
		"never authorize writing, changing, or publishing anything",
		"independent safe analysis",
	}
	for _, want := range wants {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("base prompt missing user-control phrase %q", want)
		}
	}
}

func TestUserControlScopedIssueHelpContract(t *testing.T) {
	wants := []string{
		"review all recorded issue types in the chosen scope",
		"prioritized rundown",
		"Group repeated problems",
		"prioritize real evidence-based fixes",
		"Report partial coverage explicitly",
		"never require a five-URL sample",
		"never pretend every page's content was inspected",
	}
	for _, want := range wants {
		if !strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("base prompt missing scoped-help phrase %q", want)
		}
	}
	for _, want := range []string{"up to 50", "capped at 30", "50 selected"} {
		if strings.Contains(DefaultSystemPrompt, want) {
			t.Errorf("base prompt carries per-tool limit %q; limits belong in Def.Description", want)
		}
	}
}
