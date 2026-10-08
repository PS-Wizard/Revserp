package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueengine "github.com/ps-wizard/revserp/internal/issues"
	"github.com/ps-wizard/revserp/internal/issues/shared"
	"github.com/ps-wizard/revserp/internal/websitescope"
)

// locationWebsiteAuditUnsupportedBucketID is the origin-level PageSpeed
// bucket. It cannot be measured for one branch, so it is flagged rather than
// treated as a local score.
const locationWebsiteAuditUnsupportedBucketID = "psi_cwv"

// locationWebsiteAuditAvailableConfig clones one scoring config minus the
// origin-level buckets a branch cannot measure. Remaining pillar weights are
// rescaled to sum to one so the shared scorer averages available buckets only;
// the excluded bucket scores nothing and contributes nothing to its pillar or
// the overall headline.
func locationWebsiteAuditAvailableConfig(config shared.ScoringConfig) shared.ScoringConfig {
	available := config
	available.Pillars = make(map[string]shared.PillarScoringConfig, len(config.Pillars))
	for pillarID, pillar := range config.Pillars {
		availablePillar := pillar
		availablePillar.BucketWeights = make(map[string]float64, len(pillar.BucketWeights))
		for bucketID, weight := range pillar.BucketWeights {
			if bucketID == locationWebsiteAuditUnsupportedBucketID {
				continue
			}
			availablePillar.BucketWeights[bucketID] = weight
		}
		weightSum := 0.0
		for _, weight := range availablePillar.BucketWeights {
			weightSum += weight
		}
		if weightSum > 0 {
			for bucketID, weight := range availablePillar.BucketWeights {
				availablePillar.BucketWeights[bucketID] = weight / weightSum
			}
		}
		available.Pillars[pillarID] = availablePillar
	}
	return available
}

// locationWebsiteAuditMethodChanged reports whether one stored parent score
// snapshot was built with different scoring math than the derived branch
// method. Historical scoring configs are not retained (single active row),
// so the stored snapshot version is the only comparable label.
func locationWebsiteAuditMethodChanged(storedVersion, derivedVersion string) bool {
	if strings.TrimSpace(storedVersion) == "" {
		return false
	}
	return strings.TrimSpace(storedVersion) != strings.TrimSpace(derivedVersion)
}

// locationWebsiteAuditResult is one derived branch audit outcome. State uses
// the contract scope_status literals (ready/no_matching_pages, or empty for an
// unusable scope). A missing snapshot is a state, never a zero.
type locationWebsiteAuditResult struct {
	State              string
	Snapshot           shared.ScoreBreakdownSnapshot
	HasSnapshot        bool
	MatchedPages       int
	EligiblePages      int
	ExcludedIssueTypes []string
	UnsupportedBuckets []string
}

// deriveLocationWebsiteAudit filters stored crawl signals to one branch scope
// and reruns the shared scorer over eligible in-scope pages only. Site-wide
// and origin-level PSI issues are excluded from scoring and reported as gaps.
func deriveLocationWebsiteAudit(pages []shared.CrawlPageSignal, crawlIssues []shared.CrawlIssueSignal, scoringConfig shared.ScoringConfig, scopeURL, match string) locationWebsiteAuditResult {
	result := locationWebsiteAuditResult{
		UnsupportedBuckets: []string{locationWebsiteAuditUnsupportedBucketID},
	}
	if !websitescope.ValidScopeMatch(match) || match == websitescope.ScopeMatchNone || strings.TrimSpace(scopeURL) == "" {
		return result
	}
	matched := make([]shared.CrawlPageSignal, 0, len(pages))
	for _, page := range pages {
		if websitescope.MatchWebsiteScopePage(match, scopeURL, page.URL) {
			matched = append(matched, page)
		}
	}
	result.MatchedPages = len(matched)
	if len(matched) == 0 {
		result.State = "no_matching_pages"
		return result
	}
	eligible := make([]shared.CrawlPageSignal, 0, len(matched))
	for _, page := range matched {
		if shared.IsScoreablePage(page) {
			eligible = append(eligible, page)
		}
	}
	result.EligiblePages = len(eligible)
	if len(eligible) == 0 {
		result.State = "no_matching_pages"
		return result
	}
	filtered := make([]shared.CrawlIssueSignal, 0, len(crawlIssues))
	excluded := make(map[string]struct{})
	for _, issue := range crawlIssues {
		if !shared.IsPageAddressableIssue(issue.IssueType) {
			excluded[issue.IssueType] = struct{}{}
			continue
		}
		if websitescope.MatchWebsiteScopePage(match, scopeURL, issue.URL) {
			filtered = append(filtered, issue)
		}
	}
	for issueType := range excluded {
		result.ExcludedIssueTypes = append(result.ExcludedIssueTypes, issueType)
	}
	sort.Strings(result.ExcludedIssueTypes)
	result.Snapshot = issueengine.BuildScoreBreakdownWithConfig("", eligible, filtered, locationWebsiteAuditAvailableConfig(scoringConfig), nil)
	result.HasSnapshot = true
	result.State = "ready"
	return result
}

