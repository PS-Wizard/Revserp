package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectcompetitors"
)

// fakeAddCompetitorTx is a transaction stub: only commit/rollback matter.
type fakeAddCompetitorTx struct {
	pgx.Tx
	committed  bool
	rolledBack bool
	commitErr  error
}

func (f *fakeAddCompetitorTx) Commit(_ context.Context) error {
	f.committed = true
	return f.commitErr
}

func (f *fakeAddCompetitorTx) Rollback(_ context.Context) error {
	f.rolledBack = true
	return nil
}

type fakeAddCompetitorDB struct {
	tx  *fakeAddCompetitorTx
	err error
}

func (f *fakeAddCompetitorDB) Begin(_ context.Context) (pgx.Tx, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tx, nil
}

func testAddCompetitorIDs() (pgtype.UUID, pgtype.UUID) {
	return pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
}

func TestParseAddCompetitorArgs(t *testing.T) {
	args, err := parseAddCompetitorArgs(json.RawMessage(`{"name":"Rival","url":"https://rival.example/"}`))
	if err != nil || args.Name != "Rival" || args.URL != "https://rival.example/" {
		t.Fatalf("args = %+v, err = %v", args, err)
	}
	if _, err := parseAddCompetitorArgs(json.RawMessage(`{"name":"Rival"}`)); err == nil || !strings.Contains(err.Error(), `"url"`) {
		t.Fatalf("missing url err = %v", err)
	}
	if _, err := parseAddCompetitorArgs(json.RawMessage(`{"url":"https://rival.example/","bogus":1}`)); err == nil || !strings.Contains(err.Error(), "unknown argument") {
		t.Fatalf("unknown arg err = %v", err)
	}
	if _, err := parseAddCompetitorArgs(json.RawMessage(`{"url":null}`)); err == nil {
		t.Fatal("null url must fail")
	}
}

func TestExecuteAddCompetitorDeniesLocation(t *testing.T) {
	scope := Scope{LocationID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}}
	result, err := executeAddCompetitor(context.Background(), json.RawMessage(`{"url":"https://rival.example/"}`), scope)
	if err != nil {
		t.Fatalf("location denial must be model-visible, got err %v", err)
	}
	if !strings.HasPrefix(result.Content, "add_competitor error:") || !strings.Contains(result.Content, "location") {
		t.Fatalf("content = %q, want location denial", result.Content)
	}
}

