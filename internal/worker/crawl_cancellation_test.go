package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/crawler"
)

func TestEnsureCrawlActive(t *testing.T) {
	t.Parallel()
	transient := errors.New("connection reset")
	tests := []struct {
		name    string
		rootErr error
		status  string
		readErr error
		stopped bool
	}{
		{"alive and running proceeds", nil, "running", nil, false},
		{"root cancel stops despite running row", context.Canceled, "running", nil, true},
		{"cancelled status stops with live root", nil, "cancelled", nil, true},
		{"failed status stops with live root", nil, "failed", nil, true},
		{"completed status stops with live root", nil, "completed", nil, true},
		{"missing row stops with live root", nil, "", pgx.ErrNoRows, true},
		{"transient read error fails open", nil, "", transient, false},
		{"transient read error still stops on dead root", context.Canceled, "", transient, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ensureCrawlActive(test.rootErr, test.status, test.readErr)
			if test.stopped && !errors.Is(err, errCrawlStopped) {
				t.Fatalf("error = %v, want %v", err, errCrawlStopped)
			}
			if !test.stopped && err != nil {
				t.Fatalf("error = %v, want nil", err)
			}
		})
	}
}

func TestRootCancelBeatsFetchTimeout(t *testing.T) {
	t.Parallel()
	root, stopRoot := context.WithCancel(context.Background())
	defer stopRoot()
	_, cancelFetch := context.WithTimeout(root, time.Hour)

	cancelFetch()
	if err := ensureCrawlActive(root.Err(), "running", nil); err != nil {
		t.Fatalf("spent fetch budget with live root and running row = %v, want nil (genuine failure, not a stop)", err)
	}

	stopRoot()
	if err := ensureCrawlActive(root.Err(), "running", nil); !errors.Is(err, errCrawlStopped) {
		t.Fatalf("cancelled root = %v, want %v", err, errCrawlStopped)
	}
}

func TestEnsureCrawlRunningSkipsQueryOnDeadRoot(t *testing.T) {
	t.Parallel()
	root, stopRoot := context.WithCancel(context.Background())
	stopRoot()
	var calls atomic.Int32
	read := func(ctx context.Context, crawlID pgtype.UUID) (string, error) {
		calls.Add(1)
		return "running", nil
	}
	if err := ensureCrawlRunningWithReader(root, pgtype.UUID{}, read); !errors.Is(err, errCrawlStopped) {
		t.Fatalf("error = %v, want %v", err, errCrawlStopped)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("read called %d times with dead root, want no query", got)
	}
}

func TestEnsureCrawlRunningBlockedReadEndsOnRootCancel(t *testing.T) {
	t.Parallel()
	root, stopRoot := context.WithCancel(context.Background())
	defer stopRoot()
	read := func(ctx context.Context, crawlID pgtype.UUID) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		done <- ensureCrawlRunningWithReader(root, pgtype.UUID{}, read)
	}()
	time.Sleep(20 * time.Millisecond)
	stopRoot()
	select {
	case err := <-done:
		if !errors.Is(err, errCrawlStopped) {
			t.Fatalf("error = %v, want %v", err, errCrawlStopped)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked status read did not return on root cancel")
	}
}

func scriptedStatusReader(t *testing.T, script []any) crawlStatusReader {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	return func(ctx context.Context, crawlID pgtype.UUID) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if calls >= len(script) {
			return "running", nil
		}
		entry := script[calls]
		calls++
		if err, ok := entry.(error); ok {
			return "", err
		}
		return entry.(string), nil
	}
}

func runWatcher(t *testing.T, read crawlStatusReader) (context.Context, func()) {
	t.Helper()
	root, stopRoot := context.WithCancel(context.Background())
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchCrawlStatus(root, stop, pgtype.UUID{}, read, stopRoot, 5*time.Millisecond)
	}()
	return root, func() {
		stopRoot()
		close(stop)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("watcher did not exit")
		}
	}
}

func TestWatchCrawlStatusCancelsOnStatusFlip(t *testing.T) {
	t.Parallel()
	root, finish := runWatcher(t, scriptedStatusReader(t, []any{"running", "running", "cancelled"}))
	defer finish()
	select {
	case <-root.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not cancel root after status flip to cancelled")
	}
}