type locationWebsiteAuditScores struct {
	Overall   int32 `json:"overall"`
	SEO       int32 `json:"seo"`
	AEO       int32 `json:"aeo"`
	PageSpeed int32 `json:"pagespeed"`
}

type locationWebsiteAuditBreakdownResponse struct {
	CrawlID                string                         `json:"crawl_id"`
	ScopeRevision          int32                          `json:"scope_revision"`
	ScopeURL               string                         `json:"scope_url"`
	ScopeMatch             string                         `json:"scope_match"`
	ScopeStatus            string                         `json:"scope_status"`
	EligiblePages          int                            `json:"eligible_pages"`
	MatchedPages           int                            `json:"matched_pages"`
	ExcludedSitewideIssues []string                       `json:"excluded_sitewide_issue_types"`
	UnsupportedBuckets     []string                       `json:"unsupported_buckets"`
	MethodVersion          string                         `json:"method_version"`
	ScoringConfigChanged   bool                           `json:"scoring_config_changed"`
	Breakdown              *shared.ScoreBreakdownSnapshot `json:"breakdown"`
}

// resolveLocationWebsiteAuditScope selects the current or a pinned immutable
// scope revision. A pinned revision never changes under the reader, so graphs
// cannot silently mix revisions.
func (a *App) resolveLocationWebsiteAuditScope(w http.ResponseWriter, r *http.Request, locationID pgtype.UUID) (locationWebsiteScopeRevision, bool) {
	revisions, err := listLocationWebsiteScopeRevisions(r.Context(), a.DB, locationID)
	if err != nil {
		serverError(w, r, err)
		return locationWebsiteScopeRevision{}, false
	}
	raw := strings.TrimSpace(r.URL.Query().Get("scope_revision"))
	if raw == "" {
		if len(revisions) == 0 {
			return locationWebsiteScopeRevision{}, true
		}
		return revisions[0], true
	}
	rev, err := strconv.Atoi(raw)
	if err != nil || rev <= 0 {
		writeJSONError(w, http.StatusBadRequest, "scope_revision must be a positive integer")
		return locationWebsiteScopeRevision{}, false
	}
	for _, revision := range revisions {
		if revision.Revision == int32(rev) {
			return revision, true
		}
	}
	writeJSONError(w, http.StatusNotFound, "scope revision not found")
	return locationWebsiteScopeRevision{}, false
}

// resolveLocationWebsiteAuditCrawl selects one parent crawl by ?crawl_id or
// the latest completed parent crawl. The crawl must belong to the location's
// project; foreign IDs read as not found.
func (a *App) resolveLocationWebsiteAuditCrawl(w http.ResponseWriter, r *http.Request, projectID, userID pgtype.UUID) (pgtype.UUID, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("crawl_id"))
	if raw != "" {
		crawlID, err := parseUUIDParam(raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
			return pgtype.UUID{}, false
		}
		crawl, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: userID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "crawl not found")
				return pgtype.UUID{}, false
			}
			serverError(w, r, err)
			return pgtype.UUID{}, false
		}
		if crawl.ProjectID != projectID {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return pgtype.UUID{}, false
		}
		return crawlID, true
	}
	latestID, err := a.Queries.GetLatestCompletedCrawlIDForScoreBreakdown(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "no completed crawl found")
			return pgtype.UUID{}, false
		}
		serverError(w, r, err)
		return pgtype.UUID{}, false
	}
	return latestID, true
}

