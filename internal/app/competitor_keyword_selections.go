package app

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywordanalysis"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

type competitorSelectionKeyword struct {
	Keyword string `json:"keyword"`
	Kind    string `json:"kind"`
}

type competitorSelectionGroup struct {
	CompetitorID string                       `json:"competitor_id"`
	Keywords     []competitorSelectionKeyword `json:"keywords"`
}

type competitorSelectionsResponse struct {
	Selections []competitorSelectionGroup `json:"selections"`
}

type putCompetitorSelectionRequest struct {
	CompetitorID string `json:"competitor_id"`
	Keyword      string `json:"keyword"`
	Checked      bool   `json:"checked"`
}

type deleteCompetitorSelectionRequest struct {
	CompetitorID string `json:"competitor_id"`
	Keyword      string `json:"keyword"`
}

func newCompetitorSelectionsResponse(rows []sqlc.ProjectCompetitorKeywordSelection) competitorSelectionsResponse {
	response := competitorSelectionsResponse{Selections: []competitorSelectionGroup{}}
	byCompetitor := make(map[string]int, len(rows))
	for _, row := range rows {
		if row.Kind != projectkeywords.ProjectKeywordKindNonBrand {
			continue
		}
		competitorID := row.CompetitorID.String()
		idx, ok := byCompetitor[competitorID]
		if !ok {
			byCompetitor[competitorID] = len(response.Selections)
			response.Selections = append(response.Selections, competitorSelectionGroup{
				CompetitorID: competitorID,
				Keywords:     []competitorSelectionKeyword{},
			})
			idx = len(response.Selections) - 1
		}
		response.Selections[idx].Keywords = append(response.Selections[idx].Keywords, competitorSelectionKeyword{
			Keyword: row.Keyword,
			Kind:    row.Kind,
		})
	}
	return response
}

// handleListCompetitorKeywordSelections returns one project's checked
// competitor keywords grouped by competitor. Only non-brand selections are
// listed. Any project member may read; an empty project returns an empty list.
func (a *App) handleListCompetitorKeywordSelections(w http.ResponseWriter, r *http.Request) {
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

	rows, err := a.Queries.ListCompetitorKeywordSelectionsByProject(r.Context(), projectID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, newCompetitorSelectionsResponse(rows))
}

// handlePutCompetitorKeywordSelection checks (checked=true inserts) or
// unchecks (checked=false deletes) one competitor keyword. Only non-brand
// competitor keywords are selectable: brand phrases are rejected.
func (a *App) handlePutCompetitorKeywordSelection(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var requestBody putCompetitorSelectionRequest
	if !readJSONOrRespond(w, r, &requestBody) {
		return
	}
	competitorID, err := parseUUIDParam(requestBody.CompetitorID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid competitor id")
		return
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := a.Queries.WithTx(tx)

	project, err := queries.GetProjectByIDForUserForBusinessProfileUpdate(r.Context(), sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams{
		ID:     projectID,
		UserID: principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}

	if _, err := queries.GetProjectCompetitorByIDForUser(r.Context(), sqlc.GetProjectCompetitorByIDForUserParams{
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

	if err := requireOrganizationManager(r.Context(), queries, project.OrganizationID, principal.User.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}

	display, err := projectkeywords.ValidateProjectKeywordPhrase(requestBody.Keyword)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	key := projectkeywords.NormalizeProjectKeywordKey(display)

	if !requestBody.Checked {
		if err := queries.DeleteCompetitorKeywordSelection(r.Context(), sqlc.DeleteCompetitorKeywordSelectionParams{
			ProjectID:         projectID,
			CompetitorID:      competitorID,
			NormalizedKeyword: key,
		}); err != nil {
			serverError(w, r, err)
			return
		}
	} else {
		if brand, err := competitorKeywordIsBrand(r.Context(), queries, competitorID, key); err != nil {
			serverError(w, r, err)
			return
		} else if brand {
			writeJSONError(w, http.StatusBadRequest, "only non_brand competitor keywords are selectable")
			return
		}
		if _, err := queries.UpsertCompetitorKeywordSelection(r.Context(), sqlc.UpsertCompetitorKeywordSelectionParams{
			ProjectID:         projectID,
			CompetitorID:      competitorID,
			Keyword:           display,
			NormalizedKeyword: key,
			Kind:              projectkeywords.ProjectKeywordKindNonBrand,
		}); err != nil {
			serverError(w, r, err)
			return
		}
	}

	rows, err := queries.ListCompetitorKeywordSelectionsByProject(r.Context(), projectID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, newCompetitorSelectionsResponse(rows))
}

// handleDeleteCompetitorKeywordSelection unchecks one competitor keyword,
// deleting its row. Missing rows are a no-op success so reloads stay simple.
func (a *App) handleDeleteCompetitorKeywordSelection(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var requestBody deleteCompetitorSelectionRequest
	if !readJSONOrRespond(w, r, &requestBody) {
		return
	}
	competitorID, err := parseUUIDParam(requestBody.CompetitorID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid competitor id")
		return
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := a.Queries.WithTx(tx)

	project, err := queries.GetProjectByIDForUserForBusinessProfileUpdate(r.Context(), sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams{
		ID:     projectID,
		UserID: principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}

	if _, err := queries.GetProjectCompetitorByIDForUser(r.Context(), sqlc.GetProjectCompetitorByIDForUserParams{
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

	if err := requireOrganizationManager(r.Context(), queries, project.OrganizationID, principal.User.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}

	display, err := projectkeywords.ValidateProjectKeywordPhrase(requestBody.Keyword)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := queries.DeleteCompetitorKeywordSelection(r.Context(), sqlc.DeleteCompetitorKeywordSelectionParams{
		ProjectID:         projectID,
		CompetitorID:      competitorID,
		NormalizedKeyword: projectkeywords.NormalizeProjectKeywordKey(display),
	}); err != nil {
		serverError(w, r, err)
		return
	}

	rows, err := queries.ListCompetitorKeywordSelectionsByProject(r.Context(), projectID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, newCompetitorSelectionsResponse(rows))
}

// competitorKeywordIsBrand reports whether the competitor's stored analysis
// classifies the normalized phrase as brand. Phrases without analysis are
// selectable: they store as non_brand.
func competitorKeywordIsBrand(ctx context.Context, queries *sqlc.Queries, competitorID pgtype.UUID, key string) (bool, error) {
	analyses, err := keywordanalysis.ListCompetitorAnalyses(ctx, queries, competitorID)
	if err != nil {
		return false, err
	}
	for _, analysis := range analyses {
		if analysis.Kind != projectkeywords.ProjectKeywordKindBrand {
			continue
		}
		display, err := projectkeywords.ValidateProjectKeywordPhrase(analysis.Keyword)
		if err != nil {
			continue
		}
		if projectkeywords.NormalizeProjectKeywordKey(display) == key {
			return true, nil
		}
	}
	return false, nil
}
