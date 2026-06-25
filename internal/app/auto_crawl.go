package app

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type autoCrawlSettingsResponse struct {
	Enabled        bool            `json:"enabled"`
	ConfigSnapshot json.RawMessage `json:"config_snapshot,omitempty"`
	LastEnqueuedAt string          `json:"last_enqueued_at,omitempty"`
	CreatedAt      string          `json:"created_at,omitempty"`
	UpdatedAt      string          `json:"updated_at,omitempty"`
}

type putAutoCrawlSettingsRequest struct {
	Enabled        *bool           `json:"enabled"`
	ConfigSnapshot json.RawMessage `json:"config_snapshot,omitempty"`
}

// handleGetAutoCrawlSettings returns auto-crawl settings for a project.
func (a *App) handleGetAutoCrawlSettings(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var settings sqlc.ProjectAutoCrawlSetting
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		user, _, err := a.ensureCurrentUser(r, queries)
		if err != nil {
			serverError(w, r, err)
			return err
		}

		if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
			ID:     projectID,
			UserID: user.ID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "project not found")
				return err
			}
			serverError(w, r, err)
			return err
		}

		settings, err = queries.GetProjectAutoCrawlSettings(r.Context(), projectID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			serverError(w, r, err)
			return err
		}
		return nil
	}) {
		return
	}

	if !settings.ProjectID.Valid {
		// No settings row exists yet; return clean default.
		writeJSON(w, http.StatusOK, autoCrawlSettingsResponse{Enabled: false})
		return
	}

	resp := autoCrawlSettingsResponse{
		Enabled:   settings.Enabled,
		CreatedAt: formatTimestamp(settings.CreatedAt),
		UpdatedAt: formatTimestamp(settings.UpdatedAt),
	}
	if len(settings.ConfigSnapshot) > 0 {
		resp.ConfigSnapshot = json.RawMessage(settings.ConfigSnapshot)
	}
	if settings.LastEnqueuedAt.Valid {
		resp.LastEnqueuedAt = formatTimestamp(settings.LastEnqueuedAt)
	}

	writeJSON(w, http.StatusOK, resp)
}

// handlePutAutoCrawlSettings updates auto-crawl settings for a project.
func (a *App) handlePutAutoCrawlSettings(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	var req putAutoCrawlSettingsRequest
	if err := readJSON(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if req.Enabled == nil {
		writeJSONError(w, http.StatusBadRequest, "enabled is required")
		return
	}

	// Resolve config_snapshot based on what the caller sent:
	//   - Absent (field omitted): normalizedConfig stays nil. The SQL upsert
	//     preserves existing config on update, or stores NULL on insert.
	//   - JSON null: reset to the default config snapshot.
	//   - Object: normalize and validate as provided.
	var normalizedConfig []byte
	if req.ConfigSnapshot != nil {
		if string(req.ConfigSnapshot) == "null" {
			_, norm, err := crawler.NormalizeConfigSnapshot(nil)
			if err != nil {
				writeJSONError(w, http.StatusInternalServerError, "failed to generate default config_snapshot")
				return
			}
			normalizedConfig = norm
		} else if len(req.ConfigSnapshot) > 0 {
			_, norm, err := crawler.NormalizeConfigSnapshot(req.ConfigSnapshot)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid config_snapshot: "+err.Error())
				return
			}
			normalizedConfig = norm
		}
	}

	var settings sqlc.ProjectAutoCrawlSetting
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		user, _, err := a.ensureCurrentUser(r, queries)
		if err != nil {
			serverError(w, r, err)
			return err
		}

		if _, err := queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
			ID:     projectID,
			UserID: user.ID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "project not found")
				return err
			}
			serverError(w, r, err)
			return err
		}

		settings, err = queries.UpsertProjectAutoCrawlSettings(r.Context(), sqlc.UpsertProjectAutoCrawlSettingsParams{
			ProjectID:      projectID,
			Enabled:        *req.Enabled,
			ConfigSnapshot: normalizedConfig,
		})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		return nil
	}) {
		return
	}

	resp := autoCrawlSettingsResponse{
		Enabled:   settings.Enabled,
		CreatedAt: formatTimestamp(settings.CreatedAt),
		UpdatedAt: formatTimestamp(settings.UpdatedAt),
	}
	if len(settings.ConfigSnapshot) > 0 {
		resp.ConfigSnapshot = json.RawMessage(settings.ConfigSnapshot)
	}
	if settings.LastEnqueuedAt.Valid {
		resp.LastEnqueuedAt = formatTimestamp(settings.LastEnqueuedAt)
	}

	writeJSON(w, http.StatusOK, resp)
}
