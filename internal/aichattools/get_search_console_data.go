package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/gsc"
)

const (
	gscDefaultLimit   = 25
	gscMaxLimit       = 100
	gscMaxSearchLen   = 100
	gscMaxOffset      = 25000
	gscMinDays        = 7
	gscMaxDays        = 480
	gscDefaultDays    = 180
	gscMaxRowTextRune = 120

	// gscToolName is the registered tool name; gating couples it to the
	// gsc_connector feature flag.
	gscToolName = "get_search_console_data"
)

var validGSCReports = []string{"summary", "top_queries", "question_queries", "top_pages", "countries", "devices", "opportunities"}

const getSearchConsoleDataSchema = `{
  "type": "object",
  "properties": {
    "report": {"type": "string", "enum": ["summary", "top_queries", "question_queries", "top_pages", "countries", "devices", "opportunities"], "description": "summary: headline clicks/impressions/CTR/position vs the previous period. top_queries: highest-traffic search queries. question_queries: queries phrased as questions or comparisons. top_pages: highest-traffic landing pages. countries: traffic split by country. devices: traffic split by desktop/mobile/tablet. opportunities: low-CTR and striking-distance queries plus question queries worth optimizing."},
    "days": {"type": "integer", "minimum": 7, "maximum": 480, "description": "Reporting window in days (default 180). Applies to top_queries, question_queries, top_pages, countries, and devices. Search Console data lags roughly 3 days."},
    "search": {"type": "string", "description": "Case-insensitive substring filter on the query text. Only applies to top_queries and question_queries."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 100, "description": "Max rows to return (default 25, max 100)."},
    "offset": {"type": "integer", "minimum": 0, "maximum": 25000, "description": "Page position for top_queries, question_queries, and top_pages (default 0)."}
  },
  "required": ["report"],
  "additionalProperties": false
}`

// GSCFetcher is the search console data path the worker provides. It is a
// subset of *gsc.Service so tests can substitute fakes. The worker leaves
// Scope.GSC nil when no search console access is configured; tools report that
// as an ordinary unavailable state, not an error.
type GSCFetcher interface {
	DecryptSecret(encrypted string) (string, error)
	EncryptSecret(plain string) (string, error)
	RefreshAccessToken(ctx context.Context, refreshToken string) (gsc.TokenResponse, error)
	FetchQueriesCached(ctx context.Context, accessToken, organizationID, siteURL string, options gsc.QueryPageOptions) (gsc.QueryPage, error)
	FetchOverviewCached(ctx context.Context, accessToken, organizationID, siteURL string) (gsc.OverviewPayload, error)
}

// gscFeatureReader reads the organization feature flags for a project.
type gscFeatureReader interface {
	GetOrganizationFeaturesByProjectID(ctx context.Context, arg sqlc.GetOrganizationFeaturesByProjectIDParams) (sqlc.GetOrganizationFeaturesByProjectIDRow, error)
}

// gscProjectReader reads the project row that carries the organization id.
type gscProjectReader interface {
	GetProjectByIDForUser(ctx context.Context, arg sqlc.GetProjectByIDForUserParams) (sqlc.Project, error)
}

// gscConnectionReader reads and refreshes the Google connection rows.
type gscConnectionReader interface {
	GetProjectGSCConnectionByProjectID(ctx context.Context, projectID pgtype.UUID) (sqlc.ProjectGscConnection, error)
	GetGoogleConnectionByOrganizationID(ctx context.Context, organizationID pgtype.UUID) (sqlc.GoogleConnection, error)
	UpdateGoogleConnectionTokens(ctx context.Context, arg sqlc.UpdateGoogleConnectionTokensParams) (sqlc.GoogleConnection, error)
	UpdateGoogleConnectionStatus(ctx context.Context, arg sqlc.UpdateGoogleConnectionStatusParams) error
}

// gscExecutor runs one get_search_console_data call against narrow reader
// interfaces so tests can substitute fakes without a database or Google.
type gscExecutor struct {
	features    gscFeatureReader
	projects    gscProjectReader
	connections gscConnectionReader
	fetcher     GSCFetcher
}

func getSearchConsoleDataTool() Tool {
	return Tool{
		Def: Def{
			Name:        gscToolName,
			Label:       "Get search console data",
			Description: "Read Google Search Console performance for the current project: headline clicks/impressions/CTR/position, the real queries people find the site through, question-style queries, top landing pages, country and device breakdowns, and ranking opportunities. This is actual search demand, not crawl data — use it for keyword research, query intent, audience geography, mobile-vs-desktop questions, and traffic questions. Returns a plain explanation when Search Console is not connected.",
			Schema:      json.RawMessage(getSearchConsoleDataSchema),
			Feature:     "gsc_connector",
		},
		Execute: executeGetSearchConsoleData,
	}
}

