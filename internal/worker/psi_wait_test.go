package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitAnalysisPSIReturnsReadyResult(t *testing.T) {
	t.Parallel()
	want := psiOutcome{err: errors.New("psi boom")}
	psiChan := make(chan psiOutcome, 1)
	psiChan <- want
	got, err := waitAnalysisPSI(psiChan, context.Background())
	if err != nil {
		t.Fatalf("waitAnalysisPSI: %v", err)
	}
	if got.err == nil || got.err.Error() != "psi boom" {
		t.Fatalf("outcome = %+v, want the queued PSI result", got)
	}
}

func TestWaitAnalysisPSIReturnsOnExpiredBudget(t *testing.T) {
	t.Parallel()
	psiChan := make(chan psiOutcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(10 * time.Millisecond)
	start := time.Now()
	_, err := waitAnalysisPSI(psiChan, ctx)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waitAnalysisPSI blocked %s on an expired budget", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}
