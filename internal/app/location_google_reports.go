package app

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/ga"
	"github.com/ps-wizard/revserp/internal/gsc"
	"github.com/ps-wizard/revserp/internal/websitescope"
)

const locationGoogleReportNotConfigured = "location_google_binding_not_configured"

const locationRealtimeScopeNotSupported = "location_realtime_scope_not_supported"

const (
	locationReportCoverageBranch       = "branch"
	locationReportCoveragePropertyWide = "property-wide"
)

const locationGoogleReportNotRefreshed = "location_google_report_not_refreshed"

const locationGoogleScopeNotSet = "location_website_scope_not_set"

type locationGoogleScopeSummary struct {
	Revision int32  `json:"revision"`
	URL      string `json:"url"`
	Match    string `json:"match"`
}

type locationGSCReportTarget struct {
	Source          string
	Account         sqlc.GoogleConnection
	SiteURL         string
	PermissionLevel string
	Scope           *locationGoogleScopeSummary
	PageScope       gsc.PageScopeFilter
	ScopeApplied    bool
}

type locationGoogleAnalyticsReportTarget struct {
	Source              string
	Account             sqlc.GoogleConnection
	PropertyID          string
	PropertyDisplayName string
	AccountDisplayName  string
	Scope               *locationGoogleScopeSummary
	PageFilter          ga.PagePathFilter
	ScopeApplied        bool
}

// locationGoogleScopeFilters builds the Google-side filters for one branch
// scope. Root-only scopes and unknown matches are whole-site: not applicable,
// so callers show unconfigured info instead of parent figures. Both GSC
// expressions are anchored includingRegex over the canonical scope: recorded
// page URLs may carry a query string while branch matching ignores it, so
// page equals would silently drop variants. Exact mirrors the matcher's
// strict path equality, including its trailing-slash fold (an optional
// slash, never a prefix); subtree mirrors its segment-boundary prefix rule.
func locationGoogleScopeFilters(scopeURL, scopeMatch string) (gsc.PageScopeFilter, ga.PagePathFilter, bool) {
	trimmed := strings.TrimRight(strings.TrimSpace(scopeURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return gsc.PageScopeFilter{}, ga.PagePathFilter{}, false
	}
	path := strings.TrimRight(parsed.Path, "/")
	if path == "" {
		return gsc.PageScopeFilter{}, ga.PagePathFilter{}, false
	}
	switch scopeMatch {
	case websitescope.ScopeMatchExact:
		return gsc.PageScopeFilter{Operator: gsc.PageScopeOperatorIncludingRegex, Expression: "^" + regexp.QuoteMeta(trimmed) + "/?(\\?.*)?$"},
			ga.PagePathFilter{Expression: "^" + regexp.QuoteMeta(path) + "/?$"},
			true
	case websitescope.ScopeMatchSubtree:
		// Explicit end anchor: GA matches FULL_REGEXP, and the Go parity
		// test reads partial-match semantics, so spell the full match out.
		// Without it /about would also cover /aboutus.
		return gsc.PageScopeFilter{Operator: gsc.PageScopeOperatorIncludingRegex, Expression: "^" + regexp.QuoteMeta(trimmed) + "(/.*)?(\\?.*)?$"},
			ga.PagePathFilter{Expression: "^" + regexp.QuoteMeta(path) + "(/.*)?$"},
			true
	}
	return gsc.PageScopeFilter{}, ga.PagePathFilter{}, false
}

// handleRefreshLocationGSCReport fetches one location's Search Console overview
// plus one queries page live and stores both. This explicit POST is the only
// writer; report GETs only peek the stored payloads.
func (a *App) handleRefreshLocationGSCReport(w http.ResponseWriter, r *http.Request) {
	location, _, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	params := r.URL.Query()
	target, ok := a.resolveLocationGSCReportTarget(w, r, location, params.Get("page_filter") == "true")
	if !ok {
		return
	}
	account, accessToken, ok := a.locationReportAccessToken(w, r, target.Account)
	if !ok {
		return
	}
	organizationID := location.OrganizationID.String()
	connectionID := account.ID.String()
	options := locationGSCReportQueryOptions(params, target.PageScope)
	overview, err := a.GSCService.FetchOverviewCachedWithPageScope(r.Context(), accessToken, organizationID, connectionID, target.SiteURL, target.PageScope)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch search console data")
		return
	}
	page, err := a.GSCService.FetchLocationQueriesCached(r.Context(), accessToken, organizationID, connectionID, target.SiteURL, options)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch search console queries")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"location_id":          location.ID.String(),
		"source":               target.Source,
		"google_connection_id": connectionID,
		"google_account_email": textValue(account.GoogleAccountEmail),
		"site_url":             target.SiteURL,
		"permission_level":     target.PermissionLevel,
		"scope_applied":        target.ScopeApplied,
		"coverage":             locationReportCoverage(target.ScopeApplied),
		"scope":                target.Scope,
		"overview":             overview,
		"queries":              page,
	})
}

