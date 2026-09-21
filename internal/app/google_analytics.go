package app

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/ga"
)

const googleAnalyticsReadOnlyScope = "https://www.googleapis.com/auth/analytics.readonly"

type selectProjectGoogleAnalyticsPropertyRequest struct {
	PropertyID string `json:"property_id"`
}

type analyticsPropertyResponse struct {
	PropertyID         string `json:"property_id"`
	DisplayName        string `json:"display_name"`
	AccountDisplayName string `json:"account_display_name,omitempty"`
}

type projectGoogleAnalyticsStatusResponse struct {
	HasGoogleConnection bool                        `json:"has_google_connection"`
	HasAnalyticsScope   bool                        `json:"has_analytics_scope"`
	GoogleConnectionID  string                      `json:"google_connection_id,omitempty"`
	GoogleAccountEmail  string                      `json:"google_account_email,omitempty"`
	GoogleStatus        string                      `json:"google_status,omitempty"`
	NeedsReconnect      bool                        `json:"needs_reconnect"`
	CanManageConnection bool                        `json:"can_manage_connection"`
	Connected           bool                        `json:"connected"`
	SelectedProperty    *analyticsPropertyResponse  `json:"selected_property,omitempty"`
	AvailableProperties []analyticsPropertyResponse `json:"available_properties"`
	TokenError          string                      `json:"token_error,omitempty"`
}

