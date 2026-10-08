package aichattools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywords"
	"github.com/ps-wizard/revserp/internal/locationkeywords"
)

// fakeLocationKeywordDB serves canned location_keywords rows and the profile
// snapshot without a database.
type fakeLocationKeywordDB struct {
	stored     []locationkeywords.StoredKeyword
	brand      string
	services   string
	profileErr error
	execs      []string
	execArgs   [][]any
}

func (f *fakeLocationKeywordDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	rows := make([][]any, 0, len(f.stored))
	for _, s := range f.stored {
		rows = append(rows, []any{s.Keyword, s.Normalized, s.Kind, s.Source})
	}
	return &fakeLocationKeywordRows{rows: rows}, nil
}

func (f *fakeLocationKeywordDB) QueryRow(context.Context, string, ...any) pgx.Row {
	if f.profileErr != nil {
		return &fakeLocationKeywordRow{err: f.profileErr}
	}
	return &fakeLocationKeywordRow{vals: []any{f.brand, []byte(f.services)}}
}

func (f *fakeLocationKeywordDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, sql)
	f.execArgs = append(f.execArgs, args)
	return pgconn.CommandTag{}, nil
}

type fakeLocationKeywordRow struct {
	vals []any
	err  error
}

func (r *fakeLocationKeywordRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return scanLocationKeywordVals(r.vals, dest)
}

type fakeLocationKeywordRows struct {
	rows [][]any
	pos  int
}

func (r *fakeLocationKeywordRows) Close()                        {}
func (r *fakeLocationKeywordRows) Err() error                    { return nil }
func (r *fakeLocationKeywordRows) CommandTag() pgconn.CommandTag { return pgconn.CommandTag{} }
func (r *fakeLocationKeywordRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}
func (r *fakeLocationKeywordRows) Next() bool {
	if r.pos < len(r.rows) {
		r.pos++
		return true
	}
	return false
}
func (r *fakeLocationKeywordRows) Scan(dest ...any) error {
	return scanLocationKeywordVals(r.rows[r.pos-1], dest)
}
func (r *fakeLocationKeywordRows) Values() ([]any, error) { return r.rows[r.pos-1], nil }
func (r *fakeLocationKeywordRows) RawValues() [][]byte    { return nil }
func (r *fakeLocationKeywordRows) Conn() *pgx.Conn        { return nil }

func scanLocationKeywordVals(vals []any, dest []any) error {
	if len(dest) != len(vals) {
		return fmt.Errorf("fake row arity %d, want %d", len(dest), len(vals))
	}
	for i, v := range vals {
		switch d := dest[i].(type) {
		case *string:
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("fake row value %d is %T, want string", i, v)
			}
			*d = s
		case *[]byte:
			b, ok := v.([]byte)
			if !ok {
				return fmt.Errorf("fake row value %d is %T, want []byte", i, v)
			}
			*d = b
		default:
			return fmt.Errorf("unsupported scan dest %T", dest[i])
		}
	}
	return nil
}

// fakeLocationKeywordQueries serves canned generated-query rows for the local
// keyword executors.
type fakeLocationKeywordQueries struct {
	location    sqlc.GetProjectLocationForUserRow
	locationErr error
	locked      sqlc.LockProjectLocationForQueryDraftForUserRow
	lockErr     error
	member      sqlc.OrganizationMember
	memberErr   error
	landmarks   []sqlc.LocationLandmark
	profile     sqlc.GetProjectBusinessProfileByProjectIDRow
	profileErr  error
	crawlID     pgtype.UUID
	crawlErr    error
	pages       []sqlc.ListKeywordCoveragePagesForCrawlRow
	userProfile sqlc.GetProjectBusinessProfileByProjectIDForUserRow
	inserted    []sqlc.InsertProjectLocationQueryForUserParams
	updated     []sqlc.UpdateProjectLocationQueryForUserParams
	existing    []sqlc.ProjectLocationQuery
}

func (f *fakeLocationKeywordQueries) GetProjectLocationForUser(context.Context, sqlc.GetProjectLocationForUserParams) (sqlc.GetProjectLocationForUserRow, error) {
	return f.location, f.locationErr
}

func (f *fakeLocationKeywordQueries) LockProjectLocationForQueryDraftForUser(context.Context, sqlc.LockProjectLocationForQueryDraftForUserParams) (sqlc.LockProjectLocationForQueryDraftForUserRow, error) {
	return f.locked, f.lockErr
}

func (f *fakeLocationKeywordQueries) GetOrganizationMember(context.Context, sqlc.GetOrganizationMemberParams) (sqlc.OrganizationMember, error) {
	return f.member, f.memberErr
}