func executeGetSearchConsoleData(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.Queries == nil {
		return Result{}, errors.New("get_search_console_data: scope has no queries")
	}
	exec := gscExecutor{
		features:    s.Queries,
		projects:    s.Queries,
		connections: s.Queries,
		fetcher:     s.GSC,
	}
	return exec.run(ctx, args, s.ProjectID, s.UserID)
}

// gscArgs is the raw, unvalidated argument set.
type gscArgs struct {
	Report string
	Days   int
	Search string
	Limit  int
	Offset int
}

// gscReportResponse is the JSON the model sees for query-style reports.
type gscReportResponse struct {
	Report           string   `json:"report"`
	StartDate        string   `json:"start_date"`
	EndDate          string   `json:"end_date"`
	Rows             []gscRow `json:"rows,omitempty"`
	TotalClicks      float64  `json:"total_clicks"`
	TotalImpressions float64  `json:"total_impressions"`
	NextOffset       int      `json:"next_offset,omitempty"`
	HasMore          bool     `json:"has_more,omitempty"`
}

type gscRow struct {
	Key         string  `json:"key"`
	Clicks      float64 `json:"clicks"`
	Impressions float64 `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

// gscSummaryResponse is the JSON the model sees for the summary report.
type gscSummaryResponse struct {
	Report      string           `json:"report"`
	StartDate   string           `json:"start_date"`
	EndDate     string           `json:"end_date"`
	Clicks      gscMetricSummary `json:"clicks"`
	Impressions gscMetricSummary `json:"impressions"`
	CTR         gscMetricSummary `json:"ctr"`
	Position    gscMetricSummary `json:"position"`
}

type gscMetricSummary struct {
	Current  float64 `json:"current"`
	Previous float64 `json:"previous"`
}

// gscOpportunitiesResponse is the JSON the model sees for the opportunities report.
type gscOpportunitiesResponse struct {
	Report                  string   `json:"report"`
	LowCTRQueries           []gscRow `json:"low_ctr_queries"`
	StrikingDistanceQueries []gscRow `json:"striking_distance_queries"`
	QuestionQueries         []gscRow `json:"question_queries"`
}

// run executes one get_search_console_data call. Search Console rows are live
// data capped by limit, never crawl rows, so the call does not spend the turn
// row budget. Unavailable states (feature off, no connection, no property,
// reauth needed) come back as explanatory content the model can answer with.
func (e *gscExecutor) run(ctx context.Context, raw json.RawMessage, projectID, userID pgtype.UUID) (Result, error) {
	args, err := parseGSCArgs(raw)
	if err != nil {
		return Result{Content: gscToolName + " error: " + err.Error()}, nil
	}

	features, err := e.features.GetOrganizationFeaturesByProjectID(ctx, sqlc.GetOrganizationFeaturesByProjectIDParams{UserID: userID, ProjectID: projectID})
	if err != nil {
		return Result{}, fmt.Errorf("%s: features: %w", gscToolName, err)
	}
	if !features.GscConnector {
		return unavailableGSC("Search Console is not enabled for this workspace. An admin can enable the Search Console connector in workspace features."), nil
	}
	if e.fetcher == nil {
		return unavailableGSC("Search Console access is currently unavailable."), nil
	}

	projectConnection, err := e.connections.GetProjectGSCConnectionByProjectID(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return unavailableGSC("No Search Console property is connected to this project. Connect one in the project's Search Console settings."), nil
		}
		return Result{}, fmt.Errorf("%s: project connection: %w", gscToolName, err)
	}

	project, err := e.projects.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return unavailableGSC("This project is not accessible."), nil
		}
		return Result{}, fmt.Errorf("%s: project: %w", gscToolName, err)
	}

	orgConnection, err := e.connections.GetGoogleConnectionByOrganizationID(ctx, project.OrganizationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return unavailableGSC("Search Console is not connected. Connect a Google account in the workspace settings."), nil
		}
		return Result{}, fmt.Errorf("%s: google connection: %w", gscToolName, err)
	}

	accessToken, err := e.freshAccessToken(ctx, orgConnection)
	if err != nil {
		var unavailable *gscUnavailableError
		if errors.As(err, &unavailable) {
			return unavailableGSC(unavailable.reason), nil
		}
		return Result{}, err
	}

	switch args.Report {
	case "summary":
		return e.summary(ctx, projectID, projectConnection, accessToken)
	case "opportunities":
		return e.opportunities(ctx, projectID, projectConnection, accessToken)
	case "top_queries":
		return e.page(ctx, projectID, projectConnection, accessToken, args, gsc.QueryPageOptions{QuestionsOnly: false, Search: args.Search})
	case "question_queries":
		return e.page(ctx, projectID, projectConnection, accessToken, args, gsc.QueryPageOptions{QuestionsOnly: true, Search: args.Search})
	case "top_pages":
		return e.page(ctx, projectID, projectConnection, accessToken, args, gsc.QueryPageOptions{Dimension: "page"})
	case "countries":
		return e.page(ctx, projectID, projectConnection, accessToken, args, gsc.QueryPageOptions{Dimension: "country"})
	case "devices":
		return e.page(ctx, projectID, projectConnection, accessToken, args, gsc.QueryPageOptions{Dimension: "device"})
	default:
		return Result{Content: fmt.Sprintf("%s error: invalid report %q; valid reports: %s", gscToolName, args.Report, strings.Join(validGSCReports, ", "))}, nil
	}
}

// gscUnavailableError marks an ordinary, expected state (reconnect needed,
// reauthorization required) that should surface as explanatory content rather
// than a tool failure.
type gscUnavailableError struct{ reason string }

func (e *gscUnavailableError) Error() string { return e.reason }

// freshAccessToken returns a usable access token for the connection, refreshing
// and persisting it when near expiry.
func (e *gscExecutor) freshAccessToken(ctx context.Context, connection sqlc.GoogleConnection) (string, error) {
	if connection.Status != "active" {
		return "", &gscUnavailableError{reason: "Search Console needs reauthorization. Reconnect the Google account in the workspace settings."}
	}
	accessToken, err := e.fetcher.DecryptSecret(connection.EncryptedAccessToken.String)
	if err != nil {
		return "", fmt.Errorf("%s: decrypt access token: %w", gscToolName, err)
	}
	if accessToken != "" && connection.AccessTokenExpiresAt.Valid && connection.AccessTokenExpiresAt.Time.UTC().After(time.Now().UTC().Add(time.Minute)) {
		return accessToken, nil
	}

	refreshToken, err := e.fetcher.DecryptSecret(connection.EncryptedRefreshToken)
	if err != nil {
		return "", fmt.Errorf("%s: decrypt refresh token: %w", gscToolName, err)
	}
	refreshed, err := e.fetcher.RefreshAccessToken(ctx, refreshToken)
	if err != nil {
		_ = e.connections.UpdateGoogleConnectionStatus(ctx, sqlc.UpdateGoogleConnectionStatusParams{
			ID:        connection.ID,
			Status:    "reauth_required",
			LastError: pgtype.Text{String: err.Error(), Valid: true},
		})
		return "", &gscUnavailableError{reason: "Search Console needs reauthorization. Reconnect the Google account in the workspace settings."}
	}

	encryptedAccessToken, err := e.fetcher.EncryptSecret(refreshed.AccessToken)
	if err != nil {
		return "", fmt.Errorf("%s: encrypt access token: %w", gscToolName, err)
	}
	encryptedRefreshToken := connection.EncryptedRefreshToken
	if strings.TrimSpace(refreshed.RefreshToken) != "" {
		encryptedRefreshToken, err = e.fetcher.EncryptSecret(refreshed.RefreshToken)
		if err != nil {
			return "", fmt.Errorf("%s: encrypt refresh token: %w", gscToolName, err)
		}
	}
	if _, err := e.connections.UpdateGoogleConnectionTokens(ctx, sqlc.UpdateGoogleConnectionTokensParams{
		ID:                    connection.ID,
		EncryptedAccessToken:  pgtype.Text{String: encryptedAccessToken, Valid: true},
		EncryptedRefreshToken: encryptedRefreshToken,
		AccessTokenExpiresAt:  pgtype.Timestamptz{Time: gscTokenExpiry(refreshed.ExpiresIn), Valid: true},
		Scope:                 coalesceGSC(refreshed.Scope, connection.Scope),
		Status:                "active",
		LastError:             pgtype.Text{},
	}); err != nil {
		return "", fmt.Errorf("%s: persist refreshed token: %w", gscToolName, err)
	}
	return refreshed.AccessToken, nil
}

func gscTokenExpiry(expiresInSeconds int) time.Time {
	expiresIn := expiresInSeconds
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	refreshSkew := max(expiresIn-60, 0)
	return time.Now().UTC().Add(time.Duration(refreshSkew) * time.Second)
}

func coalesceGSC(primaryValue, fallbackValue string) string {
	if strings.TrimSpace(primaryValue) != "" {
		return primaryValue
	}
	return fallbackValue
}

// summary returns headline metrics for the current window vs the previous one.
func (e *gscExecutor) summary(ctx context.Context, projectID pgtype.UUID, projectConnection sqlc.ProjectGscConnection, accessToken string) (Result, error) {
	overview, err := e.fetcher.FetchOverviewCached(ctx, accessToken, projectID.String(), projectConnection.SiteUrl)
	if err != nil {
		if gscReauthError(err) {
			return unavailableGSC("Search Console needs reauthorization. Reconnect the Google account in the workspace settings."), nil
		}
		return Result{}, fmt.Errorf("%s: overview: %w", gscToolName, err)
	}
	window, ok := overview.Windows[strconv.Itoa(gscOverviewWindowDays)]
	if !ok {
		return Result{}, fmt.Errorf("%s: overview window %d missing", gscToolName, gscOverviewWindowDays)
	}
	response := gscSummaryResponse{
		Report:      "summary",
		StartDate:   window.Range.CurrentStart,
		EndDate:     window.Range.CurrentEnd,
		Clicks:      gscMetricSummary{Current: round2(window.Summary.Clicks.Current), Previous: round2(window.Summary.Clicks.Previous)},
		Impressions: gscMetricSummary{Current: round2(window.Summary.Impressions.Current), Previous: round2(window.Summary.Impressions.Previous)},
		CTR:         gscMetricSummary{Current: round2(window.Summary.CTR.Current), Previous: round2(window.Summary.CTR.Previous)},
		Position:    gscMetricSummary{Current: round2(window.Summary.Position.Current), Previous: round2(window.Summary.Position.Previous)},
	}
	content, err := json.Marshal(response)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal summary: %w", gscToolName, err)
	}
	return Result{
		Content: string(content),
		Summary: fmt.Sprintf("summary: %s clicks vs %s previous (position %s)", formatGSCPercent(response.Clicks.Current), formatGSCPercent(response.Clicks.Previous), formatGSCPercent(response.Position.Current)),
	}, nil
}

// opportunities returns the three suggestion lists from the overview window.
func (e *gscExecutor) opportunities(ctx context.Context, projectID pgtype.UUID, projectConnection sqlc.ProjectGscConnection, accessToken string) (Result, error) {
	overview, err := e.fetcher.FetchOverviewCached(ctx, accessToken, projectID.String(), projectConnection.SiteUrl)
	if err != nil {
		if gscReauthError(err) {
			return unavailableGSC("Search Console needs reauthorization. Reconnect the Google account in the workspace settings."), nil
		}
		return Result{}, fmt.Errorf("%s: overview: %w", gscToolName, err)
	}
	window, ok := overview.Windows[strconv.Itoa(gscOverviewWindowDays)]
	if !ok {
		return Result{}, fmt.Errorf("%s: overview window %d missing", gscToolName, gscOverviewWindowDays)
	}
	response := gscOpportunitiesResponse{
		Report:                  "opportunities",
		LowCTRQueries:           shapeGSCRows(window.Opportunities.LowCTRQueries),
		StrikingDistanceQueries: shapeGSCRows(window.Opportunities.StrikingDistanceQueries),
		QuestionQueries:         shapeGSCRows(window.Opportunities.QuestionQueries),
	}
	content, err := json.Marshal(response)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal opportunities: %w", gscToolName, err)
	}
	return Result{
		Content: string(content),
		Summary: fmt.Sprintf("opportunities: %d striking-distance queries, %d low-CTR, %d question queries",
			len(response.StrikingDistanceQueries), len(response.LowCTRQueries), len(response.QuestionQueries)),
	}, nil
}

// page returns one paged search console report.
func (e *gscExecutor) page(ctx context.Context, projectID pgtype.UUID, projectConnection sqlc.ProjectGscConnection, accessToken string, args gscArgs, options gsc.QueryPageOptions) (Result, error) {
	options.Days = args.Days
	options.Limit = args.Limit
	options.Offset = args.Offset
	page, err := e.fetcher.FetchQueriesCached(ctx, accessToken, projectID.String(), projectConnection.SiteUrl, options)
	if err != nil {
		if gscReauthError(err) {
			return unavailableGSC("Search Console needs reauthorization. Reconnect the Google account in the workspace settings."), nil
		}
		return Result{}, fmt.Errorf("%s: %s: %w", gscToolName, args.Report, err)
	}

	rows := shapeGSCRows(page.Rows)
	var totalClicks, totalImpressions float64
	for _, row := range page.Rows {
		totalClicks += row.Clicks
		totalImpressions += row.Impressions
	}
	response := gscReportResponse{
		Report:           args.Report,
		StartDate:        page.StartDate,
		EndDate:          page.EndDate,
		Rows:             rows,
		TotalClicks:      round2(totalClicks),
		TotalImpressions: round2(totalImpressions),
		NextOffset:       page.Offset + len(page.Rows),
		HasMore:          page.HasMore,
	}
	content, err := json.Marshal(response)
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal page: %w", gscToolName, err)
	}
	return Result{
		Content: string(content),
		Summary: fmt.Sprintf("%s: %d rows (last %d days, %.0f clicks)", args.Report, len(rows), page.Days, totalClicks),
	}, nil
}

func shapeGSCRows(rows []gsc.SearchAnalyticsRow) []gscRow {
	shaped := make([]gscRow, 0, len(rows))
	for _, row := range rows {
		shaped = append(shaped, gscRow{
			Key:         capGSCText(firstGSCKey(row)),
			Clicks:      round2(row.Clicks),
			Impressions: round2(row.Impressions),
			CTR:         round2(row.CTR),
			Position:    round2(row.Position),
		})
	}
	return shaped
}

// firstGSCKey picks the populated dimension value from a search analytics row.
func firstGSCKey(row gsc.SearchAnalyticsRow) string {
	for _, key := range []string{row.Query, row.Page, row.Country, row.Device} {
		if key != "" {
			return key
		}
	}
	return ""
}

func capGSCText(text string) string {
	if utf8.RuneCountInString(text) <= gscMaxRowTextRune {
		return text
	}
	return string([]rune(text)[:gscMaxRowTextRune]) + "\u2026"
}

func formatGSCPercent(value float64) string {
	return fmt.Sprintf("%.0f", value)
}

// gscOverviewWindowDays is the fixed window the overview uses.
const gscOverviewWindowDays = 180

// gscReauthError reports whether an upstream error means the connection needs
// reauthorization. gsc.Error carries a message only; refresh failures surface
// as such when the Google API rejects the token.
func gscReauthError(err error) bool {
	if err == nil {
		return false
	}
	var gscErr *gsc.Error
	if errors.As(err, &gscErr) {
		return strings.Contains(strings.ToLower(gscErr.Message), "auth")
	}
	return false
}

func unavailableGSC(reason string) Result {
	return Result{Content: reason, Summary: "search console not available"}
}

// parseGSCArgs parses the tool arguments strictly: unknown keys, duplicate
// keys, and trailing data are rejected.
func parseGSCArgs(raw json.RawMessage) (gscArgs, error) {
	args := gscArgs{Days: gscDefaultDays, Limit: gscDefaultLimit}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for key, value := range fields {
		switch key {
		case "report":
			if err := json.Unmarshal(value, &args.Report); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.Report = strings.ToLower(strings.TrimSpace(args.Report))
			if !slices.Contains(validGSCReports, args.Report) {
				return args, fmt.Errorf("invalid report %q; valid reports: %s", args.Report, strings.Join(validGSCReports, ", "))
			}
		case "days":
			if err := json.Unmarshal(value, &args.Days); err != nil {
				return args, fmt.Errorf("argument %q must be an integer", key)
			}
			if args.Days < gscMinDays {
				return args, fmt.Errorf("argument %q must be at least %d", key, gscMinDays)
			}
			if args.Days > gscMaxDays {
				args.Days = gscMaxDays
			}
		case "search":
			if err := json.Unmarshal(value, &args.Search); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.Search = strings.TrimSpace(args.Search)
			if len(args.Search) > gscMaxSearchLen {
				args.Search = args.Search[:gscMaxSearchLen]
			}
		case "limit":
			if err := json.Unmarshal(value, &args.Limit); err != nil {
				return args, fmt.Errorf("argument %q must be an integer", key)
			}
			if args.Limit < 1 {
				return args, fmt.Errorf("argument %q must be at least 1", key)
			}
			if args.Limit > gscMaxLimit {
				args.Limit = gscMaxLimit
			}
		case "offset":
			if err := json.Unmarshal(value, &args.Offset); err != nil {
				return args, fmt.Errorf("argument %q must be an integer", key)
			}
			if args.Offset < 0 {
				return args, fmt.Errorf("argument %q must be >= 0", key)
			}
			if args.Offset > gscMaxOffset {
				args.Offset = gscMaxOffset
			}
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	if args.Report == "" {
		return args, errors.New("missing required argument \"report\"")
	}
	return args, nil
}