func TestAddCompetitorRunSavesWithoutCrawling(t *testing.T) {
	projectID, userID := testAddCompetitorIDs()
	tx := &fakeAddCompetitorTx{}
	var gotName, gotURL string
	exec := addCompetitorExecutor{
		queries: &sqlc.Queries{},
		db:      &fakeAddCompetitorDB{tx: tx},
		save: func(_ context.Context, _ projectcompetitors.CompetitorStore, projectID, userID pgtype.UUID, name, rawURL string) (projectcompetitors.SaveOutcome, error) {
			gotName, gotURL = name, rawURL
			return projectcompetitors.SaveOutcome{
				Competitor:     sqlc.ProjectCompetitor{SeedUrl: "https://rival.example/", Name: "Rival"},
				MaxCompetitors: 3,
				Total:          1,
			}, nil
		},
	}
	result, err := exec.run(context.Background(), json.RawMessage(`{"name":"Rival","url":"https://rival.example/"}`), projectID, userID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotName != "Rival" || gotURL != "https://rival.example/" {
		t.Fatalf("saver got name %q url %q", gotName, gotURL)
	}
	if !tx.committed {
		t.Fatal("save must commit")
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(result.Content), &content); err != nil {
		t.Fatalf("content is not JSON: %v", err)
	}
	if content["status"] != "saved, not crawled" || content["crawled"] != false {
		t.Fatalf("content = %s, want explicit saved-not-crawled", result.Content)
	}
	if content["seed_url"] != "https://rival.example/" || content["name"] != "Rival" {
		t.Fatalf("content = %s, want saved row", result.Content)
	}
	if content["remaining"] != float64(2) || content["max_competitors"] != float64(3) {
		t.Fatalf("content = %s, want current capacity", result.Content)
	}
	if !strings.Contains(result.Summary, "not crawled") {
		t.Fatalf("summary = %q, want not-crawled marker", result.Summary)
	}
}

func TestAddCompetitorRetryReturnsExisting(t *testing.T) {
	projectID, userID := testAddCompetitorIDs()
	tx := &fakeAddCompetitorTx{}
	exec := addCompetitorExecutor{
		queries: &sqlc.Queries{},
		db:      &fakeAddCompetitorDB{tx: tx},
		save: func(_ context.Context, _ projectcompetitors.CompetitorStore, _, _ pgtype.UUID, _, _ string) (projectcompetitors.SaveOutcome, error) {
			return projectcompetitors.SaveOutcome{
				Competitor:     sqlc.ProjectCompetitor{SeedUrl: "https://rival.example/", Name: "Rival"},
				MaxCompetitors: 3,
				Total:          2,
				Existed:        true,
			}, nil
		},
	}
	result, err := exec.run(context.Background(), json.RawMessage(`{"url":"https://rival.example/"}`), projectID, userID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(result.Content), &content); err != nil {
		t.Fatalf("content is not JSON: %v", err)
	}
	if content["already_existed"] != true || content["crawled"] != false {
		t.Fatalf("content = %s, want idempotent existing row without crawl", result.Content)
	}
}

func TestAddCompetitorRunSurfacesPlanLimit(t *testing.T) {
	projectID, userID := testAddCompetitorIDs()
	tx := &fakeAddCompetitorTx{}
	exec := addCompetitorExecutor{
		queries: &sqlc.Queries{},
		db:      &fakeAddCompetitorDB{tx: tx},
		save: func(_ context.Context, _ projectcompetitors.CompetitorStore, _, _ pgtype.UUID, _, _ string) (projectcompetitors.SaveOutcome, error) {
			return projectcompetitors.SaveOutcome{}, &projectcompetitors.InputError{Msg: "competitor_limit_reached"}
		},
	}
	result, err := exec.run(context.Background(), json.RawMessage(`{"url":"https://rival.example/"}`), projectID, userID)
	if err != nil {
		t.Fatalf("limit must be model-visible, got err %v", err)
	}
	if result.Content != "add_competitor error: competitor_limit_reached" {
		t.Fatalf("content = %q", result.Content)
	}
	if tx.committed {
		t.Fatal("limit breach must not commit")
	}
}

func TestAddCompetitorRunPropagatesInfraErrors(t *testing.T) {
	projectID, userID := testAddCompetitorIDs()
	exec := addCompetitorExecutor{
		queries: &sqlc.Queries{},
		db:      &fakeAddCompetitorDB{tx: &fakeAddCompetitorTx{}},
		save: func(_ context.Context, _ projectcompetitors.CompetitorStore, _, _ pgtype.UUID, _, _ string) (projectcompetitors.SaveOutcome, error) {
			return projectcompetitors.SaveOutcome{}, errors.New("connection refused")
		},
	}
	if _, err := exec.run(context.Background(), json.RawMessage(`{"url":"https://rival.example/"}`), projectID, userID); err == nil {
		t.Fatal("infrastructure failure must propagate, not read as a model error")
	}
}

func TestAddCompetitorRegistryServesProjectOnly(t *testing.T) {
	if _, ok := NewRegistry().Get(addCompetitorName); !ok {
		t.Fatal("NewRegistry must serve add_competitor")
	}
	found := false
	for _, def := range CatalogDefs() {
		if def.Name == addCompetitorName {
			found = true
			if def.Description == "" || len(def.Schema) == 0 {
				t.Fatal("add_competitor needs a description and schema")
			}
		}
	}
	if !found {
		t.Fatal("CatalogDefs must list add_competitor")
	}
	if _, ok := NewLocationScopedRegistry(nil).Get(addCompetitorName); ok {
		t.Fatal("location-scoped registry must deny add_competitor so a location never writes the parent roster")
	}
}
