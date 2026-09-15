package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/competitorgaps"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func (a *App) handleGetCompetitorGap(w http.ResponseWriter, r *http.Request) {
	_, report, ok := a.loadCompetitorGapReport(w, r)
	if !ok {
		return
	}
	// Snapshots rebuild when ReportVersion changes, so this must not be
	// cached as immutable the way a completed crawl's score-breakdown is.
	setNoStore(w)
	writeJSON(w, http.StatusOK, report)
}

func (a *App) handleListCompetitorGapIssueURLs(w http.ResponseWriter, r *http.Request) {
	crawl, report, ok := a.loadCompetitorGapReport(w, r)
	if !ok {
		return
	}

	side := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "side")))
	if side != "you" && side != "them" {
		writeJSONError(w, http.StatusBadRequest, "side must be you or them")
		return
	}
	pillar, ok := normalizeIssuePillar(chi.URLParam(r, "pillar"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "pillar must be seo, aeo, or pagespeed")
		return
	}
	bucket := strings.TrimSpace(chi.URLParam(r, "bucket"))
	issueType := strings.TrimSpace(chi.URLParam(r, "issueType"))
	if bucket == "" || issueType == "" {
		writeJSONError(w, http.StatusBadRequest, "bucket and issue type are required")
		return
	}
	if issueType == "weak_open_graph_coverage" {
		setNoStore(w)
		writeJSON(w, http.StatusOK, map[string]any{
			"urls":                 []scoreBreakdownIssueURLResponse{},
			"work_actions_enabled": false,
			"pagination": paginationResponse{
				Limit:  defaultPaginationLimit,
				Offset: 0,
				Count:  0,
				Total:  0,
			},
		})
		return
	}
	workStatus, ok := normalizeScoreBreakdownWorkStatus(r.URL.Query().Get("work_status"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "work_status must be all, needs_action, or marked_done")
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	issuesCrawlID := crawl.ID
	if side == "you" {
		gapCtx, ctxErr := a.Queries.GetCrawlGapContext(r.Context(), crawl.ID)
		if ctxErr != nil {
			if errors.Is(ctxErr, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "crawl not found")
				return
			}
			serverError(w, r, ctxErr)
			return
		}
		if !gapCtx.ParentCrawlID.Valid {
			writeJSONError(w, http.StatusBadRequest, "parent crawl is missing or not completed")
			return
		}
		issuesCrawlID = gapCtx.ParentCrawlID
	}

	sliceKeys := report.SliceURLSet(side)
	issueURLs, err := a.Queries.ListDistinctCrawlIssueURLsByTypeForCrawlByUser(r.Context(), sqlc.ListDistinctCrawlIssueURLsByTypeForCrawlByUserParams{
		CrawlID:    issuesCrawlID,
		UserID:     principal.User.ID,
		Pillar:     pillar,
		Bucket:     bucket,
		IssueType:  issueType,
		WorkStatus: workStatus,
		Limit:      10000,
		Offset:     0,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	filtered := make([]scoreBreakdownIssueURLResponse, 0, len(issueURLs))
	keepOutsideSlice := shared.IsSitewideIssue(issueType) || shared.IsGooglePSIOriginIssue(issueType)
	for _, issueURL := range issueURLs {
		if !keepOutsideSlice {
			if _, inSlice := sliceKeys[competitorgaps.NormalizeIssueURL(issueURL.Url)]; !inSlice {
				continue
			}
		}
		response := scoreBreakdownIssueURLResponse{
			URL:      issueURL.Url,
			Severity: issueURL.Severity,
			Message:  issueURL.Message,
			Details:  issueURL.Details,
			IssueID:  issueURL.IssueID.String(),
		}
		if issueURL.CrawlPageID.Valid {
			response.CrawlPageID = issueURL.CrawlPageID.String()
		}
		filtered = append(filtered, response)
	}

	total := int64(len(filtered))
	page := paginateIssueURLs(filtered, offset, limit)

	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"urls":                 page,
		"work_actions_enabled": false,
		"pagination": paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(page)),
			Total:  total,
		},
	})
}

