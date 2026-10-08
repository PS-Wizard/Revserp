package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestGetLocationLandmarksNeedsLocationScope(t *testing.T) {
	res, err := executeGetLocationLandmarks(context.Background(), json.RawMessage(`{}`), Scope{})
	if err != nil {
		t.Fatalf("out-of-scope denial must be model-visible, got error: %v", err)
	}
	if !strings.Contains(res.Content, "only runs inside a location conversation") {
		t.Fatalf("content = %q, want scope denial", res.Content)
	}
}

func TestGetLocationLandmarksNeedsQueries(t *testing.T) {
	scope := Scope{LocationID: testLocationID}
	if _, err := executeGetLocationLandmarks(context.Background(), json.RawMessage(`{}`), scope); err == nil {
		t.Fatal("scope without queries must fail")
	}
}

func TestRunGetLocationLandmarksReturnsGeometryAndSavedRows(t *testing.T) {
	queries := &fakeLocationKeywordQueries{
		location: sqlc.GetProjectLocationForUserRow{
			Name: "Acme Thamel", Address: "Thamel 1", Locality: "Kathmandu",
			Localities: []byte(`["Kathmandu","Bagmati"]`), Latitude: 27.7, Longitude: 85.3,
		},
		landmarks: []sqlc.LocationLandmark{
			{Name: "Durbar Marg", StraightLineM: 400, Categories: []string{"shopping"}, Selected: true},
			{Name: "Swayambhu", StraightLineM: 2500, Categories: nil},
		},
	}
	exec := locationLandmarkExecutor{locations: queries}
	res, err := exec.runLocal(context.Background(), json.RawMessage(`{}`), testProjectID, testLocationID, testUserID, NewBudget(10))
	if err != nil {
		t.Fatalf("runLocal: %v", err)
	}
	var body struct {
		Location struct {
			Name       string   `json:"name"`
			Address    string   `json:"address"`
			Locality   string   `json:"locality"`
			Localities []string `json:"localities"`
			Latitude   float64  `json:"latitude"`
			Longitude  float64  `json:"longitude"`
		} `json:"location"`
		Landmarks []locationLandmarkResponse `json:"landmarks"`
	}
	if err := json.Unmarshal([]byte(res.Content), &body); err != nil {
		t.Fatalf("content is not JSON: %v\n%s", err, res.Content)
	}
	if body.Location.Name != "Acme Thamel" || body.Location.Address != "Thamel 1" || body.Location.Locality != "Kathmandu" {
		t.Fatalf("geometry = %+v, want owned location facts", body.Location)
	}
	if len(body.Location.Localities) != 2 || body.Location.Latitude != 27.7 || body.Location.Longitude != 85.3 {
		t.Fatalf("geometry = %+v, want localities and coordinates", body.Location)
	}
	if len(body.Landmarks) != 2 || body.Landmarks[0].Name != "Durbar Marg" || body.Landmarks[0].StraightLineM != 400 {
		t.Fatalf("landmarks = %+v, want saved rows", body.Landmarks)
	}
	if !body.Landmarks[0].Selected || len(body.Landmarks[0].Categories) != 1 {
		t.Fatalf("landmark = %+v, want distance, categories, selection", body.Landmarks[0])
	}
	if body.Landmarks[1].Categories == nil {
		t.Fatal("null categories must read as an empty list, never null")
	}
	if !strings.Contains(res.Summary, "2 saved nearby places") {
		t.Fatalf("summary = %q, want saved count", res.Summary)
	}
}

func TestRunGetLocationLandmarksEmptyStaysHonest(t *testing.T) {
	queries := &fakeLocationKeywordQueries{
		location: sqlc.GetProjectLocationForUserRow{Name: "Acme", Localities: []byte(`[]`)},
	}
	exec := locationLandmarkExecutor{locations: queries}
	res, err := exec.runLocal(context.Background(), json.RawMessage(`{}`), testProjectID, testLocationID, testUserID, nil)
	if err != nil {
		t.Fatalf("runLocal: %v", err)
	}
	if !strings.Contains(res.Content, `"landmarks":[]`) {
		t.Fatalf("content = %s, want an honest empty list", res.Content)
	}
}

