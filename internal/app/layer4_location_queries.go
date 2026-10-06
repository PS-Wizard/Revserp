package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
	"github.com/ps-wizard/revserp/internal/textnormalization"
)

type layer4LocationQueryRecord struct {
	ID         string  `json:"id"`
	Text       string  `json:"text"`
	Ordinal    int32   `json:"ordinal"`
	Enabled    bool    `json:"enabled"`
	Kind       string  `json:"kind"`
	Source     string  `json:"source"`
	Origin     string  `json:"origin"`
	LandmarkID *string `json:"landmark_id"`
}

func newLayer4LocationQueryRecord(query sqlc.ProjectLocationQuery) layer4LocationQueryRecord {
	record := layer4LocationQueryRecord{
		ID:      query.ID.String(),
		Text:    query.Text,
		Ordinal: query.Ordinal,
		Enabled: query.Enabled,
		Kind:    query.Kind,
		Source:  query.Source,
		Origin:  query.Origin,
	}
	if query.LandmarkID.Valid {
		landmarkID := query.LandmarkID.String()
		record.LandmarkID = &landmarkID
	}
	return record
}

func (a *App) handleListProjectLocationQueries(w http.ResponseWriter, r *http.Request) {
	projectID, locationID, userID, _, ok := a.layer4Location(w, r)
	if !ok {
		return
	}
	rows, err := a.Queries.ListProjectLocationQueriesForUser(r.Context(), sqlc.ListProjectLocationQueriesForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID})
	if err != nil {
		serverError(w, r, err)
		return
	}
	records := make([]layer4LocationQueryRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, newLayer4LocationQueryRecord(row))
	}
	writeJSON(w, http.StatusOK, records)
}

type layer4QueryDraftEntry struct {
	ID      *string `json:"id"`
	Text    string  `json:"text"`
	Enabled bool    `json:"enabled"`
	Kind    string  `json:"kind"`
	Source  string  `json:"source"`
}

type layer4DraftError struct {
	status int
	msg    string
}

func (e *layer4DraftError) Error() string { return e.msg }

type layer4QueryDraftStore interface {
	lockDraftLocation(ctx context.Context) (sqlc.LockProjectLocationForQueryDraftForUserRow, error)
	deferDraftOrdinalConstraint(ctx context.Context) error
	lockDraftQueries(ctx context.Context) ([]sqlc.ProjectLocationQuery, error)
	deleteDraftManualQuery(ctx context.Context, id pgtype.UUID) error
	updateDraftQuery(ctx context.Context, update layer4DraftQueryUpdate) (sqlc.ProjectLocationQuery, error)
	insertDraftQuery(ctx context.Context, insert layer4DraftQueryInsert) (sqlc.ProjectLocationQuery, error)
}

type layer4DraftQueryUpdate struct {
	ID         pgtype.UUID
	Text       string
	Normalized string
	Ordinal    int32
	Enabled    bool
	Source     string
	Origin     string
	LandmarkID pgtype.UUID
}

type layer4DraftQueryInsert struct {
	Text       string
	Normalized string
	Ordinal    int32
	Enabled    bool
}

type layer4DraftPlan struct {
	updates []layer4DraftQueryUpdate
	inserts []layer4DraftQueryInsert
	deletes []pgtype.UUID
}

type sqlLayer4QueryDraftStore struct {
	queries    *sqlc.Queries
	tx         pgx.Tx
	projectID  pgtype.UUID
	locationID pgtype.UUID
	userID     pgtype.UUID
}

func (s *sqlLayer4QueryDraftStore) lockDraftLocation(ctx context.Context) (sqlc.LockProjectLocationForQueryDraftForUserRow, error) {
	return s.queries.LockProjectLocationForQueryDraftForUser(ctx, sqlc.LockProjectLocationForQueryDraftForUserParams{
		LocationID: s.locationID,
		ProjectID:  s.projectID,
		UserID:     s.userID,
	})
}

