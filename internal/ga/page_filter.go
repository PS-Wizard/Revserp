package ga

import (
	"strings"
)

// PagePathFilter restricts Analytics reports to one branch path at Google via
// a FULL_REGEXP match against pagePath. The zero value disables it, so
// existing whole-property callers pass it unchanged.
type PagePathFilter struct {
	Expression string
}

func (filter PagePathFilter) active() bool {
	return strings.TrimSpace(filter.Expression) != ""
}

func (filter PagePathFilter) cacheKeyPart() string {
	return strings.TrimSpace(filter.Expression)
}

func (filter PagePathFilter) dimensionFilter() map[string]any {
	return map[string]any{
		"filter": map[string]any{
			"fieldName": "pagePath",
			"stringFilter": map[string]any{
				"matchType": "FULL_REGEXP",
				"value":     strings.TrimSpace(filter.Expression),
			},
		},
	}
}

func locationOverviewCacheKey(organizationID, connectionID, propertyID string, filter PagePathFilter) string {
	return strings.Join([]string{"location", organizationID, connectionID, propertyID, filter.cacheKeyPart()}, "|")
}
