package gsc

import (
	"context"
	"strings"
)

// PageScopeFilter restricts Search Console rows to one branch scope at Google.
// An empty operator disables it, so existing whole-property callers pass the
// zero value unchanged.
type PageScopeFilter struct {
	Operator   string
	Expression string
}

// PageScopeOperatorIncludingRegex is the only Google Search Analytics page
// operator used here. Google offers page equals, but recorded page URLs carry
// optional trailing slashes and query strings while branch matching ignores
// both, so equality would silently drop variants. One anchored includingRegex
// per match kind mirrors MatchWebsiteScopePage instead.
const PageScopeOperatorIncludingRegex = "includingRegex"

func (scope PageScopeFilter) active() bool {
	return scope.Operator == PageScopeOperatorIncludingRegex && strings.TrimSpace(scope.Expression) != ""
}

func (scope PageScopeFilter) googleFilter() map[string]any {
	return map[string]any{
		"dimension":  "page",
		"operator":   scope.Operator,
		"expression": scope.Expression,
	}
}

func (scope PageScopeFilter) cacheKeyPart() string {
	if !scope.active() {
		return ""
	}
	return scope.Operator + "=" + scope.Expression
}

func searchAnalyticsPayloadWithPageScope(startDate, endDate string, dimensions []string, rowLimit int, scope PageScopeFilter) map[string]any {
	payload := map[string]any{
		"startDate":  startDate,
		"endDate":    endDate,
		"dimensions": dimensions,
		"dataState":  "final",
		"rowLimit":   rowLimit,
	}
	if scope.active() {
		payload["dimensionFilterGroups"] = []map[string]any{{
			"groupType": "and",
			"filters":   []map[string]any{scope.googleFilter()},
		}}
	}
	return payload
}

func locationOverviewCacheKey(organizationID, connectionID, siteURL string, scope PageScopeFilter) string {
	return strings.Join([]string{"location-overview", organizationID, connectionID, siteURL, scope.cacheKeyPart()}, "|")
}

// FetchOverviewWithPageScope loads one Search Console overview payload live,
// restricted to the branch scope at Google when the filter is active.
func (service *Service) FetchOverviewWithPageScope(ctx context.Context, accessToken, siteURL string, scope PageScopeFilter) (OverviewPayload, error) {
	return service.fetchOverviewWithPayloads(ctx, accessToken, siteURL, func(startDate, endDate string, dimensions []string, rowLimit int) map[string]any {
		return searchAnalyticsPayloadWithPageScope(startDate, endDate, dimensions, rowLimit, scope)
	})
}

// FetchOverviewCachedWithPageScope returns the cached location overview for
// organization+connection+site+scope, fetching live once on a miss. The
// connection is part of the key so two accounts over one site never share rows.
func (service *Service) FetchOverviewCachedWithPageScope(ctx context.Context, accessToken, organizationID, connectionID, siteURL string, scope PageScopeFilter) (OverviewPayload, error) {
	result, err := service.fetchCached(locationOverviewCacheKey(organizationID, connectionID, siteURL, scope), func() (any, error) {
		return service.FetchOverviewWithPageScope(ctx, accessToken, siteURL, scope)
	})
	if err != nil {
		return OverviewPayload{}, err
	}
	return result.(OverviewPayload), nil
}

// PeekOverviewCachedWithPageScope returns the stored location overview without
// calling Google, reporting whether one exists.
func (service *Service) PeekOverviewCachedWithPageScope(organizationID, connectionID, siteURL string, scope PageScopeFilter) (OverviewPayload, bool) {
	cached, ok := service.peekCached(locationOverviewCacheKey(organizationID, connectionID, siteURL, scope))
	if !ok {
		return OverviewPayload{}, false
	}
	payload, ok := cached.(OverviewPayload)
	return payload, ok
}

// FetchLocationQueriesCached returns the cached scoped query page for
// organization+connection+site+options, fetching live once on a miss.
func (service *Service) FetchLocationQueriesCached(ctx context.Context, accessToken, organizationID, connectionID, siteURL string, options QueryPageOptions) (QueryPage, error) {
	options = options.normalized()
	result, err := service.fetchCached(options.locationCacheKey(organizationID, connectionID, siteURL), func() (any, error) {
		return service.FetchQueries(ctx, accessToken, siteURL, options)
	})
	if err != nil {
		return QueryPage{}, err
	}
	return result.(QueryPage), nil
}

// PeekLocationQueriesCached returns the stored scoped query page for the exact
// options without calling Google, reporting whether one exists.
func (service *Service) PeekLocationQueriesCached(organizationID, connectionID, siteURL string, options QueryPageOptions) (QueryPage, bool) {
	cached, ok := service.peekCached(options.normalized().locationCacheKey(organizationID, connectionID, siteURL))
	if !ok {
		return QueryPage{}, false
	}
	page, ok := cached.(QueryPage)
	return page, ok
}
