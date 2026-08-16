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
	overview     gsc.OverviewPayload
	overviewErr  error
	pages        map[string]gsc.QueryPage
	queryErr     error
	refreshToken gsc.TokenResponse
	refreshErr   error
	decryptToken string
	decryptErr   error
	fetchCalls   int
	lastOptions  gsc.QueryPageOptions
	lastSiteURL  string
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
	return f.overview, f.overviewErr
}

// fakeGSCConnections implements the connection readers.
type fakeGSCConnections struct {
	feature           sqlc.GetOrganizationFeaturesByProjectIDRow
	featureErr        error
	project           sqlc.ProjectGscConnection
	projectErr        error
	connection        sqlc.GoogleConnection
	connectionErr     error
	updateTokensCalls int
	updateStatusCalls int
}

func (f *fakeGSCConnections) GetOrganizationFeaturesByProjectID(_ context.Context, _ sqlc.GetOrganizationFeaturesByProjectIDParams) (sqlc.GetOrganizationFeaturesByProjectIDRow, error) {
	return f.feature, f.featureErr
}

func (f *fakeGSCConnections) GetProjectGSCConnectionByProjectID(_ context.Context, _ pgtype.UUID) (sqlc.ProjectGscConnection, error) {
	return f.project, f.projectErr
}

func (f *fakeGSCConnections) GetGoogleConnectionByOrganizationID(_ context.Context, _ pgtype.UUID) (sqlc.GoogleConnection, error) {
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
		feature: sqlc.GetOrganizationFeaturesByProjectIDRow{GscConnector: true},
		project: sqlc.ProjectGscConnection{SiteUrl: "sc-domain:x.test", GoogleConnectionID: testProjectID},
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
	exec := gscExecutor{features: connections, connections: connections, fetcher: fetcher}
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
		{name: "summary only", raw: `{"report":"summary"}`, want: gscArgs{Report: "summary", Days: 180, Limit: 25}},
		{name: "full args", raw: `{"report":"top_queries","days":90,"search":"seo","limit":50,"offset":100}`,
			want: gscArgs{Report: "top_queries", Days: 90, Search: "seo", Limit: 50, Offset: 100}},
		{name: "report case-insensitive", raw: `{"report":"TOP_PAGES"}`, want: gscArgs{Report: "top_pages", Days: 180, Limit: 25}},
		{name: "days clamped to max", raw: `{"report":"top_queries","days":9999}`, want: gscArgs{Report: "top_queries", Days: 480, Limit: 25}},
		{name: "days below minimum rejected", raw: `{"report":"top_queries","days":3}`, wantErr: "at least 7"},
		{name: "limit clamped to max", raw: `{"report":"top_queries","limit":500}`, want: gscArgs{Report: "top_queries", Days: 180, Limit: 100}},
		{name: "limit zero rejected", raw: `{"report":"top_queries","limit":0}`, wantErr: "at least 1"},
		{name: "offset clamped to max", raw: `{"report":"top_queries","offset":999999}`, want: gscArgs{Report: "top_queries", Days: 180, Limit: 25, Offset: 25000}},
		{name: "offset negative rejected", raw: `{"report":"top_queries","offset":-1}`, wantErr: ">= 0"},
		{name: "missing report rejected", raw: `{}`, wantErr: "missing required argument"},
		{name: "invalid report rejected", raw: `{"report":"bogus"}`, wantErr: "invalid report"},
		{name: "unknown key rejected", raw: `{"report":"summary","bogus":1}`, wantErr: `unknown argument "bogus"`},
		{name: "duplicate key rejected", raw: `{"report":"summary","report":"top_queries"}`, wantErr: `duplicate argument "report"`},
		{name: "trailing data rejected", raw: `{"report":"summary"} {"x":1}`, wantErr: "trailing data"},
		{name: "non-object rejected", raw: `[1]`, wantErr: "must be a JSON object"},
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
			result := runGSC(t, test.connections(), test.fetcher(), `{"report":"summary"}`)
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
		{raw: `{"report":"top_queries"}`, wantReport: "top_queries", wantKey: "seo audit guide", wantDim: "query", wantSearch: ""},
		{raw: `{"report":"top_queries","search":"seo"}`, wantReport: "top_queries", wantKey: "seo tutorial", wantDim: "query", wantSearch: "seo"},
		{raw: `{"report":"question_queries"}`, wantReport: "question_queries", wantKey: "who is x", wantDim: "query", wantSearch: ""},
		{raw: `{"report":"top_pages"}`, wantReport: "top_pages", wantKey: "/blog/seo-guide", wantDim: "page", wantSearch: ""},
		{raw: `{"report":"countries"}`, wantReport: "countries", wantKey: "US", wantDim: "country", wantSearch: ""},
		{raw: `{"report":"devices"}`, wantReport: "devices", wantKey: "MOBILE", wantDim: "device", wantSearch: ""},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			result := runGSC(t, connections, fetcher, test.raw)
			var response gscReportResponse
			if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
				t.Fatalf("content not response JSON: %v\n%s", err, result.Content)
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
	result := runGSC(t, connections, fetcher, `{"report":"top_queries","offset":0,"limit":2}`)
	var response gscReportResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
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
	result := runGSC(t, connections, fetcher, `{"report":"summary"}`)
	var response gscSummaryResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("content not summary JSON: %v\n%s", err, result.Content)
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
	result := runGSC(t, connections, fetcher, `{"report":"opportunities"}`)
	var response gscOpportunitiesResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("content not opportunities JSON: %v\n%s", err, result.Content)
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

func TestGSCTokenRefreshPath(t *testing.T) {
	t.Run("refresh on expiry persists", func(t *testing.T) {
		fetcher := &fakeGSCFetcher{
			decryptToken: "",
			refreshToken: gsc.TokenResponse{AccessToken: "fresh", RefreshToken: "", ExpiresIn: 3600},
			pages:        map[string]gsc.QueryPage{"query|false|": fakeQueryPage("a")},
		}
		connections := connectedFake(fetcher)
		result := runGSC(t, connections, fetcher, `{"report":"top_queries"}`)
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
		result := runGSC(t, connections, fetcher, `{"report":"top_queries"}`)
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
	result := runGSC(t, connectedFake(fetcher), fetcher, `{"report":"top_queries"}`)
	var response gscReportResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
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
		_, err := exec.run(context.Background(), json.RawMessage(`{"report":"summary"}`), testProjectID, testUserID)
		if err == nil || !strings.Contains(err.Error(), "features") {
			t.Fatalf("run() error = %v, want wrapped features error", err)
		}
	})

	t.Run("unavailable fetcher", func(t *testing.T) {
		connections := connectedFake(&fakeGSCFetcher{})
		exec := gscExecutor{features: connections, connections: connections, fetcher: nil}
		result, err := exec.run(context.Background(), json.RawMessage(`{"report":"summary"}`), testProjectID, testUserID)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Content, "currently unavailable") {
			t.Fatalf("Content = %q, want unavailable", result.Content)
		}
	})
}

func TestExecuteGetSearchConsoleDataRequiresQueries(t *testing.T) {
	_, err := executeGetSearchConsoleData(context.Background(), json.RawMessage(`{"report":"summary"}`), Scope{})
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
		case "report", "days", "search", "limit", "offset":
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
