package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/gsc"
)

// fakeGSCFetcher implements GSCFetcher without Google.
type fakeGSCFetcher struct {
	overview      gsc.OverviewPayload
	overviewErr   error
	pages         map[string]gsc.QueryPage
	queryErr      error
	refreshToken  gsc.TokenResponse
	refreshErr    error
	decryptToken  string
	decryptErr    error
	fetchCalls    int
	overviewCalls int
	lastOptions   gsc.QueryPageOptions
	lastSiteURL   string
}

func (f *fakeGSCFetcher) DecryptSecret(string) (string, error) { return f.decryptToken, f.decryptErr }

func (f *fakeGSCFetcher) EncryptSecret(plain string) (string, error) { return "enc:" + plain, nil }

func (f *fakeGSCFetcher) RefreshAccessToken(context.Context, string) (gsc.TokenResponse, error) {
	return f.refreshToken, f.refreshErr
}

func (f *fakeGSCFetcher) FetchQueriesCached(_ context.Context, _, _, siteURL string, options gsc.QueryPageOptions) (gsc.QueryPage, error) {
	f.fetchCalls++
	f.lastOptions = options
	f.lastSiteURL = siteURL
	if f.queryErr != nil {
		return gsc.QueryPage{}, f.queryErr
	}
	dimension := options.Dimension
	if dimension == "" {
		dimension = "query"
	}
	key := dimension + "|" + strconv.FormatBool(options.QuestionsOnly) + "|" + strings.ToLower(options.Search)
	if page, ok := f.pages[key]; ok {
		return page, nil
	}
	return gsc.QueryPage{}, nil
}

func (f *fakeGSCFetcher) FetchOverviewCached(context.Context, string, string, string) (gsc.OverviewPayload, error) {
	f.overviewCalls++
	return f.overview, f.overviewErr
}

// fakeGSCConnections implements the connection readers.
type fakeGSCConnections struct {
	feature           sqlc.GetOrganizationFeaturesByProjectIDRow
	featureErr        error
	project           sqlc.ProjectGscConnection
	projectErr        error
	projectRow        sqlc.Project
	projectRowErr     error
	connection        sqlc.GoogleConnection
	connectionErr     error
	lastOrgID         pgtype.UUID
	updateTokensCalls int
	updateStatusCalls int
}

func (f *fakeGSCConnections) GetOrganizationFeaturesByProjectID(_ context.Context, _ sqlc.GetOrganizationFeaturesByProjectIDParams) (sqlc.GetOrganizationFeaturesByProjectIDRow, error) {
	return f.feature, f.featureErr
}

func (f *fakeGSCConnections) GetProjectByIDForUser(_ context.Context, _ sqlc.GetProjectByIDForUserParams) (sqlc.Project, error) {
	return f.projectRow, f.projectRowErr
}

func (f *fakeGSCConnections) GetProjectGSCConnectionByProjectID(_ context.Context, _ pgtype.UUID) (sqlc.ProjectGscConnection, error) {
	return f.project, f.projectErr
}

func (f *fakeGSCConnections) GetGoogleConnectionByOrganizationID(_ context.Context, organizationID pgtype.UUID) (sqlc.GoogleConnection, error) {
	f.lastOrgID = organizationID
	return f.connection, f.connectionErr
}

func (f *fakeGSCConnections) UpdateGoogleConnectionTokens(_ context.Context, _ sqlc.UpdateGoogleConnectionTokensParams) (sqlc.GoogleConnection, error) {
	f.updateTokensCalls++
	return f.connection, nil
}

func (f *fakeGSCConnections) UpdateGoogleConnectionStatus(_ context.Context, _ sqlc.UpdateGoogleConnectionStatusParams) error {
	f.updateStatusCalls++
	return nil
}

