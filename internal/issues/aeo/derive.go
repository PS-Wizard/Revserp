package aeo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// DeriveIssues builds AEO issues from persisted crawl facts.
func DeriveIssues(pageFacts []shared.PageFact, _ []shared.LinkFact) []shared.DerivedIssue {
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

func hasMeaningfulOGTags(ogTags []byte) bool {
	trimmedOGTags := bytes.TrimSpace(ogTags)
	if len(trimmedOGTags) == 0 || bytes.Equal(trimmedOGTags, []byte("null")) || bytes.Equal(trimmedOGTags, []byte("{}")) {
		return false
	}
	return true
}

func hasMeaningfulJSONLD(jsonLD []byte) bool {
	trimmedJSONLD := bytes.TrimSpace(jsonLD)
	if len(trimmedJSONLD) == 0 || bytes.Equal(trimmedJSONLD, []byte("null")) || bytes.Equal(trimmedJSONLD, []byte("[]")) {
		return false
	}
	return true
}

func isArticleLikePage(pageFact shared.PageFact) bool {
	if hasArticleLikeJSONLDType(pageFact.JSONLD) {
		return true
	}
	if pageFact.WordCount < authorSignalMinimumWordCount {
		return false
	}
	return hasArticleLikeURLPath(pageFact.URL)
}

func hasAuthorSignal(pageFact shared.PageFact) bool {
	if strings.TrimSpace(pageFact.Author) != "" {
		return true
	}
	var ogTags map[string]string
	if err := json.Unmarshal(pageFact.OGTags, &ogTags); err == nil {
		if strings.TrimSpace(ogTags["og:author"]) != "" || strings.TrimSpace(ogTags["article:author"]) != "" {
			return true
		}
	}
	return false
}

func hasPlainAuthorSignal(pageFact shared.PageFact) bool {
	if strings.TrimSpace(pageFact.Author) != "" {
		return true
	}
	var ogTags map[string]string
	if err := json.Unmarshal(pageFact.OGTags, &ogTags); err == nil {
		return strings.TrimSpace(ogTags["og:author"]) != "" || strings.TrimSpace(ogTags["article:author"]) != ""
	}
	return false
}

func hasWeakAuthorSignal(pageFact shared.PageFact) bool {
	authorValues := []string{strings.TrimSpace(pageFact.Author)}
	var ogTags map[string]string
	if err := json.Unmarshal(pageFact.OGTags, &ogTags); err == nil {
		authorValues = append(authorValues, strings.TrimSpace(ogTags["og:author"]), strings.TrimSpace(ogTags["article:author"]))
	}
	for _, authorValue := range authorValues {
		normalizedAuthorValue := strings.ToLower(strings.TrimSpace(authorValue))
		if normalizedAuthorValue == "" {
			continue
		}
		if _, isGenericAuthor := genericAuthorSignals[normalizedAuthorValue]; isGenericAuthor {
			return true
		}
	}
	return false
}

func hasArticleLikeJSONLDType(jsonLD []byte) bool {
	var parsedJSONLD any
	if err := json.Unmarshal(jsonLD, &parsedJSONLD); err != nil {
		return false
	}
	return hasArticleLikeJSONLDTypeValue(parsedJSONLD)
}

func hasArticleLikeJSONLDTypeValue(value any) bool {
	switch typedValue := value.(type) {
	case map[string]any:
		if rawGraphEntries, ok := typedValue["@graph"].([]any); ok {
			if slices.ContainsFunc(rawGraphEntries, hasArticleLikeJSONLDTypeValue) {
				return true
			}
		}
		return hasArticleLikeSchemaType(typedValue["@type"])
	case []any:
		if slices.ContainsFunc(typedValue, hasArticleLikeJSONLDTypeValue) {
			return true
		}
	}
	return false
}

func hasArticleLikeSchemaType(value any) bool {
	switch typedValue := value.(type) {
	case string:
		return isArticleLikeSchemaTypeName(typedValue)
	case []any:
		for _, entry := range typedValue {
			typeName, ok := entry.(string)
			if ok && isArticleLikeSchemaTypeName(typeName) {
				return true
			}
		}
	}
	return false
}

func isArticleLikeSchemaTypeName(typeName string) bool {
	switch strings.TrimSpace(typeName) {
	case "Article", "BlogPosting", "NewsArticle", "TechArticle":
		return true
	default:
		return false
	}
}

func hasArticleLikeURLPath(pageURL string) bool {
	for _, editorialPathFragment := range []string{"/blog/", "/article/", "/articles/", "/guides/", "/news/"} {
		if strings.Contains(pageURL, editorialPathFragment) {
			return true
		}
	}
	return false
}

func hasInsecureHTTPURL(pageURL string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(pageURL)), "http://")
}

