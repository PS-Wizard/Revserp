package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues"
	"github.com/ps-wizard/revserp/internal/issues/shared"
	"github.com/ps-wizard/revserp/internal/schedule"
)

// Config holds all settings for a crawl Worker instance.
type Config struct {
	ManualConcurrency int
	ManualPoll        time.Duration

	AutoConcurrency       int
	AutoPoll              time.Duration
	AutoSchedulerInterval time.Duration

	CrawlPageWorkerCount int
	PageSpeedAPIKey      string
	CrawlMaxRetries      int
	CrawlRetryBase       time.Duration
	CrawlRetryMax        time.Duration
	CrawlTimeout         time.Duration
	MaxAPIResponseBytes  int64
}

// claimedCrawlRow is the minimal data the worker needs from any claim query.
type claimedCrawlRow struct {
	ID                pgtype.UUID
	ProjectID         pgtype.UUID
	RequestedByUserID pgtype.UUID
	ConfigSnapshot    []byte
	BaseURL           string
}

func claimedFromManual(row sqlc.ClaimNextQueuedCrawlManualRow) claimedCrawlRow {
	return claimedCrawlRow{
		ID:                row.ID,
		ProjectID:         row.ProjectID,
		RequestedByUserID: row.RequestedByUserID,
		ConfigSnapshot:    row.ConfigSnapshot,
		BaseURL:           row.BaseUrl,
	}
}

func claimedFromAuto(row sqlc.ClaimNextQueuedCrawlAutoRow) claimedCrawlRow {
	return claimedCrawlRow{
		ID:                row.ID,
		ProjectID:         row.ProjectID,
		RequestedByUserID: row.RequestedByUserID,
		ConfigSnapshot:    row.ConfigSnapshot,
		BaseURL:           row.BaseUrl,
	}
}

// Worker claims queued crawls from Postgres and runs them.
type Worker struct {
	pool     *pgxpool.Pool
	queries  *sqlc.Queries
	cfg      Config
	renderer *crawler.Renderer

	cancelsMu sync.Mutex
	cancels   map[pgtype.UUID]context.CancelFunc
}

// New builds a crawl worker.
func New(pool *pgxpool.Pool, cfg Config, renderer *crawler.Renderer) *Worker {
	if cfg.ManualConcurrency <= 0 {
		cfg.ManualConcurrency = 1
	}
	if cfg.ManualPoll <= 0 {
		cfg.ManualPoll = 2 * time.Second
	}
	if cfg.AutoConcurrency <= 0 {
		cfg.AutoConcurrency = 1
	}
	if cfg.AutoPoll <= 0 {
		cfg.AutoPoll = 2 * time.Second
	}
	if cfg.AutoSchedulerInterval <= 0 {
		cfg.AutoSchedulerInterval = time.Minute
	}
	if cfg.CrawlPageWorkerCount <= 0 {
		cfg.CrawlPageWorkerCount = crawler.DefaultWorkerCount
	}
	if cfg.CrawlTimeout <= 0 {
		cfg.CrawlTimeout = defaultCrawlTimeout
	}

	return &Worker{
		pool:     pool,
		queries:  sqlc.New(pool),
		cfg:      cfg,
		renderer: renderer,
		cancels:  make(map[pgtype.UUID]context.CancelFunc),
	}
}

// registerCrawlCancel records the cancel func for an in-flight crawl so it
// can be tripped from within this same process.
func (w *Worker) registerCrawlCancel(crawlID pgtype.UUID, cancel context.CancelFunc) {
	w.cancelsMu.Lock()
	w.cancels[crawlID] = cancel
	w.cancelsMu.Unlock()
}

// unregisterCrawlCancel drops the cancel func once a crawl finishes.
func (w *Worker) unregisterCrawlCancel(crawlID pgtype.UUID) {
	w.cancelsMu.Lock()
	delete(w.cancels, crawlID)
	w.cancelsMu.Unlock()
}

