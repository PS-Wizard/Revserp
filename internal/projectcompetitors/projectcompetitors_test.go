package projectcompetitors

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type fakeCompetitorStore struct {
	project      sqlc.Project
	projectErr   error
	features     sqlc.GetOrganizationFeaturesByProjectIDRow
	featuresErr  error
	count        int64
	inserted     []sqlc.InsertProjectCompetitorParams
	insertErr    error
	insertResult sqlc.ProjectCompetitor
	list         []sqlc.ProjectCompetitor
	listErr      error
}

func (f *fakeCompetitorStore) GetProjectByIDForUserForBusinessProfileUpdate(_ context.Context, _ sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams) (sqlc.Project, error) {
	return f.project, f.projectErr
}

func (f *fakeCompetitorStore) GetOrganizationFeaturesByProjectID(_ context.Context, _ sqlc.GetOrganizationFeaturesByProjectIDParams) (sqlc.GetOrganizationFeaturesByProjectIDRow, error) {
	return f.features, f.featuresErr
}

func (f *fakeCompetitorStore) CountProjectCompetitors(_ context.Context, _ pgtype.UUID) (int64, error) {
	return f.count, nil
}

func (f *fakeCompetitorStore) InsertProjectCompetitor(_ context.Context, arg sqlc.InsertProjectCompetitorParams) (sqlc.ProjectCompetitor, error) {
	f.inserted = append(f.inserted, arg)
	if f.insertErr != nil {
		return sqlc.ProjectCompetitor{}, f.insertErr
	}
	if f.insertResult.SeedUrl == "" {
		f.insertResult.SeedUrl = arg.SeedUrl
		f.insertResult.Name = arg.Name
	}
	return f.insertResult, nil
}

func (f *fakeCompetitorStore) ListProjectCompetitorsForUser(_ context.Context, _ sqlc.ListProjectCompetitorsForUserParams) ([]sqlc.ProjectCompetitor, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.list, nil
}

func testIDs() (pgtype.UUID, pgtype.UUID) {
	return pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
}

func TestSaveProjectCompetitorValidation(t *testing.T) {
	projectID, userID := testIDs()
	cases := []struct {
		name    string
		url     string
		baseURL string
		want    string
	}{
		{"empty url", "   ", "http://9.9.9.9/", "seed_url is required"},
		{"unsupported scheme", "ftp://8.8.8.8/", "http://9.9.9.9/", "invalid seed_url"},
		{"private host", "http://169.254.169.254/", "http://9.9.9.9/", "seed_url must not point to a private or internal host"},
		{"own host", "https://9.9.9.9/rival", "http://9.9.9.9/", "competitor must not share the project host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeCompetitorStore{
				project:  sqlc.Project{BaseUrl: tc.baseURL},
				features: sqlc.GetOrganizationFeaturesByProjectIDRow{MaxCompetitors: 3},
			}
			_, err := SaveProjectCompetitor(context.Background(), store, projectID, userID, "Rival", tc.url)
			if !IsInputError(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want input error containing %q", err, tc.want)
			}
			if len(store.inserted) != 0 {
				t.Fatal("validation failure must not insert")
			}
		})
	}
}

func TestSaveProjectCompetitorSuccess(t *testing.T) {
	projectID, userID := testIDs()
	store := &fakeCompetitorStore{
		project:  sqlc.Project{BaseUrl: "http://9.9.9.9/"},
		features: sqlc.GetOrganizationFeaturesByProjectIDRow{MaxCompetitors: 3},
		count:    1,
	}
	outcome, err := SaveProjectCompetitor(context.Background(), store, projectID, userID, "", "https://8.8.8.8/path")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if outcome.Existed {
		t.Fatal("fresh save must not report existed")
	}
	if outcome.Competitor.SeedUrl != "https://8.8.8.8/path" {
		t.Fatalf("seed_url = %q, want canonical https://8.8.8.8/path", outcome.Competitor.SeedUrl)
	}
	if outcome.Competitor.Name != "8.8.8.8" {
		t.Fatalf("name = %q, want host default 8.8.8.8", outcome.Competitor.Name)
	}
	if outcome.MaxCompetitors != 3 || outcome.Total != 2 {
		t.Fatalf("capacity = max %d total %d, want max 3 total 2", outcome.MaxCompetitors, outcome.Total)
	}
}

