package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/crawler"
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
	if !readJSONOrRespond(w, r, &requestBody) {
		return
	}

	name := strings.TrimSpace(requestBody.Name)
	baseURL := strings.TrimSpace(requestBody.BaseURL)
	if name == "" || baseURL == "" {
		writeJSONError(w, http.StatusBadRequest, "name and base_url are required")
		return
	}

	normalizedBaseURL, err := crawler.NormalizeURL(baseURL, nil)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid base_url")
		return
	}
	if err := crawler.ValidatePublicHost(r.Context(), normalizedBaseURL.Hostname()); err != nil {
		writeJSONError(w, http.StatusBadRequest, "base_url must not point to a private or internal host")
		return
	}

	var project sqlc.Project
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		principal, ok := a.getPrincipal(w, r)

		if !ok {

			return errors.New("missing principal")

		}
		user := principal.User
		if _, err := queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{OrgID: organizationID, UserID: user.ID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusForbidden, "forbidden")
				return err
			}
			serverError(w, r, err)
			return err
		}

		project, err = queries.CreateProject(r.Context(), sqlc.CreateProjectParams{
			OrganizationID: organizationID,
			Name:           name,
			BaseUrl:        baseURL,
		})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		return nil
	}) {
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

	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}

	user := principal.User
	if _, err := a.Queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{OrgID: organizationID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		serverError(w, r, err)
		return
	}

	projects, err := a.Queries.ListProjectsForOrganization(r.Context(), organizationID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	responses := make([]projectResponse, 0, len(projects))
	for _, project := range projects {
		responses = append(responses, newProjectResponse(project))
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"projects": responses})
}

// handleGetProject returns a project only if the current user belongs to its organization.
func (a *App) handleGetProject(w http.ResponseWriter, r *http.Request) {
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
	project, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, newProjectResponse(project))
}

// handleDeleteProject deletes a project only if the current user belongs to its organization.
func (a *App) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		principal, ok := a.getPrincipal(w, r)

		if !ok {

			return errors.New("missing principal")

		}
		user := principal.User
		hasActiveCrawl, err := queries.HasActiveCrawlForProject(r.Context(), sqlc.HasActiveCrawlForProjectParams{
			ProjectID: projectID,
			UserID:    user.ID,
		})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		if hasActiveCrawl {
			writeJSONError(w, http.StatusConflict, "cannot delete project while a crawl is queued or running")
			return errors.New("active crawl exists")
		}

		deletedRows, err := queries.DeleteProjectByIDForUser(r.Context(), sqlc.DeleteProjectByIDForUserParams{
			ID:     projectID,
			UserID: user.ID,
		})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		if deletedRows == 0 {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return errors.New("project not found")
		}
		return nil
	}) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// cachedUserContextKey is a private context key for storing just the user row early in the
// middleware chain (before organizations are loaded).
type cachedUserContextKey struct{}

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
