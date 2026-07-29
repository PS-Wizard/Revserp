package aitools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

type fakeCompareReader struct {
	projects []sqlc.Project
	// crawlsByProject is keyed by project UUID string; only completed crawls.
	crawlsByProject map[string][]sqlc.ListCrawlsForProjectRow
	// breakdownByCrawl is keyed by crawl UUID string.
	breakdownByCrawl map[string]issueshared.ScoreBreakdownSnapshot
	overallByCrawl   map[string]int32
	// crawlUserIDs records the UserID every score read was scoped by, so a test
	// can assert the competitor side is still authorized as the caller.
	crawlUserIDs []pgtype.UUID
}

func (f *fakeCompareReader) ListProjectsForOrganizationForUser(_ context.Context, _ sqlc.ListProjectsForOrganizationForUserParams) ([]sqlc.Project, error) {
	return f.projects, nil
}

func (f *fakeCompareReader) ListCrawlsForProject(_ context.Context, arg sqlc.ListCrawlsForProjectParams) ([]sqlc.ListCrawlsForProjectRow, error) {
	if arg.Column2 != "completed" {
		return nil, nil
	}
	return f.crawlsByProject[arg.ProjectID.String()], nil
}

func (f *fakeCompareReader) GetCrawlByIDForUser(_ context.Context, arg sqlc.GetCrawlByIDForUserParams) (sqlc.GetCrawlByIDForUserRow, error) {
	f.crawlUserIDs = append(f.crawlUserIDs, arg.UserID)
	return sqlc.GetCrawlByIDForUserRow{
		OverallScore: pgtype.Int4{Int32: f.overallByCrawl[arg.ID.String()], Valid: true},
	}, nil
}

func (f *fakeCompareReader) GetCrawlScoreBreakdownByCrawlForUser(_ context.Context, arg sqlc.GetCrawlScoreBreakdownByCrawlForUserParams) (sqlc.CrawlScoreBreakdown, error) {
	encoded, err := json.Marshal(f.breakdownByCrawl[arg.CrawlID.String()])
	if err != nil {
		return sqlc.CrawlScoreBreakdown{}, err
	}
	return sqlc.CrawlScoreBreakdown{BreakdownJson: encoded}, nil
}

func snapshotWithScore(pillarScore int32) issueshared.ScoreBreakdownSnapshot {
	return issueshared.ScoreBreakdownSnapshot{
		Pillars: []issueshared.PillarScoreBreakdown{{
			ID: "seo", Label: "SEO", Score: pillarScore,
			Buckets: []issueshared.BucketScoreBreakdown{
				{ID: "serp_metadata", Label: "SERP Metadata", Score: pillarScore, AffectedURLCount: 3},
			},
		}},
	}
}

// compareFixture wires an active project with a completed crawl plus one
// competitor project with its own completed crawl.
func compareFixture() (Scope, *fakeCompareReader, pgtype.UUID) {
	userID := testUUID(1)
	orgID := testUUID(2)
	activeProjectID := testUUID(3)
	activeCrawlID := testUUID(4)
	competitorProjectID := testUUID(5)
	competitorCrawlID := testUUID(6)

	reader := &fakeCompareReader{
		projects: []sqlc.Project{
			{ID: activeProjectID, Name: "Our Site", BaseUrl: "https://ours.example"},
			{ID: competitorProjectID, Name: "Rival Co", BaseUrl: "https://rival.example"},
		},
		crawlsByProject: map[string][]sqlc.ListCrawlsForProjectRow{
			competitorProjectID.String(): {{ID: competitorCrawlID}},
			activeProjectID.String():     {{ID: activeCrawlID}},
		},
		breakdownByCrawl: map[string]issueshared.ScoreBreakdownSnapshot{
			activeCrawlID.String():     snapshotWithScore(61),
			competitorCrawlID.String(): snapshotWithScore(92),
		},
		overallByCrawl: map[string]int32{
			activeCrawlID.String():     65,
			competitorCrawlID.String(): 90,
		},
	}

	scope := Scope{UserID: userID, OrgID: orgID, ProjectID: activeProjectID, CrawlID: activeCrawlID}
	return scope, reader, competitorCrawlID
}