// handleGetLocationGSCReportOverview returns the stored location overview
// without calling Google. A miss answers cached:false so the UI offers an
// explicit refresh instead of fetching on mount.
func (a *App) handleGetLocationGSCReportOverview(w http.ResponseWriter, r *http.Request) {
	location, _, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	target, ok := a.resolveLocationGSCReportTarget(w, r, location, r.URL.Query().Get("page_filter") == "true")
	if !ok {
		return
	}
	overview, ok := a.GSCService.PeekOverviewCachedWithPageScope(location.OrganizationID.String(), target.Account.ID.String(), target.SiteURL, target.PageScope)
	if !ok {
		writeLocationReportCacheMiss(w, location.ID, target.Source, target.Scope)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"location_id":          location.ID.String(),
		"source":               target.Source,
		"google_connection_id": target.Account.ID.String(),
		"google_account_email": textValue(target.Account.GoogleAccountEmail),
		"site_url":             target.SiteURL,
		"permission_level":     target.PermissionLevel,
		"scope_applied":        target.ScopeApplied,
		"coverage":             locationReportCoverage(target.ScopeApplied),
		"scope":                target.Scope,
		"cached":               true,
		"overview":             overview,
	})
}

// handleGetLocationGSCReportQueries returns the stored scoped query page for
// the exact options without calling Google.
func (a *App) handleGetLocationGSCReportQueries(w http.ResponseWriter, r *http.Request) {
	location, _, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	params := r.URL.Query()
	target, ok := a.resolveLocationGSCReportTarget(w, r, location, params.Get("page_filter") == "true")
	if !ok {
		return
	}
	options := locationGSCReportQueryOptions(params, target.PageScope)
	page, ok := a.GSCService.PeekLocationQueriesCached(location.OrganizationID.String(), target.Account.ID.String(), target.SiteURL, options)
	if !ok {
		writeLocationReportCacheMiss(w, location.ID, target.Source, target.Scope)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"location_id":          location.ID.String(),
		"source":               target.Source,
		"google_connection_id": target.Account.ID.String(),
		"google_account_email": textValue(target.Account.GoogleAccountEmail),
		"site_url":             target.SiteURL,
		"permission_level":     target.PermissionLevel,
		"scope_applied":        target.ScopeApplied,
		"coverage":             locationReportCoverage(target.ScopeApplied),
		"scope":                target.Scope,
		"cached":               true,
		"queries":              page,
	})
}

// handleRefreshLocationGoogleAnalyticsReport fetches one location's Analytics
// overview live and stores it. This explicit POST is the only writer.
func (a *App) handleRefreshLocationGoogleAnalyticsReport(w http.ResponseWriter, r *http.Request) {
	location, _, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	target, ok := a.resolveLocationGoogleAnalyticsReportTarget(w, r, location, r.URL.Query().Get("page_filter") == "true")
	if !ok {
		return
	}
	account, accessToken, ok := a.locationReportAccessToken(w, r, target.Account)
	if !ok {
		return
	}
	overview, err := a.GAService.FetchOverviewCachedWithPagePathFilter(r.Context(), accessToken, location.OrganizationID.String(), account.ID.String(), target.PropertyID, target.PageFilter)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch analytics data")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"location_id":           location.ID.String(),
		"source":                target.Source,
		"google_connection_id":  account.ID.String(),
		"google_account_email":  textValue(account.GoogleAccountEmail),
		"property_id":           target.PropertyID,
		"property_display_name": target.PropertyDisplayName,
		"scope_applied":         target.ScopeApplied,
		"coverage":              locationReportCoverage(target.ScopeApplied),
		"scope":                 target.Scope,
		"overview":              overview,
	})
}

