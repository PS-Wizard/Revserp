package app

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/localvisibility"
	"github.com/ps-wizard/revserp/internal/locationsetup"
)

type locationInitializationStepResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type locationInitializationResponse struct {
	Status               string                               `json:"status"`
	Differences          string                               `json:"differences"`
	Steps                []locationInitializationStepResponse `json:"steps"`
	Error                string                               `json:"error,omitempty"`
	EstimatedMapsCredits *int32                               `json:"estimated_maps_credits,omitempty"`
	AIAuditID            string                               `json:"ai_audit_id,omitempty"`
	MapsRunID            string                               `json:"maps_run_id,omitempty"`
}

func newLocationInitializationResponse(setup locationsetup.Setup) locationInitializationResponse {
	status := setup.Status
	if status == "" {
		status = "ready"
	}
	response := locationInitializationResponse{
		Status: status, Differences: setup.Differences, Error: setup.Error,
		EstimatedMapsCredits: setup.EstimatedMapsCredits,
		Steps:                make([]locationInitializationStepResponse, 0, len(locationsetup.OrderedSteps)),
	}
	for _, id := range locationsetup.OrderedSteps {
		stepStatus := setup.Steps[id]
		if stepStatus == "" {
			stepStatus = locationsetup.StepPending
		}
		response.Steps = append(response.Steps, locationInitializationStepResponse{
			ID: id, Status: stepStatus, Reason: setup.Reasons[id],
		})
	}
	if setup.AIAuditID.Valid {
		response.AIAuditID = setup.AIAuditID.String()
	}
	if setup.MapsRunID.Valid {
		response.MapsRunID = setup.MapsRunID.String()
	}
	return response
}

func locationInitializationCreditEstimate(enabledQueries int) (int32, error) {
	if enabledQueries == 0 {
		enabledQueries = localvisibility.MapQueryStarterLimit
	}
	credits, err := localvisibility.ExpectedRunCredits(enabledQueries, localvisibility.GridPointCount)
	return int32(credits), err
}

func (a *App) handleGetLocationInitialization(w http.ResponseWriter, r *http.Request) {
	location, _, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	setup, err := locationsetup.Get(r.Context(), a.DB, location.ProjectID, location.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		serverError(w, r, err)
		return
	}
	response := newLocationInitializationResponse(setup)
	if response.EstimatedMapsCredits == nil {
		var enabled int
		if err := a.DB.QueryRow(r.Context(), `SELECT COUNT(*) FROM project_location_queries
			WHERE location_id = $1 AND kind = 'map' AND enabled = TRUE`, location.ID).Scan(&enabled); err != nil {
			serverError(w, r, err)
			return
		}
		estimate, err := locationInitializationCreditEstimate(enabled)
		if err != nil {
			serverError(w, r, err)
			return
		}
		response.EstimatedMapsCredits = &estimate
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleStartLocationInitialization(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	if err := requireOrganizationManager(r.Context(), a.Queries, location.OrganizationID, userID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}
	var body struct {
		Differences string `json:"differences"`
	}
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	differences, err := locationsetup.NormalizeDifferences(body.Differences)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	_, _, err = locationsetup.StartOrResume(r.Context(), tx, location.ProjectID, location.ID, userID, differences)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	if err := locationsetup.LaunchUnfinished(r.Context(), a.DB, location.ProjectID, location.ID, a.Config.SerperMapsEndpoint); err != nil {
		serverError(w, r, err)
		return
	}
	setup, err := locationsetup.Get(r.Context(), a.DB, location.ProjectID, location.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, newLocationInitializationResponse(setup))
}

func (a *App) handleRegenerateLocationMapsQueries(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	if err := requireOrganizationManager(r.Context(), a.Queries, location.OrganizationID, userID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}
	var hasProfile bool
	if err := a.DB.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM location_business_profiles
		WHERE location_id = $1 AND project_id = $2)`, location.ID, location.ProjectID).Scan(&hasProfile); err != nil {
		serverError(w, r, err)
		return
	}
	if !hasProfile {
		writeJSONError(w, http.StatusBadRequest, "save the location business profile first")
		return
	}
	setup, err := locationsetup.Get(r.Context(), a.DB, location.ProjectID, location.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		serverError(w, r, err)
		return
	}
	if err == nil && setup.Status == locationsetup.StatusRunning && setup.Steps[locationsetup.StepProfile] != locationsetup.StepCompleted {
		writeJSONError(w, http.StatusConflict, "wait for the location business profile to finish")
		return
	}
	if err := locationsetup.RegenerateMapsQueries(r.Context(), a.DB, location.ProjectID, location.ID); err != nil {
		serverError(w, r, err)
		return
	}
	a.handleGetLocationMapsQueriesStatus(w, r)
}

func (a *App) handleGetLocationMapsQueriesStatus(w http.ResponseWriter, r *http.Request) {
	location, _, ok := a.loadLocationWorkspaceLocation(w, r)
	if !ok {
		return
	}
	status, err := locationsetup.GetMapsQueriesStatus(r.Context(), a.DB, location.ProjectID, location.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}{Status: status.Status, Error: status.Error})
}
