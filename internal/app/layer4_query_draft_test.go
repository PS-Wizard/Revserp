package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// recordingLayer4DraftStore traces the draft statement order without a
// database, so the lock-first contract is asserted by a runnable test.
type recordingLayer4DraftStore struct {
	calls    []string
	existing []sqlc.ProjectLocationQuery
	lockErr  error
	deletes  []pgtype.UUID
	updates  []layer4DraftQueryUpdate
	inserts  []layer4DraftQueryInsert
	nextID   byte
}

func draftTestUUID(n byte) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, n}, Valid: true}
}

func draftTestRow(id byte, text, kind, source, origin string, ordinal int32) sqlc.ProjectLocationQuery {
	return sqlc.ProjectLocationQuery{
		ID: idUUID(id), LocationID: draftTestUUID(100),
		Text: text, Normalized: strings.ToLower(text), Ordinal: ordinal,
		Enabled: true, Kind: kind, Source: source, Origin: origin,
	}
}

func idUUID(n byte) pgtype.UUID { return draftTestUUID(n) }

func (f *recordingLayer4DraftStore) lockDraftLocation(ctx context.Context) (sqlc.LockProjectLocationForQueryDraftForUserRow, error) {
	f.calls = append(f.calls, "lockDraftLocation")
	if f.lockErr != nil {
		return sqlc.LockProjectLocationForQueryDraftForUserRow{}, f.lockErr
	}
	return sqlc.LockProjectLocationForQueryDraftForUserRow{ID: draftTestUUID(100)}, nil
}

func (f *recordingLayer4DraftStore) deferDraftOrdinalConstraint(ctx context.Context) error {
	f.calls = append(f.calls, "deferDraftOrdinalConstraint")
	return nil
}

func (f *recordingLayer4DraftStore) lockDraftQueries(ctx context.Context) ([]sqlc.ProjectLocationQuery, error) {
	f.calls = append(f.calls, "lockDraftQueries")
	return f.existing, nil
}

func (f *recordingLayer4DraftStore) deleteDraftManualQuery(ctx context.Context, id pgtype.UUID) error {
	f.calls = append(f.calls, "deleteDraftManualQuery")
	f.deletes = append(f.deletes, id)
	return nil
}

func (f *recordingLayer4DraftStore) updateDraftQuery(ctx context.Context, update layer4DraftQueryUpdate) (sqlc.ProjectLocationQuery, error) {
	f.calls = append(f.calls, "updateDraftQuery")
	f.updates = append(f.updates, update)
	return sqlc.ProjectLocationQuery{
		ID: update.ID, LocationID: draftTestUUID(100),
		Text: update.Text, Normalized: update.Normalized, Ordinal: update.Ordinal,
		Enabled: update.Enabled, Kind: "map", Source: update.Source,
		Origin: update.Origin, LandmarkID: update.LandmarkID,
	}, nil
}

func (f *recordingLayer4DraftStore) insertDraftQuery(ctx context.Context, insert layer4DraftQueryInsert) (sqlc.ProjectLocationQuery, error) {
	f.calls = append(f.calls, "insertDraftQuery")
	f.inserts = append(f.inserts, insert)
	f.nextID++
	return sqlc.ProjectLocationQuery{
		ID: draftTestUUID(200 + f.nextID), LocationID: draftTestUUID(100),
		Text: insert.Text, Normalized: insert.Normalized, Ordinal: insert.Ordinal,
		Enabled: insert.Enabled, Kind: "map", Source: "manual", Origin: "service",
	}, nil
}

func draftEntry(id *string, text string, enabled bool) layer4QueryDraftEntry {
	return layer4QueryDraftEntry{ID: id, Text: text, Enabled: enabled, Kind: "map", Source: "manual"}
}

func draftIDPtr(id pgtype.UUID) *string {
	s := id.String()
	return &s
}

