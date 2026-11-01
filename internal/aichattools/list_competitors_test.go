package aichattools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywordanalysis"
)

// fakeCompetitorRosterStore implements competitorRosterStore without a database.
type fakeCompetitorRosterStore struct {
	project   sqlc.Project
	accessErr error
	roster    []sqlc.ProjectCompetitor
	rosterErr error
	parent    pgtype.UUID
	parentErr error
	children  map[string]sqlc.GetCompetitorChildCrawlForParentRow
	childErr  error
	pages     []sqlc.ListKeywordCoveragePagesForCrawlRow
	pagesErr  error
}

func (f *fakeCompetitorRosterStore) GetProjectByIDForUser(_ context.Context, _ sqlc.GetProjectByIDForUserParams) (sqlc.Project, error) {
	if f.accessErr != nil {
		return sqlc.Project{}, f.accessErr
	}
	return f.project, nil
}

func (f *fakeCompetitorRosterStore) ListProjectCompetitorsForUser(_ context.Context, _ sqlc.ListProjectCompetitorsForUserParams) ([]sqlc.ProjectCompetitor, error) {
	if f.rosterErr != nil {
		return nil, f.rosterErr
	}
	return f.roster, nil
}

func (f *fakeCompetitorRosterStore) GetLatestCompletedCrawlForProject(_ context.Context, _ pgtype.UUID) (pgtype.UUID, error) {
	if f.parentErr != nil {
		return pgtype.UUID{}, f.parentErr
	}
	return f.parent, nil
}

func (f *fakeCompetitorRosterStore) GetCompetitorChildCrawlForParent(_ context.Context, arg sqlc.GetCompetitorChildCrawlForParentParams) (sqlc.GetCompetitorChildCrawlForParentRow, error) {
	if f.childErr != nil {
		return sqlc.GetCompetitorChildCrawlForParentRow{}, f.childErr
	}
	if row, ok := f.children[arg.CompetitorID.String()]; ok {
		return row, nil
	}
	return sqlc.GetCompetitorChildCrawlForParentRow{}, pgx.ErrNoRows
}

func (f *fakeCompetitorRosterStore) ListKeywordCoveragePagesForCrawl(_ context.Context, _ pgtype.UUID) ([]sqlc.ListKeywordCoveragePagesForCrawlRow, error) {
	if f.pagesErr != nil {
		return nil, f.pagesErr
	}
	return f.pages, nil
}

// fakeCompetitorAnalyses implements competitorAnalysesLister without a database.
type fakeCompetitorAnalyses struct {
	byCompetitor map[string][]keywordanalysis.Analysis
	err          error
}

func (f *fakeCompetitorAnalyses) ListCompetitorAnalyses(_ context.Context, competitorID pgtype.UUID) ([]keywordanalysis.Analysis, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byCompetitor[competitorID.String()], nil
}

var (
	testCompetitorAID = pgtype.UUID{Bytes: [16]byte{11}, Valid: true}
	testCompetitorBID = pgtype.UUID{Bytes: [16]byte{12}, Valid: true}
	testParentCrawlID = pgtype.UUID{Bytes: [16]byte{13}, Valid: true}
	testChildCrawlID  = pgtype.UUID{Bytes: [16]byte{14}, Valid: true}
)

func testCompetitorRoster() []sqlc.ProjectCompetitor {
	return []sqlc.ProjectCompetitor{
		{ID: testCompetitorAID, ProjectID: testProjectID, Name: "Rival Plumbing", SeedUrl: "https://rival.example/"},
		{ID: testCompetitorBID, ProjectID: testProjectID, Name: "Apex Drains", SeedUrl: "https://apex.example/"},
	}
}

func newListCompetitorsExecutor(store *fakeCompetitorRosterStore, analyses *fakeCompetitorAnalyses) *listCompetitorsExecutor {
	return &listCompetitorsExecutor{store: store, analyses: analyses}
}

func decodeListCompetitors(t *testing.T, result Result) listCompetitorsResponse {
	t.Helper()
	var response listCompetitorsResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("content is not list_competitors JSON: %v\n%s", err, result.Content)
	}
	return response
}

