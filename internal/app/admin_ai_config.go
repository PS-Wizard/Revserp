package app

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/aiaudit"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type aiConfigResponse struct {
	InternalSystemPrompt     string `json:"internal_system_prompt"`
	ExternalSystemPrompt     string `json:"external_system_prompt"`
	QuestionGenerationPrompt string `json:"question_generation_prompt"`
	UpdatedAt                string `json:"updated_at,omitempty"`
}

type adminAIConfigResponse struct {
	Config  aiConfigResponse `json:"config"`
	Default aiConfigResponse `json:"default"`
}

type putAIConfigRequest struct {
	InternalSystemPrompt     string `json:"internal_system_prompt"`
	ExternalSystemPrompt     string `json:"external_system_prompt"`
	QuestionGenerationPrompt string `json:"question_generation_prompt"`
}

// defaultAIConfig returns the built-in default AI prompt config.
func defaultAIConfig() aiConfigResponse {
	return aiConfigResponse{
		InternalSystemPrompt:     defaultAIContextPrompt(),
		ExternalSystemPrompt:     defaultAIContextPrompt(),
		QuestionGenerationPrompt: aiaudit.DefaultQuestionGenerationPrompt,
	}
}

// defaultAIContextPrompt returns the hardcoded base revserp assistant framing.
func defaultAIContextPrompt() string {
	return "You are Revserp's in-product SEO, AEO, and PageSpeed crawl issue assistant.\n" +
		"The crawl context is background, not the user's instruction. Always answer the latest user message first.\n" +
		"If the latest user message is a greeting, small talk, or a product/meta question, respond naturally and briefly; do not analyze the crawl or recommend fixes unless the user asks.\n" +
		"If the latest user message asks for crawl help, use only the provided crawl context. If context is insufficient, say exactly what is missing.\n" +
		"Avoid generic advice when affected rows include exact current field values. Produce concrete fixes.\n" +
		"Return clean markdown. Be concise. Do not include a long restatement of the selected scope unless it changes the answer.\n"
}

// handleAdminGetAIConfig returns the current AI prompt config with defaults for empty fields.
func (a *App) handleAdminGetAIConfig(w http.ResponseWriter, r *http.Request) {
	row, err := a.Queries.GetAIPromptConfig(r.Context())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, adminAIConfigResponse{
				Config:  defaultAIConfig(),
				Default: defaultAIConfig(),
			})
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

	writeJSON(w, http.StatusOK, adminAIConfigResponse{
		Config:  config,
		Default: defaultAIConfig(),
	})
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

	writeJSON(w, http.StatusOK, adminAIConfigResponse{
		Config:  config,
		Default: defaultAIConfig(),
	})
}

// handleAdminResetAIConfig clears the saved AI config so defaults take effect.
func (a *App) handleAdminResetAIConfig(w http.ResponseWriter, r *http.Request) {
	if err := a.Queries.ResetAIPromptConfig(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, adminAIConfigResponse{
		Config:  defaultAIConfig(),
		Default: defaultAIConfig(),
	})
}