// CancelCrawl trips the in-memory cancel func for crawlID if this worker
// process is the one running it, and reports whether it found one.
//
// This only works when the caller and the running crawl are in the same
// process. The API and worker binaries run as separate processes
// (cmd/api vs cmd/worker), so a cancel request handled by the API cannot
// reach this map; the DB status UPDATE (status='cancelled') remains the
// cross-process signal, observed via the periodic progress-write status
// check in the crawler runner.
func (w *Worker) CancelCrawl(crawlID pgtype.UUID) bool {
	w.cancelsMu.Lock()
	cancel, ok := w.cancels[crawlID]
	w.cancelsMu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

const schedulerAdvisoryLockID = 1748290342

// staleRunningCrawlAge is how long a crawl may sit in 'running' before it is
// considered orphaned by a crashed worker process and reclaimed as failed.
// Multiple worker replicas can run concurrently (see the scheduler's
// advisory lock), so this must be age-gated rather than an unconditional
// reset on startup, which would kill another replica's in-flight crawl.
const staleRunningCrawlAge = 2 * time.Hour

// staleRunningCrawlGrace pads a reclaim cutoff past a crawl's own hard timeout.
//
// Both sweeps must only ever catch crawls whose worker died without writing a
// terminal status. Since the heartbeat was removed there is no liveness signal,
// so "orphaned" can only be inferred from age — and a crawl still inside its
// CrawlTimeout is legitimately running, not orphaned. The recurring sweep
// previously used a flat 15 minutes, which is *below* the 30-minute default
// timeout, so any crawl running longer than 15 minutes was marked failed while
// its worker was still crawling it. Deriving the cutoff from CrawlTimeout keeps
// that invariant true even when CRAWL_TIMEOUT is overridden.
//
// The cost is detection latency: a dead worker's crawl is not reclaimed until
// CrawlTimeout elapses. Restoring a heartbeat is the only way to detect a dead
// worker sooner without risking a live crawl.
const staleRunningCrawlGrace = 5 * time.Minute

// defaultCrawlTimeout bounds one crawl end-to-end. Without it a hung origin
// (slow headers, redirect loop, stalled PSI call) holds its worker slot forever,
// and with the default concurrency of 2 a pair of bad sites halts all crawling.
// It must stay under every reclaim cutoff so a timed-out crawl is marked failed
// by its own worker rather than reclaimed as orphaned; reclaimCutoff enforces
// that rather than leaving it to two constants drifting apart.
const defaultCrawlTimeout = 30 * time.Minute

// reclaimCutoff returns the age past which a 'running' crawl is treated as
// orphaned, never earlier than the crawl's own timeout plus grace. floor keeps
// the once-at-startup sweep as conservative as it has always been.
func (w *Worker) reclaimCutoff(floor time.Duration) time.Duration {
	cutoff := w.cfg.CrawlTimeout + staleRunningCrawlGrace
	if cutoff < floor {
		return floor
	}
	return cutoff
}

// Run starts all worker pools and scheduler until the context is canceled.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.queries.ReclaimStaleRunningCrawls(ctx, pgtype.Timestamptz{
		Time:  time.Now().UTC().Add(-w.reclaimCutoff(staleRunningCrawlAge)),
		Valid: true,
	}); err != nil {
		log.Printf("failed to reclaim stale running crawls: %v", err)
	}

	go w.runScheduler(ctx)

	done := make(chan struct{}, w.cfg.ManualConcurrency+w.cfg.AutoConcurrency)

	for i := range w.cfg.ManualConcurrency {
		go func() {
			w.runManualLoop(ctx, i+1)
			done <- struct{}{}
		}()
	}

	for i := range w.cfg.AutoConcurrency {
		go func() {
			w.runAutoLoop(ctx, i+1)
			done <- struct{}{}
		}()
	}

	for range w.cfg.ManualConcurrency + w.cfg.AutoConcurrency {
		<-done
	}

	return nil
}

