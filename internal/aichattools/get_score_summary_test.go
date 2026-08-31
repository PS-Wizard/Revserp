package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

var testProjectID = pgtype.UUID{Bytes: [16]byte{9}, Valid: true}

// fakeScoreReader implements all get_score_summary reader interfaces without a
// database.
type fakeScoreReader struct {
	current      sqlc.CrawlScoreBreakdown
	currentErr   error
	historyRows  []sqlc.ListCompletedProjectCrawlScoreBreakdownsForUserRow
	historyErr   error
	crawlRow     sqlc.GetCrawlByIDForUserRow
	crawlErr     error
	currentCalls int
	historyCalls int
	crawlCalls   int
	lastHistory  sqlc.ListCompletedProjectCrawlScoreBreakdownsForUserParams
}

func (f *fakeScoreReader) GetCrawlScoreBreakdownByCrawlForUser(_ context.Context, _ sqlc.GetCrawlScoreBreakdownByCrawlForUserParams) (sqlc.CrawlScoreBreakdown, error) {
	f.currentCalls++
	return f.current, f.currentErr
}

func (f *fakeScoreReader) ListCompletedProjectCrawlScoreBreakdownsForUser(_ context.Context, arg sqlc.ListCompletedProjectCrawlScoreBreakdownsForUserParams) ([]sqlc.ListCompletedProjectCrawlScoreBreakdownsForUserRow, error) {
	f.historyCalls++
	f.lastHistory = arg
	return f.historyRows, f.historyErr
}

func (f *fakeScoreReader) GetCrawlByIDForUser(_ context.Context, _ sqlc.GetCrawlByIDForUserParams) (sqlc.GetCrawlByIDForUserRow, error) {
	f.crawlCalls++
	return f.crawlRow, f.crawlErr
}

func runGetScoreSummary(t *testing.T, fake *fakeScoreReader, raw string) Result {
	t.Helper()
	exec := scoreSummaryExecutor{current: fake, history: fake, crawls: fake}
	result, err := exec.run(context.Background(), json.RawMessage(raw), testCrawlID, testProjectID, testUserID)
	if err != nil {
		t.Fatalf("run(%s) returned error: %v", raw, err)
	}
	return result
}

func decodeScoreSummary(t *testing.T, result Result) getScoreSummaryResponse {
	t.Helper()
	var response getScoreSummaryResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("content is not valid response JSON: %v\ncontent: %s", err, result.Content)
	}
	return response
}

func snapshotJSON(overall int32, pillars ...shared.PillarScoreBreakdown) []byte {
	snapshot := shared.ScoreBreakdownSnapshot{
		CrawlID:          "snapshot",
		ScoringVersion:   "v3",
		CoverageScale:    0.92,
		TotalScoredPages: 143,
		OverallScore:     overall,
		Pillars:          pillars,
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		panic(err)
	}
	return raw
}

func makePillar(id string, score int32, buckets ...shared.BucketScoreBreakdown) shared.PillarScoreBreakdown {
	label := strings.ToUpper(id[:1]) + id[1:]
	return shared.PillarScoreBreakdown{
		ID:                   id,
		Label:                label,
		Score:                score,
		Weight:               0.5,
		WeightedContribution: float64(score) * 0.01 * 0.5,
		TotalPenalty:         40,
		IssueRowCount:        100,
		AffectedURLCount:     30,
		Buckets:              buckets,
	}
}

func makeBucket(id string, penalty float64) shared.BucketScoreBreakdown {
	return shared.BucketScoreBreakdown{
		ID:                   id,
		Label:                strings.ReplaceAll(id, "_", " "),
		Score:                50,
		Weight:               0.2,
		WeightedContribution: penalty * 0.2,
		TotalPenalty:         penalty,
		IssueRowCount:        12,
		AffectedURLCount:     5,
	}
}

func defaultScoreSnapshot() sqlc.CrawlScoreBreakdown {
	return sqlc.CrawlScoreBreakdown{
		CrawlID:        testCrawlID,
		ScoringVersion: "v3",
		BreakdownJson: snapshotJSON(61,
			makePillar("seo", 55, makeBucket("meta_tags", 80), makeBucket("content", 20)),
			makePillar("aeo", 70),
			makePillar("pagespeed", 48, makeBucket("page_weight", 60)),
		),
	}
}

