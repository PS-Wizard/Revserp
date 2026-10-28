package locationsetup

import (
	"testing"

	"github.com/ps-wizard/revserp/internal/localvisibility"
)

func stepMap(statuses ...string) map[string]string {
	m := map[string]string{}
	for i, id := range OrderedSteps {
		if i < len(statuses) {
			m[id] = statuses[i]
		} else {
			m[id] = StepPending
		}
	}
	return m
}

func TestOverallStatus(t *testing.T) {
	completed := map[string]string{}
	for _, id := range OrderedSteps {
		completed[id] = StepCompleted
	}
	if got := OverallStatus(completed); got != StatusCompleted {
		t.Fatalf("all completed = %q, want completed", got)
	}
	mixed := completed
	mixed[StepAIVisibility] = StepSkipped
	if got := OverallStatus(mixed); got != StatusCompleted {
		t.Fatalf("completed+skipped = %q, want completed", got)
	}
	running := stepMap(StepCompleted, StepCompleted, StepRunning, StepPending, StepPending, StepPending, StepPending)
	if got := OverallStatus(running); got != StatusRunning {
		t.Fatalf("partial = %q, want running", got)
	}
	failed := completed
	failed[StepMapsVisibility] = StepFailed
	if got := OverallStatus(failed); got != StatusFailed {
		t.Fatalf("one failed = %q, want failed", got)
	}
}

func TestLaunchableBranchesAreIndependent(t *testing.T) {
	steps := stepMap(StepCompleted, StepCompleted, StepCompleted, StepCompleted, StepCompleted, StepFailed, StepCompleted)
	_, questions, mapQueries, aiAudit, mapsRun := Launchable(steps)
	if questions || mapQueries || mapsRun {
		t.Fatalf("finished branches relaunch: q=%v m=%v maps=%v", questions, mapQueries, mapsRun)
	}
	if !aiAudit {
		t.Fatal("failed AI branch does not relaunch")
	}
}

func TestLaunchableWaitsForProfile(t *testing.T) {
	steps := stepMap(StepCompleted, StepRunning, StepPending, StepPending, StepPending, StepPending, StepPending)
	profile, questions, mapQueries, aiAudit, mapsRun := Launchable(steps)
	if !profile || questions || mapQueries || aiAudit || mapsRun {
		t.Fatalf("branches launch before profile: %v %v %v %v %v", profile, questions, mapQueries, aiAudit, mapsRun)
	}
}

func TestDiscloseMapsCredits(t *testing.T) {
	if got := DiscloseMapsCredits(0); got != int32(MaxDisclosedMapsCredits) {
		t.Fatalf("empty disclosure = %d, want %d", got, MaxDisclosedMapsCredits)
	}
	if MaxDisclosedMapsCredits != localvisibility.MapQueryStarterLimit*localvisibility.GridPointCount*localvisibility.MapsCreditsPerCall {
		t.Fatalf("ceiling %d != starter cap x grid x cost", MaxDisclosedMapsCredits)
	}
	if got := DiscloseMapsCredits(5); got != 5*9*3 {
		t.Fatalf("saved disclosure = %d, want 135", got)
	}
}

func TestDifferencesAreFirstWriteWins(t *testing.T) {
	if got := ApplyDifferences("", "downtown, halal"); got != "downtown, halal" {
		t.Fatalf("empty store takes input, got %q", got)
	}
	if got := ApplyDifferences("downtown", "uptown"); got != "downtown" {
		t.Fatalf("stored differences overwritten, got %q", got)
	}
	if _, err := NormalizeDifferences(string(make([]byte, MaxDifferencesLength+1))); err == nil {
		t.Fatal("overlong differences accepted")
	}
}