func connectedFake(fetcher *fakeGSCFetcher) *fakeGSCConnections {
	return &fakeGSCConnections{
		feature:    sqlc.GetOrganizationFeaturesByProjectIDRow{GscConnector: true},
		projectRow: sqlc.Project{OrganizationID: testProjectID},
		project:    sqlc.ProjectGscConnection{SiteUrl: "sc-domain:x.test", GoogleConnectionID: pgtype.UUID{Bytes: [16]byte{8}, Valid: true}},
		connection: sqlc.GoogleConnection{
			ID:                    testProjectID,
			Status:                "active",
			EncryptedAccessToken:  pgtype.Text{String: "enc:tok", Valid: true},
			EncryptedRefreshToken: "enc:refresh",
			AccessTokenExpiresAt:  pgtype.Timestamptz{Valid: true},
		},
	}
}

func runGSC(t *testing.T, connections *fakeGSCConnections, fetcher *fakeGSCFetcher, raw string) Result {
	t.Helper()
	exec := gscExecutor{features: connections, projects: connections, connections: connections, fetcher: fetcher}
	result, err := exec.run(context.Background(), json.RawMessage(raw), testProjectID, testUserID)
	if err != nil {
		t.Fatalf("run(%s) returned error: %v", raw, err)
	}
	return result
}

func fakeQueryPage(keys ...string) gsc.QueryPage {
	page := gsc.QueryPage{StartDate: "2026-02-17", EndDate: "2026-08-15", Offset: 0, Days: 180}
	for _, key := range keys {
		page.Rows = append(page.Rows, gsc.SearchAnalyticsRow{Query: key, Clicks: 10, Impressions: 100, CTR: 0.1, Position: 9.5})
	}
	return page
}

func fakeOverview() gsc.OverviewPayload {
	return gsc.OverviewPayload{
		Windows: map[string]gsc.OverviewWindow{
			"180": {
				Range: gsc.OverviewRange{CurrentStart: "2026-02-17", CurrentEnd: "2026-08-15"},
				Summary: gsc.OverviewSummary{
					Clicks:      gsc.MetricSummary{Current: 180, Previous: 150},
					Impressions: gsc.MetricSummary{Current: 9000, Previous: 8000},
					CTR:         gsc.MetricSummary{Current: 0.02, Previous: 0.019},
					Position:    gsc.MetricSummary{Current: 12.3, Previous: 13.1},
				},
				Opportunities: gsc.OverviewOpportunities{
					LowCTRQueries:           []gsc.SearchAnalyticsRow{{Query: "low ctr query", Clicks: 1, Impressions: 500}},
					StrikingDistanceQueries: []gsc.SearchAnalyticsRow{{Query: "close query", Clicks: 5, Impressions: 700}},
					QuestionQueries:         []gsc.SearchAnalyticsRow{{Query: "what is x", Clicks: 3, Impressions: 400}},
				},
			},
		},
	}
}