func paginateIssueURLs(rows []scoreBreakdownIssueURLResponse, offset, limit int32) []scoreBreakdownIssueURLResponse {
	if offset >= int32(len(rows)) {
		return []scoreBreakdownIssueURLResponse{}
	}
	start := int(offset)
	end := start + int(limit)
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end]
}

func (a *App) loadCompetitorGapReport(w http.ResponseWriter, r *http.Request) (sqlc.GetCrawlByIDForUserRow, competitorgaps.Report, bool) {
	var zero sqlc.GetCrawlByIDForUserRow
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return zero, competitorgaps.Report{}, false
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return zero, competitorgaps.Report{}, false
	}

	crawl, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: principal.User.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return zero, competitorgaps.Report{}, false
		}
		serverError(w, r, err)
		return zero, competitorgaps.Report{}, false
	}
	if crawl.Source != "competitor" {
		writeJSONError(w, http.StatusNotFound, "crawl not found")
		return zero, competitorgaps.Report{}, false
	}

	featuresRow, err := a.Queries.GetOrganizationFeaturesByProjectID(r.Context(), sqlc.GetOrganizationFeaturesByProjectIDParams{
		ProjectID: crawl.ProjectID,
		UserID:    principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return zero, competitorgaps.Report{}, false
		}
		serverError(w, r, err)
		return zero, competitorgaps.Report{}, false
	}
	if !featuresFromRow(featuresRow.AutoCrawl, featuresRow.GscConnector, featuresRow.AiChat, featuresRow.Integrations, featuresRow.AiUseInternalPrompt, featuresRow.AiMonthlyMessageLimit, featuresRow.AiConcurrentTurnLimitPerUser, featuresRow.MaxCompetitors, featuresRow.AiAllowedReasoningEfforts).Enabled(FeatureCompetitors) {
		writeJSONError(w, http.StatusForbidden, "feature not enabled for this workspace")
		return zero, competitorgaps.Report{}, false
	}

	if crawl.Status != string(CrawlStatusCompleted) {
		writeJSONError(w, http.StatusBadRequest, "competitor crawl is not completed")
		return zero, competitorgaps.Report{}, false
	}

	report, err := competitorgaps.Load(r.Context(), a.Queries, crawlID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		serverError(w, r, err)
		return zero, competitorgaps.Report{}, false
	}
	if errors.Is(err, pgx.ErrNoRows) || report.Version != competitorgaps.ReportVersion {
		gapCtx, ctxErr := a.Queries.GetCrawlGapContext(r.Context(), crawlID)
		if ctxErr != nil {
			if errors.Is(ctxErr, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "crawl not found")
				return zero, competitorgaps.Report{}, false
			}
			serverError(w, r, ctxErr)
			return zero, competitorgaps.Report{}, false
		}
		if !gapCtx.ParentCrawlID.Valid {
			writeJSONError(w, http.StatusBadRequest, "parent crawl is missing or not completed")
			return zero, competitorgaps.Report{}, false
		}
		report, err = competitorgaps.BuildAndSave(r.Context(), a.Queries, gapCtx.ParentCrawlID, crawlID)
		if err != nil {
			if errors.Is(err, competitorgaps.ErrParentNotReady) || errors.Is(err, competitorgaps.ErrParentMismatch) {
				writeJSONError(w, http.StatusBadRequest, "parent crawl is missing or not completed")
				return zero, competitorgaps.Report{}, false
			}
			if errors.Is(err, competitorgaps.ErrNotCompetitorCrawl) {
				writeJSONError(w, http.StatusNotFound, "crawl not found")
				return zero, competitorgaps.Report{}, false
			}
			serverError(w, r, err)
			return zero, competitorgaps.Report{}, false
		}
	}

	return crawl, report, true
}
