package app

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/aiaudit"
	"github.com/ps-wizard/revserp/internal/aiprompt"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type aiConfigResponse struct {
	InternalSystemPrompt     string `json:"internal_system_prompt"`
	ExternalSystemPrompt     string `json:"external_system_prompt"`
	QuestionGenerationPrompt string `json:"question_generation_prompt"`
	UpdatedAt                string `json:"updated_at,omitempty"`
}

// adminAIConfigResponse carries the stored audience deltas, the default deltas,
// and the code-owned base prompt. The base is always applied and is read-only
// here: an admin adds workspace instructions, and can never remove the base
// rules for accuracy, safety, and citation.
type adminAIConfigResponse struct {
	Config           aiConfigResponse `json:"config"`
	Default          aiConfigResponse `json:"default"`
	BaseSystemPrompt string           `json:"base_system_prompt"`
}

// newAdminAIConfigResponse pairs a stored config with the defaults and the
// code-owned base prompt, which every response must report.
func newAdminAIConfigResponse(config aiConfigResponse) adminAIConfigResponse {
	return adminAIConfigResponse{
		Config:           config,
		Default:          defaultAIConfig(),
		BaseSystemPrompt: aiprompt.DefaultSystemPrompt,
	}
}

type putAIConfigRequest struct {
	InternalSystemPrompt     string `json:"internal_system_prompt"`
	ExternalSystemPrompt     string `json:"external_system_prompt"`
	QuestionGenerationPrompt string `json:"question_generation_prompt"`
}

// defaultAIConfig returns the built-in default AI prompt config. The audience
// prompts default to empty: the code-owned base prompt always applies, so an
// audience column holds an optional delta and not a complete prompt.
func defaultAIConfig() aiConfigResponse {
	return aiConfigResponse{
		InternalSystemPrompt:     "",
		ExternalSystemPrompt:     "",
		QuestionGenerationPrompt: aiaudit.DefaultQuestionGenerationPrompt,
	}
}

// handleAdminGetAIConfig returns the current AI prompt config with defaults for empty fields.
func (a *App) handleAdminGetAIConfig(w http.ResponseWriter, r *http.Request) {
	row, err := a.Queries.GetAIPromptConfig(r.Context())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, newAdminAIConfigResponse(defaultAIConfig()))
			return
		}
		serverError(w, r, err)
		return
	}

	config := aiConfigResponse{
		InternalSystemPrompt:     row.InternalSystemPrompt,
		ExternalSystemPrompt:     row.ExternalSystemPrompt,
		QuestionGenerationPrompt: row.QuestionGenerationPrompt,
	}
	if row.UpdatedAt.Valid {
		config.UpdatedAt = row.UpdatedAt.Time.Format("2006-01-02T15:04:05Z")
	}

	writeJSON(w, http.StatusOK, newAdminAIConfigResponse(config))
}

// handleAdminPutAIConfig saves the AI prompt config.
func (a *App) handleAdminPutAIConfig(w http.ResponseWriter, r *http.Request) {
	var req putAIConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	userID, err := a.currentUserID(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	row, err := a.Queries.UpsertAIPromptConfig(r.Context(), sqlc.UpsertAIPromptConfigParams{
		InternalSystemPrompt:     req.InternalSystemPrompt,
		ExternalSystemPrompt:     req.ExternalSystemPrompt,
		QuestionGenerationPrompt: req.QuestionGenerationPrompt,
		UpdatedByUserID:          userID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	config := aiConfigResponse{
		InternalSystemPrompt:     row.InternalSystemPrompt,
		ExternalSystemPrompt:     row.ExternalSystemPrompt,
		QuestionGenerationPrompt: row.QuestionGenerationPrompt,
	}
	if row.UpdatedAt.Valid {
		config.UpdatedAt = row.UpdatedAt.Time.Format("2006-01-02T15:04:05Z")
	}

	writeJSON(w, http.StatusOK, newAdminAIConfigResponse(config))
}

// handleAdminResetAIConfig clears the saved AI config so defaults take effect.
func (a *App) handleAdminResetAIConfig(w http.ResponseWriter, r *http.Request) {
	if err := a.Queries.ResetAIPromptConfig(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, newAdminAIConfigResponse(defaultAIConfig()))
}
