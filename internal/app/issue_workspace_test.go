package app

import "testing"

func TestApplyWorkspaceWorkOnlyCreditsVerifiedAttempt(t *testing.T) {
	t.Parallel()
	current := "current-crawl"
	base := func() []workspaceDiffRow {
		baselineID := "baseline-issue"
		return []workspaceDiffRow{{
			URL: "https://example.com/a", Pillar: "seo", Bucket: "serp_metadata",
			IssueType: "missing_title", BaselineIssueID: &baselineID, ChangeType: "no_longer_detected",
		}}
	}

	t.Run("successful current attempt becomes fixed", func(t *testing.T) {
		rows := base()
		applyWorkspaceWork(rows, []workspaceWorkRow{{
			URL: rows[0].URL, Pillar: rows[0].Pillar, Bucket: rows[0].Bucket, IssueType: rows[0].IssueType,
			Status: "fixed", VerificationCrawlID: &current,
		}}, current)
		if rows[0].ChangeType != "fixed" {
			t.Fatalf("got %q", rows[0].ChangeType)
		}
	})

	t.Run("unrecorded disappearance is not fixed", func(t *testing.T) {
		rows := base()
		applyWorkspaceWork(rows, nil, current)
		if rows[0].ChangeType != "no_longer_detected" {
			t.Fatalf("got %q", rows[0].ChangeType)
		}
	})

	t.Run("old verification does not credit current crawl", func(t *testing.T) {
		rows := base()
		old := "old-crawl"
		applyWorkspaceWork(rows, []workspaceWorkRow{{
			URL: rows[0].URL, Pillar: rows[0].Pillar, Bucket: rows[0].Bucket, IssueType: rows[0].IssueType,
			Status: "fixed", VerificationCrawlID: &old,
		}}, current)
		if rows[0].ChangeType != "no_longer_detected" {
			t.Fatalf("got %q", rows[0].ChangeType)
		}
	})

	t.Run("resolved duplicate group and new relationship are separate", func(t *testing.T) {
		baselineID, currentIssueID := "baseline-group-issue", "current-group-issue"
		rows := []workspaceDiffRow{{
			URL: "https://example.com/a", Pillar: "seo", Bucket: "content_quality", IssueType: "exact_duplicate_content",
			BaselineIssueID: &baselineID, CurrentIssueID: &currentIssueID, ChangeType: "still_open",
		}}
		rows = applyWorkspaceWork(rows, []workspaceWorkRow{{
			URL: rows[0].URL, SubjectKind: "group", Pillar: rows[0].Pillar, Bucket: rows[0].Bucket, IssueType: rows[0].IssueType,
			Status: "fixed", VerificationCrawlID: &current,
		}}, current)
		if len(rows) != 2 || rows[0].ChangeType != "fixed" || rows[1].ChangeType != "new" {
			t.Fatalf("got %#v", rows)
		}
	})
}
