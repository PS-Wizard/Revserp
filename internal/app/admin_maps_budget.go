package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type mapsBudgetResponse struct {
	RemainingCredits int64 `json:"remaining_credits"`
	ReservedCredits  int64 `json:"reserved_credits"`
	SpentCredits     int64 `json:"spent_credits"`
	AvailableCredits int64 `json:"available_credits"`
}

type updateMapsBudgetRequest struct {
	RemainingCredits         *int64 `json:"remaining_credits"`
	ExpectedRemainingCredits *int64 `json:"expected_remaining_credits"`
}

func newMapsBudgetResponse(remaining, reserved, spent int64) mapsBudgetResponse {
	return mapsBudgetResponse{RemainingCredits: remaining, ReservedCredits: reserved, SpentCredits: spent, AvailableCredits: remaining - reserved}
}

func readMapsBudgetUpdate(w http.ResponseWriter, r *http.Request) (updateMapsBudgetRequest, bool) {
	var body updateMapsBudgetRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return body, false
	}
	if body.RemainingCredits == nil || body.ExpectedRemainingCredits == nil || *body.RemainingCredits < 0 || *body.RemainingCredits > 1_000_000_000 {
		writeJSONError(w, http.StatusBadRequest, "remaining_credits and expected_remaining_credits are required; new allowance must be 0–1000000000")
		return body, false
	}
	return body, true
}

func (a *App) handleAdminGetMapsBudget(w http.ResponseWriter, r *http.Request) {
	orgID, err := parseUUIDParam(chi.URLParam(r, "orgID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}
	budget, err := a.Queries.GetOrganizationMapsCreditBudget(r.Context(), orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "organization not found")
		} else {
			writeJSONError(w, http.StatusInternalServerError, "failed to load Maps spending allowance")
		}
		return
	}
	writeJSON(w, http.StatusOK, newMapsBudgetResponse(budget.RemainingCredits, budget.ReservedCredits, budget.SpentCredits))
}

func (a *App) handleAdminPutMapsBudget(w http.ResponseWriter, r *http.Request) {
	orgID, err := parseUUIDParam(chi.URLParam(r, "orgID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid organization id")
		return
	}
	body, ok := readMapsBudgetUpdate(w, r)
	if !ok {
		return
	}
	if _, err := a.Queries.GetOrganizationMapsCreditBudget(r.Context(), orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "organization not found")
		} else {
			writeJSONError(w, http.StatusInternalServerError, "failed to load Maps spending allowance")
		}
		return
	}
	budget, err := a.Queries.UpdateOrganizationMapsCreditBudget(r.Context(), sqlc.UpdateOrganizationMapsCreditBudgetParams{OrganizationID: orgID, RemainingCredits: *body.RemainingCredits, ExpectedRemainingCredits: *body.ExpectedRemainingCredits})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "Maps allowance changed; reload before saving")
		} else {
			writeJSONError(w, http.StatusInternalServerError, "failed to save Maps spending allowance")
		}
		return
	}
	writeJSON(w, http.StatusOK, newMapsBudgetResponse(budget.RemainingCredits, budget.ReservedCredits, budget.SpentCredits))
}

func (a *App) handleAdminGetPlatformMapsBudget(w http.ResponseWriter, r *http.Request) {
	budget, err := a.Queries.GetPlatformMapsCreditBudget(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load platform Maps spending allowance")
		return
	}
	writeJSON(w, http.StatusOK, newMapsBudgetResponse(budget.RemainingCredits, budget.ReservedCredits, budget.SpentCredits))
}

func (a *App) handleAdminPutPlatformMapsBudget(w http.ResponseWriter, r *http.Request) {
	body, ok := readMapsBudgetUpdate(w, r)
	if !ok {
		return
	}
	budget, err := a.Queries.UpdatePlatformMapsCreditBudget(r.Context(), sqlc.UpdatePlatformMapsCreditBudgetParams{RemainingCredits: *body.RemainingCredits, ExpectedRemainingCredits: *body.ExpectedRemainingCredits})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "Platform allowance changed; reload before saving")
		} else {
			writeJSONError(w, http.StatusInternalServerError, "failed to save platform Maps spending allowance")
		}
		return
	}
	writeJSON(w, http.StatusOK, newMapsBudgetResponse(budget.RemainingCredits, budget.ReservedCredits, budget.SpentCredits))
}