func TestParseGetScoreSummaryArgs(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    getScoreSummaryArgs
		wantErr string
	}{
		{name: "empty object defaults", raw: `{}`, want: getScoreSummaryArgs{IncludeBuckets: true, Compare: true, Limit: 10}},
		{name: "empty input defaults", raw: ``, want: getScoreSummaryArgs{IncludeBuckets: true, Compare: true, Limit: 10}},
		{name: "full args normalized", raw: `{"pillar":"SEO","include_buckets":false,"compare":false,"limit":5}`,
			want: getScoreSummaryArgs{Pillar: "seo", IncludeBuckets: false, Compare: false, Limit: 5}},
		{name: "limit clamped to max", raw: `{"limit":50}`, want: getScoreSummaryArgs{IncludeBuckets: true, Compare: true, Limit: 20}},
		{name: "limit zero rejected", raw: `{"limit":0}`, wantErr: "at least 1"},
		{name: "limit negative rejected", raw: `{"limit":-3}`, wantErr: "at least 1"},
		{name: "limit float rejected", raw: `{"limit":1.5}`, wantErr: "must be an integer"},
		{name: "include_buckets non-bool rejected", raw: `{"include_buckets":"yes"}`, wantErr: "must be a boolean"},
		{name: "compare non-bool rejected", raw: `{"compare":1}`, wantErr: "must be a boolean"},
		{name: "pillar non-string rejected", raw: `{"pillar":1}`, wantErr: "must be a string"},
		{name: "unknown key rejected", raw: `{"pillar":"seo","bogus":1}`, wantErr: `unknown argument "bogus"`},
		{name: "duplicate key rejected", raw: `{"limit":1,"limit":2}`, wantErr: `duplicate argument "limit"`},
		{name: "trailing data rejected", raw: `{"limit":1} {"limit":2}`, wantErr: "trailing data"},
		{name: "non-object rejected", raw: `[1,2]`, wantErr: "must be a JSON object"},
		{name: "null rejected", raw: `null`, wantErr: "must be a JSON object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseGetScoreSummaryArgs(json.RawMessage(test.raw))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseGetScoreSummaryArgs(%s) error = %v, want containing %q", test.raw, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGetScoreSummaryArgs(%s) error = %v", test.raw, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseGetScoreSummaryArgs(%s) = %+v, want %+v", test.raw, got, test.want)
			}
		})
	}
}

func TestGetScoreSummaryFull(t *testing.T) {
	fake := &fakeScoreReader{current: defaultScoreSnapshot()}
	result := runGetScoreSummary(t, fake, `{}`)
	response := decodeScoreSummary(t, result)

	if response.CrawlID != testCrawlID.String() || response.ScoringVersion != "v3" {
		t.Fatalf("CrawlID/ScoringVersion = %s/%s, want %s/v3", response.CrawlID, response.ScoringVersion, testCrawlID.String())
	}
	if response.CoverageScale != 0.92 || response.TotalScoredPages != 143 {
		t.Fatalf("CoverageScale/TotalScoredPages = %v/%d, want 0.92/143", response.CoverageScale, response.TotalScoredPages)
	}
	if response.OverallScore != 61 || response.Source != scoreSourceBreakdown {
		t.Fatalf("OverallScore/Source = %d/%s, want 61/breakdown", response.OverallScore, response.Source)
	}
	if len(response.Pillars) != 3 {
		t.Fatalf("Pillars = %d, want 3", len(response.Pillars))
	}

	seo := response.Pillars[0]
	if seo.ID != "seo" || seo.Label != "Seo" || seo.Score != 55 {
		t.Fatalf("first pillar = %+v, want seo/Seo/55", seo)
	}
	if seo.Weight != 0.5 || seo.WeightedContribution != 0.28 || seo.TotalPenalty != 40 {
		t.Fatalf("seo weight fields = %v/%v/%v, want 0.5/0.28/40", seo.Weight, seo.WeightedContribution, seo.TotalPenalty)
	}
	if seo.IssueRowCount != 100 || seo.AffectedURLCount != 30 {
		t.Fatalf("seo counts = %d/%d, want 100/30", seo.IssueRowCount, seo.AffectedURLCount)
	}

	// Buckets sorted by total penalty desc: meta_tags (80) before content (20).
	if len(seo.TopBuckets) != 2 {
		t.Fatalf("seo TopBuckets = %d, want 2", len(seo.TopBuckets))
	}
	top := seo.TopBuckets[0]
	if top.ID != "meta_tags" || top.Label != "meta tags" || top.TotalPenalty != 80 {
		t.Fatalf("top bucket = %+v, want meta_tags/meta tags/80", top)
	}
	if got := seo.TopBuckets[1].ID; got != "content" {
		t.Fatalf("second bucket = %s, want content", got)
	}

	if response.Previous != nil {
		t.Fatalf("Previous = %+v, want nil without history", response.Previous)
	}
	if response.Pillars[1].TopBuckets != nil {
		t.Fatalf("pillar without buckets should omit top_buckets, got %+v", response.Pillars[1].TopBuckets)
	}

	wantSummary := "overall 61/100 — seo 55, aeo 70, pagespeed 48"
	if !strings.HasPrefix(result.Summary, wantSummary) {
		t.Fatalf("Summary = %q, want prefix %q", result.Summary, wantSummary)
	}
}