func TestListCompetitorsRejectsArguments(t *testing.T) {
	store := &fakeCompetitorRosterStore{project: sqlc.Project{ID: testProjectID}}
	exec := newListCompetitorsExecutor(store, &fakeCompetitorAnalyses{})
	result, err := exec.run(context.Background(), json.RawMessage(`{"project_id":"x"}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	if !strings.Contains(result.Content, "unknown argument") {
		t.Fatalf("content = %q, want argument rejection", result.Content)
	}
}

func TestListCompetitorsAccessDenied(t *testing.T) {
	store := &fakeCompetitorRosterStore{accessErr: pgx.ErrNoRows}
	exec := newListCompetitorsExecutor(store, &fakeCompetitorAnalyses{})
	result, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	if !strings.Contains(result.Content, "project not found or access denied") {
		t.Fatalf("content = %q, want access denial", result.Content)
	}
}

func TestListCompetitorsEmptyRoster(t *testing.T) {
	store := &fakeCompetitorRosterStore{project: sqlc.Project{ID: testProjectID}, parentErr: pgx.ErrNoRows}
	exec := newListCompetitorsExecutor(store, &fakeCompetitorAnalyses{})
	result, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	response := decodeListCompetitors(t, result)
	if response.Total != 0 || response.Competitors == nil || len(response.Competitors) != 0 {
		t.Fatalf("response = %+v, want empty non-null roster", response)
	}
	if result.Summary != "no competitors saved yet" {
		t.Fatalf("summary = %q, want empty-roster summary", result.Summary)
	}
}

func TestListCompetitorsReportsCrawlStatusAndKeywordCounts(t *testing.T) {
	rank := int32(3)
	store := &fakeCompetitorRosterStore{
		project: sqlc.Project{ID: testProjectID},
		roster:  testCompetitorRoster(),
		parent:  testParentCrawlID,
		children: map[string]sqlc.GetCompetitorChildCrawlForParentRow{
			testCompetitorAID.String(): {ID: testChildCrawlID, Status: "completed"},
			testCompetitorBID.String(): {Status: "queued"},
		},
	}
	analyses := &fakeCompetitorAnalyses{byCompetitor: map[string][]keywordanalysis.Analysis{
		testCompetitorAID.String(): {
			{Keyword: "Rival", Kind: "brand", Rank: &rank},
			{Keyword: "plumbing repair", Kind: "non_brand"},
			{Keyword: "drain cleaning", Kind: "non_brand"},
		},
	}}
	exec := newListCompetitorsExecutor(store, analyses)
	result, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	response := decodeListCompetitors(t, result)
	if response.Total != 2 || len(response.Competitors) != 2 {
		t.Fatalf("response = %+v, want 2 competitors", response)
	}
	first := response.Competitors[0]
	if first.Name != "Rival Plumbing" || first.SeedURL != "https://rival.example/" || first.CrawlStatus != "completed" {
		t.Fatalf("first = %+v, want rival row with completed crawl", first)
	}
	if first.BrandKeywords != 1 || first.NonBrandKeywords != 2 {
		t.Fatalf("first counts = %+v, want 1 brand and 2 non-brand", first)
	}
	second := response.Competitors[1]
	if second.CrawlStatus != "queued" || second.BrandKeywords != 0 || second.NonBrandKeywords != 0 {
		t.Fatalf("second = %+v, want queued row with zero counts", second)
	}
	if result.Summary != "competitors: 2 (1 crawled)" {
		t.Fatalf("summary = %q, want roster counts", result.Summary)
	}
}

func TestListCompetitorsWithoutParentCrawlMarksNotCrawled(t *testing.T) {
	store := &fakeCompetitorRosterStore{
		project:   sqlc.Project{ID: testProjectID},
		roster:    testCompetitorRoster(),
		parentErr: pgx.ErrNoRows,
	}
	exec := newListCompetitorsExecutor(store, &fakeCompetitorAnalyses{})
	result, err := exec.run(context.Background(), json.RawMessage(`{}`), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("infra error: %v", err)
	}
	response := decodeListCompetitors(t, result)
	for _, entry := range response.Competitors {
		if entry.CrawlStatus != "not_crawled" {
			t.Fatalf("entry = %+v, want not_crawled without a parent crawl", entry)
		}
	}
}

func TestExecuteListCompetitorsDeniesLocation(t *testing.T) {
	scope := Scope{LocationID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}}
	result, err := executeListCompetitors(context.Background(), json.RawMessage(`{}`), scope)
	if err != nil {
		t.Fatalf("location denial must be model-visible, got err %v", err)
	}
	if !strings.HasPrefix(result.Content, "list_competitors error:") || !strings.Contains(result.Content, "location") {
		t.Fatalf("content = %q, want location denial", result.Content)
	}
}
