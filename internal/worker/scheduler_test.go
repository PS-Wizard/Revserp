package worker

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestNextAutoCrawlRunAdvancesByFrequency(t *testing.T) {
	fired := time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC)
	s := sqlc.ProjectAutoCrawlSetting{
		FrequencyDays: 3,
		RunAt:         pgtype.Time{Microseconds: 3 * 3600 * 1_000_000, Valid: true},
		Timezone:      "UTC",
		NextRunAt:     pgtype.Timestamptz{Time: fired, Valid: true},
	}

	got := nextAutoCrawlRun(s, fired.Add(time.Minute))
	want := time.Date(2026, 7, 16, 3, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestNextAutoCrawlRunFallsBackOnBadTimezone(t *testing.T) {
	fired := time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC)
	s := sqlc.ProjectAutoCrawlSetting{
		FrequencyDays: 1,
		RunAt:         pgtype.Time{Microseconds: 3 * 3600 * 1_000_000, Valid: true},
		Timezone:      "Not/AZone",
		NextRunAt:     pgtype.Timestamptz{Time: fired, Valid: true},
	}

	got := nextAutoCrawlRun(s, fired.Add(time.Minute))
	want := time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestSchedulerConfigNormalizeNil(t *testing.T) {
	// Passing nil to NormalizeConfigSnapshot should return defaults.
	snapshot, normalized, err := crawler.NormalizeConfigSnapshot(nil)
	if err != nil {
		t.Fatalf("NormalizeConfigSnapshot(nil) returned error: %v", err)
	}
	if snapshot.MaxDepth != 2 {
		t.Errorf("default MaxDepth: got %d, want 2", snapshot.MaxDepth)
	}
	if len(normalized) == 0 {
		t.Error("normalized bytes should not be empty")
	}
}

func TestSchedulerConfigNormalizeEmpty(t *testing.T) {
	snapshot, normalized, err := crawler.NormalizeConfigSnapshot([]byte{})
	if err != nil {
		t.Fatalf("NormalizeConfigSnapshot([]byte{}) returned error: %v", err)
	}
	if snapshot.MaxDepth != 2 {
		t.Errorf("default MaxDepth: got %d, want 2", snapshot.MaxDepth)
	}
	if len(normalized) == 0 {
		t.Error("normalized bytes should not be empty")
	}
}