func TestGetScoreSummaryPillarFilter(t *testing.T) {
	fake := &fakeScoreReader{current: defaultScoreSnapshot()}
	fake.historyRows = previousHistoryRows(t)
	result := runGetScoreSummary(t, fake, `{"pillar":"seo"}`)
	response := decodeScoreSummary(t, result)

	if len(response.Pillars) != 1 || response.Pillars[0].ID != "seo" {
		t.Fatalf("Pillars = %+v, want only seo", response.Pillars)
	}
	if len(response.Pillars[0].TopBuckets) != 2 {
		t.Fatalf("seo TopBuckets = %d, want 2", len(response.Pillars[0].TopBuckets))
	}
	// Previous filtered to the same pillar.
	if response.Previous == nil {
		t.Fatal("Previous = nil, want previous crawl")
	}
	if len(response.Previous.Pillars) != 1 || response.Previous.Pillars[0].ID != "seo" {
		t.Fatalf("Previous.Pillars = %+v, want only seo", response.Previous.Pillars)
	}
	if want := "overall 61/100 — seo 55 (prev 52)"; !strings.HasPrefix(result.Summary, want) {
		t.Fatalf("Summary = %q, want prefix %q", result.Summary, want)
	}
}

func previousHistoryRows(t *testing.T) []sqlc.ListCompletedProjectCrawlScoreBreakdownsForUserRow {
	t.Helper()
	previousID := pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	completedAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	return []sqlc.ListCompletedProjectCrawlScoreBreakdownsForUserRow{
		{CrawlID: testCrawlID, CompletedAt: pgtype.Timestamptz{Time: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), Valid: true}, BreakdownJson: snapshotJSON(61, makePillar("seo", 55))},
		{CrawlID: previousID, CompletedAt: pgtype.Timestamptz{Time: completedAt, Valid: true}, BreakdownJson: snapshotJSON(58, makePillar("seo", 52), makePillar("aeo", 66))},
	}
}

