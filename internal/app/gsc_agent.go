package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/app/aitools"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/gsc"
)

// Reasons Search Console cannot be read for a project. These are ordinary
// states, so they are returned as tool content rather than errors: the model
// should tell the user how to connect, not report a malfunction.
const (
	gscAgentNoOrgConnection = "Google Search Console is not connected for this organization. An organization owner can connect it from the Search Console page in the dashboard. Answer using crawl data instead, and say that search traffic data is unavailable until it is connected."
	gscAgentNoProjectSite   = "Google Search Console is connected for this organization, but this project has no Search Console property selected. An organization owner can select one from the Search Console page in the dashboard."
	gscAgentNeedsReconnect  = "The Google Search Console connection needs to be reconnected before its data can be read. An organization owner can reconnect it from the Search Console page in the dashboard."
)

// searchConsoleForAgent is the authorized Search Console read path behind the
// get_search_console_data tool. Scope supplies the tenant: the project comes
// from the session's open project, the site from that project's selection, and
// the credentials from the project's organization.
func (a *App) searchConsoleForAgent(ctx context.Context, scope aitools.Scope, query aitools.SearchConsoleQuery) (aitools.SearchConsoleReport, error) {
	if !scope.ProjectID.Valid {
		return aitools.SearchConsoleReport{Unavailable: "No project is open, so there is no Search Console property to read."}, nil
	}

	project, err := a.Queries.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{ID: scope.ProjectID, UserID: scope.UserID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return aitools.SearchConsoleReport{Unavailable: "The open project could not be found."}, nil
		}
		return aitools.SearchConsoleReport{}, err
	}

	projectConnection, hasProjectConnection, err := getProjectGSCConnectionByProjectID(ctx, a.Queries, project.ID)
	if err != nil {
		return aitools.SearchConsoleReport{}, err
	}
	googleConnection, hasGoogleConnection, err := getGoogleConnectionByOrganizationID(ctx, a.Queries, project.OrganizationID)
	if err != nil {
		return aitools.SearchConsoleReport{}, err
	}

	switch {
	case !hasGoogleConnection:
		return aitools.SearchConsoleReport{Unavailable: gscAgentNoOrgConnection}, nil
	case googleConnection.Status != "active":
		return aitools.SearchConsoleReport{Unavailable: gscAgentNeedsReconnect}, nil
	case !hasProjectConnection:
		return aitools.SearchConsoleReport{Unavailable: gscAgentNoProjectSite}, nil
	}

	_, accessToken, err := a.ensureFreshGoogleConnection(ctx, a.Queries, googleConnection)
	if err != nil {
		// A refresh failure here is a credential state the user can fix, not a
		// server fault, so it stays in the same "unavailable" channel.
		return aitools.SearchConsoleReport{Unavailable: gscAgentNeedsReconnect}, nil
	}

	return a.buildSearchConsoleReport(ctx, accessToken, project.OrganizationID.String(), projectConnection.SiteUrl, query)
}

func (a *App) buildSearchConsoleReport(ctx context.Context, accessToken, organizationID, siteURL string, query aitools.SearchConsoleQuery) (aitools.SearchConsoleReport, error) {
	switch query.Report {
	case "top_queries", "question_queries":
		page, err := a.GSCService.FetchQueriesCached(ctx, accessToken, organizationID, siteURL, gsc.QueryPageOptions{
			Days:          query.Days,
			Limit:         query.Limit,
			Search:        query.Search,
			QuestionsOnly: query.Report == "question_queries",
		})
		if err != nil {
			return aitools.SearchConsoleReport{}, err
		}
		return marshalSearchConsoleReport(map[string]any{
			"site_url":   siteURL,
			"start_date": page.StartDate,
			"end_date":   page.EndDate,
			"rows":       page.Rows,
			"has_more":   page.HasMore,
		}, fmt.Sprintf("%d %s", len(page.Rows), searchConsoleSummaryNoun(query.Report)))

	case "summary", "top_pages", "countries", "devices", "opportunities":
		window, err := a.fetchSearchConsoleWindow(ctx, accessToken, organizationID, siteURL)
		if err != nil {
			return aitools.SearchConsoleReport{}, err
		}
		return marshalSearchConsoleWindowReport(siteURL, window, query)
	}

	return aitools.SearchConsoleReport{}, fmt.Errorf("unknown report %q", query.Report)
}