func (f *fakeLocationKeywordQueries) ListLocationLandmarksForUser(context.Context, sqlc.ListLocationLandmarksForUserParams) ([]sqlc.LocationLandmark, error) {
	return f.landmarks, nil
}

func (f *fakeLocationKeywordQueries) GetProjectBusinessProfileByProjectID(context.Context, pgtype.UUID) (sqlc.GetProjectBusinessProfileByProjectIDRow, error) {
	return f.profile, f.profileErr
}

func (f *fakeLocationKeywordQueries) LockProjectLocationQueriesForUser(context.Context, sqlc.LockProjectLocationQueriesForUserParams) ([]sqlc.ProjectLocationQuery, error) {
	return f.existing, nil
}

func (f *fakeLocationKeywordQueries) InsertProjectLocationQueryForUser(_ context.Context, arg sqlc.InsertProjectLocationQueryForUserParams) (sqlc.ProjectLocationQuery, error) {
	f.inserted = append(f.inserted, arg)
	return sqlc.ProjectLocationQuery{}, nil
}

func (f *fakeLocationKeywordQueries) UpdateProjectLocationQueryForUser(_ context.Context, arg sqlc.UpdateProjectLocationQueryForUserParams) (sqlc.ProjectLocationQuery, error) {
	f.updated = append(f.updated, arg)
	return sqlc.ProjectLocationQuery{}, nil
}

func (f *fakeLocationKeywordQueries) GetLatestCompletedCrawlForProject(context.Context, pgtype.UUID) (pgtype.UUID, error) {
	return f.crawlID, f.crawlErr
}

func (f *fakeLocationKeywordQueries) ListKeywordCoveragePagesForCrawl(context.Context, pgtype.UUID) ([]sqlc.ListKeywordCoveragePagesForCrawlRow, error) {
	return f.pages, nil
}

func (f *fakeLocationKeywordQueries) GetProjectBusinessProfileByProjectIDForUser(context.Context, sqlc.GetProjectBusinessProfileByProjectIDForUserParams) (sqlc.GetProjectBusinessProfileByProjectIDForUserRow, error) {
	return f.userProfile, nil
}

func testLocationAccess() *fakeLocationKeywordQueries {
	return &fakeLocationKeywordQueries{
		location: sqlc.GetProjectLocationForUserRow{Localities: []byte(`["Springfield"]`)},
		locked:   sqlc.LockProjectLocationForQueryDraftForUserRow{},
		member:   sqlc.OrganizationMember{Role: "owner"},
	}
}

func TestLocationKeywordListsLocalReads(t *testing.T) {
	db := &fakeLocationKeywordDB{
		stored: []locationkeywords.StoredKeyword{
			{Keyword: "Acme", Normalized: "acme", Kind: "brand", Source: "user"},
			{Keyword: "Plumber", Normalized: "plumber", Kind: "non_brand", Source: "selected"},
		},
		brand:    "Acme",
		services: `["Plumber"]`,
	}
	queries := testLocationAccess()
	queries.landmarks = []sqlc.LocationLandmark{{Name: "Mall", Selected: true}, {Name: "Far", Selected: false}}
	exec := locationKeywordListsLocalExecutor{db: db, locations: queries}
	res, err := exec.runLocal(context.Background(), json.RawMessage(`{}`), testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("runLocal: %v", err)
	}
	var lists map[string]locationkeywords.KeywordGroup
	if err := json.Unmarshal([]byte(res.Content), &lists); err != nil {
		t.Fatalf("content is not keyword JSON: %v\n%s", err, res.Content)
	}
	if len(lists["user_defined"].Branded) != 1 || len(lists["selected"].NonBranded) != 1 {
		t.Fatalf("stored lists wrong: %s", res.Content)
	}
	joined := strings.Join(lists["revserp_suggested"].NonBranded, "\n")
	for _, want := range []string{"Plumber", "Springfield", "Mall", "Far", "Plumber in Springfield", "Plumber in Mall", "Plumber in Far"} {
		found := false
		for _, got := range lists["revserp_suggested"].NonBranded {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("suggestions lack %q: %q", want, joined)
		}
	}

	var payload struct {
		SuggestedOrigins locationkeywords.SuggestedOrigins `json:"suggested_origins"`
	}
	if err := json.Unmarshal([]byte(res.Content), &payload); err != nil {
		t.Fatalf("content origins: %v", err)
	}
	if sources := payload.SuggestedOrigins["mall"]; len(sources) == 0 {
		t.Fatalf("suggested_origins missing landmark provenance: %s", res.Content)
	}
}