// runManualLoop repeatedly claims and processes manual crawls.
func (w *Worker) runManualLoop(ctx context.Context, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		row, err := w.queries.ClaimNextQueuedCrawlManual(ctx)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if err := sleepOrCancel(ctx, w.cfg.ManualPoll); err != nil {
					return
				}
				continue
			}
			if isRunningCrawlConflict(err) {
				if err := sleepOrCancel(ctx, w.cfg.ManualPoll); err != nil {
					return
				}
				continue
			}
			log.Printf("manual worker %d transient error claiming crawl: %v", workerID, err)
			if err := sleepOrCancel(ctx, w.cfg.ManualPoll); err != nil {
				return
			}
			continue
		}

		claimed := claimedFromManual(row)
		log.Printf("manual worker %d claimed crawl: crawl_id=%s project_id=%s source=manual", workerID, claimed.ID.String(), claimed.ProjectID.String())
		if err := w.runCrawlGuarded(ctx, claimed); err != nil {
			log.Printf("manual worker %d crawl failed: crawl_id=%s error=%v", workerID, claimed.ID.String(), err)
			continue
		}
		log.Printf("manual worker %d crawl completed: crawl_id=%s", workerID, claimed.ID.String())
	}
}

// runAutoLoop repeatedly claims and processes auto crawls.
func (w *Worker) runAutoLoop(ctx context.Context, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		row, err := w.queries.ClaimNextQueuedCrawlAuto(ctx)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if err := sleepOrCancel(ctx, w.cfg.AutoPoll); err != nil {
					return
				}
				continue
			}
			log.Printf("auto worker %d transient error claiming crawl: %v", workerID, err)
			if err := sleepOrCancel(ctx, w.cfg.AutoPoll); err != nil {
				return
			}
			continue
		}

		claimed := claimedFromAuto(row)
		log.Printf("auto worker %d claimed crawl: crawl_id=%s project_id=%s source=auto", workerID, claimed.ID.String(), claimed.ProjectID.String())
		if err := w.runCrawlGuarded(ctx, claimed); err != nil {
			log.Printf("auto worker %d crawl failed: crawl_id=%s error=%v", workerID, claimed.ID.String(), err)
			continue
		}
		log.Printf("auto worker %d crawl completed: crawl_id=%s", workerID, claimed.ID.String())
	}
}

// runScheduler periodically enqueues auto crawls for due projects.
func (w *Worker) runScheduler(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.AutoSchedulerInterval)
	defer ticker.Stop()

	for {
		if err := w.sweepDueAutoCrawls(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Printf("auto-crawl scheduler sweep error: %v", err)
		}

		if err := w.queries.ReclaimStaleRunningCrawls(ctx, pgtype.Timestamptz{
			Time:  time.Now().UTC().Add(-w.reclaimCutoff(0)),
			Valid: true,
		}); err != nil {
			log.Printf("periodic reclaim of stale running crawls failed: %v", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) sweepDueAutoCrawls(ctx context.Context) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Acquire a transaction-scoped advisory lock so only one scheduler
	// instance sweeps at a time. pg_try_advisory_xact_lock releases
	// automatically when the transaction ends.
	var locked bool
	if err := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock($1)", schedulerAdvisoryLockID).Scan(&locked); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	if !locked {
		log.Printf("auto-crawl scheduler: advisory lock not acquired, another instance is sweeping")
		return tx.Rollback(ctx)
	}

	queries := w.queries.WithTx(tx)

	batchLimit := int32(100)

	settings, err := queries.ListDueAutoCrawlSettings(ctx, batchLimit)
	if err != nil {
		return fmt.Errorf("list due settings: %w", err)
	}

	if len(settings) == 0 {
		return tx.Rollback(ctx)
	}

	enqueued := 0
	for _, s := range settings {
		configSnapshot := s.ConfigSnapshot
		if len(configSnapshot) == 0 {
			// Normalize empty config to default snapshot.
			_, norm, err := crawler.NormalizeConfigSnapshot(nil)
			if err != nil {
				log.Printf("auto-crawl scheduler: failed to normalize default config for project %s: %v", s.ProjectID.String(), err)
				continue
			}
			configSnapshot = norm
		}

		// requested_by_user_id is NULL for auto crawls.
		_, err := queries.CreateCrawl(ctx, sqlc.CreateCrawlParams{
			ProjectID:         s.ProjectID,
			RequestedByUserID: pgtype.UUID{},
			Source:            "auto",
			Status:            "queued",
			ConfigSnapshot:    configSnapshot,
			StartedAt:         pgtype.Timestamptz{},
		})
		if err != nil {
			log.Printf("auto-crawl scheduler: failed to create auto crawl for project %s: %v", s.ProjectID.String(), err)
			continue
		}

		if err := queries.UpdateAutoCrawlEnqueued(ctx, sqlc.UpdateAutoCrawlEnqueuedParams{
			ProjectID: s.ProjectID,
			NextRunAt: pgtype.Timestamptz{Time: nextAutoCrawlRun(s, time.Now()), Valid: true},
		}); err != nil {
			log.Printf("auto-crawl scheduler: failed to advance schedule for project %s: %v", s.ProjectID.String(), err)
			continue
		}

		enqueued++
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	if enqueued > 0 {
		log.Printf("auto-crawl scheduler: enqueued %d auto crawls", enqueued)
	}
	return nil
}

// nextAutoCrawlRun computes the slot after the one that just fired: the
// scheduled next_run_at plus frequency_days at the configured wall-clock time
// in the project's timezone, skipping past any missed slots.
func nextAutoCrawlRun(s sqlc.ProjectAutoCrawlSetting, now time.Time) time.Time {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		loc = time.UTC
	}
	totalMinutes := int(s.RunAt.Microseconds / 60_000_000)
	hour, minute := totalMinutes/60, totalMinutes%60
	frequencyDays := int(s.FrequencyDays)
	if frequencyDays < 1 {
		frequencyDays = 1
	}
	from := now
	if s.NextRunAt.Valid {
		from = s.NextRunAt.Time
	}
	return schedule.Advance(from, now, frequencyDays, hour, minute, loc)
}