func TestRunGetLocationLandmarksRejectsArgsAndDeniesForeigners(t *testing.T) {
	exec := locationLandmarkExecutor{locations: &fakeLocationKeywordQueries{locationErr: pgx.ErrNoRows}}
	res, err := exec.runLocal(context.Background(), json.RawMessage(`{"place":"x"}`), testProjectID, testLocationID, testUserID, nil)
	if err != nil {
		t.Fatalf("unknown args must be model-visible, got error: %v", err)
	}
	if !strings.Contains(res.Content, "unknown argument") {
		t.Fatalf("content = %q, want arg rejection", res.Content)
	}
	exec = locationLandmarkExecutor{locations: &fakeLocationKeywordQueries{locationErr: pgx.ErrNoRows}}
	res, err = exec.runLocal(context.Background(), json.RawMessage(`{}`), testProjectID, testLocationID, testUserID, nil)
	if err != nil {
		t.Fatalf("foreign location must be model-visible, got error: %v", err)
	}
	if !strings.Contains(res.Content, "access denied") {
		t.Fatalf("content = %q, want denial", res.Content)
	}
	boom := errors.New("boom")
	exec = locationLandmarkExecutor{locations: &fakeLocationKeywordQueries{locationErr: boom}}
	if _, err := exec.runLocal(context.Background(), json.RawMessage(`{}`), testProjectID, testLocationID, testUserID, nil); !errors.Is(err, boom) {
		t.Fatalf("infra error must propagate, got %v", err)
	}
}

func TestGetLocationLandmarksRegistryServesLocationTurns(t *testing.T) {
	for _, registry := range []*Registry{NewRegistry(), NewLocationScopedRegistry(nil)} {
		tool, ok := registry.Get(locationLandmarksName)
		if !ok {
			t.Fatalf("registry missing %s", locationLandmarksName)
		}
		if len(tool.Def.Schema) == 0 || strings.TrimSpace(tool.Def.Description) == "" {
			t.Fatalf("tool %s needs a schema and description", locationLandmarksName)
		}
	}
	found := false
	for _, def := range CatalogDefs() {
		if def.Name == locationLandmarksName {
			found = true
		}
	}
	if !found {
		t.Fatalf("catalog missing %s; admin gating cannot see it", locationLandmarksName)
	}
}

func TestRunGetLocationLandmarksEnforcesRowBudget(t *testing.T) {
	boom := errors.New("must not query on an empty budget")
	exec := locationLandmarkExecutor{locations: &fakeLocationKeywordQueries{locationErr: boom}}
	res, err := exec.runLocal(context.Background(), json.RawMessage(`{}`), testProjectID, testLocationID, testUserID, NewBudget(0))
	if err != nil {
		t.Fatalf("empty budget must be model-visible, got error: %v", err)
	}
	if !strings.Contains(res.Content, "row budget for this turn is exhausted") {
		t.Fatalf("content = %q, want budget notice without any query", res.Content)
	}

	queries := &fakeLocationKeywordQueries{
		location: sqlc.GetProjectLocationForUserRow{Name: "Acme"},
		landmarks: []sqlc.LocationLandmark{
			{Name: "Durbar Marg", StraightLineM: 400},
			{Name: "Swayambhu", StraightLineM: 2500},
		},
	}
	exec = locationLandmarkExecutor{locations: queries}
	budget := NewBudget(1)
	res, err = exec.runLocal(context.Background(), json.RawMessage(`{}`), testProjectID, testLocationID, testUserID, budget)
	if err != nil {
		t.Fatalf("runLocal: %v", err)
	}
	var body struct {
		Landmarks  []locationLandmarkResponse `json:"landmarks"`
		Truncated  bool                       `json:"truncated"`
		TotalSaved int                        `json:"total_saved"`
	}
	if err := json.Unmarshal([]byte(res.Content), &body); err != nil {
		t.Fatalf("content is not JSON: %v", err)
	}
	if len(body.Landmarks) != 1 || !body.Truncated || body.TotalSaved != 2 {
		t.Fatalf("body = %+v, want 1 bounded row with honest truncation metadata", body)
	}
	if budget.Remaining() != 0 {
		t.Fatalf("remaining = %d, want 0 after spending the bounded row", budget.Remaining())
	}
	if !strings.Contains(res.Summary, "1 of 2") {
		t.Fatalf("summary = %q, want bounded count", res.Summary)
	}
}