func TestSaveProjectCompetitorDenied(t *testing.T) {
	projectID, userID := testIDs()
	store := &fakeCompetitorStore{projectErr: pgx.ErrNoRows}
	_, err := SaveProjectCompetitor(context.Background(), store, projectID, userID, "Rival", "https://8.8.8.8/")
	if !IsInputError(err) || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("err = %v, want access-denied input error", err)
	}
}

func TestSaveProjectCompetitorLimit(t *testing.T) {
	projectID, userID := testIDs()
	for _, tc := range []struct {
		name  string
		max   int32
		count int64
	}{
		{"full plan", 3, 3},
		{"zero capacity plan", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeCompetitorStore{
				project:  sqlc.Project{BaseUrl: "http://9.9.9.9/"},
				features: sqlc.GetOrganizationFeaturesByProjectIDRow{MaxCompetitors: tc.max},
				count:    tc.count,
			}
			_, err := SaveProjectCompetitor(context.Background(), store, projectID, userID, "Rival", "https://8.8.8.8/")
			if !IsInputError(err) || err.Error() != "competitor_limit_reached" {
				t.Fatalf("err = %v, want competitor_limit_reached", err)
			}
			if len(store.inserted) != 0 {
				t.Fatal("limit breach must not insert")
			}
		})
	}
}

func TestSaveProjectCompetitorDuplicateReturnsExisting(t *testing.T) {
	projectID, userID := testIDs()
	existing := sqlc.ProjectCompetitor{SeedUrl: "https://8.8.8.8/", Name: "Rival"}
	store := &fakeCompetitorStore{
		project:  sqlc.Project{BaseUrl: "http://9.9.9.9/"},
		features: sqlc.GetOrganizationFeaturesByProjectIDRow{MaxCompetitors: 3},
		count:    1,
		list:     []sqlc.ProjectCompetitor{existing},
	}
	outcome, err := SaveProjectCompetitor(context.Background(), store, projectID, userID, "Rival", "https://8.8.8.8/")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !outcome.Existed {
		t.Fatal("retry of identical canonical URL must report existed")
	}
	if outcome.Competitor.SeedUrl != existing.SeedUrl || outcome.Competitor.Name != existing.Name {
		t.Fatalf("outcome = %+v, want existing row", outcome.Competitor)
	}
	if len(store.inserted) != 0 {
		t.Fatal("duplicate path must not insert; the lookup under lock replaces the catch-unique-then-query path")
	}
	if outcome.Total != 1 {
		t.Fatalf("total = %d, want actual count 1, not count+1", outcome.Total)
	}
}

func TestSaveProjectCompetitorDuplicateAtFullCapacity(t *testing.T) {
	projectID, userID := testIDs()
	existing := sqlc.ProjectCompetitor{SeedUrl: "https://8.8.8.8/", Name: "Rival"}
	store := &fakeCompetitorStore{
		project:  sqlc.Project{BaseUrl: "http://9.9.9.9/"},
		features: sqlc.GetOrganizationFeaturesByProjectIDRow{MaxCompetitors: 1},
		count:    1,
		list:     []sqlc.ProjectCompetitor{existing},
	}
	outcome, err := SaveProjectCompetitor(context.Background(), store, projectID, userID, "Rival", "https://8.8.8.8/")
	if err != nil {
		t.Fatalf("retry at full capacity: %v", err)
	}
	if !outcome.Existed || outcome.Competitor.SeedUrl != existing.SeedUrl {
		t.Fatalf("outcome = %+v, want existing row despite full plan", outcome)
	}
	if outcome.Total != 1 {
		t.Fatalf("total = %d, want actual count 1", outcome.Total)
	}
	if len(store.inserted) != 0 {
		t.Fatal("duplicate at full capacity must not insert")
	}
}

func TestSaveProjectCompetitorExistingLookupErrorPropagates(t *testing.T) {
	projectID, userID := testIDs()
	store := &fakeCompetitorStore{
		project:  sqlc.Project{BaseUrl: "http://9.9.9.9/"},
		features: sqlc.GetOrganizationFeaturesByProjectIDRow{MaxCompetitors: 3},
		listErr:  errors.New("connection refused"),
	}
	_, err := SaveProjectCompetitor(context.Background(), store, projectID, userID, "Rival", "https://8.8.8.8/")
	if err == nil || IsInputError(err) {
		t.Fatalf("err = %v, want the infrastructure lookup failure", err)
	}
	if len(store.inserted) != 0 {
		t.Fatal("lookup failure must not insert")
	}
}
