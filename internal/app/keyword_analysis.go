package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywordanalysis"
)

type keywordAnalysisResponse struct {
	Keywords []keywordanalysis.Analysis `json:"keywords"`
}

// handleGetProjectKeywordAnalysis returns the stored AI metric estimates for
// one project's keywords. Estimates are model guesses, never measured search
// data; an empty project returns an empty list.
func (a *App) handleGetProjectKeywordAnalysis(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	if _, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: principal.User.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}

	analyses, err := keywordanalysis.ListProjectAnalyses(r.Context(), a.Queries, projectID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, keywordAnalysisResponse{Keywords: analyses})
}

// handleGetLocationKeywordAnalysis returns the stored AI metric estimates
// for one location's keywords. Estimates are model guesses, never measured
// search data; a location without estimates returns an empty list.
func (a *App) handleGetLocationKeywordAnalysis(w http.ResponseWriter, r *http.Request) {
	_, locationID, _, _, ok := a.layer4Location(w, r)
	if !ok {
		return
	}
	analyses, err := keywordanalysis.ListLocationAnalyses(r.Context(), a.Queries, locationID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, keywordAnalysisResponse{Keywords: analyses})
}

// handleGetCompetitorKeywordAnalysis returns the stored competitor keywords
// with their AI metric estimates. Estimates are model guesses grounded in the
// competitor crawl, never measured search data.
func (a *App) handleGetCompetitorKeywordAnalysis(w http.ResponseWriter, r *http.Request) {
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

	if _, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: principal.User.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}

	if _, err := a.Queries.GetProjectCompetitorByIDForUser(r.Context(), sqlc.GetProjectCompetitorByIDForUserParams{
		CompetitorID: competitorID,
		UserID:       principal.User.ID,
		ProjectID:    projectID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "competitor not found")
			return
		}
		serverError(w, r, err)
		return
	}

	analyses, err := keywordanalysis.ListCompetitorAnalyses(r.Context(), a.Queries, competitorID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, keywordAnalysisResponse{Keywords: analyses})
}
