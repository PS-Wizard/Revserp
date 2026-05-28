package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type createProjectRequest struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
}

type projectResponse struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
	BaseURL        string `json:"base_url"`
}

// handleCreateProject creates a project inside an organization the user belongs to.
func (a *App) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	organizationID, err := parseUUIDParam(chi.URLParam(r, "organizationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}

	var requestBody createProjectRequest
	if err := readJSON(r, &requestBody); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	name := strings.TrimSpace(requestBody.Name)
	baseURL := strings.TrimSpace(requestBody.BaseURL)
	if name == "" || baseURL == "" {
		writeJSONError(w, http.StatusBadRequest, "name and base_url are required")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback(r.Context())

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{OrgID: organizationID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	project, err := queries.CreateProject(r.Context(), sqlc.CreateProjectParams{
		OrganizationID: organizationID,
		Name:           name,
		BaseUrl:        baseURL,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, newProjectResponse(project))
}

// handleListProjects lists projects for an organization the user belongs to.
func (a *App) handleListProjects(w http.ResponseWriter, r *http.Request) {
	organizationID, err := parseUUIDParam(chi.URLParam(r, "organizationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback(r.Context())

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{OrgID: organizationID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	projects, err := queries.ListProjectsForOrganization(r.Context(), organizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	responses := make([]projectResponse, 0, len(projects))
	for _, project := range projects {
		responses = append(responses, newProjectResponse(project))
	}

	writeJSON(w, http.StatusOK, map[string]any{"projects": responses})
}

// handleGetProject returns a project only if the current user belongs to its organization.
func (a *App) handleGetProject(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer tx.Rollback(r.Context())

	queries := a.Queries.WithTx(tx)
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	project, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, newProjectResponse(project))
}

// ensureCurrentUser ensures the authenticated identity has a local user and default organization.
func (a *App) ensureCurrentUser(r *http.Request, queries *sqlc.Queries) (sqlc.User, []sqlc.ListOrganizationsForUserRow, error) {
	identity, ok := internalauth.IdentityFromContext(r.Context())
	if !ok {
		return sqlc.User{}, nil, errors.New("missing identity")
	}

	return a.ensureUserAndOrganizations(r, queries, identity)
}

// newProjectResponse converts a DB project into an API response.
func newProjectResponse(project sqlc.Project) projectResponse {
	return projectResponse{
		ID:             project.ID.String(),
		OrganizationID: project.OrganizationID.String(),
		Name:           project.Name,
		BaseURL:        project.BaseUrl,
	}
}

// parseUUIDParam parses a UUID path parameter.
func parseUUIDParam(value string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(value); err != nil {
		return pgtype.UUID{}, err
	}

	return id, nil
}