func TestSaveLayer4QueryDraftLocksParentFirst(t *testing.T) {
	manual := draftTestRow(1, "plumber", "map", "manual", "service", 0)
	generated := draftTestRow(2, "plumber in kathmandu", "map", "generated", "locality", 1)
	ai := draftTestRow(3, "best plumber?", "ai_question", "generated", "service", 0)
	fake := &recordingLayer4DraftStore{existing: []sqlc.ProjectLocationQuery{manual, generated, ai}}

	entries := []layer4QueryDraftEntry{
		draftEntry(draftIDPtr(manual.ID), "  emergency plumber ", true),
		{Text: "plumber near me", Enabled: true, Kind: "map", Source: "manual"},
	}
	records, err := saveLayer4QueryDraft(context.Background(), fake, entries)
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	// The parent lock is the first statement; the draft rows are read only
	// after it, and nothing else touches the database before the lock.
	wantOrder := []string{"lockDraftLocation", "deferDraftOrdinalConstraint", "lockDraftQueries"}
	for i, want := range wantOrder {
		if i >= len(fake.calls) || fake.calls[i] != want {
			t.Fatalf("statement trace = %v, want prefix %v", fake.calls, wantOrder)
		}
	}
	if len(fake.updates) != 2 {
		t.Fatalf("updates = %d, want 1 submitted + 1 disabled generated", len(fake.updates))
	}
	if fake.updates[0].ID != manual.ID || fake.updates[0].Text != "emergency plumber" || fake.updates[0].Ordinal != 0 {
		t.Fatalf("manual update = %+v", fake.updates[0])
	}
	if len(fake.inserts) != 1 || fake.inserts[0].Text != "plumber near me" || fake.inserts[0].Ordinal != 1 {
		t.Fatalf("inserts = %+v", fake.inserts)
	}
	disabled := fake.updates[1]
	if disabled.ID != generated.ID || disabled.Enabled || disabled.Ordinal != 2 || disabled.Source != "generated" {
		t.Fatalf("omitted generated must disable and append, got %+v", disabled)
	}
	if len(fake.deletes) != 0 {
		t.Fatalf("claimed manual rows must not delete, got %v", fake.deletes)
	}
	// ai_question rows are outside the map draft: never updated, deleted, or returned.
	for _, record := range records {
		if record.Kind != "map" {
			t.Fatalf("saved records must be map-only, got %+v", record)
		}
	}
	if len(records) != 3 || records[0].Ordinal != 0 || records[1].Ordinal != 1 || records[2].Ordinal != 2 {
		t.Fatalf("saved records = %+v, want ordinals 0,1,2", records)
	}
}

func TestSaveLayer4QueryDraftOmittedManualDeletesEditedGeneratedBecomesManual(t *testing.T) {
	manual := draftTestRow(1, "plumber", "map", "manual", "service", 0)
	generated := draftTestRow(2, "plumber in kathmandu", "map", "generated", "locality", 1)
	omitted := draftTestRow(3, "plumber in patan", "map", "generated", "locality", 2)
	fake := &recordingLayer4DraftStore{existing: []sqlc.ProjectLocationQuery{manual, generated, omitted}}

	entries := []layer4QueryDraftEntry{
		draftEntry(draftIDPtr(generated.ID), "plumber in lalitpur", true),
		{Text: "plumber in patan", Enabled: true, Kind: "map", Source: "manual"},
	}
	records, err := saveLayer4QueryDraft(context.Background(), fake, entries)
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if len(fake.deletes) != 1 || fake.deletes[0] != manual.ID {
		t.Fatalf("omitted manual must delete, got %v", fake.deletes)
	}
	// A new row matching the omitted generated text reuses it instead of
	// inserting a duplicate; the edited generated row becomes manual.
	byID := map[string]layer4DraftQueryUpdate{}
	for _, update := range fake.updates {
		byID[update.ID.String()] = update
	}
	edited, ok := byID[generated.ID.String()]
	if !ok || edited.Source != "manual" || edited.Text != "plumber in lalitpur" || edited.Ordinal != 0 {
		t.Fatalf("edited generated must become manual at ordinal 0, got %+v", fake.updates)
	}
	reused, ok := byID[omitted.ID.String()]
	if !ok || reused.Source != "generated" || !reused.Enabled || reused.Ordinal != 1 {
		t.Fatalf("adopted generated must stay generated at ordinal 1, got %+v", fake.updates)
	}
	if len(fake.inserts) != 0 {
		t.Fatalf("adopted generated text must not insert, got %+v", fake.inserts)
	}
	if len(records) != 2 || records[0].Ordinal != 0 || records[1].Ordinal != 1 {
		t.Fatalf("saved records = %+v, want 2 ordered", records)
	}
}