// loadLocationWebsiteAuditSignals loads stored scoring signals for one crawl.
func (a *App) loadLocationWebsiteAuditSignals(w http.ResponseWriter, r *http.Request, crawlID pgtype.UUID) ([]shared.CrawlPageSignal, []shared.CrawlIssueSignal, bool) {
	pageRows, err := a.Queries.ListCrawlPageSignalsForCrawl(r.Context(), crawlID)
	if err != nil {
		serverError(w, r, err)
		return nil, nil, false
	}
	issueRows, err := a.Queries.ListCrawlIssuesForCrawl(r.Context(), crawlID)
	if err != nil {
		serverError(w, r, err)
		return nil, nil, false
	}
	return issueengine.PageSignalsFromRows(pageRows), issueengine.IssueSignalsFromRows(issueRows), true
}

// locationWebsiteAuditScopeURL extracts the usable scope URL from a revision.
// A null current scope or match none means no branch audit, never a parent
// score fallback.
func locationWebsiteAuditScopeURL(scope locationWebsiteScopeRevision) (string, bool) {
	if scope.URL == nil || strings.TrimSpace(*scope.URL) == "" || scope.Match == websitescope.ScopeMatchNone {
		return "", false
	}
	return *scope.URL, true
}

// locationWebsiteAuditStoredMethodChanged compares one crawl's stored parent
// score snapshot version against the derived branch method. A crawl without a
// stored snapshot is too fresh to have drifted; an unreadable snapshot reads
// as changed rather than silently passing.
func locationWebsiteAuditStoredMethodChanged(r *http.Request, a *App, crawlID, userID pgtype.UUID, derivedVersion string) bool {
	stored, err := a.Queries.GetCrawlScoreBreakdownByCrawlForUser(r.Context(), sqlc.GetCrawlScoreBreakdownByCrawlForUserParams{CrawlID: crawlID, UserID: userID})
	if err != nil {
		return false
	}
	var snapshot shared.ScoreBreakdownSnapshot
	if err := json.Unmarshal(stored.BreakdownJson, &snapshot); err != nil {
		return true
	}
	return locationWebsiteAuditMethodChanged(snapshot.ScoringVersion, derivedVersion)
}

// handleGetLocationWebsiteAuditBreakdown returns one derived branch score
// breakdown over stored parent crawl evidence. The breakdown reuses the exact
// parent snapshot shape so shared UI renders it directly.
func (a *App) handleGetLocationWebsiteAuditBreakdown(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	scope, ok := a.resolveLocationWebsiteAuditScope(w, r, location.ID)
	if !ok {
		return
	}
	response := locationWebsiteAuditBreakdownResponse{
		ScopeStatus:            "unconfigured",
		ExcludedSitewideIssues: []string{},
		UnsupportedBuckets:     []string{locationWebsiteAuditUnsupportedBucketID},
	}
	if scopeURL, usable := locationWebsiteAuditScopeURL(scope); usable {
		response.ScopeRevision = scope.Revision
		response.ScopeURL = scopeURL
		response.ScopeMatch = scope.Match
		crawlID, ok := a.resolveLocationWebsiteAuditCrawl(w, r, location.ProjectID, userID)
		if !ok {
			return
		}
		pages, crawlIssues, ok := a.loadLocationWebsiteAuditSignals(w, r, crawlID)
		if !ok {
			return
		}
		config, err := issueengine.LoadActiveScoringConfig(r.Context(), a.Queries)
		if err != nil {
			serverError(w, r, err)
			return
		}
		derived := deriveLocationWebsiteAudit(pages, crawlIssues, config, scopeURL, scope.Match)
		response.ScopeStatus = derived.State
		if derived.HasSnapshot {
			response.MethodVersion = derived.Snapshot.ScoringVersion
			response.ScoringConfigChanged = locationWebsiteAuditStoredMethodChanged(r, a, crawlID, userID, derived.Snapshot.ScoringVersion)
		}
		response.EligiblePages = derived.EligiblePages
		response.MatchedPages = derived.MatchedPages
		response.ExcludedSitewideIssues = derived.ExcludedIssueTypes
		response.UnsupportedBuckets = derived.UnsupportedBuckets
		response.CrawlID = crawlID.String()
		if derived.HasSnapshot {
			snapshot := derived.Snapshot
			response.Breakdown = &snapshot
		}
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, response)
}