func TestParseGSCArgs(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    gscArgs
		wantErr string
	}{
		{name: "one report", raw: `{"reports":["summary"]}`, want: gscArgs{Reports: []string{"summary"}, Days: 180, Limit: 25}},
		{name: "full args", raw: `{"reports":["top_queries"],"days":90,"search":"seo","limit":50,"offset":100}`,
			want: gscArgs{Reports: []string{"top_queries"}, Days: 90, Search: "seo", Limit: 50, Offset: 100}},
		{name: "report case-insensitive", raw: `{"reports":["TOP_PAGES"]}`, want: gscArgs{Reports: []string{"top_pages"}, Days: 180, Limit: 25}},
		{name: "reports deduplicated", raw: `{"reports":["summary","summary","top_queries"]}`, want: gscArgs{Reports: []string{"summary", "top_queries"}, Days: 180, Limit: 25}},
		{name: "reports order preserved", raw: `{"reports":["top_pages","summary"]}`, want: gscArgs{Reports: []string{"top_pages", "summary"}, Days: 180, Limit: 25}},
		{name: "days clamped to max", raw: `{"reports":["top_queries"],"days":9999}`, want: gscArgs{Reports: []string{"top_queries"}, Days: 480, Limit: 25}},
		{name: "days below minimum rejected", raw: `{"reports":["top_queries"],"days":3}`, wantErr: "at least 7"},
		{name: "limit clamped to max", raw: `{"reports":["top_queries"],"limit":500}`, want: gscArgs{Reports: []string{"top_queries"}, Days: 180, Limit: 100}},
		{name: "limit zero rejected", raw: `{"reports":["top_queries"],"limit":0}`, wantErr: "at least 1"},
		{name: "offset clamped to max", raw: `{"reports":["top_queries"],"offset":999999}`, want: gscArgs{Reports: []string{"top_queries"}, Days: 180, Limit: 25, Offset: 25000}},
		{name: "offset negative rejected", raw: `{"reports":["top_queries"],"offset":-1}`, wantErr: ">= 0"},
		{name: "missing reports rejected", raw: `{}`, wantErr: "missing required argument"},
		{name: "empty reports rejected", raw: `{"reports":[]}`, wantErr: "must not be empty"},
		{name: "invalid report rejected", raw: `{"reports":["bogus"]}`, wantErr: "invalid report"},
		{name: "unknown key rejected", raw: `{"reports":["summary"],"bogus":1}`, wantErr: `unknown argument "bogus"`},
		{name: "duplicate key rejected", raw: `{"reports":["summary"],"reports":["top_queries"]}`, wantErr: `duplicate argument "reports"`},
		{name: "trailing data rejected", raw: `{"reports":["summary"]} {"x":1}`, wantErr: "trailing data"},
		{name: "non-object rejected", raw: `[1]`, wantErr: "must be a JSON object"},
		{name: "valid exact range", raw: `{"reports":["top_queries"],"start_date":"2025-08-22","end_date":"2025-09-10"}`, want: gscArgs{Reports: []string{"top_queries"}, Days: 180, StartDate: "2025-08-22", EndDate: "2025-09-10", Limit: 25}},
		{name: "exact range overrides days", raw: `{"reports":["top_queries"],"days":90,"start_date":"2025-08-22","end_date":"2025-09-10"}`, want: gscArgs{Reports: []string{"top_queries"}, Days: 90, StartDate: "2025-08-22", EndDate: "2025-09-10", Limit: 25}},
		{name: "missing end_date rejected", raw: `{"reports":["top_queries"],"start_date":"2025-08-22"}`, wantErr: "must be supplied together"},
		{name: "missing start_date rejected", raw: `{"reports":["top_queries"],"end_date":"2025-09-10"}`, wantErr: "must be supplied together"},
		{name: "malformed start_date rejected", raw: `{"reports":["top_queries"],"start_date":"2025/08/22","end_date":"2025-09-10"}`, wantErr: "YYYY-MM-DD"},
		{name: "malformed end_date rejected", raw: `{"reports":["top_queries"],"start_date":"2025-08-22","end_date":"not-a-date"}`, wantErr: "YYYY-MM-DD"},
		{name: "reversed range rejected", raw: `{"reports":["top_queries"],"start_date":"2025-09-10","end_date":"2025-08-22"}`, wantErr: "<="},
		{name: "too short range rejected", raw: `{"reports":["top_queries"],"start_date":"2025-09-10","end_date":"2025-09-12"}`, wantErr: "at least 7"},
		{name: "too long range rejected", raw: `{"reports":["top_queries"],"start_date":"2024-01-01","end_date":"2025-09-10"}`, wantErr: "at most 480"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseGSCArgs(json.RawMessage(test.raw))
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseGSCArgs(%s) error = %v, want containing %q", test.raw, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGSCArgs(%s) error = %v", test.raw, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseGSCArgs(%s) = %+v, want %+v", test.raw, got, test.want)
			}
		})
	}
}