func (a *App) handleProjectGoogleAnalyticsStatus(w http.ResponseWriter, r *http.Request) {
	project, user, ok := a.googleAnalyticsProject(w, r)
	if !ok {
		return
	}
	membership, err := a.Queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{OrgID: project.OrganizationID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	selection, selected, err := getProjectGoogleAnalyticsConnectionByProjectID(r.Context(), a.Queries, project.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	connection, connectedGoogle, err := getGoogleConnectionByOrganizationID(r.Context(), a.Queries, project.OrganizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	response := projectGoogleAnalyticsStatusResponse{HasGoogleConnection: connectedGoogle, CanManageConnection: membership.Role == "owner", AvailableProperties: []analyticsPropertyResponse{}}
	if !connectedGoogle {
		writeJSON(w, http.StatusOK, response)
		return
	}
	if selected {
		response.Connected = true
		response.SelectedProperty = analyticsPropertyResponseFromConnection(selection)
	}
	response.GoogleConnectionID = connection.ID.String()
	response.GoogleAccountEmail = textValue(connection.GoogleAccountEmail)
	response.GoogleStatus = connection.Status
	response.HasAnalyticsScope = hasGoogleScope(connection.Scope, googleAnalyticsReadOnlyScope)
	response.NeedsReconnect = connection.Status == "reauth_required" || !response.HasAnalyticsScope
	if !response.HasAnalyticsScope {
		writeJSON(w, http.StatusOK, response)
		return
	}
	if connection.Status != "active" {
		response.TokenError = textValue(connection.LastError)
		writeJSON(w, http.StatusOK, response)
		return
	}
	connection, token, err := a.ensureFreshGoogleConnection(r.Context(), a.Queries, connection)
	if err != nil {
		response.GoogleStatus = connection.Status
		response.NeedsReconnect = connection.Status == "reauth_required"
		response.TokenError = err.Error()
		writeJSON(w, http.StatusOK, response)
		return
	}
	properties, err := a.GAService.ListProperties(r.Context(), token)
	if err != nil {
		response.TokenError = err.Error()
	} else {
		response.AvailableProperties = rankAnalyticsProperties(project.BaseUrl, project.Name, properties)
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleSelectProjectGoogleAnalyticsProperty(w http.ResponseWriter, r *http.Request) {
	project, _, ok := a.googleAnalyticsOwnerProject(w, r)
	if !ok {
		return
	}
	var request selectProjectGoogleAnalyticsPropertyRequest
	if !readJSONOrRespond(w, r, &request) {
		return
	}
	propertyID := strings.TrimSpace(request.PropertyID)
	if propertyID == "" {
		writeJSONError(w, http.StatusBadRequest, "property_id is required")
		return
	}
	connection, found, err := getGoogleConnectionByOrganizationID(r.Context(), a.Queries, project.OrganizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !found || !hasGoogleScope(connection.Scope, googleAnalyticsReadOnlyScope) {
		writeJSONError(w, http.StatusBadRequest, "google analytics requires reconnect")
		return
	}
	connection, token, err := a.ensureFreshGoogleConnection(r.Context(), a.Queries, connection)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to refresh google connection")
		return
	}
	properties, err := a.GAService.ListProperties(r.Context(), token)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch analytics properties")
		return
	}
	var selected *ga.Property
	for index := range properties {
		if properties[index].PropertyID == propertyID {
			selected = &properties[index]
			break
		}
	}
	if selected == nil {
		writeJSONError(w, http.StatusBadRequest, "selected property is not available for this google account")
		return
	}
	_, err = a.Queries.UpsertProjectGoogleAnalyticsConnection(r.Context(), sqlc.UpsertProjectGoogleAnalyticsConnectionParams{
		ProjectID:           project.ID,
		GoogleConnectionID:  connection.ID,
		PropertyID:          selected.PropertyID,
		PropertyDisplayName: selected.DisplayName,
		AccountDisplayName:  pgText(selected.AccountDisplayName),
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleDisconnectProjectGoogleAnalytics(w http.ResponseWriter, r *http.Request) {
	project, _, ok := a.googleAnalyticsOwnerProject(w, r)
	if !ok {
		return
	}
	deleted, err := a.Queries.DeleteProjectGoogleAnalyticsConnectionByProjectID(r.Context(), project.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if deleted == 0 {
		writeJSONError(w, http.StatusNotFound, "project is not connected to google analytics")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleProjectGoogleAnalyticsOverview(w http.ResponseWriter, r *http.Request) {
	project, _, ok := a.googleAnalyticsProject(w, r)
	if !ok {
		return
	}
	selection, selected, err := getProjectGoogleAnalyticsConnectionByProjectID(r.Context(), a.Queries, project.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !selected {
		writeJSONError(w, http.StatusBadRequest, "project is not connected to google analytics")
		return
	}
	connection, found, err := getGoogleConnectionByOrganizationID(r.Context(), a.Queries, project.OrganizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !found || !hasGoogleScope(connection.Scope, googleAnalyticsReadOnlyScope) {
		writeJSONError(w, http.StatusBadRequest, "google analytics requires reconnect")
		return
	}
	_, token, err := a.ensureFreshGoogleConnection(r.Context(), a.Queries, connection)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to refresh google connection")
		return
	}
	overview, err := a.GAService.FetchOverviewCached(r.Context(), token, project.OrganizationID.String(), selection.PropertyID)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch analytics data")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project_id": project.ID.String(), "property_id": selection.PropertyID, "property_name": selection.PropertyDisplayName, "google_connection": connection.ID.String(), "overview": overview})
}

func (a *App) handleProjectGoogleAnalyticsRealtime(w http.ResponseWriter, r *http.Request) {
	project, _, ok := a.googleAnalyticsProject(w, r)
	if !ok {
		return
	}
	selection, selected, err := getProjectGoogleAnalyticsConnectionByProjectID(r.Context(), a.Queries, project.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !selected {
		writeJSONError(w, http.StatusBadRequest, "project is not connected to google analytics")
		return
	}
	connection, found, err := getGoogleConnectionByOrganizationID(r.Context(), a.Queries, project.OrganizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if !found || !hasGoogleScope(connection.Scope, googleAnalyticsReadOnlyScope) {
		writeJSONError(w, http.StatusBadRequest, "google analytics requires reconnect")
		return
	}
	_, token, err := a.ensureFreshGoogleConnection(r.Context(), a.Queries, connection)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to refresh google connection")
		return
	}
	activeUsers, err := a.GAService.FetchRealtime(r.Context(), token, selection.PropertyID)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch analytics realtime data")
		return
	}
	writeJSON(w, http.StatusOK, map[string]float64{"active_users": activeUsers})
}

func (a *App) googleAnalyticsProject(w http.ResponseWriter, r *http.Request) (sqlc.Project, sqlc.User, bool) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return sqlc.Project{}, sqlc.User{}, false
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return sqlc.Project{}, sqlc.User{}, false
	}
	project, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: principal.User.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
		} else {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
		}
		return sqlc.Project{}, sqlc.User{}, false
	}
	return project, principal.User, true
}

func (a *App) googleAnalyticsOwnerProject(w http.ResponseWriter, r *http.Request) (sqlc.Project, sqlc.User, bool) {
	project, user, ok := a.googleAnalyticsProject(w, r)
	if !ok {
		return sqlc.Project{}, sqlc.User{}, false
	}
	if err := requireOrganizationOwner(r.Context(), a.Queries, project.OrganizationID, user.ID); err != nil {
		writeInvitePermissionError(w, err)
		return sqlc.Project{}, sqlc.User{}, false
	}
	return project, user, true
}

func getProjectGoogleAnalyticsConnectionByProjectID(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID) (sqlc.ProjectGoogleAnalyticsConnection, bool, error) {
	connection, err := queries.GetProjectGoogleAnalyticsConnectionByProjectID(ctx, projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ProjectGoogleAnalyticsConnection{}, false, nil
	}
	if err != nil {
		return sqlc.ProjectGoogleAnalyticsConnection{}, false, err
	}
	return connection, true, nil
}

func hasGoogleScope(scope, wanted string) bool {
	for _, value := range strings.Fields(scope) {
		if value == wanted {
			return true
		}
	}
	return false
}

func analyticsPropertyResponseFromConnection(connection sqlc.ProjectGoogleAnalyticsConnection) *analyticsPropertyResponse {
	return &analyticsPropertyResponse{PropertyID: connection.PropertyID, DisplayName: connection.PropertyDisplayName, AccountDisplayName: textValue(connection.AccountDisplayName)}
}

func rankAnalyticsProperties(projectURL, projectName string, properties []ga.Property) []analyticsPropertyResponse {
	host := strings.ToLower(strings.TrimPrefix(projectHost(projectURL), "www."))
	name := strings.ToLower(strings.TrimSpace(projectName))
	responses := make([]analyticsPropertyResponse, 0, len(properties))
	for _, property := range properties {
		responses = append(responses, analyticsPropertyResponse{PropertyID: property.PropertyID, DisplayName: property.DisplayName, AccountDisplayName: property.AccountDisplayName})
	}
	sort.Slice(responses, func(left, right int) bool {
		leftMatch := analyticsPropertyMatches(responses[left], host, name)
		rightMatch := analyticsPropertyMatches(responses[right], host, name)
		if leftMatch != rightMatch {
			return leftMatch
		}
		if responses[left].AccountDisplayName != responses[right].AccountDisplayName {
			return responses[left].AccountDisplayName < responses[right].AccountDisplayName
		}
		if responses[left].DisplayName != responses[right].DisplayName {
			return responses[left].DisplayName < responses[right].DisplayName
		}
		return responses[left].PropertyID < responses[right].PropertyID
	})
	return responses
}

func projectHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func analyticsPropertyMatches(property analyticsPropertyResponse, host, name string) bool {
	value := strings.ToLower(property.AccountDisplayName + " " + property.DisplayName)
	return (host != "" && strings.Contains(value, host)) || (name != "" && strings.Contains(value, name))
}