func (s *sqlLayer4QueryDraftStore) deferDraftOrdinalConstraint(ctx context.Context) error {
	_, err := s.tx.Exec(ctx, `SET CONSTRAINTS project_location_queries_location_id_kind_ordinal_key DEFERRED`)
	return err
}

func (s *sqlLayer4QueryDraftStore) lockDraftQueries(ctx context.Context) ([]sqlc.ProjectLocationQuery, error) {
	return s.queries.LockProjectLocationQueriesForUser(ctx, sqlc.LockProjectLocationQueriesForUserParams{
		LocationID: s.locationID,
		ProjectID:  s.projectID,
		UserID:     s.userID,
	})
}

func (s *sqlLayer4QueryDraftStore) deleteDraftManualQuery(ctx context.Context, id pgtype.UUID) error {
	deleted, err := s.queries.DeleteProjectLocationQueryForUser(ctx, sqlc.DeleteProjectLocationQueryForUserParams{
		ID:         id,
		LocationID: s.locationID,
		ProjectID:  s.projectID,
		UserID:     s.userID,
	})
	if err != nil {
		return err
	}
	if deleted == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *sqlLayer4QueryDraftStore) updateDraftQuery(ctx context.Context, update layer4DraftQueryUpdate) (sqlc.ProjectLocationQuery, error) {
	return s.queries.UpdateProjectLocationQueryForUser(ctx, sqlc.UpdateProjectLocationQueryForUserParams{
		Text:       update.Text,
		Normalized: update.Normalized,
		Ordinal:    update.Ordinal,
		Enabled:    update.Enabled,
		Kind:       "map",
		Source:     update.Source,
		Origin:     update.Origin,
		LandmarkID: update.LandmarkID,
		ID:         update.ID,
		LocationID: s.locationID,
		ProjectID:  s.projectID,
		UserID:     s.userID,
	})
}

func (s *sqlLayer4QueryDraftStore) insertDraftQuery(ctx context.Context, insert layer4DraftQueryInsert) (sqlc.ProjectLocationQuery, error) {
	return s.queries.InsertProjectLocationQueryForUser(ctx, sqlc.InsertProjectLocationQueryForUserParams{
		Text:       insert.Text,
		Normalized: insert.Normalized,
		Ordinal:    insert.Ordinal,
		Enabled:    insert.Enabled,
		Kind:       "map",
		Source:     "manual",
		Origin:     "service",
		LandmarkID: pgtype.UUID{},
		LocationID: s.locationID,
		ProjectID:  s.projectID,
		UserID:     s.userID,
	})
}