func TestGSCUnavailableStates(t *testing.T) {
	tests := []struct {
		name        string
		connections func() *fakeGSCConnections
		fetcher     func() *fakeGSCFetcher
		wantContent string
		wantNoDB    bool
	}{
		{
			name:        "feature flag off",
			connections: func() *fakeGSCConnections { return &fakeGSCConnections{} },
			fetcher:     func() *fakeGSCFetcher { return &fakeGSCFetcher{} },
			wantContent: "Search Console is not enabled",
		},
		{
			name: "no project property",
			connections: func() *fakeGSCConnections {
				return &fakeGSCConnections{feature: sqlc.GetOrganizationFeaturesByProjectIDRow{GscConnector: true}, projectErr: pgx.ErrNoRows}
			},
			fetcher:     func() *fakeGSCFetcher { return &fakeGSCFetcher{} },
			wantContent: "No Search Console property is connected",
		},
		{
			name: "no google connection",
			connections: func() *fakeGSCConnections {
				return &fakeGSCConnections{
					feature:       sqlc.GetOrganizationFeaturesByProjectIDRow{GscConnector: true},
					project:       sqlc.ProjectGscConnection{SiteUrl: "sc-domain:x.test", GoogleConnectionID: testProjectID},
					connectionErr: pgx.ErrNoRows,
				}
			},
			fetcher:     func() *fakeGSCFetcher { return &fakeGSCFetcher{} },
			wantContent: "Search Console is not connected",
		},
		{
			name: "connection requires reconnect",
			connections: func() *fakeGSCConnections {
				c := connectedFake(&fakeGSCFetcher{})
				c.connection.Status = "reauth_required"
				return c
			},
			fetcher:     func() *fakeGSCFetcher { return &fakeGSCFetcher{} },
			wantContent: "reauthorization",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runGSC(t, test.connections(), test.fetcher(), `{"reports":["summary"]}`)
			if !strings.Contains(result.Content, test.wantContent) {
				t.Fatalf("Content = %q, want containing %q", result.Content, test.wantContent)
			}
			if result.Summary != "search console not available" {
				t.Fatalf("Summary = %q, want search console not available", result.Summary)
			}
		})
	}
}

func TestGSCQueryReportForwarding(t *testing.T) {
	fetcher := &fakeGSCFetcher{pages: map[string]gsc.QueryPage{
		"query|false|":    fakeQueryPage("seo audit guide"),
		"query|false|seo": fakeQueryPage("seo tutorial"),
		"query|true|":     fakeQueryPage("who is x"),
		"page|false|":     fakeQueryPage("/blog/seo-guide"),
		"country|false|":  fakeQueryPage("US"),
		"device|false|":   fakeQueryPage("MOBILE"),
	}}
	connections := connectedFake(fetcher)

	tests := []struct {
		raw        string
		wantReport string
		wantKey    string
		wantDim    string
		wantSearch string
	}{
		{raw: `{"reports":["top_queries"]}`, wantReport: "top_queries", wantKey: "seo audit guide", wantDim: "query", wantSearch: ""},
		{raw: `{"reports":["top_queries"],"search":"seo"}`, wantReport: "top_queries", wantKey: "seo tutorial", wantDim: "query", wantSearch: "seo"},
		{raw: `{"reports":["question_queries"]}`, wantReport: "question_queries", wantKey: "who is x", wantDim: "query", wantSearch: ""},
		{raw: `{"reports":["top_pages"]}`, wantReport: "top_pages", wantKey: "/blog/seo-guide", wantDim: "page", wantSearch: ""},
		{raw: `{"reports":["countries"]}`, wantReport: "countries", wantKey: "US", wantDim: "country", wantSearch: ""},
		{raw: `{"reports":["devices"]}`, wantReport: "devices", wantKey: "MOBILE", wantDim: "device", wantSearch: ""},
	}

	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			result := runGSC(t, connections, fetcher, test.raw)
			var response gscReportResponse
			if err := json.Unmarshal(firstGSCSection(t, result), &response); err != nil {
				t.Fatalf("section not response JSON: %v\n%s", err, result.Content)
			}
			if len(response.Rows) != 1 || response.Rows[0].Key != test.wantKey {
				t.Fatalf("Rows = %+v, want key %q", response.Rows, test.wantKey)
			}
			gotDim := fetcher.lastOptions.Dimension
			if gotDim == "" {
				gotDim = "query"
			}
			if gotDim != test.wantDim {
				t.Fatalf("dimension = %q, want %q", gotDim, test.wantDim)
			}
			if fetcher.lastOptions.Search != test.wantSearch {
				t.Fatalf("search = %q, want %q", fetcher.lastOptions.Search, test.wantSearch)
			}
			if response.TotalClicks != 10 || response.HasMore {
				t.Fatalf("TotalClicks/HasMore = %v/%v, want 10/false", response.TotalClicks, response.HasMore)
			}
			if response.StartDate != "2026-02-17" || response.EndDate != "2026-08-15" {
				t.Fatalf("dates = %s..%s", response.StartDate, response.EndDate)
			}
			if want := test.wantReport + ": 1 rows (last 180 days, 10 clicks)"; result.Summary != want {
				t.Fatalf("Summary = %q, want %q", result.Summary, want)
			}
		})
	}
}