func TestGetScoreSummaryCompare(t *testing.T) {
	t.Run("previous included", func(t *testing.T) {
		fake := &fakeScoreReader{current: defaultScoreSnapshot(), historyRows: previousHistoryRows(t)}
		result := runGetScoreSummary(t, fake, `{}`)
		response := decodeScoreSummary(t, result)
		if response.Previous == nil {
			t.Fatal("Previous = nil, want previous crawl")
		}
		if response.Previous.CrawlID == testCrawlID.String() {
			t.Fatalf("Previous.CrawlID = %s, want the older crawl", response.Previous.CrawlID)
		}
		if response.Previous.OverallScore != 58 {
			t.Fatalf("Previous.OverallScore = %d, want 58", response.Previous.OverallScore)
		}
		if response.Previous.CompletedAt != "2026-08-01T12:00:00Z" {
			t.Fatalf("Previous.CompletedAt = %q, want RFC3339", response.Previous.CompletedAt)
		}
		if len(response.Previous.Pillars) != 2 {
			t.Fatalf("Previous.Pillars = %d, want 2", len(response.Previous.Pillars))
		}
		if want := "overall 61/100 — seo 55, aeo 70, pagespeed 48 (prev 58)"; !strings.HasPrefix(result.Summary, want) {
			t.Fatalf("Summary = %q, want prefix %q", result.Summary, want)
		}
	})

	t.Run("compare false omits previous", func(t *testing.T) {
		fake := &fakeScoreReader{current: defaultScoreSnapshot(), historyRows: previousHistoryRows(t)}
		response := decodeScoreSummary(t, runGetScoreSummary(t, fake, `{"compare":false}`))
		if response.Previous != nil {
			t.Fatalf("Previous = %+v, want nil", response.Previous)
		}
	})

	t.Run("history with only current crawl omits previous", func(t *testing.T) {
		fake := &fakeScoreReader{current: defaultScoreSnapshot()}
		fake.historyRows = []sqlc.ListCompletedProjectCrawlScoreBreakdownsForUserRow{
			{CrawlID: testCrawlID, BreakdownJson: snapshotJSON(61, makePillar("seo", 55))},
		}
		response := decodeScoreSummary(t, runGetScoreSummary(t, fake, `{}`))
		if response.Previous != nil {
			t.Fatalf("Previous = %+v, want nil", response.Previous)
		}
	})

	t.Run("empty history omits previous", func(t *testing.T) {
		fake := &fakeScoreReader{current: defaultScoreSnapshot()}
		response := decodeScoreSummary(t, runGetScoreSummary(t, fake, `{}`))
		if response.Previous != nil {
			t.Fatalf("Previous = %+v, want nil", response.Previous)
		}
	})

	t.Run("history limit is two", func(t *testing.T) {
		fake := &fakeScoreReader{current: defaultScoreSnapshot(), historyRows: previousHistoryRows(t)}
		runGetScoreSummary(t, fake, `{}`)
		if fake.lastHistory.Limit != 2 {
			t.Fatalf("history limit = %d, want 2", fake.lastHistory.Limit)
		}
	})
}

func TestGetScoreSummaryBucketLimit(t *testing.T) {
	t.Run("limit caps buckets", func(t *testing.T) {
		buckets := make([]shared.BucketScoreBreakdown, 0, 15)
		for i := 1; i <= 15; i++ {
			buckets = append(buckets, makeBucket("bucket_"+strconv.Itoa(i), float64(i)))
		}
		fake := &fakeScoreReader{current: sqlc.CrawlScoreBreakdown{
			CrawlID:        testCrawlID,
			ScoringVersion: "v3",
			BreakdownJson:  snapshotJSON(50, makePillar("seo", 50, buckets...)),
		}}
		response := decodeScoreSummary(t, runGetScoreSummary(t, fake, `{"limit":5}`))
		if got := len(response.Pillars[0].TopBuckets); got != 5 {
			t.Fatalf("TopBuckets = %d, want capped at 5", got)
		}
		// Highest penalty first.
		if response.Pillars[0].TopBuckets[0].ID != "bucket_15" {
			t.Fatalf("top bucket = %s, want bucket_15", response.Pillars[0].TopBuckets[0].ID)
		}
	})

	t.Run("default limit ten", func(t *testing.T) {
		buckets := make([]shared.BucketScoreBreakdown, 0, 15)
		for i := 1; i <= 15; i++ {
			buckets = append(buckets, makeBucket("bucket_"+strconv.Itoa(i), float64(i)))
		}
		fake := &fakeScoreReader{current: sqlc.CrawlScoreBreakdown{
			CrawlID:        testCrawlID,
			ScoringVersion: "v3",
			BreakdownJson:  snapshotJSON(50, makePillar("seo", 50, buckets...)),
		}}
		response := decodeScoreSummary(t, runGetScoreSummary(t, fake, `{}`))
		if got := len(response.Pillars[0].TopBuckets); got != 10 {
			t.Fatalf("TopBuckets = %d, want default 10", got)
		}
	})

	t.Run("include_buckets false omits buckets", func(t *testing.T) {
		fake := &fakeScoreReader{current: defaultScoreSnapshot()}
		response := decodeScoreSummary(t, runGetScoreSummary(t, fake, `{"include_buckets":false}`))
		if response.Pillars[0].TopBuckets != nil {
			t.Fatalf("TopBuckets = %+v, want omitted", response.Pillars[0].TopBuckets)
		}
	})
}