func TestWatchCrawlStatusIgnoresTransientErrors(t *testing.T) {
	t.Parallel()
	root, finish := runWatcher(t, scriptedStatusReader(t, []any{errors.New("connection reset"), errors.New("connection reset"), errors.New("connection reset"), "running"}))
	defer finish()
	select {
	case <-time.After(80 * time.Millisecond):
	case <-root.Done():
		t.Fatal("watcher cancelled root on transient read errors")
	}
}

func TestWatchCrawlStatusStopsMissingRow(t *testing.T) {
	t.Parallel()
	root, finish := runWatcher(t, scriptedStatusReader(t, []any{pgx.ErrNoRows}))
	defer finish()
	select {
	case <-root.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not cancel root for a missing row")
	}
}

func TestWatchCrawlStatusExitsOnStop(t *testing.T) {
	t.Parallel()
	_, finish := runWatcher(t, scriptedStatusReader(t, []any{}))
	finish()
}

func TestWatchCrawlStatusBlockedReadEndsOnRootCancel(t *testing.T) {
	t.Parallel()
	root, stopRoot := context.WithCancel(context.Background())
	read := func(ctx context.Context, crawlID pgtype.UUID) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	inner, cancelInner := context.WithCancel(root)
	go func() {
		defer close(done)
		watchCrawlStatus(inner, stop, pgtype.UUID{}, read, func() {}, 5*time.Millisecond)
	}()
	time.Sleep(50 * time.Millisecond)
	stopRoot()
	cancelInner()
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher with blocked read did not exit on root cancel")
	}
}

type fakeProgressReporter struct {
	alive   bool
	err     error
	reports []crawler.CrawlAnalysisProgress
}

func (f *fakeProgressReporter) UpdateCrawlAnalysisProgress(ctx context.Context, crawlID pgtype.UUID, progress crawler.CrawlAnalysisProgress) (bool, error) {
	f.reports = append(f.reports, progress)
	return f.alive, f.err
}

func TestReportAnalysisStage(t *testing.T) {
	t.Parallel()
	progress := crawler.CrawlAnalysisProgress{Stage: crawler.AnalysisStageCheckingLinks}
	t.Run("live report proceeds without liveness check", func(t *testing.T) {
		t.Parallel()
		reporter := &fakeProgressReporter{alive: true}
		checked := false
		err := reportAnalysisStage(context.Background(), reporter, pgtype.UUID{}, progress, func() error {
			checked = true
			return nil
		})
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		if checked {
			t.Fatal("liveness re-check ran on a live report")
		}
		if len(reporter.reports) != 1 || reporter.reports[0].Stage != crawler.AnalysisStageCheckingLinks {
			t.Fatalf("reports = %+v, want one checking_links report", reporter.reports)
		}
	})
	t.Run("dead row stops", func(t *testing.T) {
		t.Parallel()
		reporter := &fakeProgressReporter{alive: false}
		err := reportAnalysisStage(context.Background(), reporter, pgtype.UUID{}, progress, func() error { return nil })
		if !errors.Is(err, errCrawlStopped) {
			t.Fatalf("error = %v, want %v", err, errCrawlStopped)
		}
	})
	t.Run("report error with live crawl continues", func(t *testing.T) {
		t.Parallel()
		reporter := &fakeProgressReporter{err: errors.New("db down")}
		err := reportAnalysisStage(context.Background(), reporter, pgtype.UUID{}, progress, func() error { return nil })
		if err != nil {
			t.Fatalf("error = %v, want nil (progress is advisory)", err)
		}
	})
	t.Run("report error with halted crawl stops", func(t *testing.T) {
		t.Parallel()
		reporter := &fakeProgressReporter{err: errors.New("db down")}
		halted := fmt.Errorf("wrapped: %w", errCrawlStopped)
		err := reportAnalysisStage(context.Background(), reporter, pgtype.UUID{}, progress, func() error { return halted })
		if !errors.Is(err, errCrawlStopped) {
			t.Fatalf("error = %v, want %v", err, errCrawlStopped)
		}
	})
}

func TestNewDefaultsTimeouts(t *testing.T) {
	t.Parallel()
	w := New(nil, Config{}, nil)
	if got := w.cfg.AnalysisTimeout; got != 30*time.Minute {
		t.Fatalf("AnalysisTimeout = %v, want 30m", got)
	}
	if got := w.cfg.CrawlTimeout; got != 60*time.Minute {
		t.Fatalf("CrawlTimeout = %v, want 60m", got)
	}
}
