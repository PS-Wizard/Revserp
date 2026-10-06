package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Reading the organization allowance must never provision it or call the provider.
func (a *App) handleGetProjectMapsBudget(w http.ResponseWriter, r *http.Request) {
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

	budget, err := a.Queries.GetOrganizationMapsCreditBudget(r.Context(), project.OrganizationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "Maps spending allowance not found")
			return
		}
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newMapsBudgetResponse(budget.RemainingCredits, budget.ReservedCredits, budget.SpentCredits))
}
