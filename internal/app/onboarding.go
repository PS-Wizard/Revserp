package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// completed_at is server-owned: the server sets it on completion.
type onboardingState struct {
	Version     int     `json:"version"`
	Status      string  `json:"status"`
	Checkpoint  string  `json:"checkpoint"`
	ProjectID   *string `json:"project_id,omitempty"`
	CrawlID     *string `json:"crawl_id,omitempty"`
	CompletedAt *string `json:"completed_at,omitempty"`
}

var validOnboardingStatuses = map[string]bool{
	"not_started": true,
	"in_progress": true,
	"skipped":     true,
	"completed":   true,
}

var validOnboardingCheckpoints = map[string]bool{
	"new_project":         true,
	"project_details":     true,
	"waiting_for_results": true,
	"overview":            true,
	"score_cards":         true,
	"audit_navigation":    true,
	"bucket_scores":       true,
	"issue_table":         true,
	"issue_details":       true,
	"revbot":              true,
	"revbot_input":        true,
	"revbot_history":      true,
	"business_profile":    true,
	"integrations":        true,
	"finish":              true,
}

func defaultOnboardingState() onboardingState {
	return onboardingState{Version: 1, Status: "not_started", Checkpoint: "new_project"}
}

func parseOnboardingState(raw []byte) (onboardingState, error) {
	if len(raw) == 0 {
		return defaultOnboardingState(), nil
	}
	var state onboardingState
	if err := json.Unmarshal(raw, &state); err != nil {
		return onboardingState{}, err
	}
	return state, nil
}

func (a *App) handleGetOnboarding(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	raw, err := a.Queries.GetUserOnboardingState(r.Context(), principal.User.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		serverError(w, r, err)
		return
	}
	state, err := parseOnboardingState(raw)
	if err != nil {
		serverError(w, r, err)
		return
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, state)
}

func (a *App) handlePutOnboarding(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	var state onboardingState
	if !readJSONOrRespond(w, r, &state) {
		return
	}
	if !a.validateOnboardingState(w, r, &state, principal.User.ID) {
		return
	}
	state.CompletedAt = nil
	if state.Status == "completed" {
		completedAt, err := resolveOnboardingCompletedAt(r, a.Queries, principal.User.ID)
		if err != nil {
			serverError(w, r, err)
			return
		}
		state.CompletedAt = &completedAt
	}
	raw, err := json.Marshal(state)
	if err != nil {
		serverError(w, r, err)
		return
	}
	stored, err := a.Queries.UpdateUserOnboardingState(r.Context(), sqlc.UpdateUserOnboardingStateParams{
		ID:              principal.User.ID,
		OnboardingState: raw,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	canonical, err := parseOnboardingState(stored)
	if err != nil {
		serverError(w, r, err)
		return
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, canonical)
}

func (a *App) validateOnboardingState(w http.ResponseWriter, r *http.Request, state *onboardingState, userID pgtype.UUID) bool {
	if state.Version != 1 {
		writeJSONError(w, http.StatusBadRequest, "unsupported onboarding version")
		return false
	}
	if !validOnboardingStatuses[state.Status] {
		writeJSONError(w, http.StatusBadRequest, "invalid onboarding status")
		return false
	}
	if state.Checkpoint == "" || !validOnboardingCheckpoints[state.Checkpoint] {
		writeJSONError(w, http.StatusBadRequest, "invalid onboarding checkpoint")
		return false
	}
	var projectID pgtype.UUID
	if state.ProjectID != nil && strings.TrimSpace(*state.ProjectID) != "" {
		id, err := parseUUIDParam(strings.TrimSpace(*state.ProjectID))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid project id")
			return false
		}
		if _, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: id, UserID: userID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "project not found")
				return false
			}
			serverError(w, r, err)
			return false
		}
		projectID = id
		normalized := id.String()
		state.ProjectID = &normalized
	} else {
		state.ProjectID = nil
	}
	if state.CrawlID != nil && strings.TrimSpace(*state.CrawlID) != "" {
		if !projectID.Valid {
			writeJSONError(w, http.StatusBadRequest, "project id is required with crawl id")
			return false
		}
		id, err := parseUUIDParam(strings.TrimSpace(*state.CrawlID))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
			return false
		}
		crawl, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: id, UserID: userID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "crawl not found")
				return false
			}
			serverError(w, r, err)
			return false
		}
		if crawl.ProjectID != projectID {
			writeJSONError(w, http.StatusBadRequest, "crawl does not belong to project")
			return false
		}
		normalized := id.String()
		state.CrawlID = &normalized
	} else {
		state.CrawlID = nil
	}
	return true
}

func resolveOnboardingCompletedAt(r *http.Request, queries *sqlc.Queries, userID pgtype.UUID) (string, error) {
	raw, err := queries.GetUserOnboardingState(r.Context(), userID)
	if err != nil {
		return "", err
	}
	current, err := parseOnboardingState(raw)
	if err != nil {
		return "", err
	}
	if current.Status == "completed" && current.CompletedAt != nil {
		if _, err := time.Parse(time.RFC3339, *current.CompletedAt); err == nil {
			return *current.CompletedAt, nil
		}
		return "", errors.New("stored onboarding completed_at is not RFC3339")
	}
	return time.Now().UTC().Format(time.RFC3339), nil
}
