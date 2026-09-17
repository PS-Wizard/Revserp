package app

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/competitorcrawls"
	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type createCompetitorRequest struct {
	SeedURL string `json:"seed_url"`
	Name    string `json:"name"`
}

type competitorResponse struct {
	ID        string         `json:"id"`
	SeedURL   string         `json:"seed_url"`
	Name      string         `json:"name"`
	CreatedAt string         `json:"created_at"`
	Crawl     *crawlResponse `json:"crawl"`
}

type listCompetitorsResponse struct {
	MaxCompetitors int32                `json:"max_competitors"`
	Competitors    []competitorResponse `json:"competitors"`
}

func (a *App) handleListProjectCompetitors(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User

	featuresRow, err := a.Queries.GetOrganizationFeaturesByProjectID(r.Context(), sqlc.GetOrganizationFeaturesByProjectIDParams{
		ProjectID: projectID,
		UserID:    user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		serverError(w, r, err)
		return
	}

	parentCrawlID, err := resolveCompetitorListParentCrawlID(r, a.Queries, projectID, user.ID)
	if err != nil {
		var paramErr featureParamError
		if errors.As(err, &paramErr) {
			writeJSONError(w, http.StatusBadRequest, paramErr.Error())
			return
		}
		serverError(w, r, err)
		return
	}

	rows, err := a.Queries.ListProjectCompetitorsWithCrawlForUser(r.Context(), sqlc.ListProjectCompetitorsWithCrawlForUserParams{
		ProjectID:     projectID,
		UserID:        user.ID,
		ParentCrawlID: parentCrawlID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	competitors := make([]competitorResponse, 0, len(rows))
	for _, row := range rows {
		item := competitorResponse{
			ID:        row.ID.String(),
			SeedURL:   row.SeedUrl,
			Name:      row.Name,
			CreatedAt: formatTimestamp(row.CreatedAt),
		}
		if row.CrawlID.Valid {
			crawl := newCompetitorCrawlResponse(row)
			item.Crawl = &crawl
		}
		competitors = append(competitors, item)
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, listCompetitorsResponse{
		MaxCompetitors: featuresRow.MaxCompetitors,
		Competitors:    competitors,
	})
}

func (a *App) handleCreateProjectCompetitor(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var requestBody createCompetitorRequest
	if !readJSONOrRespond(w, r, &requestBody) {
		return
	}

	seedURL := strings.TrimSpace(requestBody.SeedURL)
	if seedURL == "" {
		writeJSONError(w, http.StatusBadRequest, "seed_url is required")
		return
	}

	normalizedSeedURL, err := crawler.NormalizeURL(seedURL, nil)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid seed_url")
		return
	}
	if err := crawler.ValidatePublicHost(r.Context(), normalizedSeedURL.Hostname()); err != nil {
		writeJSONError(w, http.StatusBadRequest, "seed_url must not point to a private or internal host")
		return
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User

	project, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		serverError(w, r, err)
		return
	}

	projectBaseURL, err := url.Parse(project.BaseUrl)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "project base_url is invalid")
		return
	}
	if crawler.IsInternalURL(projectBaseURL, normalizedSeedURL) {
		writeJSONError(w, http.StatusBadRequest, "competitor must not share the project host")
		return
	}

	featuresRow, err := a.Queries.GetOrganizationFeaturesByProjectID(r.Context(), sqlc.GetOrganizationFeaturesByProjectIDParams{
		ProjectID: projectID,
		UserID:    user.ID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	count, err := a.Queries.CountProjectCompetitors(r.Context(), projectID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if count >= int64(featuresRow.MaxCompetitors) {
		writeJSONError(w, http.StatusConflict, "competitor_limit_reached")
		return
	}

	name := strings.TrimSpace(requestBody.Name)
	if name == "" {
		name = normalizedSeedURL.Hostname()
	}

	competitor, err := a.Queries.InsertProjectCompetitor(r.Context(), sqlc.InsertProjectCompetitorParams{
		ProjectID: projectID,
		SeedUrl:   normalizedSeedURL.String(),
		Name:      name,
	})
	if err != nil {
		if isProjectCompetitorSeedUniqueViolation(err) {
			writeJSONError(w, http.StatusConflict, "competitor seed_url already exists for this project")
			return
		}
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, competitorResponse{
		ID:        competitor.ID.String(),
		SeedURL:   competitor.SeedUrl,
		Name:      competitor.Name,
		CreatedAt: formatTimestamp(competitor.CreatedAt),
		Crawl:     nil,
	})
}

func (a *App) handleDeleteProjectCompetitor(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	competitorID, err := parseUUIDParam(chi.URLParam(r, "competitorID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid competitor id")
		return
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User

	rowsAffected, err := a.Queries.DeleteProjectCompetitorForUser(r.Context(), sqlc.DeleteProjectCompetitorForUserParams{
		CompetitorID: competitorID,
		UserID:       user.ID,
		ProjectID:    projectID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	if rowsAffected == 0 {
		writeJSONError(w, http.StatusNotFound, "competitor not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleEnqueueProjectCompetitorCrawls(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User

	if _, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		serverError(w, r, err)
		return
	}

	parentCrawlID, err := resolveCompetitorParentCrawlID(r, a.Queries, projectID, user.ID)
	if err != nil {
		var paramErr featureParamError
		if errors.As(err, &paramErr) {
			writeJSONError(w, http.StatusBadRequest, paramErr.Error())
			return
		}
		serverError(w, r, err)
		return
	}
	if !parentCrawlID.Valid {
		writeJSONError(w, http.StatusBadRequest, "a completed project crawl is required")
		return
	}

	createdIDs, err := competitorcrawls.EnqueueMissing(r.Context(), a.Queries, parentCrawlID, user.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	crawls := make([]map[string]string, 0, len(createdIDs))
	for _, id := range createdIDs {
		crawls = append(crawls, map[string]string{"id": id.String()})
	}

	writeJSON(w, http.StatusOK, map[string]any{"crawls": crawls})
}

func resolveCompetitorListParentCrawlID(r *http.Request, queries *sqlc.Queries, projectID, userID pgtype.UUID) (pgtype.UUID, error) {
	parentCrawlID, crawl, err := lookupCompetitorParentCrawl(r, queries, projectID, userID)
	if err != nil || !parentCrawlID.Valid {
		return parentCrawlID, err
	}
	if crawl.Status != "completed" || !crawler.IsManualLikeSource(crawl.Source) {
		return pgtype.UUID{}, nil
	}
	return parentCrawlID, nil
}

func resolveCompetitorParentCrawlID(r *http.Request, queries *sqlc.Queries, projectID, userID pgtype.UUID) (pgtype.UUID, error) {
	parentCrawlID, crawl, err := lookupCompetitorParentCrawl(r, queries, projectID, userID)
	if err != nil || !parentCrawlID.Valid {
		return parentCrawlID, err
	}
	if crawl.Status != "completed" || !crawler.IsManualLikeSource(crawl.Source) {
		return pgtype.UUID{}, featureParamError("invalid crawl id")
	}
	return parentCrawlID, nil
}

func lookupCompetitorParentCrawl(r *http.Request, queries *sqlc.Queries, projectID, userID pgtype.UUID) (pgtype.UUID, sqlc.GetCrawlByIDForUserRow, error) {
	crawlParam := strings.TrimSpace(r.URL.Query().Get("crawl"))
	if crawlParam == "" {
		return pgtype.UUID{}, sqlc.GetCrawlByIDForUserRow{}, nil
	}

	parentCrawlID, err := parseUUIDParam(crawlParam)
	if err != nil {
		return pgtype.UUID{}, sqlc.GetCrawlByIDForUserRow{}, featureParamError("invalid crawl id")
	}

	crawl, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{
		ID:     parentCrawlID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, sqlc.GetCrawlByIDForUserRow{}, featureParamError("invalid crawl id")
		}
		return pgtype.UUID{}, sqlc.GetCrawlByIDForUserRow{}, err
	}
	if crawl.ProjectID != projectID {
		return pgtype.UUID{}, sqlc.GetCrawlByIDForUserRow{}, featureParamError("invalid crawl id")
	}

	return parentCrawlID, crawl, nil
}

func newCompetitorCrawlResponse(row sqlc.ListProjectCompetitorsWithCrawlForUserRow) crawlResponse {
	status := ""
	if row.CrawlStatus.Valid {
		status = row.CrawlStatus.String
	}
	urlsDiscovered := int32(0)
	if row.CrawlUrlsDiscovered.Valid {
		urlsDiscovered = row.CrawlUrlsDiscovered.Int32
	}
	urlsCrawled := int32(0)
	if row.CrawlUrlsCrawled.Valid {
		urlsCrawled = row.CrawlUrlsCrawled.Int32
	}
	maxDepthReached := int32(0)
	if row.CrawlMaxDepthReached.Valid {
		maxDepthReached = row.CrawlMaxDepthReached.Int32
	}
	response := buildCrawlResponse(
		row.CrawlID,
		row.ProjectID,
		status,
		row.CrawlPhase,
		row.CrawlConfigSnapshot,
		urlsDiscovered,
		urlsCrawled,
		maxDepthReached,
		row.CrawlGooglePsiResults,
		row.CrawlHasLlmsTxt,
		row.CrawlSeoScore,
		row.CrawlAeoScore,
		row.CrawlPagespeedScore,
		row.CrawlOverallScore,
		row.CrawlStartedAt,
		row.CrawlCompletedAt,
		row.CrawlCreatedAt,
	)
	if row.CrawlParentCrawlID.Valid {
		response.ParentCrawlID = row.CrawlParentCrawlID.String()
	}
	if row.CrawlCompetitorID.Valid {
		response.CompetitorID = row.CrawlCompetitorID.String()
	}
	response.Source = "competitor"
	return response
}

func isProjectCompetitorSeedUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return false
	}
	return postgresError.Code == "23505" && postgresError.ConstraintName == "project_competitors_project_id_seed_url_key"
}
