package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/crawler"
)

var errCrawlStopped = errors.New("crawl stopped")

// errAnalysisDeadline marks an expired analysis budget with a live root and a
// running row: a genuine failure to record, never a stop and never advisory.
var errAnalysisDeadline = errors.New("analysis deadline exceeded")

func logCrawlLoopResult(source string, workerID int, crawlID string, err error) {
	if errors.Is(err, errCrawlStopped) {
		log.Printf("%s worker %d crawl stopped: crawl_id=%s error=%v", source, workerID, crawlID, err)
		return
	}
	log.Printf("%s worker %d crawl failed: crawl_id=%s error=%v", source, workerID, crawlID, err)
}

const crawlStatusPollInterval = 2 * time.Second

const crawlStatusReadTimeout = 3 * time.Second

type crawlStatusReader func(ctx context.Context, crawlID pgtype.UUID) (string, error)

func (w *Worker) readCrawlStatus(ctx context.Context, crawlID pgtype.UUID) (string, error) {
	row, err := w.queries.GetCrawlByID(ctx, crawlID)
	if err != nil {
		return "", err
	}
	return row.Status, nil
}

func ensureCrawlActive(rootErr error, status string, readErr error) error {
	if rootErr != nil {
		return fmt.Errorf("crawl root done: %w", errCrawlStopped)
	}
	if readErr != nil {
		if errors.Is(readErr, pgx.ErrNoRows) {
			return fmt.Errorf("crawl row missing: %w", errCrawlStopped)
		}
		return nil
	}
	if status != "running" {
		return fmt.Errorf("crawl status %q: %w", status, errCrawlStopped)
	}
	return nil
}

// ensureCrawlRunningWithReader is ensureCrawlActive with one status read. A
// done root returns before any query; the read is a bounded child of the root
// so a cancel also interrupts a wedged read.
func ensureCrawlRunningWithReader(crawlRoot context.Context, crawlID pgtype.UUID, read crawlStatusReader) error {
	if err := crawlRoot.Err(); err != nil {
		return fmt.Errorf("crawl root done: %w", errCrawlStopped)
	}
	readCtx, cancel := context.WithTimeout(crawlRoot, crawlStatusReadTimeout)
	defer cancel()
	status, readErr := read(readCtx, crawlID)
	return ensureCrawlActive(crawlRoot.Err(), status, readErr)
}

func (w *Worker) ensureCrawlRunning(crawlRoot context.Context, crawlID pgtype.UUID) error {
	return ensureCrawlRunningWithReader(crawlRoot, crawlID, w.readCrawlStatus)
}

func watchCrawlStatus(crawlRoot context.Context, stop <-chan struct{}, crawlID pgtype.UUID, read crawlStatusReader, stopRoot context.CancelFunc, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-crawlRoot.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
		}
		readCtx, cancel := context.WithTimeout(crawlRoot, crawlStatusReadTimeout)
		status, readErr := read(readCtx, crawlID)
		cancel()
		if readErr != nil && !errors.Is(readErr, pgx.ErrNoRows) {
			log.Printf("crawl status read failed, retrying: crawl_id=%s error=%v", crawlID.String(), readErr)
		}
		if ensureCrawlActive(nil, status, readErr) != nil {
			log.Printf("crawl stop observed: crawl_id=%s status=%q error=%v", crawlID.String(), status, readErr)
			stopRoot()
			return
		}
	}
}

type analysisProgressReporter interface {
	UpdateCrawlAnalysisProgress(ctx context.Context, crawlID pgtype.UUID, progress crawler.CrawlAnalysisProgress) (bool, error)
}

func reportAnalysisStage(ctx context.Context, reporter analysisProgressReporter, crawlID pgtype.UUID, progress crawler.CrawlAnalysisProgress, isHalted func() error) error {
	if ctx.Err() != nil {
		if haltErr := isHalted(); haltErr != nil {
			return haltErr
		}
		return fmt.Errorf("analysis %s: %w", progress.Stage, errAnalysisDeadline)
	}
	alive, err := reporter.UpdateCrawlAnalysisProgress(ctx, crawlID, progress)
	if err == nil && alive {
		return nil
	}
	if haltErr := isHalted(); haltErr != nil {
		return haltErr
	}
	if ctx.Err() != nil {
		return fmt.Errorf("analysis %s: %w", progress.Stage, errAnalysisDeadline)
	}
	if err != nil {
		log.Printf("analysis progress report failed, continuing: crawl_id=%s stage=%s error=%v", crawlID.String(), progress.Stage, err)
		return nil
	}
	return fmt.Errorf("crawl no longer running at stage %s: %w", progress.Stage, errCrawlStopped)
}

func resolveFailureAction(haltErr error, ctxErr error) error {
	if haltErr != nil {
		return haltErr
	}
	if ctxErr != nil {
		return errAnalysisDeadline
	}
	return nil
}