// runCrawlGuarded runs one crawl under a bounded timeout and turns a panic into
// a failed crawl instead of a dead process.
//
// Go terminates the whole program on an unrecovered panic in any goroutine, so
// without this one bad page would kill the worker process and every crawl its
// siblings were running. The persist path can panic today (mustMarshalJSON on an
// unencodable page field), so this is a live risk rather than a theoretical one.
// The crawl row is marked failed here so it does not sit in 'running' until the
// stale-crawl reclaim eventually notices it.
func (w *Worker) runCrawlGuarded(ctx context.Context, claimed claimedCrawlRow) (err error) {
	crawlCtx, cancelTimeout := context.WithTimeout(ctx, w.cfg.CrawlTimeout)
	defer cancelTimeout()

	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}

		log.Printf("PANIC recovered in crawl: crawl_id=%s value=%v\n%s", claimed.ID.String(), recovered, debug.Stack())
		// Detached from ctx: the timeout may already have fired, and the status
		// write has to land regardless.
		finalCtx := context.WithoutCancel(ctx)
		if failErr := crawler.NewStore(w.pool).MarkCrawlFailed(finalCtx, claimed.ID, 0, 0, 0); failErr != nil {
			log.Printf("failed to mark panicked crawl as failed: crawl_id=%s error=%v", claimed.ID.String(), failErr)
		}
		err = fmt.Errorf("crawl panicked: %v", recovered)
	}()

	return w.runCrawl(crawlCtx, claimed)
}