func TestLocationKeywordListsLocalAccessDenied(t *testing.T) {
	queries := testLocationAccess()
	queries.locationErr = pgx.ErrNoRows
	exec := locationKeywordListsLocalExecutor{db: &fakeLocationKeywordDB{}, locations: queries}
	res, err := exec.runLocal(context.Background(), json.RawMessage(`{}`), testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("denial must stay model-visible: %v", err)
	}
	if !strings.Contains(res.Content, "location not found or access denied") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestLocationKeywordUpdateLocalReplacesSelectedOnly(t *testing.T) {
	db := &fakeLocationKeywordDB{
		stored: []locationkeywords.StoredKeyword{
			{Keyword: "Acme", Normalized: "acme", Kind: "brand", Source: "user"},
			{Keyword: "Old Pick", Normalized: "old pick", Kind: "non_brand", Source: "selected"},
		},
		brand:    "Acme",
		services: `["Plumber"]`,
	}
	queries := testLocationAccess()
	queries.location = sqlc.GetProjectLocationForUserRow{Localities: []byte(`["Springfield"]`)}
	exec := locationKeywordUpdateLocalExecutor{db: db, locations: queries}
	res, err := exec.runLocal(context.Background(),
		json.RawMessage(`{"brand_keywords":["Acme"],"non_brand_keywords":["Leak Repair"],"source":"selected"}`),
		testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("runLocal: %v", err)
	}
	var body map[string][]string
	if err := json.Unmarshal([]byte(res.Content), &body); err != nil {
		t.Fatalf("content is not keyword JSON: %v\n%s", err, res.Content)
	}
	if len(body["brand_keywords"]) != 1 || len(body["non_brand_keywords"]) != 1 {
		t.Fatalf("response = %s", res.Content)
	}
	for _, stmt := range db.execs {
		if strings.Contains(stmt, "IN ('user','selected')") {
			t.Fatalf("selected-only update must not wipe user rows: %q", stmt)
		}
	}
	if len(queries.inserted) != 2 {
		t.Fatalf("inserted %d map queries, want 2 (Acme, Leak Repair)", len(queries.inserted))
	}
	for _, q := range queries.inserted {
		if q.Kind != "map" || q.Source != "manual" || !q.Enabled {
			t.Fatalf("draft row wrong: %+v", q)
		}
	}
}

func TestLocationKeywordUpdateLocalSyncsDraftEnabledExactly(t *testing.T) {
	db := &fakeLocationKeywordDB{brand: "Acme", services: `["Plumber"]`}
	queries := testLocationAccess()
	queries.location = sqlc.GetProjectLocationForUserRow{Localities: []byte(`["Springfield"]`)}
	queries.existing = []sqlc.ProjectLocationQuery{
		{Text: "Leak Repair", Normalized: "leak repair", Ordinal: 0, Enabled: false, Kind: "map", Source: "generated", Origin: "landmark"},
		{Text: "Acme", Normalized: "acme", Ordinal: 1, Enabled: true, Kind: "map", Source: "manual", Origin: "service"},
		{Text: "Old Pick", Normalized: "old pick", Ordinal: 2, Enabled: true, Kind: "map", Source: "manual", Origin: "service"},
	}
	exec := locationKeywordUpdateLocalExecutor{db: db, locations: queries}
	_, err := exec.runLocal(context.Background(),
		json.RawMessage(`{"brand_keywords":["Acme"],"non_brand_keywords":["Leak Repair", "New Pick"],"source":"selected"}`),
		testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("runLocal: %v", err)
	}
	if len(queries.updated) != 2 {
		t.Fatalf("updated = %+v, want reselected enable + deselected disable", queries.updated)
	}
	byText := map[string]sqlc.UpdateProjectLocationQueryForUserParams{}
	for _, u := range queries.updated {
		byText[u.Text] = u
		if u.Kind != "map" {
			t.Fatalf("update kind = %q, map draft only", u.Kind)
		}
	}
	if u, ok := byText["Leak Repair"]; !ok || !u.Enabled || u.Ordinal != 0 || u.Source != "generated" || u.Origin != "landmark" {
		t.Fatalf("reselected disabled row must enable in place: %+v", byText)
	}
	if u, ok := byText["Old Pick"]; !ok || u.Enabled || u.Ordinal != 2 || u.Text != "Old Pick" {
		t.Fatalf("deselected enabled row must disable in place: %+v", byText)
	}
	if len(queries.inserted) != 1 || queries.inserted[0].Text != "New Pick" {
		t.Fatalf("inserted = %+v, want only the genuinely new phrase", queries.inserted)
	}
	if queries.inserted[0].Ordinal != 3 || !queries.inserted[0].Enabled {
		t.Fatalf("new row must append enabled: %+v", queries.inserted[0])
	}
}

func TestLocationKeywordUpdateLocalEmptySelectionDisablesAll(t *testing.T) {
	db := &fakeLocationKeywordDB{brand: "Acme", services: `[]`}
	queries := testLocationAccess()
	queries.location = sqlc.GetProjectLocationForUserRow{Localities: []byte(`[]`)}
	queries.existing = []sqlc.ProjectLocationQuery{
		{Text: "Acme", Normalized: "acme", Ordinal: 0, Enabled: true, Kind: "map", Source: "manual", Origin: "service"},
		{Text: "Old Pick", Normalized: "old pick", Ordinal: 1, Enabled: false, Kind: "map", Source: "manual", Origin: "service"},
	}
	exec := locationKeywordUpdateLocalExecutor{db: db, locations: queries}
	_, err := exec.runLocal(context.Background(),
		json.RawMessage(`{"brand_keywords":[],"non_brand_keywords":[],"source":"selected"}`),
		testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("runLocal: %v", err)
	}
	if len(queries.updated) != 1 || queries.updated[0].Text != "Acme" || queries.updated[0].Enabled {
		t.Fatalf("empty selection must disable every enabled draft row: %+v", queries.updated)
	}
	if len(queries.inserted) != 0 {
		t.Fatalf("empty selection must insert nothing: %+v", queries.inserted)
	}
}

func TestLocationKeywordUpdateLocalForbidden(t *testing.T) {
	queries := testLocationAccess()
	queries.member = sqlc.OrganizationMember{Role: "member"}
	exec := locationKeywordUpdateLocalExecutor{db: &fakeLocationKeywordDB{}, locations: queries}
	res, err := exec.runLocal(context.Background(),
		json.RawMessage(`{"brand_keywords":["a"],"non_brand_keywords":["b"],"source":"selected"}`),
		testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("denial must stay model-visible: %v", err)
	}
	if !strings.Contains(res.Content, "only organization owners") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestLocationKeywordUpdateLocalConflictIsModelVisible(t *testing.T) {
	exec := locationKeywordUpdateLocalExecutor{db: &fakeLocationKeywordDB{}, locations: testLocationAccess()}
	res, err := exec.runLocal(context.Background(),
		json.RawMessage(`{"brand_keywords":["Acme"],"non_brand_keywords":["ACME"],"source":"selected"}`),
		testProjectID, testLocationID, testUserID)
	if err != nil {
		t.Fatalf("conflict must stay model-visible: %v", err)
	}
	if !strings.Contains(res.Content, "both branded") {
		t.Fatalf("content = %q", res.Content)
	}
}

func TestLocationKeywordCoverageLocalUsesSelected(t *testing.T) {
	db := &fakeLocationKeywordDB{
		stored: []locationkeywords.StoredKeyword{
			{Keyword: "Plumber", Normalized: "plumber", Kind: "non_brand", Source: "selected"},
			{Keyword: "Ignored Parent Term", Normalized: "ignored parent term", Kind: "non_brand", Source: "user"},
		},
	}
	queries := testLocationAccess()
	queries.crawlID = pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	queries.pages = []sqlc.ListKeywordCoveragePagesForCrawlRow{{
		Url: "https://acme.example/plumber", Title: "Best Plumber in Town", H1: "Plumber",
		StatusCode: 200, ContentType: "text/html",
	}}
	exec := locationKeywordCoverageLocalExecutor{db: db, locations: queries}
	res, err := exec.runLocal(context.Background(), json.RawMessage(`{}`), testProjectID, testLocationID, testUserID, nil)
	if err != nil {
		t.Fatalf("runLocal: %v", err)
	}
	var body keywordCoverageResponse
	if err := json.Unmarshal([]byte(res.Content), &body); err != nil {
		t.Fatalf("content is not coverage JSON: %v\n%s", err, res.Content)
	}
	if body.TotalSeeds != 1 || body.Seeds[0].Keyword != "Plumber" {
		t.Fatalf("coverage must key on selected local keywords only: %s", res.Content)
	}
	if body.Seeds[0].State != string(keywords.StateLikelyTargeted) {
		t.Fatalf("state = %q", body.Seeds[0].State)
	}
}

func TestLocationKeywordCoverageLocalNoCrawl(t *testing.T) {
	queries := testLocationAccess()
	queries.crawlErr = pgx.ErrNoRows
	exec := locationKeywordCoverageLocalExecutor{db: &fakeLocationKeywordDB{}, locations: queries}
	res, err := exec.runLocal(context.Background(), json.RawMessage(`{}`), testProjectID, testLocationID, testUserID, nil)
	if err != nil {
		t.Fatalf("runLocal: %v", err)
	}
	if !strings.Contains(res.Content, "no completed crawl") {
		t.Fatalf("content = %q", res.Content)
	}
}
