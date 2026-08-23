package issues

import (
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/ps-wizard/revserp/internal/issues/aeo"
	pagespeed "github.com/ps-wizard/revserp/internal/issues/page_speed"
	"github.com/ps-wizard/revserp/internal/issues/seo"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// DeriveIssues builds backend issue rows from persisted crawl facts.
func DeriveIssues(pageFacts []shared.PageFact, linkFacts []shared.LinkFact) []shared.DerivedIssue {
	derivedIssues, _ := DeriveIssuesWithDuplicateEvidence(pageFacts, linkFacts)
	return derivedIssues
}

// DeriveIssuesWithDuplicateEvidence derives all issues and returns duplicate
// evidence from the same SEO pass.
func DeriveIssuesWithDuplicateEvidence(pageFacts []shared.PageFact, linkFacts []shared.LinkFact) ([]shared.DerivedIssue, seo.DuplicateEvidence) {
	var derivedIssues []shared.DerivedIssue
	scoreablePageFacts := make([]shared.PageFact, 0, len(pageFacts))
	brokenPageFacts := make([]shared.PageFact, 0)
	for _, pageFact := range pageFacts {
		if !shared.IsScoreableContentType(pageFact.ContentType) {
			continue
		}
		// A broken page reports only that it is broken. Passing it to the content
		// rules would turn one problem into a handful of missing-metadata issues,
		// and the link-target rules already attribute it to the pages linking here.
		if !pageFact.IsHealthy() {
			brokenPageFacts = append(brokenPageFacts, pageFact)
			continue
		}
		scoreablePageFacts = append(scoreablePageFacts, pageFact)
	}
	derivedIssues = append(derivedIssues, seo.DeriveBrokenPageIssues(brokenPageFacts)...)
	seoStartedAt := time.Now()
	seoIssues, duplicateEvidence := seo.DeriveIssuesWithDuplicateEvidence(slices.Clone(scoreablePageFacts), linkFacts)
	derivedIssues = append(derivedIssues, seoIssues...)
	seoElapsed := time.Since(seoStartedAt)
	aeoStartedAt := time.Now()
	derivedIssues = append(derivedIssues, aeo.DeriveIssues(scoreablePageFacts, linkFacts)...)
	aeoElapsed := time.Since(aeoStartedAt)
	pagespeedStartedAt := time.Now()
	derivedIssues = append(derivedIssues, pagespeed.DeriveIssues(scoreablePageFacts, linkFacts)...)
	pagespeedElapsed := time.Since(pagespeedStartedAt)
	log.Printf("derive breakdown: scoreable_pages=%d broken_pages=%d seo=%s aeo=%s pagespeed=%s", len(scoreablePageFacts), len(brokenPageFacts), seoElapsed.Round(time.Millisecond), aeoElapsed.Round(time.Millisecond), pagespeedElapsed.Round(time.Millisecond))
	return derivedIssues, duplicateEvidence
}

var recommendedFixes = map[string]string{
	"missing_title":                              "Add a descriptive <title> tag that reflects the page intent and primary query.",
	"title_too_long":                             "Shorten the title to roughly 30-60 characters, keep the main intent first, and remove filler or repeated brand text.",
	"title_too_short":                            "Expand the title so it clearly describes the page intent while staying concise and useful in search results.",
	"duplicate_title":                            "Give this page a unique title that distinguishes it from similar pages and matches its specific intent.",
	"missing_meta_description":                   "Add a meta description that summarizes the page clearly and matches the search intent.",
	"meta_description_too_long":                  "Shorten the meta description so the key summary fits cleanly within a typical search snippet.",
	"meta_description_too_short":                 "Expand the meta description so it gives a fuller summary of the page while staying concise.",
	"duplicate_meta_description":                 "Write a unique meta description for this page so it does not reuse copy from similar pages.",
	"missing_h1":                                 "Add one primary H1 heading that matches the page topic and aligns with the title.",
	"multiple_h1":                                "Keep a single primary H1 on the page and demote secondary headings to lower levels.",
	"title_h1_mismatch":                          "Align the title and H1 so both communicate the same page intent without unnecessary wording differences.",
	"missing_h2_on_long_page":                    "Break the page into clearer sections with descriptive H2 subheadings.",
	"skipped_heading_levels":                     "Restructure the heading outline so heading levels progress in order without skipping levels.",
	"thin_content":                               "Add more useful original content that better answers the page intent and supports the target query.",
	"near_empty_visible_content":                 "Add substantial visible page content so the page offers more than placeholder or utility copy.",
	"exact_duplicate_content":                    "Consolidate duplicate pages into a canonical source, merge overlapping copy, and redirect or canonicalize true duplicates.",
	"near_duplicate_content":                     "Differentiate this page with more unique content or consolidate it with the strongest canonical version if the intent overlaps too heavily.",
	"missing_canonical":                          "Add a canonical link element pointing to the preferred absolute page URL.",
	"malformed_canonical":                        "Replace the canonical value with a valid absolute HTTP or HTTPS URL.",
	"canonical_differs":                          "Confirm the canonical target is intentional; otherwise point the canonical to this page's preferred URL.",
	"canonical_points_to_non_indexable_page":     "Point the canonical to an indexable preferred page or make the canonical target indexable if it should remain the source page.",
	"noindex_page":                               "Remove the noindex directive if this page should be eligible to appear in search results.",
	"nofollow_page":                              "Remove the nofollow directive if search engines should follow the page's internal links.",
	"client_error_status":                        "Fix the underlying HTTP error so the page responds successfully and can be crawled and indexed.",
	"server_error_status":                        "Fix the underlying HTTP error so the page responds successfully and can be crawled and indexed.",
	"soft_404":                                   "Return a real 404 or 410 status for URLs that do not exist, instead of a 200 with not-found content. A success status tells search engines the page is real, so the URL stays in the index as thin duplicate content and the broken link that led here goes unreported.",
	"fetch_failed":                               "Confirm the URL is reachable: check DNS, TLS, server errors, and any rate limiting or bot protection that blocks crawlers. If the page no longer exists, remove the internal links pointing to it.",
	"missing_viewport":                           "Add a viewport meta tag so the page renders correctly on mobile devices.",
	"missing_lang":                               "Add a lang attribute to the root HTML element to declare the page language.",
	"images_missing_alt":                         "Add descriptive alt text to meaningful images and keep decorative images empty with alt=\"\".",
	"images_missing_dimensions":                  "Set explicit image width and height attributes where possible to reduce layout shifts.",
	"too_many_images_on_page":                    "Reduce unnecessary images or add more supporting content so the page has a stronger content-to-image balance.",
	"no_internal_links_out":                      "Add relevant internal links from this page to other important pages on the site.",
	"low_internal_links_out":                     "Add relevant internal links from this page to other important pages on the site.",
	"orphan_like_page":                           "Add more internal links pointing to this page from relevant navigational or contextual sources.",
	"low_internal_links_in":                      "Add more internal links pointing to this page from relevant navigational or contextual sources.",
	"very_deep_page":                             "Improve crawl depth by linking to this page from stronger parent, hub, or navigation pages.",
	"internal_links_to_broken_pages":             "Update or remove internal links that point to broken targets.",
	"internal_links_to_redirects":                "Update internal links so they point directly to the final destination URL instead of a redirect.",
	"missing_og_tags":                            "Add core Open Graph tags so the page has stronger sharing and entity-preview signals.",
	"weak_open_graph_coverage":                   "Roll out core Open Graph tags across a larger share of important pages on the site.",
	"missing_structured_data":                    "Add page-appropriate JSON-LD structured data using a schema type that matches the page purpose.",
	"generic_structured_data_only":               "Replace generic schema with more specific structured data that better describes the page's real content type.",
	"schema_missing_core_fields":                 "Expand the structured data to include core identity fields like name, url, and description where appropriate.",
	"missing_https":                              "Serve this page over HTTPS and update internal references to use the secure version.",
	"missing_author_signal":                      "Add a clear author attribution signal in visible content, metadata, or structured data.",
	"weak_author_signal":                         "Strengthen the author attribution so it identifies a specific credible person or source.",
	"article_missing_article_schema":             "Add Article or BlogPosting structured data to this article-like page.",
	"article_missing_publisher_identity":         "Expose author, publisher, or mainEntityOfPage details in the article schema.",
	"author_signal_not_supported_by_schema":      "Align visible author attribution with matching author or publisher identity in structured data.",
	"long_article_has_no_citations":              "Add credible external sources or citations that support important claims on the page.",
	"missing_external_citations":                 "Add credible external sources or citations that support important claims on the page.",
	"long_article_has_weak_citations":            "Strengthen citation support with more relevant and authoritative external references.",
	"weak_external_citations":                    "Strengthen citation support with more relevant and authoritative external references.",
	"faq_like_page_missing_faq_schema":           "Add FAQPage structured data that reflects the page's question-and-answer content.",
	"missing_website_schema":                     "Add WebSite structured data for the site-level entity.",
	"missing_org_identity_schema":                "Add Organization, LocalBusiness, Person, or another appropriate identity schema for the site or business.",
	"missing_about_page":                         "Add an About page that explains who is behind the site and why the content is trustworthy.",
	"missing_contact_page":                       "Add a Contact page with a clear contact destination for users and systems.",
	"missing_policy_page":                        "Add core policy pages such as privacy and terms to strengthen trust signals.",
	"homepage_missing_org_contact_trust_signals": "Strengthen homepage trust signals with organization identity, clear about/contact coverage, and supporting schema markup.",
	"slow_response_time":                         "Reduce server response time by improving backend performance, caching, and origin latency.",
	"large_page_size":                            "Reduce page weight by compressing assets, removing unnecessary payloads, and loading fewer heavy resources.",
	"moderate_page_size":                         "Trim page weight where possible so the page loads with less asset overhead.",
}

// RecommendedFix returns deterministic fix guidance for one issue type.
func RecommendedFix(pillar string, bucket string, issueType string, message string, details string) string {
	if fix, ok := recommendedFixes[strings.TrimSpace(issueType)]; ok {
		return fix
	}

	trimmedMessage := strings.TrimSpace(message)
	trimmedDetails := strings.TrimSpace(details)
	issueLabel := shared.HumanizeIdentifier(issueType)
	bucketLabel := shared.HumanizeIdentifier(bucket)
	if trimmedMessage != "" && trimmedDetails != "" {
		return fmt.Sprintf("Review %s in %s and update the page so it satisfies this issue: %s %s", issueLabel, bucketLabel, trimmedMessage, trimmedDetails)
	}
	if trimmedMessage != "" {
		return fmt.Sprintf("Review %s in %s and update the page so it satisfies this issue: %s", issueLabel, bucketLabel, trimmedMessage)
	}
	return fmt.Sprintf("Review %s in %s and update the page so it meets the expected requirement.", issueLabel, bucketLabel)
}