// planLayer4QueryDraft is the pure draft reconciler: submitted positions are
// ordinals, omitted manual rows are deleted, omitted generated rows are
// disabled and appended after the submitted ordinals, and an edited generated
// row becomes manual. It rejects empty, duplicate, and foreign ids.
func planLayer4QueryDraft(entries []layer4QueryDraftEntry, existing []sqlc.ProjectLocationQuery) (layer4DraftPlan, error) {
	byID := make(map[string]sqlc.ProjectLocationQuery, len(existing))
	for _, row := range existing {
		byID[row.ID.String()] = row
	}
	type submitted struct {
		id      pgtype.UUID
		hasID   bool
		stored  sqlc.ProjectLocationQuery
		display string
		key     string
		enabled bool
	}
	subs := make([]submitted, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for i, entry := range entries {
		if entry.Kind != "map" {
			return layer4DraftPlan{}, &layer4DraftError{status: http.StatusBadRequest, msg: "query kind must be map"}
		}
		if entry.Source != "manual" && entry.Source != "generated" {
			return layer4DraftPlan{}, &layer4DraftError{status: http.StatusBadRequest, msg: "query source must be manual or generated"}
		}
		display := textnormalization.NormalizeTextDisplay(entry.Text)
		if display == "" {
			return layer4DraftPlan{}, &layer4DraftError{status: http.StatusBadRequest, msg: fmt.Sprintf("query %d must not be empty", i+1)}
		}
		if len(display) > localvisibility.MaxMapQueryBytes {
			return layer4DraftPlan{}, &layer4DraftError{status: http.StatusBadRequest, msg: fmt.Sprintf("query %d exceeds %d bytes", i+1, localvisibility.MaxMapQueryBytes)}
		}
		key := textnormalization.NormalizeTextKey(entry.Text)
		if seen[key] {
			return layer4DraftPlan{}, &layer4DraftError{status: http.StatusBadRequest, msg: "duplicate query"}
		}
		seen[key] = true
		s := submitted{display: display, key: key, enabled: entry.Enabled}
		if entry.ID != nil {
			id, err := parseUUIDParam(*entry.ID)
			if err != nil {
				return layer4DraftPlan{}, &layer4DraftError{status: http.StatusBadRequest, msg: "invalid query id"}
			}
			stored, ok := byID[id.String()]
			if !ok {
				return layer4DraftPlan{}, &layer4DraftError{status: http.StatusBadRequest, msg: "unknown query"}
			}
			s.id, s.hasID, s.stored = id, true, stored
		}
		subs = append(subs, s)
	}
	var plan layer4DraftPlan
	claimed := make(map[string]bool, len(subs))
	for _, s := range subs {
		if s.hasID {
			if claimed[s.id.String()] {
				return layer4DraftPlan{}, &layer4DraftError{status: http.StatusBadRequest, msg: "duplicate query id"}
			}
			claimed[s.id.String()] = true
		}
	}
	omittedGenerated := make(map[string]sqlc.ProjectLocationQuery)
	for _, row := range existing {
		if claimed[row.ID.String()] {
			continue
		}
		if row.Source == "manual" {
			plan.deletes = append(plan.deletes, row.ID)
			continue
		}
		omittedGenerated[row.Normalized] = row
	}
	for i, s := range subs {
		ordinal := int32(i)
		if s.hasID {
			source := s.stored.Source
			if source == "generated" && s.key != s.stored.Normalized {
				source = "manual"
			}
			plan.updates = append(plan.updates, layer4DraftQueryUpdate{
				ID: s.id, Text: s.display, Normalized: s.key, Ordinal: ordinal,
				Enabled: s.enabled, Source: source, Origin: s.stored.Origin, LandmarkID: s.stored.LandmarkID,
			})
			continue
		}
		if gen, ok := omittedGenerated[s.key]; ok {
			delete(omittedGenerated, s.key)
			plan.updates = append(plan.updates, layer4DraftQueryUpdate{
				ID: gen.ID, Text: s.display, Normalized: s.key, Ordinal: ordinal,
				Enabled: s.enabled, Source: gen.Source, Origin: gen.Origin, LandmarkID: gen.LandmarkID,
			})
			continue
		}
		plan.inserts = append(plan.inserts, layer4DraftQueryInsert{
			Text: s.display, Normalized: s.key, Ordinal: ordinal, Enabled: s.enabled,
		})
	}
	rest := make([]sqlc.ProjectLocationQuery, 0, len(omittedGenerated))
	for _, row := range omittedGenerated {
		rest = append(rest, row)
	}
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].Ordinal != rest[j].Ordinal {
			return rest[i].Ordinal < rest[j].Ordinal
		}
		return rest[i].ID.String() < rest[j].ID.String()
	})
	for j, row := range rest {
		plan.updates = append(plan.updates, layer4DraftQueryUpdate{
			ID: row.ID, Text: row.Text, Normalized: row.Normalized, Ordinal: int32(len(subs) + j),
			Enabled: false, Source: row.Source, Origin: row.Origin, LandmarkID: row.LandmarkID,
		})
	}
	return plan, nil
}