// fetchSearchConsoleWindow returns the overview's single configured window,
// which is what the dashboard shows.
func (a *App) fetchSearchConsoleWindow(ctx context.Context, accessToken, organizationID, siteURL string) (gsc.OverviewWindow, error) {
	overview, err := a.GSCService.FetchOverviewCached(ctx, accessToken, organizationID, siteURL)
	if err != nil {
		return gsc.OverviewWindow{}, err
	}
	for _, window := range overview.Windows {
		return window, nil
	}
	return gsc.OverviewWindow{}, errors.New("search console returned no reporting window")
}

func marshalSearchConsoleWindowReport(siteURL string, window gsc.OverviewWindow, query aitools.SearchConsoleQuery) (aitools.SearchConsoleReport, error) {
	switch query.Report {
	case "summary":
		return marshalSearchConsoleReport(map[string]any{
			"site_url": siteURL,
			"range":    window.Range,
			"summary":  window.Summary,
		}, "search console summary")

	case "top_pages":
		rows := trimSearchConsoleRows(window.TopPages, query.Limit)
		return marshalSearchConsoleReport(map[string]any{
			"site_url":   siteURL,
			"start_date": window.Range.CurrentStart,
			"end_date":   window.Range.CurrentEnd,
			"rows":       rows,
		}, fmt.Sprintf("%d landing pages", len(rows)))

	case "countries":
		rows := trimSearchConsoleRows(window.CountryBreakdown, query.Limit)
		return marshalSearchConsoleReport(map[string]any{
			"site_url":   siteURL,
			"start_date": window.Range.CurrentStart,
			"end_date":   window.Range.CurrentEnd,
			// Country codes are ISO 3166-1 alpha-3, as Search Console returns them.
			"rows": rows,
		}, fmt.Sprintf("%d countries", len(rows)))

	case "devices":
		rows := trimSearchConsoleRows(window.DeviceBreakdown, query.Limit)
		return marshalSearchConsoleReport(map[string]any{
			"site_url":   siteURL,
			"start_date": window.Range.CurrentStart,
			"end_date":   window.Range.CurrentEnd,
			"rows":       rows,
		}, fmt.Sprintf("%d devices", len(rows)))

	default:
		lowCTR := trimSearchConsoleRows(window.Opportunities.LowCTRQueries, query.Limit)
		strikingDistance := trimSearchConsoleRows(window.Opportunities.StrikingDistanceQueries, query.Limit)
		return marshalSearchConsoleReport(map[string]any{
			"site_url":                  siteURL,
			"start_date":                window.Range.CurrentStart,
			"end_date":                  window.Range.CurrentEnd,
			"low_ctr_queries":           lowCTR,
			"striking_distance_queries": strikingDistance,
		}, fmt.Sprintf("%d low-CTR, %d striking-distance queries", len(lowCTR), len(strikingDistance)))
	}
}

func marshalSearchConsoleReport(payload map[string]any, summary string) (aitools.SearchConsoleReport, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return aitools.SearchConsoleReport{}, err
	}
	return aitools.SearchConsoleReport{Content: string(encoded), Summary: summary}, nil
}

func trimSearchConsoleRows(rows []gsc.SearchAnalyticsRow, limit int) []gsc.SearchAnalyticsRow {
	if limit > 0 && len(rows) > limit {
		return rows[:limit]
	}
	return rows
}

func searchConsoleSummaryNoun(report string) string {
	if report == "question_queries" {
		return "question queries"
	}
	return "queries"
}
