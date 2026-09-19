package sqlc

import (
	"strings"
	"testing"
)

// TestReclaimStaleRunningAIWorkerJobsFailsSetupSteps guards the hand-written SQL
// against accidental regeneration that drops the setup recovery side effect.
// The query is durable SQL, not Go, so the test asserts its shape directly.
func TestReclaimStaleRunningAIWorkerJobsFailsSetupSteps(t *testing.T) {
	for _, want := range []string{
		"UPDATE ai_worker_jobs",
		"UPDATE project_setup",
		"WHERE status = 'running'",
		"WHEN 'business_profile_bootstrap' THEN 'profile_generation'",
		"WHEN 'prompt_generation' THEN 'prompt_generation'",
		"WHEN 'visibility_run' THEN 'visibility'",
		"AND ps.status = CASE stale.job_type",
		"'reclaimed: worker restarted during setup; retry'",
	} {
		if !strings.Contains(reclaimStaleRunningAIWorkerJobs, want) {
			t.Errorf("reclaim SQL missing %q", want)
		}
	}
}

// TestReclaimStaleRunningCrawlsFailsSetup guards the crawl reclaim CTEs: a
// crashed crawl worker must fail its linked active setup too, or the setup
// would stay active forever.
func TestReclaimStaleRunningCrawlsFailsSetup(t *testing.T) {
	for _, want := range []string{
		"UPDATE crawls",
		"UPDATE project_setup",
		"failed_step = 'crawling'",
		"WHERE ps.crawl_id = stale.id",
		"AND ps.status = 'crawling'",
		"'crawl reclaimed after worker restart'",
	} {
		if !strings.Contains(reclaimStaleRunningCrawls, want) {
			t.Errorf("crawl reclaim SQL missing %q", want)
		}
	}
}