func TestSaveLayer4QueryDraftLockMissIsNotFound(t *testing.T) {
	fake := &recordingLayer4DraftStore{lockErr: pgx.ErrNoRows}
	_, err := saveLayer4QueryDraft(context.Background(), fake, []layer4QueryDraftEntry{})
	var draftErr *layer4DraftError
	if !errors.As(err, &draftErr) || draftErr.status != http.StatusNotFound {
		t.Fatalf("lock miss err = %v, want 404", err)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "lockDraftLocation" {
		t.Fatalf("failed lock must stop the trace, got %v", fake.calls)
	}
}

func TestPlanLayer4QueryDraftValidation(t *testing.T) {
	known := draftTestRow(1, "plumber", "map", "manual", "service", 0)
	otherKind := draftTestRow(2, "best plumber?", "ai_question", "generated", "service", 0)
	foreign := draftTestUUID(99)
	foreignStr := foreign.String()
	for _, tc := range []struct {
		name    string
		entries []layer4QueryDraftEntry
		want    string
	}{
		{"empty text", []layer4QueryDraftEntry{draftEntry(nil, "   ", true)}, "query 1 must not be empty"},
		{"duplicate", []layer4QueryDraftEntry{draftEntry(nil, "Plumber", true), draftEntry(nil, "plumber ", true)}, "duplicate query"},
		{"bad kind", []layer4QueryDraftEntry{{Text: "x", Enabled: true, Kind: "ai_question", Source: "manual"}}, "query kind must be map"},
		{"bad source", []layer4QueryDraftEntry{{Text: "x", Enabled: true, Kind: "map", Source: "auto"}}, "query source must be manual or generated"},
		{"bad id", []layer4QueryDraftEntry{draftEntry(ptr("nope"), "x", true)}, "invalid query id"},
		{"foreign id", []layer4QueryDraftEntry{draftEntry(&foreignStr, "x", true)}, "unknown query"},
		{"other kind id", []layer4QueryDraftEntry{draftEntry(draftIDPtr(otherKind.ID), "best plumber?", true)}, "unknown query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := planLayer4QueryDraft(tc.entries, []sqlc.ProjectLocationQuery{known})
			var draftErr *layer4DraftError
			if !errors.As(err, &draftErr) || draftErr.msg != tc.want || draftErr.status != http.StatusBadRequest {
				t.Fatalf("plan err = %v, want %q", err, tc.want)
			}
		})
	}
}

func ptr(s string) *string { return &s }

func TestNormalizeLocalities(t *testing.T) {
	got := normalizeLocalities([]string{"  Kathmandu ", "", "kathmandu", "Lalitpur"})
	if len(got) != 2 || got[0] != "Kathmandu" || got[1] != "Lalitpur" {
		t.Fatalf("localities = %#v", got)
	}
	if got := normalizeLocalities(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil localities must become an empty array, got %#v", got)
	}
}

func TestHandleCreateLocalVisibilityRunRequiresExpectedCredits(t *testing.T) {
	app := &App{}
	rr := callCreateLocalVisibilityRun(t, app, pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		"00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000003", `{}`)
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "expected_credits is required") {
		t.Fatalf("status = %d body = %s, want 400 expected_credits is required", rr.Code, rr.Body.String())
	}
}

func TestHandleUpdateLocationQueriesRejectsBadIDsWithoutDB(t *testing.T) {
	app := &App{}
	rr := callUpdateLocationQueries(t, app, pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, "nope", "also-bad", `[]`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
	rr = callUpdateLocationQueries(t, app, pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		"00000000-0000-0000-0000-000000000002", "nope", `[]`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHandleUpdateLocationQueriesValidationReachesBeforeTx(t *testing.T) {
	// Malformed JSON is rejected before any database work, so this runs with
	// a nil pool.
	app := &App{}
	rr := httptest.NewRecorder()
	req := localVisibilityRequest(t, http.MethodPut, pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		map[string]string{"projectID": "00000000-0000-0000-0000-000000000002", "locationID": "00000000-0000-0000-0000-000000000003"}, `nope`)
	app.handleUpdateLocationQueries(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestSaveLayer4QueryDraftRejectsDuplicateIDs(t *testing.T) {
	row := draftTestRow(1, "plumber", "map", "manual", "service", 0)
	store := &recordingLayer4DraftStore{existing: []sqlc.ProjectLocationQuery{row}}
	_, err := saveLayer4QueryDraft(context.Background(), store, []layer4QueryDraftEntry{
		draftEntry(draftIDPtr(row.ID), "plumber", true),
		draftEntry(draftIDPtr(row.ID), "electrician", true),
	})
	if err == nil || err.Error() != "duplicate query id" {
		t.Fatalf("expected duplicate query id error, got %v", err)
	}
	if len(store.updates)+len(store.inserts)+len(store.deletes) != 0 {
		t.Fatal("invalid draft performed writes")
	}
}

func TestSaveLayer4QueryDraftKeepsEveryEntryBeyondFive(t *testing.T) {
	// The 1-5 Maps query product cap is gone: a draft save accepts any count
	// and never truncates. Seven enabled entries must all persist in order.
	store := &recordingLayer4DraftStore{}
	entries := make([]layer4QueryDraftEntry, 7)
	for i := range entries {
		entries[i] = draftEntry(nil, fmt.Sprintf("query %d", i), true)
	}
	records, err := saveLayer4QueryDraft(context.Background(), store, entries)
	if err != nil {
		t.Fatalf("save seven-entry draft: %v", err)
	}
	if len(store.inserts) != 7 || len(records) != 7 {
		t.Fatalf("inserts = %d records = %d, want 7 each (no truncation)", len(store.inserts), len(records))
	}
	for i, record := range records {
		if int(record.Ordinal) != i {
			t.Fatalf("record %d ordinal = %d, want %d", i, record.Ordinal, i)
		}
	}
}