func selectSiteIssuePageFact(pageFacts []shared.PageFact) (shared.PageFact, bool) {
	if len(pageFacts) == 0 {
		return shared.PageFact{}, false
	}
	selectedPageFact := pageFacts[0]
	for _, pageFact := range pageFacts[1:] {
		if pageLooksLikeHomepage(pageFact.URL) && !pageLooksLikeHomepage(selectedPageFact.URL) {
			selectedPageFact = pageFact
			continue
		}
		if pageFact.Depth < selectedPageFact.Depth {
			selectedPageFact = pageFact
		}
	}
	return selectedPageFact, true
}

func selectHomepagePageFact(pageFacts []shared.PageFact) (shared.PageFact, bool) {
	for _, pageFact := range pageFacts {
		if pageLooksLikeHomepage(pageFact.URL) {
			return pageFact, true
		}
	}
	return shared.PageFact{}, false
}

func pageLooksLikeHomepage(pageURL string) bool {
	trimmedPageURL := strings.TrimSpace(strings.ToLower(pageURL))
	return strings.HasSuffix(trimmedPageURL, "/") && countURLPathSegments(trimmedPageURL) == 0
}

func countURLPathSegments(pageURL string) int {
	withoutProtocol := strings.TrimPrefix(strings.TrimPrefix(pageURL, "https://"), "http://")
	pathParts := strings.SplitN(withoutProtocol, "/", 2)
	if len(pathParts) < 2 {
		return 0
	}
	trimmedPath := strings.Trim(pathParts[1], "/")
	if trimmedPath == "" {
		return 0
	}
	return len(strings.Split(trimmedPath, "/"))
}

func looksLikeAboutPage(lowerURL string) bool {
	return strings.Contains(lowerURL, "/about") || strings.Contains(lowerURL, "/company") || strings.Contains(lowerURL, "/team")
}

func looksLikeContactPage(lowerURL string) bool {
	return strings.Contains(lowerURL, "/contact") || strings.Contains(lowerURL, "/support")
}

func looksLikePolicyPage(lowerURL string) bool {
	for _, fragment := range []string{"/privacy", "/terms", "/policy", "/policies"} {
		if strings.Contains(lowerURL, fragment) {
			return true
		}
	}
	return false
}

func hasOnlyGenericStructuredData(jsonLD []byte) bool {
	typeNames := collectSchemaTypeNames(jsonLD)
	if len(typeNames) == 0 {
		return false
	}
	for typeName := range typeNames {
		if _, isGenericType := genericSchemaTypes[typeName]; !isGenericType {
			return false
		}
	}
	return true
}

func hasSchemaType(jsonLD []byte, targetType string) bool {
	_, exists := collectSchemaTypeNames(jsonLD)[targetType]
	return exists
}

func hasAnySchemaType(jsonLD []byte, targetTypes []string) bool {
	typeNames := collectSchemaTypeNames(jsonLD)
	for _, targetType := range targetTypes {
		if _, exists := typeNames[targetType]; exists {
			return true
		}
	}
	return false
}

