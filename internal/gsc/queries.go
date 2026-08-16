package gsc

import (
	"context"
	"strconv"
	"strings"
)

// FetchQueries loads one page of Search Console query rows live from Google.
// Search and question filters are pushed to Google as dimension filters, so
// paging walks the filtered result set rather than a locally trimmed slice of
// the top rows by clicks — question-style queries are long tail and never
// appear in a site's top 25.
func (service *Service) FetchQueries(ctx context.Context, accessToken, siteURL string, options QueryPageOptions) (QueryPage, error) {
	options = options.normalized()
	startDate, endDate := getDateRange(options.Days, 0)

	payload := map[string]any{
		"startDate":  startDate,
		"endDate":    endDate,
		"dimensions": []string{options.dimension()},
		"dataState":  "final",
		// One row past the page tells us whether another page exists without
		// spending a second request on a count.
		"rowLimit": options.Limit + 1,
		"startRow": options.Offset,
	}
	if filters := options.dimensionFilters(); len(filters) > 0 {
		payload["dimensionFilterGroups"] = []map[string]any{{
			"groupType": "and",
			"filters":   filters,
		}}
	}

	rows, err := service.querySearchAnalytics(ctx, accessToken, siteURL, payload)
	if err != nil {
		return QueryPage{}, err
	}

	hasMore := len(rows) > options.Limit
	if hasMore {
		rows = rows[:options.Limit]
	}

	return QueryPage{
		Rows:      rows,
		Days:      options.Days,
		Limit:     options.Limit,
		Offset:    options.Offset,
		HasMore:   hasMore,
		StartDate: startDate,
		EndDate:   endDate,
	}, nil
}

// FetchQueriesCached returns a cached query page for this exact request when one
// exists and is younger than responseCacheTTL, otherwise it fetches live. Search
// Console data lags by days, so a repeated search or a page revisit never needs
// a fresh upstream call.
func (service *Service) FetchQueriesCached(ctx context.Context, accessToken, organizationID, siteURL string, options QueryPageOptions) (QueryPage, error) {
	options = options.normalized()
	result, err := service.fetchCached(options.cacheKey(organizationID, siteURL), func() (any, error) {
		return service.FetchQueries(ctx, accessToken, siteURL, options)
	})
	if err != nil {
		return QueryPage{}, err
	}
	return result.(QueryPage), nil
}

func (options QueryPageOptions) normalized() QueryPageOptions {
	options.Days = clampInt(options.Days, queryPageDefaultDays, queryPageMinDays, queryPageMaxDays)
	options.Limit = clampInt(options.Limit, queryPageDefaultLimit, 1, queryPageMaxLimit)

	if options.Offset < 0 {
		options.Offset = 0
	}
	if options.Offset > queryPageMaxOffset {
		options.Offset = queryPageMaxOffset
	}

	options.Search = strings.TrimSpace(options.Search)
	if len(options.Search) > queryPageMaxSearch {
		options.Search = strings.TrimSpace(options.Search[:queryPageMaxSearch])
	}
	if options.Dimension == "" {
		options.Dimension = "query"
	}
	return options
}
func (options QueryPageOptions) dimension() string {
	switch options.Dimension {
	case "page", "country", "device", "query":
		return options.Dimension
	default:
		return "query"
	}
}

// dimensionFilters builds Google's dimension filters for this page. "contains"
// is a literal substring match, so a user's search text is never interpreted as
// a pattern.
func (options QueryPageOptions) dimensionFilters() []map[string]any {
	filters := make([]map[string]any, 0, 2)
	if options.QuestionsOnly {
		filters = append(filters, map[string]any{
			"dimension":  "query",
			"operator":   "includingRegex",
			"expression": questionQueryPattern,
		})
	}
	if options.Search != "" {
		filters = append(filters, map[string]any{
			"dimension":  "query",
			"operator":   "contains",
			"expression": options.Search,
		})
	}
	return filters
}

func (options QueryPageOptions) cacheKey(organizationID, siteURL string) string {
	return strings.Join([]string{
		"queries",
		organizationID,
		siteURL,
		strconv.Itoa(options.Days),
		strconv.Itoa(options.Limit),
		strconv.Itoa(options.Offset),
		options.dimension(),
		strconv.FormatBool(options.QuestionsOnly),
		strings.ToLower(options.Search),
	}, "|")
}

func clampInt(value, fallback, minimum, maximum int) int {
	if value <= 0 {
		value = fallback
	}
	if value < minimum {
		value = minimum
	}
	if value > maximum {
		value = maximum
	}
	return value
}
