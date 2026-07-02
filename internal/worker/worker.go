package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// Config holds all settings for a crawl Worker instance.
type Config struct {
	ManualConcurrency int
	ManualPoll        time.Duration

	AutoConcurrency       int
	AutoPoll              time.Duration
	AutoSchedulerInterval time.Duration
	AutoCrawlInterval     time.Duration

	CrawlPageWorkerCount int
	PageSpeedAPIKey      string
	CrawlMaxRetries      int
	CrawlRetryBase       time.Duration
	CrawlRetryMax        time.Duration
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
		cfg.AutoSchedulerInterval = 5 * time.Minute
	}
	if cfg.AutoCrawlInterval <= 0 {
		cfg.AutoCrawlInterval = 24 * time.Hour
	}
	if cfg.CrawlPageWorkerCount <= 0 {
		cfg.CrawlPageWorkerCount = crawler.DefaultWorkerCount
	}

	return &Worker{
		pool:     pool,
		queries:  sqlc.New(pool),
		cfg:      cfg,
		renderer: renderer,
	}
}

const schedulerAdvisoryLockID = 1748290342

// staleRunningCrawlAge is how long a crawl may sit in 'running' before it is
// considered orphaned by a crashed worker process and reclaimed as failed.
// Multiple worker replicas can run concurrently (see the scheduler's
// advisory lock), so this must be age-gated rather than an unconditional
// reset on startup, which would kill another replica's in-flight crawl.
const staleRunningCrawlAge = 2 * time.Hour

// Run starts all worker pools and scheduler until the context is canceled.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.queries.ReclaimStaleRunningCrawls(ctx, pgtype.Timestamptz{
		Time:  time.Now().UTC().Add(-staleRunningCrawlAge),
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
		if err := w.runCrawl(ctx, claimed); err != nil {
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
		if err := w.runCrawl(ctx, claimed); err != nil {
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

	cutoff := time.Now().UTC().Add(-w.cfg.AutoCrawlInterval)
	batchLimit := int32(100)

	settings, err := queries.ListDueAutoCrawlSettings(ctx, sqlc.ListDueAutoCrawlSettingsParams{
		CompletedAt: pgtype.Timestamptz{Time: cutoff, Valid: true},
		Limit:       batchLimit,
	})
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

		if err := queries.UpdateAutoCrawlLastEnqueuedAt(ctx, s.ProjectID); err != nil {
			log.Printf("auto-crawl scheduler: failed to update last_enqueued_at for project %s: %v", s.ProjectID.String(), err)
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

	fetcher := crawler.NewFetcher(crawlConfig.FetchTimeout, crawlConfig.UserAgent, w.cfg.CrawlMaxRetries, w.cfg.CrawlRetryBase, w.cfg.CrawlRetryMax)
	parser := crawler.NewParser()
	crawlStore := crawler.NewStore(w.pool)
	issueStore := issues.NewStore(w.pool)
	runner := crawler.NewRunner(crawlConfig, w.cfg.CrawlPageWorkerCount, fetcher, parser).
		WithRenderer(w.renderer).
		WithStore(crawlStore).
		WithDeferredFinalStatus()

	_, crawlRunSummary, err := runner.RunAndPersistWithSummary(ctx, claimed.ID, claimed.BaseURL)
	if err != nil {
		return fmt.Errorf("run crawl: %w", err)
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
		result, psiErr := w.enrichCrawlWithGooglePSI(ctx, claimed.ID, claimed.BaseURL)
		log.Printf("phase timing: crawl_id=%s google_psi=%s (concurrent)", claimed.ID.String(), time.Since(psiStartedAt).Round(time.Millisecond))
		psiChan <- psiOutcome{result: result, err: psiErr}
	}()

	deriveStartedAt := time.Now()
	derivedIssueCount, err := issueStore.DeriveIssues(ctx, claimed.ID)
	log.Printf("phase timing: crawl_id=%s derive_issues=%s", claimed.ID.String(), time.Since(deriveStartedAt).Round(time.Millisecond))

	psi := <-psiChan
	googlePSIResult := psi.result
	if psi.err != nil {
		log.Printf("google psi enrichment failed: crawl_id=%s error=%v", claimed.ID.String(), psi.err)
	}
	if err != nil {
		if failErr := crawlStore.MarkCrawlFailed(ctx, claimed.ID, crawlRunSummary.URLsDiscovered, crawlRunSummary.URLsCrawled, crawlRunSummary.MaxDepthReached); failErr != nil {
			return fmt.Errorf("derive issues: %w (also failed to mark crawl failed: %v)", err, failErr)
		}
		return fmt.Errorf("derive issues: %w", err)
	}

	googlePSIIssueCount, err := w.persistGooglePSIIssues(ctx, claimed.ID, googlePSIResult)
	if err != nil {
		log.Printf("google psi issue persistence failed: crawl_id=%s error=%v", claimed.ID.String(), err)
	}
	psiInput := toSharedPSIScoreInput(googlePSIResult)
	scoreStartedAt := time.Now()
	crawlScores, err := issueStore.ScoreCrawl(ctx, claimed.ID, psiInput)
	log.Printf("phase timing: crawl_id=%s score_crawl=%s", claimed.ID.String(), time.Since(scoreStartedAt).Round(time.Millisecond))
	if err != nil {
		if failErr := crawlStore.MarkCrawlFailed(ctx, claimed.ID, crawlRunSummary.URLsDiscovered, crawlRunSummary.URLsCrawled, crawlRunSummary.MaxDepthReached); failErr != nil {
			return fmt.Errorf("score crawl: %w (also failed to mark crawl failed: %v)", err, failErr)
		}
		return fmt.Errorf("score crawl: %w", err)
	}

	log.Printf("derived crawl issues: crawl_id=%s count=%d google_psi_count=%d", claimed.ID.String(), derivedIssueCount, googlePSIIssueCount)
	log.Printf("calculated crawl scores: crawl_id=%s seo=%d aeo=%d pagespeed=%d overall=%d", claimed.ID.String(), crawlScores.SEOScore, crawlScores.AEOScore, crawlScores.PageSpeedScore, crawlScores.OverallScore)
	if err := crawlStore.MarkCrawlCompleted(ctx, claimed.ID, crawlRunSummary.URLsDiscovered, crawlRunSummary.URLsCrawled, crawlRunSummary.MaxDepthReached); err != nil {
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