func hasSchemaCoreFields(jsonLD []byte) bool {
	var parsedJSONLD any
	if err := json.Unmarshal(jsonLD, &parsedJSONLD); err != nil {
		return false
	}
	return hasSchemaCoreFieldsValue(parsedJSONLD)
}

func hasSchemaCoreFieldsValue(value any) bool {
	switch typedValue := value.(type) {
	case map[string]any:
		if hasAnyNonEmptyField(typedValue, "name") && (hasAnyNonEmptyField(typedValue, "url") || hasAnyNonEmptyField(typedValue, "description")) {
			return true
		}
		for _, nestedValue := range typedValue {
			if hasSchemaCoreFieldsValue(nestedValue) {
				return true
			}
		}
	case []any:
		for _, entry := range typedValue {
			if hasSchemaCoreFieldsValue(entry) {
				return true
			}
		}
	}
	return false
}

func hasArticlePublisherIdentity(jsonLD []byte) bool {
	var parsedJSONLD any
	if err := json.Unmarshal(jsonLD, &parsedJSONLD); err != nil {
		return false
	}
	return hasArticlePublisherIdentityValue(parsedJSONLD)
}

func hasArticlePublisherIdentityValue(value any) bool {
	switch typedValue := value.(type) {
	case map[string]any:
		if hasArticleLikeSchemaType(typedValue["@type"]) && (hasAnyNonEmptyField(typedValue, "author") || hasAnyNonEmptyField(typedValue, "publisher") || hasAnyNonEmptyField(typedValue, "mainEntityOfPage")) {
			return true
		}
		for _, nestedValue := range typedValue {
			if hasArticlePublisherIdentityValue(nestedValue) {
				return true
			}
		}
	case []any:
		for _, entry := range typedValue {
			if hasArticlePublisherIdentityValue(entry) {
				return true
			}
		}
	}
	return false
}

func authorSignalMatchesSchema(pageFact shared.PageFact) bool {
	authorNames := collectPlainAuthorNames(pageFact)
	if len(authorNames) == 0 {
		return false
	}
	schemaIdentityNames := collectSchemaIdentityNames(pageFact.JSONLD)
	if len(schemaIdentityNames) == 0 {
		return false
	}
	for authorName := range authorNames {
		if _, exists := schemaIdentityNames[authorName]; exists {
			return true
		}
	}
	return false
}

func collectPlainAuthorNames(pageFact shared.PageFact) map[string]struct{} {
	authorNames := make(map[string]struct{})
	addNormalizedName(authorNames, pageFact.Author)
	var ogTags map[string]string
	if err := json.Unmarshal(pageFact.OGTags, &ogTags); err == nil {
		addNormalizedName(authorNames, ogTags["og:author"])
		addNormalizedName(authorNames, ogTags["article:author"])
	}
	return authorNames
}

func collectSchemaIdentityNames(jsonLD []byte) map[string]struct{} {
	var parsedJSONLD any
	if err := json.Unmarshal(jsonLD, &parsedJSONLD); err != nil {
		return map[string]struct{}{}
	}
	identityNames := make(map[string]struct{})
	collectSchemaIdentityNamesInto(parsedJSONLD, identityNames)
	return identityNames
}

func collectSchemaIdentityNamesInto(value any, identityNames map[string]struct{}) {
	switch typedValue := value.(type) {
	case map[string]any:
		for _, key := range []string{"author", "publisher"} {
			if nestedValue, exists := typedValue[key]; exists {
				collectSchemaNamesFromIdentityValue(nestedValue, identityNames)
			}
		}
		for _, nestedValue := range typedValue {
			collectSchemaIdentityNamesInto(nestedValue, identityNames)
		}
	case []any:
		for _, entry := range typedValue {
			collectSchemaIdentityNamesInto(entry, identityNames)
		}
	}
}