func TestGSCQueryPaging(t *testing.T) {
	fetcher := &fakeGSCFetcher{
		pages: map[string]gsc.QueryPage{
			"query|false|": {Rows: []gsc.SearchAnalyticsRow{{Query: "a"}, {Query: "b"}}, HasMore: true, Offset: 0, StartDate: "d", EndDate: "e"},
		},
		queryErr: nil,
	}
	connections := connectedFake(fetcher)
	result := runGSC(t, connections, fetcher, `{"reports":["top_queries"],"offset":0,"limit":2}`)
	var response gscReportResponse
	if err := json.Unmarshal(firstGSCSection(t, result), &response); err != nil {
		t.Fatal(err)
	}
	if !response.HasMore || response.NextOffset != 2 {
		t.Fatalf("HasMore/NextOffset = %v/%d, want true/2", response.HasMore, response.NextOffset)
	}
	if fetcher.lastOptions.Offset != 0 || fetcher.lastOptions.Limit != 2 {
		t.Fatalf("forwarded options = %+v", fetcher.lastOptions)
	}
}

func TestGSCSummaryReport(t *testing.T) {
	fetcher := &fakeGSCFetcher{overview: fakeOverview()}
	connections := connectedFake(fetcher)
	result := runGSC(t, connections, fetcher, `{"reports":["summary"]}`)
	var response gscSummaryResponse
	if err := json.Unmarshal(firstGSCSection(t, result), &response); err != nil {
		t.Fatalf("section not summary JSON: %v\n%s", err, result.Content)
	}
	if response.Clicks.Current != 180 || response.Clicks.Previous != 150 {
		t.Fatalf("clicks = %+v, want 180/150", response.Clicks)
	}
	if response.Position.Current != 12.3 {
		t.Fatalf("position = %v, want 12.3", response.Position.Current)
	}
	if response.StartDate != "2026-02-17" {
		t.Fatalf("StartDate = %q", response.StartDate)
	}
	if !strings.Contains(result.Summary, "clicks vs 150 previous") {
		t.Fatalf("Summary = %q", result.Summary)
	}
}

func TestGSCOpportunitiesReport(t *testing.T) {
	fetcher := &fakeGSCFetcher{overview: fakeOverview()}
	connections := connectedFake(fetcher)
	result := runGSC(t, connections, fetcher, `{"reports":["opportunities"]}`)
	var response gscOpportunitiesResponse
	if err := json.Unmarshal(firstGSCSection(t, result), &response); err != nil {
		t.Fatalf("section not opportunities JSON: %v\n%s", err, result.Content)
	}
	if len(response.LowCTRQueries) != 1 || response.LowCTRQueries[0].Key != "low ctr query" {
		t.Fatalf("LowCTRQueries = %+v", response.LowCTRQueries)
	}
	if len(response.StrikingDistanceQueries) != 1 || response.QuestionQueries[0].Key != "what is x" {
		t.Fatalf("opportunity lists wrong: %+v", response)
	}
	if !strings.Contains(result.Summary, "1 striking-distance queries") {
		t.Fatalf("Summary = %q", result.Summary)
	}
}
func firstGSCSection(t *testing.T, result Result) json.RawMessage {
	t.Helper()
	var multi gscMultiReportResponse
	if err := json.Unmarshal([]byte(result.Content), &multi); err != nil {
		t.Fatalf("content not multi-report JSON: %v\n%s", err, result.Content)
	}
	if len(multi.Reports) != 1 {
		t.Fatalf("Reports = %d sections, want 1\n%s", len(multi.Reports), result.Content)
	}
	return multi.Reports[0]
}

