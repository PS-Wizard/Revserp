package crawler

import (
	"context"
	"errors"
	"testing"
)

// The batch loop is pure over a runBatch stub, so callback counting and stop
// behavior are verified without a database.
func TestResolveBatchesReportsCumulativeCounts(t *testing.T) {
	batches := []int64{400000, 240000, 0}
	var calls int
	var reported []int64
	total, err := resolveBatchesWithProgress(context.Background(),
		func(context.Context) (int64, error) {
			rows := batches[calls]
			calls++
			return rows, nil
		},
		func(cumulative int64) error {
			reported = append(reported, cumulative)
			return nil
		})
	if err != nil {
		t.Fatalf("resolve batches: %v", err)
	}
	if total != 640000 {
		t.Fatalf("total = %d, want 640000", total)
	}
	if len(reported) != 2 || reported[0] != 400000 || reported[1] != 640000 {
		t.Fatalf("reported = %v, want cumulative [400000 640000]", reported)
	}
}

func TestResolveBatchesCallbackErrorStopsBeforeNextBatch(t *testing.T) {
	stopErr := errors.New("cancelled")
	var calls int
	total, err := resolveBatchesWithProgress(context.Background(),
		func(context.Context) (int64, error) {
			calls++
			return 400000, nil
		},
		func(int64) error { return stopErr })
	if !errors.Is(err, stopErr) {
		t.Fatalf("err = %v, want the callback error", err)
	}
	if total != 400000 {
		t.Fatalf("total = %d, want completed 400000", total)
	}
	if calls != 1 {
		t.Fatalf("batches ran = %d, want 1: callback error must stop before the next batch", calls)
	}
}

func TestResolveBatchesCancelledContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int
	total, err := resolveBatchesWithProgress(ctx,
		func(context.Context) (int64, error) {
			calls++
			return 1, nil
		},
		nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if total != 0 || calls != 0 {
		t.Fatalf("total = %d calls = %d, want no batch to run", total, calls)
	}
}

func TestResolveBatchesPropagatesBatchError(t *testing.T) {
	batchErr := errors.New("statement timeout")
	total, err := resolveBatchesWithProgress(context.Background(),
		func(context.Context) (int64, error) { return 0, batchErr },
		nil)
	if !errors.Is(err, batchErr) {
		t.Fatalf("err = %v, want the batch error", err)
	}
	if total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
}

func TestResolveBatchesEmptyImmediately(t *testing.T) {
	var reported int
	total, err := resolveBatchesWithProgress(context.Background(),
		func(context.Context) (int64, error) { return 0, nil },
		func(int64) error { reported++; return nil })
	if err != nil {
		t.Fatalf("resolve batches: %v", err)
	}
	if total != 0 || reported != 0 {
		t.Fatalf("total = %d callbacks = %d, want no work and no callback", total, reported)
	}
}