// runCrawl executes one claimed crawl, derives backend issues, and calculates crawl scores.
func (w *Worker) runCrawl(ctx context.Context, claimed claimedCrawlRow) error {
	crawlConfig, err := crawler.ConfigFromBaseURLAndSnapshot(claimed.BaseURL, claimed.ConfigSnapshot)
	if err != nil {
		store := crawler.NewStore(w.pool)
		if failErr := store.MarkCrawlFailed(ctx, claimed.ID, 0, 0, 0); failErr != nil {
			return fmt.Errorf("build crawler config: %w (also failed to mark crawl failed: %v)", err, failErr)
		}
		return fmt.Errorf("build crawler config: %w", err)
	}

	// Per-crawl cancellation: registered so CancelCrawl can trip it for
	// same-process cancel requests (see CancelCrawl's cross-process caveat).
	crawlCtx, cancelCrawl := context.WithCancel(ctx)
	w.registerCrawlCancel(claimed.ID, cancelCrawl)
	defer func() {
		w.unregisterCrawlCancel(claimed.ID)
		cancelCrawl()
	}()

	fetcher := crawler.NewFetcher(crawlConfig.FetchTimeout, crawlConfig.UserAgent, w.cfg.CrawlMaxRetries, w.cfg.CrawlRetryBase, w.cfg.CrawlRetryMax).
		WithMaxBodyBytes(w.cfg.MaxAPIResponseBytes)
	parser := crawler.NewParser()
	crawlStore := crawler.NewStore(w.pool)
	issueStore := issues.NewStore(w.pool)
	runner := crawler.NewRunner(crawlConfig, w.cfg.CrawlPageWorkerCount, fetcher, parser).
		WithRenderer(w.renderer).
		WithStore(crawlStore).
		WithDeferredFinalStatus()

	// Incremental crawl: ask each origin whether a page changed since the last
	// completed crawl, and copy unchanged pages forward instead of refetching and
	// reparsing them. Any failure here degrades to a full crawl, never an error.
	if crawlConfig.ForceFullCrawl {
		log.Printf("incremental crawl disabled by config: crawl_id=%s", claimed.ID.String())
	} else if baseline, baselineErr := crawlStore.LoadBaseline(crawlCtx, claimed.ProjectID, claimed.ID); baselineErr != nil {
		log.Printf("baseline load failed, crawling unconditionally: crawl_id=%s error=%v", claimed.ID.String(), baselineErr)
	} else if baseline != nil {
		runner = runner.WithBaseline(baseline, crawlStore)
		log.Printf("incremental crawl enabled: crawl_id=%s baseline_crawl_id=%s baseline_pages=%d", claimed.ID.String(), baseline.CrawlID.String(), baseline.Len())
	} else {
		log.Printf("no usable crawl baseline, crawling unconditionally: crawl_id=%s", claimed.ID.String())
	}

	if phaseErr := crawlStore.UpdateCrawlPhase(crawlCtx, claimed.ID, "crawling"); phaseErr != nil {
		log.Printf("update crawl phase to crawling failed: crawl_id=%s error=%v", claimed.ID.String(), phaseErr)
	}

	_, crawlRunSummary, err := runner.RunAndPersistWithSummary(crawlCtx, claimed.ID, claimed.BaseURL)
	if err != nil {
		return fmt.Errorf("run crawl: %w", err)
	}

	// Attribute broken targets back to the pages linking to them. This has to
	// happen after the crawl loop and before derivation, since it is what gives
	// the broken/redirecting internal-link issues their input. A failure here
	// costs those two issue types, not the crawl.
	resolveStartedAt := time.Now()
	resolvedLinks, err := crawlStore.ResolveInternalLinkTargetStatuses(crawlCtx, claimed.ID)
	if err != nil {
		log.Printf("resolve internal link target statuses failed: crawl_id=%s error=%v", claimed.ID.String(), err)
	} else {
		log.Printf("phase timing: crawl_id=%s resolve_link_targets=%s resolved=%d",
			claimed.ID.String(), time.Since(resolveStartedAt).Round(time.Millisecond), resolvedLinks)
	}

	// Google PSI is an independent ~15-90s network call whose result is only
	// needed at scoring time, so run it concurrently with issue derivation
	// (different tables, no shared rows) instead of serially before it.
	if w.cfg.PageSpeedAPIKey != "" {
		log.Printf("starting google psi: crawl_id=%s url=%s strategy=mobile", claimed.ID.String(), claimed.BaseURL)
	}
	type psiOutcome struct {
		result *googlePSIStoredResult
		err    error
	}
	psiChan := make(chan psiOutcome, 1)
	go func() {
		psiStartedAt := time.Now()
		result, psiErr := w.enrichCrawlWithGooglePSI(crawlCtx, claimed.ID, claimed.BaseURL)
		log.Printf("phase timing: crawl_id=%s google_psi=%s (concurrent)", claimed.ID.String(), time.Since(psiStartedAt).Round(time.Millisecond))
		psiChan <- psiOutcome{result: result, err: psiErr}
	}()

	if phaseErr := crawlStore.UpdateCrawlPhase(crawlCtx, claimed.ID, "analyzing"); phaseErr != nil {
		log.Printf("update crawl phase to analyzing failed: crawl_id=%s error=%v", claimed.ID.String(), phaseErr)
	}

	deriveStartedAt := time.Now()
	derivedIssueCount, crawlPages, err := issueStore.DeriveIssuesWithPages(crawlCtx, claimed.ID)
	log.Printf("phase timing: crawl_id=%s derive_issues=%s", claimed.ID.String(), time.Since(deriveStartedAt).Round(time.Millisecond))

	psi := <-psiChan
	googlePSIResult := psi.result
	if psi.err != nil {
		log.Printf("google psi enrichment failed: crawl_id=%s error=%v", claimed.ID.String(), psi.err)
	}
	if err != nil {
		finalCtx := context.WithoutCancel(ctx)
		if failErr := crawlStore.MarkCrawlFailed(finalCtx, claimed.ID, crawlRunSummary.URLsDiscovered, crawlRunSummary.URLsCrawled, crawlRunSummary.MaxDepthReached); failErr != nil {
			return fmt.Errorf("derive issues: %w (also failed to mark crawl failed: %v)", err, failErr)
		}
		return fmt.Errorf("derive issues: %w", err)
	}

	googlePSIIssueCount, err := w.persistGooglePSIIssues(crawlCtx, claimed.ID, googlePSIResult)
	if err != nil {
		log.Printf("google psi issue persistence failed: crawl_id=%s error=%v", claimed.ID.String(), err)
	}
	psiInput := toSharedPSIScoreInput(googlePSIResult)
	scoreStartedAt := time.Now()
	crawlScores, err := issueStore.ScoreCrawlWithPages(crawlCtx, claimed.ID, crawlPages, psiInput)
	log.Printf("phase timing: crawl_id=%s score_crawl=%s", claimed.ID.String(), time.Since(scoreStartedAt).Round(time.Millisecond))
	if err != nil {
		finalCtx := context.WithoutCancel(ctx)
		if failErr := crawlStore.MarkCrawlFailed(finalCtx, claimed.ID, crawlRunSummary.URLsDiscovered, crawlRunSummary.URLsCrawled, crawlRunSummary.MaxDepthReached); failErr != nil {
			return fmt.Errorf("score crawl: %w (also failed to mark crawl failed: %v)", err, failErr)
		}
		return fmt.Errorf("score crawl: %w", err)
	}

	log.Printf("derived crawl issues: crawl_id=%s count=%d google_psi_count=%d", claimed.ID.String(), derivedIssueCount, googlePSIIssueCount)
	log.Printf("calculated crawl scores: crawl_id=%s seo=%d aeo=%d pagespeed=%d overall=%d", claimed.ID.String(), crawlScores.SEOScore, crawlScores.AEOScore, crawlScores.PageSpeedScore, crawlScores.OverallScore)

	finalCtx := context.WithoutCancel(ctx)
	if err := crawlStore.MarkCrawlCompleted(finalCtx, claimed.ID, crawlRunSummary.URLsDiscovered, crawlRunSummary.URLsCrawled, crawlRunSummary.MaxDepthReached); err != nil {
		log.Printf("crawl succeeded but mark-completed status write failed: crawl_id=%s error=%v", claimed.ID.String(), err)
		return fmt.Errorf("mark crawl completed: %w", err)
	}

	return nil
}

// sleepOrCancel sleeps for a poll interval or returns when context is canceled.
func sleepOrCancel(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func toSharedPSIScoreInput(result *googlePSIStoredResult) *shared.GooglePSIScoreInput {
	if result == nil || !result.Mobile.Success || result.Mobile.PerformanceScore == nil {
		return nil
	}
	return &shared.GooglePSIScoreInput{MobilePerformanceScore: result.Mobile.PerformanceScore}

}

// isRunningCrawlConflict reports whether the one-running-crawl-per-user index rejected a claim.
func isRunningCrawlConflict(err error) bool {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}

	return postgresError.Code == "23505"
}
