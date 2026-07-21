package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/app/aitools"
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

	var settings sqlc.ProjectAutoCrawlSetting
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		user, _, err := a.ensureCurrentUser(r, queries)
		if err != nil {
			serverError(w, r, err)
			return err
		}

		settings, err = a.applyAutoCrawlSettings(r.Context(), queries, projectID, user.ID, autoCrawlSettingsInput{
			Enabled:        *req.Enabled,
			FrequencyDays:  req.FrequencyDays,
			RunAt:          req.RunAt,
			Timezone:       req.Timezone,
			ConfigSnapshot: req.ConfigSnapshot,
		})
		if err != nil {
			writeAutoCrawlSettingsError(w, r, err)
			return err
		}
		return nil
	}) {
		return
	}

	writeJSON(w, http.StatusOK, autoCrawlSettingsToResponse(settings))
}

// autoCrawlBadRequestError marks a validation failure that maps to HTTP 400.
type autoCrawlBadRequestError struct{ msg string }

func (e *autoCrawlBadRequestError) Error() string { return e.msg }

func autoCrawlBadRequest(msg string) error { return &autoCrawlBadRequestError{msg: msg} }

// errAutoCrawlDefaultConfig is the (effectively unreachable) failure to build
// the default config_snapshot, preserved as a distinct 500 for the HTTP path.
var errAutoCrawlDefaultConfig = errors.New("failed to generate default config_snapshot")

// autoCrawlSettingsInput is the effective auto-crawl request shared by the HTTP
// handler and the AI agent tool. ConfigSnapshot follows the same tri-state
// contract as the HTTP request: nil preserves stored config, "null" resets to
// default, and an object is normalized as provided.
type autoCrawlSettingsInput struct {
	Enabled        bool
	FrequencyDays  *int32
	RunAt          *string
	Timezone       *string
	ConfigSnapshot json.RawMessage
}

// applyAutoCrawlSettings is the shared core for auto-crawl configuration:
// validation, project ownership, next_run_at computation, and the upsert. Both
// the HTTP handler and the AI agent closure call it so behavior stays identical.
func (a *App) applyAutoCrawlSettings(ctx context.Context, queries *sqlc.Queries, projectID, userID pgtype.UUID, in autoCrawlSettingsInput) (sqlc.ProjectAutoCrawlSetting, error) {
	if in.FrequencyDays != nil &&
		(*in.FrequencyDays < 1 || *in.FrequencyDays > maxAutoCrawlFrequencyDays) {
		return sqlc.ProjectAutoCrawlSetting{}, autoCrawlBadRequest(fmt.Sprintf("frequency_days must be between 1 and %d", maxAutoCrawlFrequencyDays))
	}
	if in.RunAt != nil {
		if _, _, err := parseRunAt(*in.RunAt); err != nil {
			return sqlc.ProjectAutoCrawlSetting{}, autoCrawlBadRequest(err.Error())
		}
	}
	if in.Timezone != nil {
		if *in.Timezone == "" {
			return sqlc.ProjectAutoCrawlSetting{}, autoCrawlBadRequest("timezone must not be empty")
		}
		if _, err := time.LoadLocation(*in.Timezone); err != nil {
			return sqlc.ProjectAutoCrawlSetting{}, autoCrawlBadRequest("timezone must be a valid IANA timezone name")
		}
	}

	// Resolve config_snapshot based on what the caller sent:
	//   - Absent (nil): normalizedConfig stays nil. The SQL upsert preserves
	//     existing config on update, or stores NULL on insert.
	//   - JSON null: reset to the default config snapshot.
	//   - Object: normalize and validate as provided.
	var normalizedConfig []byte
	if in.ConfigSnapshot != nil {
		if string(in.ConfigSnapshot) == "null" {
			_, norm, err := crawler.NormalizeConfigSnapshot(nil)
			if err != nil {
				return sqlc.ProjectAutoCrawlSetting{}, errAutoCrawlDefaultConfig
			}
			normalizedConfig = norm
		} else if len(in.ConfigSnapshot) > 0 {
			_, norm, err := crawler.NormalizeConfigSnapshot(in.ConfigSnapshot)
			if err != nil {
				return sqlc.ProjectAutoCrawlSetting{}, autoCrawlBadRequest("invalid config_snapshot: " + err.Error())
			}
			normalizedConfig = norm
		}
	}

	if _, err := queries.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: userID,
	}); err != nil {
		return sqlc.ProjectAutoCrawlSetting{}, err
	}

	existing, err := queries.GetProjectAutoCrawlSettings(ctx, projectID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.ProjectAutoCrawlSetting{}, err
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
	if in.FrequencyDays != nil {
		frequencyDays = *in.FrequencyDays
	}
	if in.RunAt != nil {
		runAt = *in.RunAt
	}
	if in.Timezone != nil {
		timezone = *in.Timezone
	}

	hour, minute, err := parseRunAt(runAt)
	if err != nil {
		return sqlc.ProjectAutoCrawlSetting{}, autoCrawlBadRequest(err.Error())
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return sqlc.ProjectAutoCrawlSetting{}, autoCrawlBadRequest("timezone must be a valid IANA timezone name")
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
	if in.Enabled && (scheduleChanged || !nextRunAt.Valid) {
		nextRunAt = pgtype.Timestamptz{
			Time:  schedule.NextOccurrence(time.Now(), hour, minute, location),
			Valid: true,
		}
	}

	settings, err := queries.UpsertProjectAutoCrawlSettings(ctx, sqlc.UpsertProjectAutoCrawlSettingsParams{
		ProjectID:      projectID,
		Enabled:        in.Enabled,
		ConfigSnapshot: normalizedConfig,
		FrequencyDays:  frequencyDays,
		RunAt:          runAtToPgTime(hour, minute),
		Timezone:       timezone,
		NextRunAt:      nextRunAt,
	})
	if err != nil {
		return sqlc.ProjectAutoCrawlSetting{}, err
	}
	return settings, nil
}

// writeAutoCrawlSettingsError maps applyAutoCrawlSettings errors to the same
// HTTP responses the handler produced before extraction.
func writeAutoCrawlSettingsError(w http.ResponseWriter, r *http.Request, err error) {
	var badReq *autoCrawlBadRequestError
	switch {
	case errors.As(err, &badReq):
		writeJSONError(w, http.StatusBadRequest, badReq.msg)
	case errors.Is(err, pgx.ErrNoRows):
		writeJSONError(w, http.StatusNotFound, "project not found")
	case errors.Is(err, errAutoCrawlDefaultConfig):
		writeJSONError(w, http.StatusInternalServerError, "failed to generate default config_snapshot")
	default:
		serverError(w, r, err)
	}
}

// configureAutoCrawlForAgent runs the authorized auto-crawl configuration path
// for the AI agent's configure_auto_crawl tool, in its own transaction.
func (a *App) configureAutoCrawlForAgent(ctx context.Context, scope aitools.Scope, params aitools.AutoCrawlParams) error {
	in := autoCrawlSettingsInput{
		Enabled:        params.Enabled,
		RunAt:          params.RunAt,
		Timezone:       params.Timezone,
		ConfigSnapshot: params.ConfigSnapshot,
	}
	if params.FrequencyDays != nil {
		frequency := int32(*params.FrequencyDays)
		in.FrequencyDays = &frequency
	}

	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	queries := a.Queries.WithTx(tx)
	if _, err := a.applyAutoCrawlSettings(ctx, queries, scope.ProjectID, scope.UserID, in); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
