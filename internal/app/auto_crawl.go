package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/schedule"
)

const (
	defaultAutoCrawlFrequencyDays = 1
	defaultAutoCrawlRunAt         = "03:00"
	defaultAutoCrawlTimezone      = "UTC"
	maxAutoCrawlFrequencyDays     = 30
)

type autoCrawlSettingsResponse struct {
	Enabled        bool            `json:"enabled"`
	ConfigSnapshot json.RawMessage `json:"config_snapshot,omitempty"`
	LastEnqueuedAt string          `json:"last_enqueued_at,omitempty"`
	FrequencyDays  int32           `json:"frequency_days"`
	RunAt          string          `json:"run_at"`
	Timezone       string          `json:"timezone"`
	NextRunAt      string          `json:"next_run_at,omitempty"`
	CreatedAt      string          `json:"created_at,omitempty"`
	UpdatedAt      string          `json:"updated_at,omitempty"`
}

type putAutoCrawlSettingsRequest struct {
	Enabled        *bool           `json:"enabled"`
	ConfigSnapshot json.RawMessage `json:"config_snapshot,omitempty"`
	FrequencyDays  *int32          `json:"frequency_days,omitempty"`
	RunAt          *string         `json:"run_at,omitempty"`
	Timezone       *string         `json:"timezone,omitempty"`
}

func parseRunAt(value string) (hour, minute int, err error) {
	if _, err := fmt.Sscanf(value, "%2d:%2d", &hour, &minute); err != nil || len(value) != 5 || value[2] != ':' {
		return 0, 0, fmt.Errorf("run_at must be in HH:MM format")
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("run_at must be a valid time of day")
	}
	return hour, minute, nil
}

func formatRunAt(value pgtype.Time) string {
	if !value.Valid {
		return defaultAutoCrawlRunAt
	}
	totalMinutes := value.Microseconds / 60_000_000
	return fmt.Sprintf("%02d:%02d", totalMinutes/60, totalMinutes%60)
}

func runAtToPgTime(hour, minute int) pgtype.Time {
	return pgtype.Time{
		Microseconds: (int64(hour)*3600 + int64(minute)*60) * 1_000_000,
		Valid:        true,
	}
}

func autoCrawlSettingsToResponse(settings sqlc.ProjectAutoCrawlSetting) autoCrawlSettingsResponse {
	resp := autoCrawlSettingsResponse{
		Enabled:       settings.Enabled,
		FrequencyDays: settings.FrequencyDays,
		RunAt:         formatRunAt(settings.RunAt),
		Timezone:      settings.Timezone,
		CreatedAt:     formatTimestamp(settings.CreatedAt),
		UpdatedAt:     formatTimestamp(settings.UpdatedAt),
	}
	if len(settings.ConfigSnapshot) > 0 {
		resp.ConfigSnapshot = json.RawMessage(settings.ConfigSnapshot)
	}
	if settings.LastEnqueuedAt.Valid {
		resp.LastEnqueuedAt = formatTimestamp(settings.LastEnqueuedAt)
	}
	if settings.NextRunAt.Valid {
		resp.NextRunAt = formatTimestamp(settings.NextRunAt)
	}
	return resp
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
		writeJSON(w, http.StatusOK, autoCrawlSettingsResponse{
			Enabled:       false,
			FrequencyDays: defaultAutoCrawlFrequencyDays,
			RunAt:         defaultAutoCrawlRunAt,
			Timezone:      defaultAutoCrawlTimezone,
		})
		return
	}

	writeJSON(w, http.StatusOK, autoCrawlSettingsToResponse(settings))
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

	if req.FrequencyDays != nil &&
		(*req.FrequencyDays < 1 || *req.FrequencyDays > maxAutoCrawlFrequencyDays) {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("frequency_days must be between 1 and %d", maxAutoCrawlFrequencyDays))
		return
	}
	if req.RunAt != nil {
		if _, _, err := parseRunAt(*req.RunAt); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Timezone != nil {
		if *req.Timezone == "" {
			writeJSONError(w, http.StatusBadRequest, "timezone must not be empty")
			return
		}
		if _, err := time.LoadLocation(*req.Timezone); err != nil {
			writeJSONError(w, http.StatusBadRequest, "timezone must be a valid IANA timezone name")
			return
		}
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

		existing, err := queries.GetProjectAutoCrawlSettings(r.Context(), projectID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			serverError(w, r, err)
			return err
		}

		// Effective schedule: defaults, overlaid by the existing row, overlaid
		// by whatever the request provided.
		frequencyDays := int32(defaultAutoCrawlFrequencyDays)
		runAt := defaultAutoCrawlRunAt
		timezone := defaultAutoCrawlTimezone
		if existing.ProjectID.Valid {
			frequencyDays = existing.FrequencyDays
			runAt = formatRunAt(existing.RunAt)
			timezone = existing.Timezone
		}
		if req.FrequencyDays != nil {
			frequencyDays = *req.FrequencyDays
		}
		if req.RunAt != nil {
			runAt = *req.RunAt
		}
		if req.Timezone != nil {
			timezone = *req.Timezone
		}

		hour, minute, err := parseRunAt(runAt)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return err
		}
		location, err := time.LoadLocation(timezone)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "timezone must be a valid IANA timezone name")
			return err
		}

		// Recompute next_run_at when enabling with a changed (or missing)
		// schedule; otherwise keep the current slot so re-saving an unchanged
		// config mid-cycle doesn't pull the next crawl earlier.
		scheduleChanged := !existing.ProjectID.Valid ||
			existing.FrequencyDays != frequencyDays ||
			formatRunAt(existing.RunAt) != runAt ||
			existing.Timezone != timezone ||
			!existing.Enabled
		nextRunAt := existing.NextRunAt
		if *req.Enabled && (scheduleChanged || !nextRunAt.Valid) {
			nextRunAt = pgtype.Timestamptz{
				Time:  schedule.NextOccurrence(time.Now(), hour, minute, location),
				Valid: true,
			}
		}

		settings, err = queries.UpsertProjectAutoCrawlSettings(r.Context(), sqlc.UpsertProjectAutoCrawlSettingsParams{
			ProjectID:      projectID,
			Enabled:        *req.Enabled,
			ConfigSnapshot: normalizedConfig,
			FrequencyDays:  frequencyDays,
			RunAt:          runAtToPgTime(hour, minute),
			Timezone:       timezone,
			NextRunAt:      nextRunAt,
		})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		return nil
	}) {
		return
	}

	writeJSON(w, http.StatusOK, autoCrawlSettingsToResponse(settings))
}
