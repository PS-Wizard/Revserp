package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// handleAdminListOrgProjects lists projects for an organization (admin-only, no membership check).
func (a *App) handleAdminListOrgProjects(w http.ResponseWriter, r *http.Request) {
	orgID, err := parseUUIDParam(chi.URLParam(r, "orgID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}

	projects, err := a.Queries.ListProjectsForOrganization(r.Context(), orgID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	responses := make([]projectResponse, 0, len(projects))
	for _, project := range projects {
		responses = append(responses, newProjectResponse(project))
	}

	writeJSON(w, http.StatusOK, map[string]any{"projects": responses})
}

// handleAdminListProjectCrawls lists crawls for a project (admin-only, no membership check).
func (a *App) handleAdminListProjectCrawls(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	statusFilter, err := parseCrawlStatusFilter(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	total, err := a.Queries.CountCrawlsForProject(r.Context(), sqlc.CountCrawlsForProjectParams{
		ProjectID: projectID,
		Column2:   statusFilter,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	crawls, err := a.Queries.ListCrawlsForProject(r.Context(), sqlc.ListCrawlsForProjectParams{
		ProjectID: projectID,
		Column2:   statusFilter,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	responses := make([]crawlResponse, 0, len(crawls))
	for _, crawl := range crawls {
		responses = append(responses, newCrawlResponseFromListRow(crawl))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"crawls": responses,
		"pagination": paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(responses)),
			Total:  total,
		},
	})
}

// handleAdminGetCrawlScoreBreakdown returns one crawl score breakdown for platform admins.
func (a *App) handleAdminGetCrawlScoreBreakdown(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	breakdown, err := a.Queries.GetCrawlScoreBreakdownByCrawl(r.Context(), crawlID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl score breakdown not found")
			return
		}
		serverError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(breakdown.BreakdownJson)
}
