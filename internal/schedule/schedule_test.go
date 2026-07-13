package schedule

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location %s: %v", name, err)
	}
	return loc
}

func TestNextOccurrenceLaterToday(t *testing.T) {
	loc := mustLoc(t, "UTC")
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, loc)

	got := NextOccurrence(now, 15, 30, loc)
	want := time.Date(2026, 7, 13, 15, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestNextOccurrenceRollsToTomorrow(t *testing.T) {
	loc := mustLoc(t, "UTC")
	now := time.Date(2026, 7, 13, 16, 0, 0, 0, loc)

	got := NextOccurrence(now, 15, 30, loc)
	want := time.Date(2026, 7, 14, 15, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestNextOccurrenceExactTimeRolls(t *testing.T) {
	loc := mustLoc(t, "UTC")
	now := time.Date(2026, 7, 13, 15, 30, 0, 0, loc)

	// Exactly at hh:mm counts as passed; next slot is tomorrow.
	got := NextOccurrence(now, 15, 30, loc)
	want := time.Date(2026, 7, 14, 15, 30, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestNextOccurrenceRespectsTimezone(t *testing.T) {
	loc := mustLoc(t, "Asia/Kathmandu")                  // UTC+5:45
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC) // 15:45 in Kathmandu

	got := NextOccurrence(now, 15, 0, loc)
	// 15:00 Kathmandu already passed today; next is tomorrow 15:00 = 09:15 UTC.
	want := time.Date(2026, 7, 14, 9, 15, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got.UTC(), want)
	}
}

func TestAdvanceByFrequency(t *testing.T) {
	loc := mustLoc(t, "UTC")
	from := time.Date(2026, 7, 13, 3, 0, 0, 0, loc)
	now := from.Add(time.Minute) // just enqueued

	got := Advance(from, now, 3, 3, 0, loc)
	want := time.Date(2026, 7, 16, 3, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestAdvanceCollapsesBacklog(t *testing.T) {
	loc := mustLoc(t, "UTC")
	from := time.Date(2026, 7, 1, 3, 0, 0, 0, loc)
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, loc) // scheduler was down ~12 days

	got := Advance(from, now, 2, 3, 0, loc)
	// Slots 7/3, 7/5 ... 7/13 are all in the past; first future slot is 7/15.
	want := time.Date(2026, 7, 15, 3, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestAdvanceKeepsWallClockAcrossDST(t *testing.T) {
	loc := mustLoc(t, "America/New_York")
	// 2026 DST starts March 8 in the US.
	from := time.Date(2026, 3, 7, 3, 0, 0, 0, loc)
	now := from.Add(time.Minute)

	got := Advance(from, now, 1, 3, 0, loc)
	want := time.Date(2026, 3, 8, 3, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
	if h := got.In(loc).Hour(); h != 3 {
		t.Errorf("wall-clock hour after DST: got %d, want 3", h)
	}
}