// handleListLocationWebsiteAuditIssueURLs returns paginated in-scope affected
// URLs for one grouped issue type, selected by query params. Page-addressable
// rows only; the shape mirrors the parent score-breakdown URL list without
// parent work state.
func (a *App) handleListLocationWebsiteAuditIssueURLs(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	pillar, ok := normalizeIssuePillar(query.Get("pillar"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "pillar must be seo, aeo, or pagespeed")
		return
	}
	bucket := strings.TrimSpace(query.Get("bucket"))
	issueType := strings.TrimSpace(query.Get("issue_type"))
	if bucket == "" || issueType == "" {
		writeJSONError(w, http.StatusBadRequest, "bucket and issue_type are required")
		return
	}
	if !shared.IsPageAddressableIssue(issueType) {
		writeJSONError(w, http.StatusBadRequest, "issue type is site-wide and has no branch page list")
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	scope, ok := a.resolveLocationWebsiteAuditScope(w, r, location.ID)
	if !ok {
		return
	}
	scopeURL, usable := locationWebsiteAuditScopeURL(scope)
	if !usable {
		writeJSONError(w, http.StatusNotFound, "location has no website scope")
		return
	}
	crawlID, ok := a.resolveLocationWebsiteAuditCrawl(w, r, location.ProjectID, userID)
	if !ok {
		return
	}
	rows, err := a.Queries.ListCrawlIssuesForCrawl(r.Context(), crawlID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	seen := make(map[string]scoreBreakdownIssueURLResponse)
	for _, row := range rows {
		if row.Pillar != pillar || row.Bucket != bucket || row.IssueType != issueType {
			continue
		}
		if !websitescope.MatchWebsiteScopePage(scope.Match, scopeURL, row.Url) {
			continue
		}
		if _, exists := seen[row.Url]; exists {
			continue
		}
		entry := scoreBreakdownIssueURLResponse{URL: row.Url, Severity: row.Severity, Message: row.Message, Details: row.Details, IssueID: row.ID.String()}
		if row.CrawlPageID.Valid {
			entry.CrawlPageID = row.CrawlPageID.String()
		}
		seen[row.Url] = entry
	}
	ordered := make([]scoreBreakdownIssueURLResponse, 0, len(seen))
	for _, entry := range seen {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(leftIndex, rightIndex int) bool { return ordered[leftIndex].URL < ordered[rightIndex].URL })
	total := int64(len(ordered))
	start := int(offset)
	if start > len(ordered) {
		start = len(ordered)
	}
	end := start + int(limit)
	if end > len(ordered) {
		end = len(ordered)
	}
	paged := ordered[start:end]
	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"urls":                 paged,
		"work_actions_enabled": false,
		"pagination": paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(paged)),
			Total:  total,
		},
	})
}

type locationWebsiteAuditPageResponse struct {
	CrawlPageID string `json:"crawl_page_id"`
	URL         string `json:"url"`
	Title       string `json:"title"`
}

// handleListLocationWebsiteAuditPages returns paginated stored pages inside
// one branch scope. Every row links to existing parent page detail views.
func (a *App) handleListLocationWebsiteAuditPages(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	scope, ok := a.resolveLocationWebsiteAuditScope(w, r, location.ID)
	if !ok {
		return
	}
	scopeURL, usable := locationWebsiteAuditScopeURL(scope)
	if !usable {
		writeJSONError(w, http.StatusNotFound, "location has no website scope")
		return
	}
	crawlID, ok := a.resolveLocationWebsiteAuditCrawl(w, r, location.ProjectID, userID)
	if !ok {
		return
	}
	pageRows, err := a.DB.Query(r.Context(), `SELECT id, url, title FROM crawl_pages WHERE crawl_id = $1 ORDER BY url ASC`, crawlID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	matched := make([]locationWebsiteAuditPageResponse, 0)
	for pageRows.Next() {
		var id pgtype.UUID
		var pageURL string
		var title pgtype.Text
		if err := pageRows.Scan(&id, &pageURL, &title); err != nil {
			pageRows.Close()
			serverError(w, r, err)
			return
		}
		if !websitescope.MatchWebsiteScopePage(scope.Match, scopeURL, pageURL) {
			continue
		}
		entry := locationWebsiteAuditPageResponse{CrawlPageID: id.String(), URL: pageURL}
		if title.Valid {
			entry.Title = title.String
		}
		matched = append(matched, entry)
	}
	pageRows.Close()
	total := int64(len(matched))
	start := int(offset)
	if start > len(matched) {
		start = len(matched)
	}
	end := start + int(limit)
	if end > len(matched) {
		end = len(matched)
	}
	paged := matched[start:end]
	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"pages": paged,
		"pagination": paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(paged)),
			Total:  total,
		},
	})
}