// handleGetLocationGoogleAnalyticsReportOverview returns the stored location
// Analytics overview without calling Google.
func (a *App) handleGetLocationGoogleAnalyticsReportOverview(w http.ResponseWriter, r *http.Request) {
	location, _, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	target, ok := a.resolveLocationGoogleAnalyticsReportTarget(w, r, location, r.URL.Query().Get("page_filter") == "true")
	if !ok {
		return
	}
	overview, ok := a.GAService.PeekOverviewCachedWithPagePathFilter(location.OrganizationID.String(), target.Account.ID.String(), target.PropertyID, target.PageFilter)
	if !ok {
		writeLocationReportCacheMiss(w, location.ID, target.Source, target.Scope)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"location_id":           location.ID.String(),
		"source":                target.Source,
		"google_connection_id":  target.Account.ID.String(),
		"google_account_email":  textValue(target.Account.GoogleAccountEmail),
		"property_id":           target.PropertyID,
		"property_display_name": target.PropertyDisplayName,
		"scope_applied":         target.ScopeApplied,
		"coverage":              locationReportCoverage(target.ScopeApplied),
		"scope":                 target.Scope,
		"cached":                true,
		"overview":              overview,
	})
}

// handleGetLocationGoogleAnalyticsRealtime returns the live active-user count.
// The Realtime API exposes no page dimension, so a branch-scoped location
// answers supported:false instead of a property-wide count dressed as local
// figures. A custom full-property binding reports the property-wide count
// labeled as such. Mounts only inside the realtime view, never on the root.
func (a *App) handleGetLocationGoogleAnalyticsRealtime(w http.ResponseWriter, r *http.Request) {
	location, _, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	target, ok := a.resolveLocationGoogleAnalyticsReportTarget(w, r, location, r.URL.Query().Get("page_filter") == "true")
	if !ok {
		return
	}
	if target.ScopeApplied {
		writeJSON(w, http.StatusOK, map[string]any{
			"location_id":          location.ID.String(),
			"source":               target.Source,
			"google_connection_id": target.Account.ID.String(),
			"property_id":          target.PropertyID,
			"scope_applied":        true,
			"scope":                target.Scope,
			"supported":            false,
			"reason":               locationRealtimeScopeNotSupported,
		})
		return
	}
	account, accessToken, ok := a.locationReportAccessToken(w, r, target.Account)
	if !ok {
		return
	}
	activeUsers, err := a.GAService.FetchRealtime(r.Context(), accessToken, target.PropertyID)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch analytics realtime data")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"location_id":          location.ID.String(),
		"source":               target.Source,
		"google_connection_id": account.ID.String(),
		"property_id":          target.PropertyID,
		"scope_applied":        false,
		"scope":                target.Scope,
		"coverage":             locationReportCoveragePropertyWide,
		"supported":            true,
		"active_users":         activeUsers,
	})
}