func collectSchemaNamesFromIdentityValue(value any, identityNames map[string]struct{}) {
	switch typedValue := value.(type) {
	case string:
		addNormalizedName(identityNames, typedValue)
	case map[string]any:
		if rawName, exists := typedValue["name"]; exists {
			if name, ok := rawName.(string); ok {
				addNormalizedName(identityNames, name)
			}
		}
		for _, nestedValue := range typedValue {
			collectSchemaNamesFromIdentityValue(nestedValue, identityNames)
		}
	case []any:
		for _, entry := range typedValue {
			collectSchemaNamesFromIdentityValue(entry, identityNames)
		}
	}
}

func addNormalizedName(target map[string]struct{}, value string) {
	normalizedValue := strings.ToLower(strings.TrimSpace(value))
	if normalizedValue == "" {
		return
	}
	target[normalizedValue] = struct{}{}
}

func hasAnyNonEmptyField(value map[string]any, fieldName string) bool {
	rawValue, exists := value[fieldName]
	if !exists {
		return false
	}
	return valueHasContent(rawValue)
}

func valueHasContent(value any) bool {
	switch typedValue := value.(type) {
	case string:
		return strings.TrimSpace(typedValue) != ""
	case map[string]any:
		if rawName, exists := typedValue["name"]; exists {
			if name, ok := rawName.(string); ok && strings.TrimSpace(name) != "" {
				return true
			}
		}
		if rawURL, exists := typedValue["url"]; exists {
			if url, ok := rawURL.(string); ok && strings.TrimSpace(url) != "" {
				return true
			}
		}
		return len(typedValue) > 0
	case []any:
		for _, entry := range typedValue {
			if valueHasContent(entry) {
				return true
			}
		}
	}
	return false
}

func isFAQLikePage(pageFact shared.PageFact) bool {
	if strings.Contains(strings.ToLower(pageFact.URL), "/faq") {
		return true
	}
	questionLikeHeadingCount := countQuestionLikeHeadings(pageFact.HeadingOutline)
	if questionLikeHeadingCount >= faqLikeQuestionHeadingThreshold {
		return true
	}
	return strings.Count(pageFact.VisibleText, "?") >= faqLikeQuestionMarkThreshold
}

func countQuestionLikeHeadings(headingOutline []byte) int {
	var parsedOutline []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(headingOutline, &parsedOutline); err != nil {
		return 0
	}
	questionLikeHeadingCount := 0
	for _, heading := range parsedOutline {
		if strings.Contains(heading.Text, "?") {
			questionLikeHeadingCount++
		}
	}
	return questionLikeHeadingCount
}

func collectSchemaTypeNames(jsonLD []byte) map[string]struct{} {
	var parsedJSONLD any
	if err := json.Unmarshal(jsonLD, &parsedJSONLD); err != nil {
		return map[string]struct{}{}
	}
	typeNames := make(map[string]struct{})
	collectSchemaTypeNamesInto(parsedJSONLD, typeNames)
	return typeNames
}

func collectSchemaTypeNamesInto(value any, typeNames map[string]struct{}) {
	switch typedValue := value.(type) {
	case map[string]any:
		if rawTypeValue, ok := typedValue["@type"]; ok {
			switch typeValue := rawTypeValue.(type) {
			case string:
				typeNames[strings.TrimSpace(typeValue)] = struct{}{}
			case []any:
				for _, entry := range typeValue {
					entryTypeName, ok := entry.(string)
					if ok {
						typeNames[strings.TrimSpace(entryTypeName)] = struct{}{}
					}
				}
			}
		}
		for _, nestedValue := range typedValue {
			collectSchemaTypeNamesInto(nestedValue, typeNames)
		}
	case []any:
		for _, entry := range typedValue {
			collectSchemaTypeNamesInto(entry, typeNames)
		}
	}
}

var genericAuthorSignals = map[string]struct{}{
	"admin":  {},
	"team":   {},
	"staff":  {},
	"editor": {},
}

var genericSchemaTypes = map[string]struct{}{
	"Thing":          {},
	"WebPage":        {},
	"CollectionPage": {},
	"ItemPage":       {},
}