type locationWebsiteAuditHistoryEntry struct {
	CrawlID       string                      `json:"crawl_id"`
	CompletedAt   string                      `json:"completed_at"`
	ScopeRevision int32                       `json:"scope_revision"`
	ScopeURL      string                      `json:"scope_url"`
	ScopeMatch    string                      `json:"scope_match"`
	ScopeStatus   string                      `json:"scope_status"`
	MethodVersion string                      `json:"method_version"`
	ConfigChanged bool                        `json:"scoring_config_changed"`
	EligiblePages int                         `json:"eligible_pages"`
	MatchedPages  int                         `json:"matched_pages"`
	Scores        *locationWebsiteAuditScores `json:"scores"`
}

// handleGetLocationWebsiteAuditHistory returns derived branch scores across
// completed parent crawls under one pinned scope revision. History never
// mixes revisions: every entry carries the revision it was derived under.
func (a *App) handleGetLocationWebsiteAuditHistory(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	limit := int32(20)
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeJSONError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > 50 {
			n = 50
		}
		limit = int32(n)
	}
	scope, ok := a.resolveLocationWebsiteAuditScope(w, r, location.ID)
	if !ok {
		return
	}
	scopeURL, usable := locationWebsiteAuditScopeURL(scope)
	if !usable {
		setNoStore(w)
		writeJSON(w, http.StatusOK, map[string]any{"crawls": []locationWebsiteAuditHistoryEntry{}})
		return
	}
	completed, err := a.Queries.ListCompletedProjectCrawlScoreBreakdownsForUser(r.Context(), sqlc.ListCompletedProjectCrawlScoreBreakdownsForUserParams{
		ProjectID: location.ProjectID,
		UserID:    userID,
		Limit:     limit,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	config, err := issueengine.LoadActiveScoringConfig(r.Context(), a.Queries)
	if err != nil {
		serverError(w, r, err)
		return
	}
	entries := make([]locationWebsiteAuditHistoryEntry, 0, len(completed))
	for _, crawl := range completed {
		pageRows, err := a.Queries.ListCrawlPageSignalsForCrawl(r.Context(), crawl.CrawlID)
		if err != nil {
			serverError(w, r, err)
			return
		}
		issueRows, err := a.Queries.ListCrawlIssuesForCrawl(r.Context(), crawl.CrawlID)
		if err != nil {
			serverError(w, r, err)
			return
		}
		derived := deriveLocationWebsiteAudit(issueengine.PageSignalsFromRows(pageRows), issueengine.IssueSignalsFromRows(issueRows), config, scopeURL, scope.Match)
		entry := locationWebsiteAuditHistoryEntry{
			CrawlID:       crawl.CrawlID.String(),
			ScopeRevision: scope.Revision,
			ScopeURL:      scopeURL,
			ScopeMatch:    scope.Match,
			ScopeStatus:   derived.State,
			EligiblePages: derived.EligiblePages,
			MatchedPages:  derived.MatchedPages,
		}
		if crawl.CompletedAt.Valid {
			entry.CompletedAt = crawl.CompletedAt.Time.Format("2006-01-02T15:04:05Z")
		}
		if derived.HasSnapshot {
			entry.MethodVersion = derived.Snapshot.ScoringVersion
			var storedSnapshot shared.ScoreBreakdownSnapshot
			if err := json.Unmarshal(crawl.BreakdownJson, &storedSnapshot); err != nil {
				entry.ConfigChanged = true
			} else {
				entry.ConfigChanged = locationWebsiteAuditMethodChanged(storedSnapshot.ScoringVersion, derived.Snapshot.ScoringVersion)
			}
			crawlScores := derived.Snapshot.CrawlScores()
			entry.Scores = &locationWebsiteAuditScores{
				Overall:   crawlScores.OverallScore,
				SEO:       crawlScores.SEOScore,
				AEO:       crawlScores.AEOScore,
				PageSpeed: crawlScores.PageSpeedScore,
			}
		}
		entries = append(entries, entry)
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"crawls": entries})
}
