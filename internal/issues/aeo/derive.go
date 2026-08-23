package aeo

import (
	"fmt"
	"strings"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// DeriveIssues builds AEO issues from persisted crawl facts.
func DeriveIssues(pageFacts []shared.PageFact, _ []shared.LinkFact, siteFacts shared.SiteFacts) []shared.DerivedIssue {
	var derivedIssues []shared.DerivedIssue
	siteIssuePageFact, hasSiteIssuePageFact := selectSiteIssuePageFact(pageFacts)
	homepagePageFact, hasHomepagePageFact := selectHomepagePageFact(pageFacts)

	ogCoveredPageCount := 0
	hasAboutPage := false
	hasContactPage := false
	hasPolicyPage := false
	hasWebsiteSchema := false
	hasOrganizationSchema := false
	homepageHasPublisherIdentity := false

	for _, pageFact := range pageFacts {
		isArticleLike := isArticleLikePage(pageFact)
		hasJSONLD := hasMeaningfulJSONLD(pageFact.JSONLD)

		if !hasMeaningfulOGTags(pageFact.OGTags) {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "trust", "missing_og_tags", "low", "Page is missing Open Graph tags", "Add core Open Graph tags for richer sharing previews."))
		} else {
			ogCoveredPageCount++
		}

		if !hasJSONLD {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "answerability", "missing_structured_data", "high", "Page is missing structured data", "Add JSON-LD structured data to the page."))
		} else {
			if hasOnlyGenericStructuredData(pageFact.JSONLD) {
				derivedIssues = append(derivedIssues, newIssue(pageFact, "answerability", "generic_structured_data_only", "medium", "Page only exposes generic structured data", "Structured data only uses generic schema types like WebPage or Thing."))
			}
			if !hasSchemaCoreFields(pageFact.JSONLD) {
				derivedIssues = append(derivedIssues, newIssue(pageFact, "trust", "schema_missing_core_fields", "high", "Structured data is missing core identity fields", "Schema markup should expose core fields like name, url, and description."))
			}
			if hasSchemaType(pageFact.JSONLD, "WebSite") {
				hasWebsiteSchema = true
			}
			if hasAnySchemaType(pageFact.JSONLD, []string{"Organization", "LocalBusiness", "Person", "Corporation"}) {
				hasOrganizationSchema = true
			}
		}

		if hasInsecureHTTPURL(pageFact.URL) {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "trust", "missing_https", "medium", "Page is not served over HTTPS", "Serve the page over HTTPS to strengthen user and platform trust signals."))
		}

		if isArticleLike && !hasAuthorSignal(pageFact) {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "expertise", "missing_author_signal", "medium", "Page is missing an author signal", "Article-like page does not expose author attribution via metadata or structured data."))
		} else if isArticleLike && hasWeakAuthorSignal(pageFact) {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "expertise", "weak_author_signal", "low", "Page has a weak author signal", "Author attribution looks generic or low-confidence."))
		}

		if isArticleLike && hasJSONLD && !hasArticleLikeJSONLDType(pageFact.JSONLD) {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "answerability", "article_missing_article_schema", "medium", "Article-like page is missing article schema", "Add Article or BlogPosting structured data to the page."))
		}
		if isArticleLike && hasJSONLD && !hasArticlePublisherIdentity(pageFact.JSONLD) {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "expertise", "article_missing_publisher_identity", "high", "Article-like page is missing publisher identity in schema", "Article schema should expose author, publisher, or mainEntityOfPage fields."))
		}
		if isArticleLike && hasPlainAuthorSignal(pageFact) && hasJSONLD && !authorSignalMatchesSchema(pageFact) {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "expertise", "author_signal_not_supported_by_schema", "high", "Visible author signal is not supported by schema", "Author attribution exists in page metadata, but structured data does not support it with matching author or publisher identity."))
		}

		switch {
		case isArticleLike && pageFact.WordCount >= longArticleNoCitationWordCountThreshold && pageFact.ExternalLinks == 0:
			derivedIssues = append(derivedIssues, newIssue(pageFact, "authoritativeness", "long_article_has_no_citations", "high", "Long article has no citations", fmt.Sprintf("Article-like page has %d words and no external citation links.", pageFact.WordCount)))
		case isArticleLike && pageFact.WordCount >= longArticleWeakCitationWordCountThreshold && pageFact.ExternalLinks <= weakExternalCitationMaximumCount:
			derivedIssues = append(derivedIssues, newIssue(pageFact, "authoritativeness", "long_article_has_weak_citations", "high", "Long article has weak citation support", fmt.Sprintf("Article-like page has %d words and only %d external citation link(s).", pageFact.WordCount, pageFact.ExternalLinks)))
		case isArticleLike && pageFact.ExternalLinks < externalCitationMinimumCount:
			derivedIssues = append(derivedIssues, newIssue(pageFact, "authoritativeness", "missing_external_citations", "medium", "Page is missing external citations", "Article-like page does not link to any external sources or references."))
		case isArticleLike && pageFact.WordCount >= strongCitationWordCountThreshold && pageFact.ExternalLinks <= weakExternalCitationMaximumCount:
			derivedIssues = append(derivedIssues, newIssue(pageFact, "authoritativeness", "weak_external_citations", "low", "Page has weak external citation support", fmt.Sprintf("Article-like page has %d external citation link(s) across %d words.", pageFact.ExternalLinks, pageFact.WordCount)))
		}

		if isFAQLikePage(pageFact) && !hasSchemaType(pageFact.JSONLD, "FAQPage") {
			derivedIssues = append(derivedIssues, newIssue(pageFact, "answerability", "faq_like_page_missing_faq_schema", "high", "FAQ-like page is missing FAQ schema", "Page looks like an FAQ but does not expose FAQPage structured data."))
		}

		lowerURL := strings.ToLower(strings.TrimSpace(pageFact.URL))
		if looksLikeAboutPage(lowerURL) {
			hasAboutPage = true
		}
		if looksLikeContactPage(lowerURL) {
			hasContactPage = true
		}
		if looksLikePolicyPage(lowerURL) {
			hasPolicyPage = true
		}
		if hasHomepagePageFact && strings.TrimSpace(pageFact.URL) == strings.TrimSpace(homepagePageFact.URL) && hasJSONLD && hasArticlePublisherIdentity(pageFact.JSONLD) {
			homepageHasPublisherIdentity = true
		}
	}

	if hasSiteIssuePageFact {
		if len(pageFacts) > 0 && float64(ogCoveredPageCount)/float64(len(pageFacts)) < weakOpenGraphCoverageThreshold {
			derivedIssues = append(derivedIssues, newIssue(siteIssuePageFact, "trust", "weak_open_graph_coverage", "high", "Open Graph coverage is weak across the crawl", fmt.Sprintf("Only %d of %d crawled pages expose Open Graph tags.", ogCoveredPageCount, len(pageFacts))))
		}
		if !hasWebsiteSchema {
			derivedIssues = append(derivedIssues, newIssue(siteIssuePageFact, "trust", "missing_website_schema", "high", "Site is missing WebSite schema", "Add WebSite structured data to the site."))
		}
		if !hasOrganizationSchema {
			derivedIssues = append(derivedIssues, newIssue(siteIssuePageFact, "authoritativeness", "missing_org_identity_schema", "high", "Site is missing organization or person identity schema", "Add Organization, LocalBusiness, Person, or similar entity schema."))
		}
		if !hasAboutPage {
			derivedIssues = append(derivedIssues, newIssue(siteIssuePageFact, "experience", "missing_about_page", "medium", "Site is missing an about page", "Add an About page that explains who is behind the site."))
		}
		if !hasContactPage {
			derivedIssues = append(derivedIssues, newIssue(siteIssuePageFact, "experience", "missing_contact_page", "medium", "Site is missing a contact page", "Add a Contact page so visitors and systems can find a contact destination."))
		}
		if !hasPolicyPage {
			derivedIssues = append(derivedIssues, newIssue(siteIssuePageFact, "experience", "missing_policy_page", "low", "Site is missing policy pages", "Add privacy, terms, or similar policy pages to strengthen trust signals."))
		}
		if siteFacts.HasLlmsTxt.Valid && !siteFacts.HasLlmsTxt.Bool {
			derivedIssues = append(derivedIssues, newIssue(siteIssuePageFact, "trust", "missing_llms_txt", "high", "Site is missing an /llms.txt file", "The /llms.txt convention (llmstxt.org) helps AI answer engines discover and cite site content. Add an /llms.txt file at the site root listing key pages in Markdown."))
		}
	}

	if hasHomepagePageFact && !hasOrganizationSchema && !hasAboutPage && !hasContactPage && !homepageHasPublisherIdentity {
		derivedIssues = append(derivedIssues, newIssue(homepagePageFact, "trust", "homepage_missing_org_contact_trust_signals", "high", "Homepage is missing core trust identity signals", "Homepage is missing organization schema, visible about/contact coverage, and publisher identity signals."))
	}

	return derivedIssues
}

func newIssue(pageFact shared.PageFact, bucket string, issueType string, severity string, message string, details string) shared.DerivedIssue {
	return shared.DerivedIssue{
		CrawlPageID: pageFact.ID,
		URL:         pageFact.URL,
		Pillar:      PillarID,
		Bucket:      bucket,
		IssueType:   issueType,
		Severity:    severity,
		Message:     message,
		Details:     details,
	}
}
