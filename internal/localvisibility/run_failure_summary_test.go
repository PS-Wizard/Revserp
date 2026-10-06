package localvisibility

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func runCell(status string, started, creditKnown bool) sqlc.GetLocalRunCellsRow {
	cell := sqlc.GetLocalRunCellsRow{CallStatus: status, CreditKnown: creditKnown}
	if started {
		cell.StartedAt = pgtype.Timestamptz{Time: time.Unix(0, 0), Valid: true}
	}
	return cell
}

func TestSummarizeRunOutcome(t *testing.T) {
	tests := []struct {
		name           string
		cells          []sqlc.GetLocalRunCellsRow
		reserved       int32
		wantStatus     string
		wantSuccessful int
		wantFailed     int
		wantUnstarted  int
		wantPending    int
		wantAmbiguous  int
		wantHeld       int32
		wantContains   []string
		wantAbsent     []string
	}{
		{
			name: "all charged one failed no holds",
			cells: []sqlc.GetLocalRunCellsRow{
				runCell("success_nonempty", true, true),
				runCell("success_empty", true, true),
				runCell("success_nonempty", true, true),
				runCell("request_failed", true, true),
			},
			reserved:       0,
			wantStatus:     "partial",
			wantSuccessful: 3,
			wantFailed:     1,
			wantHeld:       0,
			wantContains: []string{
				"3 of 4 calls succeeded, 1 failed, 0 were not attempted, 0 started but left no outcome",
				"no credits remain held",
			},
			wantAbsent: []string{"remain held pending", "unconfirmed charges", "retain reservations"},
		},
		{
			name: "unknown held charge stays reserved",
			cells: []sqlc.GetLocalRunCellsRow{
				runCell("success_nonempty", true, true),
				runCell("success_empty", true, true),
				runCell("pending", true, false),
			},
			reserved:       3,
			wantStatus:     "partial",
			wantSuccessful: 2,
			wantPending:    1,
			wantAmbiguous:  1,
			wantHeld:       3,
			wantContains: []string{
				"3 credits remain held pending reconciliation",
				"calls with unconfirmed charges: 1. Their cost is not yet confirmed",
			},
			wantAbsent: []string{"no credits remain held"},
		},
		{
			name: "unstarted cells release their reservations",
			cells: []sqlc.GetLocalRunCellsRow{
				runCell("success_nonempty", true, true),
				runCell("pending", false, false),
				runCell("pending", false, false),
			},
			reserved:       6,
			wantStatus:     "partial",
			wantSuccessful: 1,
			wantUnstarted:  2,
			wantHeld:       0,
			wantContains: []string{
				"2 were not attempted",
				"no credits remain held",
			},
			wantAbsent: []string{"credits remain held pending", "unconfirmed charges"},
		},
		{
			name: "started but pending is not unattempted",
			cells: []sqlc.GetLocalRunCellsRow{
				runCell("success_nonempty", true, true),
				runCell("pending", true, false),
			},
			reserved:       3,
			wantStatus:     "partial",
			wantSuccessful: 1,
			wantPending:    1,
			wantAmbiguous:  1,
			wantHeld:       3,
			wantContains: []string{
				"0 were not attempted, 1 started but left no outcome",
			},
		},
		{
			name: "zero success run fails",
			cells: []sqlc.GetLocalRunCellsRow{
				runCell("request_failed", true, true),
				runCell("request_failed", true, true),
			},
			reserved:   0,
			wantStatus: "failed",
			wantFailed: 2,
			wantHeld:   0,
			wantContains: []string{
				"0 of 2 calls succeeded, 2 failed",
				"no credits remain held",
			},
		},
		{
			name: "completed run stays completed",
			cells: []sqlc.GetLocalRunCellsRow{
				runCell("success_nonempty", true, true),
				runCell("success_empty", true, true),
			},
			reserved:       0,
			wantStatus:     "completed",
			wantSuccessful: 2,
			wantHeld:       0,
			wantContains:   []string{"2 of 2 calls succeeded"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := summarizeRunOutcome(tc.cells, tc.reserved)
			if got.status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", got.status, tc.wantStatus)
			}
			if got.successful != tc.wantSuccessful {
				t.Fatalf("successful = %d, want %d", got.successful, tc.wantSuccessful)
			}
			if got.requestFailed != tc.wantFailed {
				t.Fatalf("requestFailed = %d, want %d", got.requestFailed, tc.wantFailed)
			}
			if got.unstarted != tc.wantUnstarted {
				t.Fatalf("unstarted = %d, want %d", got.unstarted, tc.wantUnstarted)
			}
			if got.startedPending != tc.wantPending {
				t.Fatalf("startedPending = %d, want %d", got.startedPending, tc.wantPending)
			}
			if got.ambiguous != tc.wantAmbiguous {
				t.Fatalf("ambiguous = %d, want %d", got.ambiguous, tc.wantAmbiguous)
			}
			if got.held != tc.wantHeld {
				t.Fatalf("held = %d, want %d", got.held, tc.wantHeld)
			}
			msg := got.message()
			for _, want := range tc.wantContains {
				if !strings.Contains(msg, want) {
					t.Fatalf("message = %q, want it to contain %q", msg, want)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(msg, absent) {
					t.Fatalf("message = %q, want it to omit %q", msg, absent)
				}
			}
		})
	}
}