func TestGSCCombinedReports(t *testing.T) {
	fetcher := &fakeGSCFetcher{
		overview: fakeOverview(),
		pages: map[string]gsc.QueryPage{
			"query|false|": fakeQueryPage("seo audit guide"),
			"page|false|":  fakeQueryPage("/blog/seo-guide"),
		},
	}
	connections := connectedFake(fetcher)
	result := runGSC(t, connections, fetcher, `{"reports":["top_queries","summary","opportunities","top_pages"],"limit":50}`)

	var multi gscMultiReportResponse
	if err := json.Unmarshal([]byte(result.Content), &multi); err != nil {
		t.Fatalf("content not multi-report JSON: %v\n%s", err, result.Content)
	}
	if len(multi.Reports) != 4 {
		t.Fatalf("Reports = %d sections, want 4", len(multi.Reports))
	}

	var querySection gscReportResponse
	if err := json.Unmarshal(multi.Reports[0], &querySection); err != nil {
		t.Fatal(err)
	}
	if querySection.Report != "top_queries" || len(querySection.Rows) != 1 || querySection.Rows[0].Key != "seo audit guide" {
		t.Fatalf("section[0] = %+v, want top_queries with seo audit guide", querySection)
	}

	var summarySection gscSummaryResponse
	if err := json.Unmarshal(multi.Reports[1], &summarySection); err != nil {
		t.Fatal(err)
	}
	if summarySection.Report != "summary" || summarySection.Clicks.Current != 180 {
		t.Fatalf("section[1] = %+v, want summary with 180 clicks", summarySection)
	}

	var oppSection gscOpportunitiesResponse
	if err := json.Unmarshal(multi.Reports[2], &oppSection); err != nil {
		t.Fatal(err)
	}
	if oppSection.Report != "opportunities" || len(oppSection.StrikingDistanceQueries) != 1 {
		t.Fatalf("section[2] = %+v, want opportunities", oppSection)
	}

	var pageSection gscReportResponse
	if err := json.Unmarshal(multi.Reports[3], &pageSection); err != nil {
		t.Fatal(err)
	}
	if pageSection.Report != "top_pages" || len(pageSection.Rows) != 1 || pageSection.Rows[0].Key != "/blog/seo-guide" {
		t.Fatalf("section[3] = %+v, want top_pages with /blog/seo-guide", pageSection)
	}

	if fetcher.fetchCalls != 2 {
		t.Fatalf("query fetches = %d, want 2 (top_queries + top_pages)", fetcher.fetchCalls)
	}
	if fetcher.overviewCalls != 1 {
		t.Fatalf("overview fetches = %d, want 1 (shared by summary + opportunities)", fetcher.overviewCalls)
	}
	// The combined limit is shared across reports: 150/4 = 37 rows per section.
	if fetcher.lastOptions.Limit != 37 {
		t.Fatalf("forwarded limit = %d, want 37", fetcher.lastOptions.Limit)
	}
	if want := "4 reports returned (top_queries, summary, opportunities, top_pages)"; result.Summary != want {
		t.Fatalf("Summary = %q, want %q", result.Summary, want)
	}
}

func TestGSCSingleReportKeepsRichSummary(t *testing.T) {
	fetcher := &fakeGSCFetcher{pages: map[string]gsc.QueryPage{"query|false|": fakeQueryPage("seo audit guide")}}
	result := runGSC(t, connectedFake(fetcher), fetcher, `{"reports":["top_queries"]}`)
	if want := "top_queries: 1 rows (last 180 days, 10 clicks)"; result.Summary != want {
		t.Fatalf("Summary = %q, want %q", result.Summary, want)
	}
}

func TestGSCCombinedLimitUnclampedForSingleReport(t *testing.T) {
	fetcher := &fakeGSCFetcher{pages: map[string]gsc.QueryPage{"query|false|": fakeQueryPage("a")}}
	runGSC(t, connectedFake(fetcher), fetcher, `{"reports":["top_queries"],"limit":100}`)
	if fetcher.lastOptions.Limit != 100 {
		t.Fatalf("forwarded limit = %d, want 100 (no clamp for one report)", fetcher.lastOptions.Limit)
	}
}