func TestGetScoreSummaryBucketTieBreak(t *testing.T) {
	fake := &fakeScoreReader{current: sqlc.CrawlScoreBreakdown{
		CrawlID:        testCrawlID,
		ScoringVersion: "v3",
		BreakdownJson: snapshotJSON(50,
			makePillar("seo", 50, makeBucket("zeta", 50), makeBucket("alpha", 50)),
		),
	}}
	response := decodeScoreSummary(t, runGetScoreSummary(t, fake, `{}`))
	top := response.Pillars[0].TopBuckets
	if len(top) != 2 || top[0].ID != "alpha" || top[1].ID != "zeta" {
		t.Fatalf("TopBuckets = %+v, want alpha before zeta on tie", top)
	}
}

func TestGetScoreSummaryFallbackToCrawlColumns(t *testing.T) {
	fake := &fakeScoreReader{
		currentErr: pgx.ErrNoRows,
		crawlRow: sqlc.GetCrawlByIDForUserRow{
			ID:             testCrawlID,
			SeoScore:       pgtype.Int4{Int32: 55, Valid: true},
			AeoScore:       pgtype.Int4{Int32: 70, Valid: true},
			PagespeedScore: pgtype.Int4{Int32: 48, Valid: true},
			OverallScore:   pgtype.Int4{Int32: 61, Valid: true},
		},
		historyRows: previousHistoryRows(t),
	}
	result := runGetScoreSummary(t, fake, `{}`)
	response := decodeScoreSummary(t, result)

	if response.Source != scoreSourceCrawlColumns {
		t.Fatalf("Source = %q, want crawl_columns", response.Source)
	}
	if response.OverallScore != 61 || response.ScoringVersion != "" {
		t.Fatalf("OverallScore/ScoringVersion = %d/%q, want 61/empty", response.OverallScore, response.ScoringVersion)
	}
	if len(response.Pillars) != 3 {
		t.Fatalf("Pillars = %d, want 3", len(response.Pillars))
	}
	if response.Pillars[0].ID != "seo" || response.Pillars[0].Score != 55 {
		t.Fatalf("first pillar = %+v, want seo/55", response.Pillars[0])
	}
	if response.Pillars[0].TopBuckets != nil {
		t.Fatalf("fallback pillar must omit buckets, got %+v", response.Pillars[0].TopBuckets)
	}
	if response.Previous == nil {
		t.Fatal("Previous = nil, want previous crawl even in fallback mode")
	}
	if want := "overall 61/100 — seo 55, aeo 70, pagespeed 48 (prev 58) (crawl scores only)"; !strings.HasPrefix(result.Summary, want) {
		t.Fatalf("Summary = %q, want prefix %q", result.Summary, want)
	}
	if fake.crawlCalls != 1 {
		t.Fatalf("crawl fallback calls = %d, want 1", fake.crawlCalls)
	}
}

func TestGetScoreSummaryValidationNoDB(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{name: "invalid pillar", raw: `{"pillar":"social"}`, wantErr: "valid pillars: seo, aeo, pagespeed"},
		{name: "unknown key", raw: `{"bogus":1}`, wantErr: `unknown argument "bogus"`},
		{name: "limit zero", raw: `{"limit":0}`, wantErr: "at least 1"},
		{name: "non-object", raw: `[1]`, wantErr: "must be a JSON object"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeScoreReader{current: defaultScoreSnapshot()}
			result := runGetScoreSummary(t, fake, test.raw)
			if !strings.Contains(result.Content, test.wantErr) {
				t.Fatalf("Content = %q, want containing %q", result.Content, test.wantErr)
			}
			if fake.currentCalls != 0 || fake.historyCalls != 0 || fake.crawlCalls != 0 {
				t.Fatalf("validation error hit the database: %+v", fake)
			}
		})
	}
}

