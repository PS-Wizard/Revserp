package aichattools

import (
	"context"
	"testing"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type fakePromptEnqueuer struct {
	calls int
	last  sqlc.EnqueueAIWorkerJobParams
}

func (f *fakePromptEnqueuer) EnqueueAIWorkerJob(_ context.Context, arg sqlc.EnqueueAIWorkerJobParams) (sqlc.EnqueueAIWorkerJobRow, error) {
	f.calls++
	f.last = arg
	return sqlc.EnqueueAIWorkerJobRow{JobType: arg.JobType, ProjectID: arg.ProjectID}, nil
}

// TestEnqueuePromptGenerationRespectsSuppression pins the split between normal
// chat and setup bootstrap: chat enqueues the question-generation follow-up,
// bootstrap suppression does not because setup chaining owns that enqueue.
func TestEnqueuePromptGenerationRespectsSuppression(t *testing.T) {
	enqueuer := &fakePromptEnqueuer{}
	enqueuePromptGenerationAfterProfileWrite(context.Background(), enqueuer, testProjectID, false)
	if enqueuer.calls != 1 {
		t.Fatalf("chat enqueues = %d, want 1", enqueuer.calls)
	}
	if enqueuer.last.JobType != "prompt_generation" || enqueuer.last.ProjectID != testProjectID {
		t.Fatalf("enqueued %+v, want prompt_generation for the project", enqueuer.last)
	}

	suppressed := &fakePromptEnqueuer{}
	enqueuePromptGenerationAfterProfileWrite(context.Background(), suppressed, testProjectID, true)
	if suppressed.calls != 0 {
		t.Fatalf("suppressed enqueues = %d, want 0", suppressed.calls)
	}
}