func TestGSCTokenRefreshPath(t *testing.T) {
	t.Run("refresh on expiry persists", func(t *testing.T) {
		fetcher := &fakeGSCFetcher{
			decryptToken: "",
			refreshToken: gsc.TokenResponse{AccessToken: "fresh", RefreshToken: "", ExpiresIn: 3600},
			pages:        map[string]gsc.QueryPage{"query|false|": fakeQueryPage("a")},
		}
		connections := connectedFake(fetcher)
		result := runGSC(t, connections, fetcher, `{"reports":["top_queries"]}`)
		if connections.updateTokensCalls != 1 {
			t.Fatalf("update tokens calls = %d, want 1", connections.updateTokensCalls)
		}
		if strings.Contains(result.Content, "not available") {
			t.Fatalf("Content = %q, want fetched report", result.Content)
		}
	})

	t.Run("refresh failure marks reauth", func(t *testing.T) {
		fetcher := &fakeGSCFetcher{decryptToken: "", refreshErr: errors.New("invalid_grant")}
		connections := connectedFake(fetcher)
		result := runGSC(t, connections, fetcher, `{"reports":["top_queries"]}`)
		if connections.updateStatusCalls != 1 {
			t.Fatalf("update status calls = %d, want 1", connections.updateStatusCalls)
		}
		if !strings.Contains(result.Content, "reauthorization") {
			t.Fatalf("Content = %q, want reauthorization", result.Content)
		}
	})
}

func TestGSCRowTextCap(t *testing.T) {
	fetcher := &fakeGSCFetcher{pages: map[string]gsc.QueryPage{
		"query|false|": fakeQueryPage(strings.Repeat("x", 200)),
	}}
	result := runGSC(t, connectedFake(fetcher), fetcher, `{"reports":["top_queries"]}`)
	var response gscReportResponse
	if err := json.Unmarshal(firstGSCSection(t, result), &response); err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(response.Rows[0].Key) != gscMaxRowTextRune+1 {
		t.Fatalf("key runes = %d, want %d with ellipsis", utf8.RuneCountInString(response.Rows[0].Key), gscMaxRowTextRune+1)
	}
}

func TestGSCDatabaseErrorsPropagate(t *testing.T) {
	t.Run("features error", func(t *testing.T) {
		connections := &fakeGSCConnections{featureErr: errors.New("boom")}
		exec := gscExecutor{features: connections, connections: connections, fetcher: &fakeGSCFetcher{}}
		_, err := exec.run(context.Background(), json.RawMessage(`{"reports":["summary"]}`), testProjectID, testUserID)
		if err == nil || !strings.Contains(err.Error(), "features") {
			t.Fatalf("run() error = %v, want wrapped features error", err)
		}
	})

	t.Run("unavailable fetcher", func(t *testing.T) {
		connections := connectedFake(&fakeGSCFetcher{})
		exec := gscExecutor{features: connections, connections: connections, fetcher: nil}
		result, err := exec.run(context.Background(), json.RawMessage(`{"reports":["summary"]}`), testProjectID, testUserID)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Content, "currently unavailable") {
			t.Fatalf("Content = %q, want unavailable", result.Content)
		}
	})
}

func TestExecuteGetSearchConsoleDataRequiresQueries(t *testing.T) {
	_, err := executeGetSearchConsoleData(context.Background(), json.RawMessage(`{"reports":["summary"]}`), Scope{})
	if err == nil || !strings.Contains(err.Error(), "no queries") {
		t.Fatalf("executeGetSearchConsoleData error = %v, want no-queries error", err)
	}
}

func TestSearchConsoleToolDef(t *testing.T) {
	tool := getSearchConsoleDataTool()
	if tool.Def.Name != gscToolName || tool.Def.Feature != "gsc_connector" {
		t.Fatalf("tool def = %+v, want gsc_connector feature", tool.Def)
	}
	if tool.Def.Label == "" || tool.Def.Description == "" || len(tool.Def.Schema) == 0 {
		t.Fatalf("tool def incomplete: %+v", tool.Def)
	}
	// The schema must not accept tenant identifiers.
	var schema map[string]any
	if err := json.Unmarshal(tool.Def.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema properties missing")
	}
	for name := range properties {
		switch name {
		case "reports", "days", "start_date", "end_date", "search", "limit", "offset":
		default:
			t.Fatalf("schema property %q must not exist", name)
		}
	}
}