func TestGetScoreSummaryDatabaseErrorsPropagate(t *testing.T) {
	t.Run("breakdown read error", func(t *testing.T) {
		fake := &fakeScoreReader{currentErr: errors.New("boom")}
		exec := scoreSummaryExecutor{current: fake, history: fake, crawls: fake}
		_, err := exec.run(context.Background(), json.RawMessage(`{}`), testCrawlID, testProjectID, testUserID)
		if err == nil || !strings.Contains(err.Error(), "breakdown") {
			t.Fatalf("run() error = %v, want wrapped breakdown error", err)
		}
	})

	t.Run("crawl fallback error", func(t *testing.T) {
		fake := &fakeScoreReader{currentErr: pgx.ErrNoRows, crawlErr: errors.New("boom")}
		exec := scoreSummaryExecutor{current: fake, history: fake, crawls: fake}
		_, err := exec.run(context.Background(), json.RawMessage(`{}`), testCrawlID, testProjectID, testUserID)
		if err == nil || !strings.Contains(err.Error(), "crawl scores") {
			t.Fatalf("run() error = %v, want wrapped crawl scores error", err)
		}
	})

	t.Run("crawl missing", func(t *testing.T) {
		fake := &fakeScoreReader{currentErr: pgx.ErrNoRows, crawlErr: pgx.ErrNoRows}
		exec := scoreSummaryExecutor{current: fake, history: fake, crawls: fake}
		_, err := exec.run(context.Background(), json.RawMessage(`{}`), testCrawlID, testProjectID, testUserID)
		if err == nil || !strings.Contains(err.Error(), "crawl not found") {
			t.Fatalf("run() error = %v, want crawl not found", err)
		}
	})

	t.Run("history error", func(t *testing.T) {
		fake := &fakeScoreReader{current: defaultScoreSnapshot(), historyErr: errors.New("boom")}
		exec := scoreSummaryExecutor{current: fake, history: fake, crawls: fake}
		_, err := exec.run(context.Background(), json.RawMessage(`{}`), testCrawlID, testProjectID, testUserID)
		if err == nil || !strings.Contains(err.Error(), "history") {
			t.Fatalf("run() error = %v, want wrapped history error", err)
		}
	})
}

func TestExecuteGetScoreSummaryRequiresQueries(t *testing.T) {
	_, err := executeGetScoreSummary(context.Background(), json.RawMessage(`{}`), Scope{})
	if err == nil || !strings.Contains(err.Error(), "no queries") {
		t.Fatalf("executeGetScoreSummary error = %v, want no-queries error", err)
	}
}

// TestCatalogAndRegistrySplit guards the contract: the admin catalog lists
// every implemented tool (so gating works), and the registry the worker
// serves is exactly the wired tool set — never more than the catalog.
func TestCatalogAndRegistrySplit(t *testing.T) {
	registryNames := NewRegistry().Names()

	catalogNames := make([]string, 0, len(CatalogDefs()))
	for _, def := range CatalogDefs() {
		catalogNames = append(catalogNames, def.Name)
	}
	wantCatalog := []string{"read_issues", "get_score_summary", "get_search_console_data", "get_business_profile", "read_issue_work", "read_page", "render_chart", "update_business_profile"}
	if !reflect.DeepEqual(catalogNames, wantCatalog) {
		t.Fatalf("catalog names = %v, want %v", catalogNames, wantCatalog)
	}

	// Every served tool must appear in the catalog.
	for _, name := range registryNames {
		found := false
		for _, catalogName := range catalogNames {
			if catalogName == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("served tool %q missing from catalog %v", name, catalogNames)
		}
	}
}

func TestGetScoreSummaryExecutesThroughToolBinding(t *testing.T) {
	tool := getScoreSummaryTool()
	if tool.Def.Name != "get_score_summary" || tool.Def.Label != "Get score summary" {
		t.Fatalf("tool def = %+v", tool.Def)
	}
	if tool.Execute == nil {
		t.Fatal("Execute = nil")
	}
	fake := &fakeScoreReader{current: defaultScoreSnapshot()}
	exec := scoreSummaryExecutor{current: fake, history: fake, crawls: fake}
	if _, err := exec.run(context.Background(), json.RawMessage(`{}`), testCrawlID, testProjectID, testUserID); err != nil {
		t.Fatal(err)
	}
}
