package shared

import "strings"

// sitewideIssueTypes is the set of issue types that are site-level
// rather than page-addressable. Kept here as the single source of truth.
var sitewideIssueTypes = map[string]struct{}{
	"weak_open_graph_coverage":                   {},
	"missing_website_schema":                     {},
	"missing_org_identity_schema":                {},
	"missing_about_page":                         {},
	"missing_contact_page":                       {},
	"missing_policy_page":                        {},
	"missing_llms_txt":                           {},
	"homepage_missing_org_contact_trust_signals": {},
}

// IsSitewideIssue reports whether the issue type is site-wide.
func IsSitewideIssue(issueType string) bool {
	_, ok := sitewideIssueTypes[strings.TrimSpace(issueType)]
	return ok
}

// IsGooglePSIOriginIssue reports whether the issue type is an origin-level
// Google PSI issue that is not attributable to a single page.
func IsGooglePSIOriginIssue(issueType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(issueType)), "google_psi_")
}

// IsPageAddressableIssue reports whether the issue type should be counted
// toward per-page health scoring.
func IsPageAddressableIssue(issueType string) bool {
	return !IsSitewideIssue(issueType) && !IsGooglePSIOriginIssue(issueType)
}