func TestToolFeaturesCoupling(t *testing.T) {
	if feature := ToolFeatures()[gscToolName]; feature != "gsc_connector" {
		t.Fatalf("ToolFeatures()[%s] = %q, want gsc_connector", gscToolName, feature)
	}
	if _, ok := ToolFeatures()["read_issues"]; ok {
		t.Fatal("read_issues must not be feature-coupled")
	}
}

// TestGSCGoogleConnectionLookupUsesOrganizationID is the regression test for
// the bug where the tool passed the project-connection id as the organization
// id, so every connected workspace reported "Search Console is not connected".
func TestGSCGoogleConnectionLookupUsesOrganizationID(t *testing.T) {
	fetcher := &fakeGSCFetcher{pages: map[string]gsc.QueryPage{"query|false|": fakeQueryPage("a")}}
	connections := connectedFake(fetcher)
	orgID := pgtype.UUID{Bytes: [16]byte{42}, Valid: true}
	connections.projectRow.OrganizationID = orgID
	runGSC(t, connections, fetcher, `{"reports":["top_queries"]}`)
	if !connections.lastOrgID.Valid || connections.lastOrgID != orgID {
		t.Fatalf("google connection looked up by %v, want the project's organization id %v", connections.lastOrgID, orgID)
	}
}

func TestParseGSCArgsDefaultsWithExactDates(t *testing.T) {
	args, err := parseGSCArgs(json.RawMessage(`{"reports":["top_queries"]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args.StartDate != "" || args.EndDate != "" {
		t.Fatalf("expected no exact dates, got %+v", args)
	}
	if args.Days != 180 {
		t.Fatalf("Days = %d, want 180", args.Days)
	}
}

func TestGSCExactDateForwarding(t *testing.T) {
	fetcher := &fakeGSCFetcher{pages: map[string]gsc.QueryPage{
		"query|false|": {Rows: []gsc.SearchAnalyticsRow{{Query: "a"}}, StartDate: "2025-08-22", EndDate: "2025-09-10", Days: 20},
	}}
	connections := connectedFake(fetcher)
	result := runGSC(t, connections, fetcher, `{"reports":["top_queries"],"start_date":"2025-08-22","end_date":"2025-09-10"}`)
	if fetcher.lastOptions.StartDate != "2025-08-22" || fetcher.lastOptions.EndDate != "2025-09-10" {
		t.Fatalf("forwarded dates = %q..%q, want 2025-08-22..2025-09-10", fetcher.lastOptions.StartDate, fetcher.lastOptions.EndDate)
	}
	var resp gscReportResponse
	if err := json.Unmarshal(firstGSCSection(t, result), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.StartDate != "2025-08-22" || resp.EndDate != "2025-09-10" {
		t.Fatalf("response dates = %s..%s, want 2025-08-22..2025-09-10", resp.StartDate, resp.EndDate)
	}
	if !strings.Contains(result.Summary, "20 days") {
		t.Fatalf("Summary = %q, want containing 20 days", result.Summary)
	}
}

func TestGSCExactDateErrorsAreToolErrors(t *testing.T) {
	fetcher := &fakeGSCFetcher{}
	connections := connectedFake(fetcher)
	tests := []struct {
		raw     string
		wantErr string
	}{
		{raw: `{"reports":["top_queries"],"start_date":"2025-08-22"}`, wantErr: "must be supplied together"},
		{raw: `{"reports":["top_queries"],"start_date":"bad","end_date":"2025-09-10"}`, wantErr: "YYYY-MM-DD"},
		{raw: `{"reports":["top_queries"],"start_date":"2025-09-10","end_date":"2025-08-22"}`, wantErr: "<="},
	}
	for _, tc := range tests {
		result := runGSC(t, connections, fetcher, tc.raw)
		if !strings.Contains(result.Content, "get_search_console_data error") || !strings.Contains(result.Content, tc.wantErr) {
			t.Fatalf("run(%s) Content = %q, want error containing %q", tc.raw, result.Content, tc.wantErr)
		}
		if fetcher.fetchCalls != 0 {
			t.Fatalf("fetch should not be called on parse error")
		}
	}
}