func TestExecCompareProjects_ReturnsBothSidesAndCompareAction(t *testing.T) {
	scope, reader, competitorCrawlID := compareFixture()

	result, err := execCompareProjects(context.Background(), json.RawMessage(`{"competitor":"rival co"}`), scope, reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var output compareOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("content is not compareOutput JSON: %v", err)
	}
	if output.Current.ProjectName != "Our Site" || output.Competitor.ProjectName != "Rival Co" {
		t.Fatalf("expected both sides named, got %+v", output)
	}
	// Both sides must carry real scores; a comparison the model cannot explain
	// is the specific failure this tool exists to prevent.
	if output.Current.Scores.OverallScore != 65 || output.Competitor.Scores.OverallScore != 90 {
		t.Fatalf("expected per-side overall scores 65/90, got %d/%d",
			output.Current.Scores.OverallScore, output.Competitor.Scores.OverallScore)
	}
	if len(output.Current.Scores.Pillars) == 0 || len(output.Competitor.Scores.Pillars) == 0 {
		t.Fatal("expected pillar breakdowns on both sides")
	}
	if output.Competitor.Scores.Pillars[0].Buckets[0].Score != 92 {
		t.Fatalf("expected competitor bucket score 92, got %d", output.Competitor.Scores.Pillars[0].Buckets[0].Score)
	}

	if result.CompareCrawlID != competitorCrawlID.String() {
		t.Fatalf("expected compare action to target the competitor's latest completed crawl, got %q", result.CompareCrawlID)
	}
	if result.CompareProjectID != testUUID(5).String() {
		t.Fatalf("expected compare action to carry the competitor project, got %q", result.CompareProjectID)
	}

	// Every crawl read stays scoped to the caller, including the competitor's.
	if len(reader.crawlUserIDs) != 2 {
		t.Fatalf("expected two crawl reads, got %d", len(reader.crawlUserIDs))
	}
	for _, got := range reader.crawlUserIDs {
		if got != scope.UserID {
			t.Fatalf("crawl read escaped caller scope: %v", got)
		}
	}
}

func TestExecCompareProjects_RejectsBadTargets(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		mutate  func(*Scope, *fakeCompareReader)
		wantErr string
	}{
		{
			name:    "unknown project",
			args:    `{"competitor":"Nobody"}`,
			wantErr: "no visible project",
		},
		{
			name:    "comparing a project with itself",
			args:    `{"competitor":"Our Site"}`,
			wantErr: "two different projects",
		},
		{
			name: "competitor has never completed a crawl",
			args: `{"competitor":"Rival Co"}`,
			mutate: func(_ *Scope, r *fakeCompareReader) {
				delete(r.crawlsByProject, testUUID(5).String())
			},
			wantErr: "no completed crawl",
		},
		{
			name: "no crawl open on the active side",
			args: `{"competitor":"Rival Co"}`,
			mutate: func(s *Scope, _ *fakeCompareReader) {
				s.CrawlID = pgtype.UUID{}
			},
			wantErr: "completed crawl before running",
		},
		{
			name:    "tenant IDs are not accepted as arguments",
			args:    `{"competitor":"Rival Co","project_id":"` + testUUID(5).String() + `"}`,
			wantErr: "must be exactly an object",
		},
		{
			name: "ambiguous name",
			args: `{"competitor":"Rival Co"}`,
			mutate: func(_ *Scope, r *fakeCompareReader) {
				r.projects = append(r.projects, sqlc.Project{ID: testUUID(7), Name: "rival co"})
			},
			wantErr: "multiple visible projects",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scope, reader, _ := compareFixture()
			if tc.mutate != nil {
				tc.mutate(&scope, reader)
			}
			_, err := execCompareProjects(context.Background(), json.RawMessage(tc.args), scope, reader)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}
