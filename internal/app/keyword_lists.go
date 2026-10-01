package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

type projectKeywordListsResponse struct {
	CanManageKeywords bool                              `json:"can_manage_keywords"`
	UserDefined       []projectkeywords.Keyword         `json:"user_defined"`
	RevserpSuggested  []projectkeywords.Keyword         `json:"revserp_suggested"`
	Combined          []projectkeywords.CombinedKeyword `json:"combined"`
}

type addProjectKeywordRequest struct {
	Keyword string `json:"keyword"`
	Kind    string `json:"kind"`
}

func newProjectKeywordListsResponse(lists projectkeywords.KeywordLists, canManage bool) projectKeywordListsResponse {
	response := projectKeywordListsResponse{
		CanManageKeywords: canManage,
		UserDefined:       lists.UserDefined,
		RevserpSuggested:  lists.RevserpSuggested,
		Combined:          lists.Combined,
	}
	if response.UserDefined == nil {
		response.UserDefined = []projectkeywords.Keyword{}
	}
	if response.RevserpSuggested == nil {
		response.RevserpSuggested = []projectkeywords.Keyword{}
	}
	if response.Combined == nil {
		response.Combined = []projectkeywords.CombinedKeyword{}
	}
	return response
}

// handleGetProjectKeywordLists returns one project's user, suggested, and
// combined keyword lists for any project member.
func (a *App) handleGetProjectKeywordLists(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	project, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
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

	membership, err := a.Queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{
		OrgID:  project.OrganizationID,
		UserID: principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		serverError(w, r, err)
		return
	}

	lists, err := projectkeywords.LoadProjectKeywordLists(r.Context(), a.Queries, projectID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, newProjectKeywordListsResponse(lists, membership.Role == "owner"))
}

// handleAddProjectKeyword stores one user phrase, returning the full list
// shape. Same phrase and kind is idempotent. Keyword edits never enqueue
// prompt generation.
func (a *App) handleAddProjectKeyword(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var requestBody addProjectKeywordRequest
	if !readJSONOrRespond(w, r, &requestBody) {
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

	if err := requireOrganizationOwner(r.Context(), queries, project.OrganizationID, principal.User.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}

	if _, _, err := projectkeywords.AddUserProjectKeyword(r.Context(), queries, projectID, requestBody.Keyword, requestBody.Kind); err != nil {
		if errors.Is(err, projectkeywords.ErrProjectKeywordConflict) {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, projectkeywords.ErrProjectKeywordInvalid) || errors.Is(err, projectkeywords.ErrProjectKeywordLimit) {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		serverError(w, r, err)
		return
	}

	lists, err := projectkeywords.LoadProjectKeywordLists(r.Context(), queries, projectID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, newProjectKeywordListsResponse(lists, true))
}

// handleDeleteProjectKeyword deletes one user phrase of this project,
// returning the full list shape. Revserp rows and other projects are 404.
func (a *App) handleDeleteProjectKeyword(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	keywordID, err := parseUUIDParam(chi.URLParam(r, "keywordID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid keyword id")
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

	if err := requireOrganizationOwner(r.Context(), queries, project.OrganizationID, principal.User.ID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}

	if err := projectkeywords.DeleteUserProjectKeyword(r.Context(), queries, projectID, keywordID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(r.Context())
			writeJSONError(w, http.StatusNotFound, "keyword not found")
			return
		}
		serverError(w, r, err)
		return
	}

	lists, err := projectkeywords.LoadProjectKeywordLists(r.Context(), queries, projectID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, newProjectKeywordListsResponse(lists, true))
}