// resolveLocationGSCReportTarget resolves the effective report target or writes
// the unconfigured/error response itself. ok=false always means done: inherit
// without a usable branch scope answers configured:false, never parent figures.
func (a *App) resolveLocationGSCReportTarget(w http.ResponseWriter, r *http.Request, location sqlc.GetProjectLocationForUserRow, wantPageFilter bool) (*locationGSCReportTarget, bool) {
	binding, hasBinding, err := getLocationGSCBinding(r.Context(), a.DB, location.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return nil, false
	}
	mode := locationGoogleModeInherit
	if hasBinding {
		mode = binding.Mode
	}
	if mode == locationGoogleModeOff {
		writeJSONError(w, http.StatusBadRequest, locationGoogleReportNotConfigured)
		return nil, false
	}
	if mode == locationGoogleModeCustom {
		if !hasBinding || !binding.GoogleConnectionID.Valid || !binding.SiteURL.Valid {
			writeJSONError(w, http.StatusBadRequest, locationGoogleReportNotConfigured)
			return nil, false
		}
		target := &locationGSCReportTarget{Source: locationGoogleModeCustom, SiteURL: textValue(binding.SiteURL), PermissionLevel: textValue(binding.PermissionLevel)}
		if !a.fillLocationReportAccount(w, r, location.OrganizationID, binding.GoogleConnectionID, &target.Account) {
			return nil, false
		}
		if !wantPageFilter {
			return target, true
		}
		scope, scopeFilters, ok := a.locationReportBranchScope(w, r, location.ID)
		if !ok {
			return nil, false
		}
		if scope == nil {
			writeLocationReportUnconfigured(w, location.ID, target.Source)
			return nil, false
		}
		target.Scope, target.PageScope, target.ScopeApplied = scope, scopeFilters.GSC, true
		return target, true
	}
	projectConnection, hasProjectConnection, err := getProjectGSCConnectionByProjectID(r.Context(), a.Queries, location.ProjectID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return nil, false
	}
	if !hasProjectConnection {
		writeJSONError(w, http.StatusBadRequest, locationGoogleReportNotConfigured)
		return nil, false
	}
	target := &locationGSCReportTarget{Source: locationGoogleModeInherit, SiteURL: projectConnection.SiteUrl, PermissionLevel: textValue(projectConnection.PermissionLevel)}
	if !a.fillLocationReportAccount(w, r, location.OrganizationID, projectConnection.GoogleConnectionID, &target.Account) {
		return nil, false
	}
	scope, scopeFilters, ok := a.locationReportBranchScope(w, r, location.ID)
	if !ok {
		return nil, false
	}
	if scope == nil {
		writeLocationReportUnconfigured(w, location.ID, target.Source)
		return nil, false
	}
	target.Scope, target.PageScope, target.ScopeApplied = scope, scopeFilters.GSC, true
	return target, true
}

// resolveLocationGoogleAnalyticsReportTarget mirrors the GSC resolver for the
// Analytics binding. ok=false always means the response is already written.
func (a *App) resolveLocationGoogleAnalyticsReportTarget(w http.ResponseWriter, r *http.Request, location sqlc.GetProjectLocationForUserRow, wantPageFilter bool) (*locationGoogleAnalyticsReportTarget, bool) {
	binding, hasBinding, err := getLocationGoogleAnalyticsBinding(r.Context(), a.DB, location.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return nil, false
	}
	mode := locationGoogleModeInherit
	if hasBinding {
		mode = binding.Mode
	}
	if mode == locationGoogleModeOff {
		writeJSONError(w, http.StatusBadRequest, locationGoogleReportNotConfigured)
		return nil, false
	}
	if mode == locationGoogleModeCustom {
		if !hasBinding || !binding.GoogleConnectionID.Valid || !binding.PropertyID.Valid {
			writeJSONError(w, http.StatusBadRequest, locationGoogleReportNotConfigured)
			return nil, false
		}
		target := &locationGoogleAnalyticsReportTarget{
			Source:              locationGoogleModeCustom,
			PropertyID:          textValue(binding.PropertyID),
			PropertyDisplayName: textValue(binding.PropertyDisplayName),
			AccountDisplayName:  textValue(binding.AccountDisplayName),
		}
		if !a.fillLocationReportAccount(w, r, location.OrganizationID, binding.GoogleConnectionID, &target.Account) {
			return nil, false
		}
		if !wantPageFilter {
			return target, true
		}
		scope, scopeFilters, ok := a.locationReportBranchScope(w, r, location.ID)
		if !ok {
			return nil, false
		}
		if scope == nil {
			writeLocationReportUnconfigured(w, location.ID, target.Source)
			return nil, false
		}
		target.Scope, target.PageFilter, target.ScopeApplied = scope, scopeFilters.GA, true
		return target, true
	}
	selection, hasSelection, err := getProjectGoogleAnalyticsConnectionByProjectID(r.Context(), a.Queries, location.ProjectID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return nil, false
	}
	if !hasSelection {
		writeJSONError(w, http.StatusBadRequest, locationGoogleReportNotConfigured)
		return nil, false
	}
	target := &locationGoogleAnalyticsReportTarget{
		Source:              locationGoogleModeInherit,
		PropertyID:          selection.PropertyID,
		PropertyDisplayName: selection.PropertyDisplayName,
		AccountDisplayName:  textValue(selection.AccountDisplayName),
	}
	if !a.fillLocationReportAccount(w, r, location.OrganizationID, selection.GoogleConnectionID, &target.Account) {
		return nil, false
	}
	scope, scopeFilters, ok := a.locationReportBranchScope(w, r, location.ID)
	if !ok {
		return nil, false
	}
	if scope == nil {
		writeLocationReportUnconfigured(w, location.ID, target.Source)
		return nil, false
	}
	target.Scope, target.PageFilter, target.ScopeApplied = scope, scopeFilters.GA, true
	return target, true
}