// saveLayer4QueryDraft applies one atomic draft write. Callers must commit or
// roll back the surrounding transaction; the plan step means validation never
// leaves a half-written draft.
func saveLayer4QueryDraft(ctx context.Context, store layer4QueryDraftStore, entries []layer4QueryDraftEntry) ([]layer4LocationQueryRecord, error) {
	// The parent lock is the first statement: concurrent drafts for one
	// location serialize here, and no location read may precede it.
	if _, err := store.lockDraftLocation(ctx); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &layer4DraftError{status: http.StatusNotFound, msg: "location not found"}
		}
		return nil, err
	}
	if err := store.deferDraftOrdinalConstraint(ctx); err != nil {
		return nil, err
	}
	existing, err := store.lockDraftQueries(ctx)
	if err != nil {
		return nil, err
	}
	mapRows := make([]sqlc.ProjectLocationQuery, 0, len(existing))
	for _, row := range existing {
		if row.Kind == "map" {
			mapRows = append(mapRows, row)
		}
	}
	plan, err := planLayer4QueryDraft(entries, mapRows)
	if err != nil {
		return nil, err
	}
	for _, id := range plan.deletes {
		if err := store.deleteDraftManualQuery(ctx, id); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, &layer4DraftError{status: http.StatusNotFound, msg: "location not found"}
			}
			return nil, err
		}
	}
	saved := make([]sqlc.ProjectLocationQuery, 0, len(plan.updates)+len(plan.inserts))
	for _, update := range plan.updates {
		row, err := store.updateDraftQuery(ctx, update)
		if err != nil {
			switch {
			case isLayer4UniqueViolation(err):
				return nil, &layer4DraftError{status: http.StatusBadRequest, msg: "duplicate query"}
			case errors.Is(err, pgx.ErrNoRows):
				return nil, &layer4DraftError{status: http.StatusNotFound, msg: "location not found"}
			default:
				return nil, err
			}
		}
		saved = append(saved, row)
	}
	for _, insert := range plan.inserts {
		row, err := store.insertDraftQuery(ctx, insert)
		if err != nil {
			switch {
			case isLayer4UniqueViolation(err):
				return nil, &layer4DraftError{status: http.StatusBadRequest, msg: "duplicate query"}
			case errors.Is(err, pgx.ErrNoRows):
				return nil, &layer4DraftError{status: http.StatusNotFound, msg: "location not found"}
			default:
				return nil, err
			}
		}
		saved = append(saved, row)
	}
	sort.Slice(saved, func(i, j int) bool {
		if saved[i].Ordinal != saved[j].Ordinal {
			return saved[i].Ordinal < saved[j].Ordinal
		}
		return saved[i].ID.String() < saved[j].ID.String()
	})
	records := make([]layer4LocationQueryRecord, 0, len(saved))
	for _, row := range saved {
		records = append(records, newLayer4LocationQueryRecord(row))
	}
	return records, nil
}

func (a *App) handleUpdateLocationQueries(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	locationID, err := parseUUIDParam(chi.URLParam(r, "locationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid location id")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	var entries []layer4QueryDraftEntry
	if !readStrictJSONOrRespond(w, r, &entries) {
		return
	}

	// withTx cannot run the raw ordinal deferral, so this handler owns its
	// transaction and commits only after the full plan applies.
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(r.Context())
		}
	}()
	store := &sqlLayer4QueryDraftStore{
		queries:    a.Queries.WithTx(tx),
		tx:         tx,
		projectID:  projectID,
		locationID: locationID,
		userID:     principal.User.ID,
	}
	records, err := saveLayer4QueryDraft(r.Context(), store, entries)
	if err != nil {
		var draftErr *layer4DraftError
		if errors.As(err, &draftErr) {
			writeJSONError(w, draftErr.status, draftErr.msg)
			return
		}
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	committed = true
	writeJSON(w, http.StatusOK, records)
}
