package issues

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ps-wizard/revserp/internal/issues/aeo"
	pagespeed "github.com/ps-wizard/revserp/internal/issues/page_speed"
	"github.com/ps-wizard/revserp/internal/issues/seo"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// DeriveIssues builds backend issue rows from persisted crawl facts.
func DeriveIssues(pageFacts []shared.PageFact, linkFacts []shared.LinkFact) []shared.DerivedIssue {
	var derivedIssues []shared.DerivedIssue
	scoreablePageFacts := make([]shared.PageFact, 0, len(pageFacts))
	for _, pageFact := range pageFacts {
		if !shared.IsScoreableContentType(pageFact.ContentType) {
			continue
		}
		scoreablePageFacts = append(scoreablePageFacts, pageFact)
	}
	derivedIssues = append(derivedIssues, seo.DeriveIssues(slices.Clone(scoreablePageFacts), linkFacts)...)
	derivedIssues = append(derivedIssues, aeo.DeriveIssues(scoreablePageFacts, linkFacts)...)
	derivedIssues = append(derivedIssues, pagespeed.DeriveIssues(scoreablePageFacts, linkFacts)...)
	return derivedIssues
}

// RecommendedFix returns deterministic fix guidance for one issue type.
func RecommendedFix(pillar string, bucket string, issueType string, message string, details string) string {
	switch strings.TrimSpace(issueType) {
	case "missing_title":
		return "Add a descriptive <title> tag that reflects the page intent and primary query."
	case "title_too_long":
		return "Shorten the title to roughly 30-60 characters, keep the main intent first, and remove filler or repeated brand text."
	case "title_too_short":
		return "Expand the title so it clearly describes the page intent while staying concise and useful in search results."
	case "duplicate_title":
		return "Give this page a unique title that distinguishes it from similar pages and matches its specific intent."
	case "missing_meta_description":
		return "Add a meta description that summarizes the page clearly and matches the search intent."
	case "meta_description_too_long":
		return "Shorten the meta description so the key summary fits cleanly within a typical search snippet."
	case "meta_description_too_short":
		return "Expand the meta description so it gives a fuller summary of the page while staying concise."
	case "duplicate_meta_description":
		return "Write a unique meta description for this page so it does not reuse copy from similar pages."
	case "missing_h1":
		return "Add one primary H1 heading that matches the page topic and aligns with the title."
	case "multiple_h1":
		return "Keep a single primary H1 on the page and demote secondary headings to lower levels."
	case "title_h1_mismatch":
		return "Align the title and H1 so both communicate the same page intent without unnecessary wording differences."
	case "missing_h2_on_long_page":
		return "Break the page into clearer sections with descriptive H2 subheadings."
	case "skipped_heading_levels":
		return "Restructure the heading outline so heading levels progress in order without skipping levels."
	case "thin_content":
		return "Add more useful original content that better answers the page intent and supports the target query."
	case "near_empty_visible_content":
		return "Add substantial visible page content so the page offers more than placeholder or utility copy."
	case "exact_duplicate_content":
		return "Consolidate duplicate pages into a canonical source, merge overlapping copy, and redirect or canonicalize true duplicates."
	case "near_duplicate_content":
		return "Differentiate this page with more unique content or consolidate it with the strongest canonical version if the intent overlaps too heavily."
	case "missing_canonical":
		return "Add a canonical link element pointing to the preferred absolute page URL."
	case "malformed_canonical":
		return "Replace the canonical value with a valid absolute HTTP or HTTPS URL."
	case "canonical_differs":
		return "Confirm the canonical target is intentional; otherwise point the canonical to this page's preferred URL."
	case "canonical_points_to_non_indexable_page":
		return "Point the canonical to an indexable preferred page or make the canonical target indexable if it should remain the source page."
	case "noindex_page":
		return "Remove the noindex directive if this page should be eligible to appear in search results."
	case "nofollow_page":
		return "Remove the nofollow directive if search engines should follow the page's internal links."
	case "client_error_status", "server_error_status":
		return "Fix the underlying HTTP error so the page responds successfully and can be crawled and indexed."
	case "missing_viewport":
		return "Add a viewport meta tag so the page renders correctly on mobile devices."
	case "missing_lang":
		return "Add a lang attribute to the root HTML element to declare the page language."
	case "images_missing_alt":
		return "Add descriptive alt text to meaningful images and keep decorative images empty with alt=\"\"."
	case "images_missing_dimensions":
		return "Set explicit image width and height attributes where possible to reduce layout shifts."
	case "too_many_images_on_page":
		return "Reduce unnecessary images or add more supporting content so the page has a stronger content-to-image balance."
	case "no_internal_links_out", "low_internal_links_out":
		return "Add relevant internal links from this page to other important pages on the site."
	case "orphan_like_page", "low_internal_links_in":
		return "Add more internal links pointing to this page from relevant navigational or contextual sources."
	case "very_deep_page":
		return "Improve crawl depth by linking to this page from stronger parent, hub, or navigation pages."
	case "internal_links_to_broken_pages":
		return "Update or remove internal links that point to broken targets."
	case "internal_links_to_redirects":
		return "Update internal links so they point directly to the final destination URL instead of a redirect."
	case "missing_og_tags":
		return "Add core Open Graph tags so the page has stronger sharing and entity-preview signals."
	case "weak_open_graph_coverage":
		return "Roll out core Open Graph tags across a larger share of important pages on the site."
	case "missing_structured_data":
		return "Add page-appropriate JSON-LD structured data using a schema type that matches the page purpose."
	case "generic_structured_data_only":
		return "Replace generic schema with more specific structured data that better describes the page's real content type."
	case "schema_missing_core_fields":
		return "Expand the structured data to include core identity fields like name, url, and description where appropriate."
	case "missing_https":
		return "Serve this page over HTTPS and update internal references to use the secure version."
	case "missing_author_signal":
		return "Add a clear author attribution signal in visible content, metadata, or structured data."
	case "weak_author_signal":
		return "Strengthen the author attribution so it identifies a specific credible person or source."
	case "article_missing_article_schema":
		return "Add Article or BlogPosting structured data to this article-like page."
	case "article_missing_publisher_identity":
		return "Expose author, publisher, or mainEntityOfPage details in the article schema."
	case "author_signal_not_supported_by_schema":
		return "Align visible author attribution with matching author or publisher identity in structured data."
	case "long_article_has_no_citations", "missing_external_citations":
		return "Add credible external sources or citations that support important claims on the page."
	case "long_article_has_weak_citations", "weak_external_citations":
		return "Strengthen citation support with more relevant and authoritative external references."
	case "faq_like_page_missing_faq_schema":
		return "Add FAQPage structured data that reflects the page's question-and-answer content."
	case "missing_website_schema":
		return "Add WebSite structured data for the site-level entity."
	case "missing_org_identity_schema":
		return "Add Organization, LocalBusiness, Person, or another appropriate identity schema for the site or business."
	case "missing_about_page":
		return "Add an About page that explains who is behind the site and why the content is trustworthy."
	case "missing_contact_page":
		return "Add a Contact page with a clear contact destination for users and systems."
	case "missing_policy_page":
		return "Add core policy pages such as privacy and terms to strengthen trust signals."
	case "homepage_missing_org_contact_trust_signals":
		return "Strengthen homepage trust signals with organization identity, clear about/contact coverage, and supporting schema markup."
	case "slow_response_time":
		return "Reduce server response time by improving backend performance, caching, and origin latency."
	case "large_page_size":
		return "Reduce page weight by compressing assets, removing unnecessary payloads, and loading fewer heavy resources."
	case "moderate_page_size":
		return "Trim page weight where possible so the page loads with less asset overhead."
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
