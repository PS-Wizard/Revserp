// Package schedule computes auto-crawl run times: every N days at a fixed
// wall-clock time in a given timezone.
package schedule

import "time"

// atWallClock returns the given date's hh:mm in loc. Going through time.Date
// (rather than Add) keeps the wall-clock time stable across DST transitions.
func atWallClock(day time.Time, hour, minute int, loc *time.Location) time.Time {
	d := day.In(loc)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, loc)
}

// NextOccurrence returns the first hh:mm in loc strictly after `after`.
func NextOccurrence(after time.Time, hour, minute int, loc *time.Location) time.Time {
	candidate := atWallClock(after, hour, minute, loc)
	for !candidate.After(after) {
		candidate = atWallClock(candidate.AddDate(0, 0, 1), hour, minute, loc)
	}
	return candidate
}

// Advance returns the slot frequencyDays after `from` at hh:mm in loc,
// skipping forward until the result is strictly after `now` so a backlog of
// missed slots collapses into a single future one.
func Advance(from, now time.Time, frequencyDays, hour, minute int, loc *time.Location) time.Time {
	next := atWallClock(from.In(loc).AddDate(0, 0, frequencyDays), hour, minute, loc)
	for !next.After(now) {
		next = atWallClock(next.AddDate(0, 0, frequencyDays), hour, minute, loc)
	}
	return next
}