type locationReportScopeFilters struct {
	GSC gsc.PageScopeFilter
	GA  ga.PagePathFilter
}

// locationReportBranchScope resolves the pinned-or-current branch scope into
// Google-side filters. A nil summary means unconfigured: no usable scope, no
// root-only scope, never a whole-property fallback.
func (a *App) locationReportBranchScope(w http.ResponseWriter, r *http.Request, locationID pgtype.UUID) (*locationGoogleScopeSummary, locationReportScopeFilters, bool) {
	scope, ok := a.resolveLocationWebsiteAuditScope(w, r, locationID)
	if !ok {
		return nil, locationReportScopeFilters{}, false
	}
	scopeURL, hasURL := locationWebsiteAuditScopeURL(scope)
	if !hasURL {
		return nil, locationReportScopeFilters{}, true
	}
	gscFilter, gaFilter, applicable := locationGoogleScopeFilters(scopeURL, scope.Match)
	if !applicable {
		return nil, locationReportScopeFilters{}, true
	}
	return &locationGoogleScopeSummary{Revision: scope.Revision, URL: scopeURL, Match: scope.Match},
		locationReportScopeFilters{GSC: gscFilter, GA: gaFilter},
		true
}

func (a *App) fillLocationReportAccount(w http.ResponseWriter, r *http.Request, organizationID, connectionID pgtype.UUID, account *sqlc.GoogleConnection) bool {
	loaded, found, err := getGoogleAccountConnectionByID(r.Context(), a.DB, connectionID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return false
	}
	if !found || !uuidEqual(loaded.OrganizationID, organizationID) {
		writeJSONError(w, http.StatusBadRequest, "google account not found")
		return false
	}
	*account = loaded
	return true
}

func (a *App) locationReportAccessToken(w http.ResponseWriter, r *http.Request, account sqlc.GoogleConnection) (sqlc.GoogleConnection, string, bool) {
	account, accessToken, err := a.ensureFreshGoogleConnection(r.Context(), a.Queries, account)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to refresh google connection")
		return sqlc.GoogleConnection{}, "", false
	}
	return account, accessToken, true
}

func locationGSCReportQueryOptions(params url.Values, scope gsc.PageScopeFilter) gsc.QueryPageOptions {
	return gsc.QueryPageOptions{
		Days:          parseIntQueryParam(params.Get("days")),
		Limit:         parseIntQueryParam(params.Get("limit")),
		Offset:        parseIntQueryParam(params.Get("offset")),
		Search:        params.Get("search"),
		Dimension:     params.Get("dimension"),
		QuestionsOnly: params.Get("preset") == "questions",
		PageScope:     scope,
	}
}

func locationReportCoverage(scopeApplied bool) string {
	if scopeApplied {
		return locationReportCoverageBranch
	}
	return locationReportCoveragePropertyWide
}

func writeLocationReportUnconfigured(w http.ResponseWriter, locationID pgtype.UUID, source string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"location_id":   locationID.String(),
		"source":        source,
		"configured":    false,
		"reason":        locationGoogleScopeNotSet,
		"scope":         nil,
		"scope_applied": false,
	})
}

func writeLocationReportCacheMiss(w http.ResponseWriter, locationID pgtype.UUID, source string, scope *locationGoogleScopeSummary) {
	writeJSON(w, http.StatusOK, map[string]any{
		"location_id":   locationID.String(),
		"source":        source,
		"configured":    true,
		"cached":        false,
		"reason":        locationGoogleReportNotRefreshed,
		"scope":         scope,
		"scope_applied": scope != nil,
	})
}
