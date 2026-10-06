package app

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/textnormalization"
)

type projectServiceRecord struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func newProjectServiceRecord(service sqlc.ProjectService) projectServiceRecord {
	return projectServiceRecord{ID: service.ID.String(), Label: service.Label}
}

type layer4ServiceLabelRequest struct {
	Label string `json:"label"`
}

type layer4ProjectServiceEditorRecord struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Excluded bool   `json:"excluded"`
}

type layer4LocationServicesResponse struct {
	Effective    []string                           `json:"effective"`
	Project      []layer4ProjectServiceEditorRecord `json:"project"`
	LocationOnly []string                           `json:"location_only"`
}

type layer4ServiceOverrideEntry struct {
	ServiceID    *string `json:"service_id"`
	ServiceLabel *string `json:"service_label"`
	Mode         string  `json:"mode"`
}

type layer4PutLocationServicesRequest struct {
	Overrides []layer4ServiceOverrideEntry `json:"overrides"`
}

type layer4LocationOnlyService struct {
	Display string
	Key     string
}

type layer4ServiceReferenceOverride struct {
	ServiceID pgtype.UUID
	Mode      string
}

func layer4ServiceLabelKey(raw string) (string, string, error) {
	if !utf8.ValidString(raw) {
		return "", "", errors.New("service label must be valid utf-8")
	}
	if strings.ContainsRune(raw, 0) {
		return "", "", errors.New("service label must not contain nul")
	}
	display := textnormalization.NormalizeTextDisplay(raw)
	if display == "" {
		return "", "", errors.New("service label must not be empty")
	}
	if len(display) > maxLocationNameBytes {
		return "", "", fmt.Errorf("service label must fit within %d bytes", maxLocationNameBytes)
	}
	return display, textnormalization.NormalizeTextKey(raw), nil
}

func isLayer4UniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isLayer4ForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func (a *App) layer4ProjectUser(w http.ResponseWriter, r *http.Request) (pgtype.UUID, pgtype.UUID, bool) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	if _, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: principal.User.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
		} else {
			serverError(w, r, err)
		}
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	return projectID, principal.User.ID, true
}

func (a *App) layer4Location(w http.ResponseWriter, r *http.Request) (pgtype.UUID, pgtype.UUID, pgtype.UUID, sqlc.GetProjectLocationForUserRow, bool) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, sqlc.GetProjectLocationForUserRow{}, false
	}
	locationID, err := parseUUIDParam(chi.URLParam(r, "locationID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid location id")
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, sqlc.GetProjectLocationForUserRow{}, false
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, sqlc.GetProjectLocationForUserRow{}, false
	}
	location, err := a.Queries.GetProjectLocationForUser(r.Context(), sqlc.GetProjectLocationForUserParams{ID: locationID, ID_2: projectID, UserID: principal.User.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "location not found")
		} else {
			serverError(w, r, err)
		}
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, sqlc.GetProjectLocationForUserRow{}, false
	}
	return projectID, locationID, principal.User.ID, location, true
}

func (a *App) handleListProjectServices(w http.ResponseWriter, r *http.Request) {
	projectID, userID, ok := a.layer4ProjectUser(w, r)
	if !ok {
		return
	}
	rows, err := a.Queries.ListProjectServicesForUser(r.Context(), sqlc.ListProjectServicesForUserParams{ProjectID: projectID, UserID: userID})
	if err != nil {
		serverError(w, r, err)
		return
	}
	records := make([]projectServiceRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, newProjectServiceRecord(row))
	}
	writeJSON(w, http.StatusOK, records)
}

func (a *App) handleCreateProjectService(w http.ResponseWriter, r *http.Request) {
	projectID, userID, ok := a.layer4ProjectUser(w, r)
	if !ok {
		return
	}
	var body layer4ServiceLabelRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	display, key, err := layer4ServiceLabelKey(body.Label)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	service, err := a.Queries.CreateProjectServiceForUser(r.Context(), sqlc.CreateProjectServiceForUserParams{Label: display, NormalizedLabel: key, ProjectID: projectID, UserID: userID})
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeJSONError(w, http.StatusNotFound, "project not found")
		case isLayer4UniqueViolation(err):
			writeJSONError(w, http.StatusUnprocessableEntity, "service already exists")
		default:
			serverError(w, r, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, newProjectServiceRecord(service))
}

func (a *App) handleRenameProjectService(w http.ResponseWriter, r *http.Request) {
	serviceID, err := parseUUIDParam(chi.URLParam(r, "serviceID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	projectID, userID, ok := a.layer4ProjectUser(w, r)
	if !ok {
		return
	}
	var body layer4ServiceLabelRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	display, key, err := layer4ServiceLabelKey(body.Label)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	service, err := a.Queries.RenameProjectServiceForUser(r.Context(), sqlc.RenameProjectServiceForUserParams{Label: display, NormalizedLabel: key, ServiceID: serviceID, ProjectID: projectID, UserID: userID})
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeJSONError(w, http.StatusNotFound, "service not found")
		case isLayer4UniqueViolation(err):
			writeJSONError(w, http.StatusUnprocessableEntity, "service already exists")
		default:
			serverError(w, r, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, newProjectServiceRecord(service))
}

func (a *App) handleDeleteProjectService(w http.ResponseWriter, r *http.Request) {
	serviceID, err := parseUUIDParam(chi.URLParam(r, "serviceID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	projectID, userID, ok := a.layer4ProjectUser(w, r)
	if !ok {
		return
	}
	deleted, err := a.Queries.DeleteProjectServiceForUser(r.Context(), sqlc.DeleteProjectServiceForUserParams{ServiceID: serviceID, ProjectID: projectID, UserID: userID})
	if err != nil {
		serverError(w, r, err)
		return
	}
	if deleted == 0 {
		writeJSONError(w, http.StatusNotFound, "service not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func layer4ValidateServiceOverrides(entries []layer4ServiceOverrideEntry) ([]layer4ServiceReferenceOverride, []layer4LocationOnlyService, error) {
	if entries == nil {
		return nil, nil, errors.New("overrides is required")
	}
	references := make([]layer4ServiceReferenceOverride, 0, len(entries))
	locationOnly := make([]layer4LocationOnlyService, 0, len(entries))
	serviceIDModes := make(map[string]string, len(entries))
	seenLabels := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.Mode != "include" && entry.Mode != "exclude" {
			return nil, nil, errors.New("override mode must be include or exclude")
		}
		hasID := entry.ServiceID != nil
		hasLabel := entry.ServiceLabel != nil
		if hasID == hasLabel {
			return nil, nil, errors.New("exactly one of service_id, service_label must be set")
		}
		if hasID {
			serviceID, err := parseUUIDParam(*entry.ServiceID)
			if err != nil {
				return nil, nil, errors.New("invalid service_id")
			}
			key := serviceID.String()
			if existingMode, ok := serviceIDModes[key]; ok {
				if existingMode == entry.Mode {
					continue
				}
				return nil, nil, fmt.Errorf("conflicting override modes for service_id %s", key)
			}
			serviceIDModes[key] = entry.Mode
			references = append(references, layer4ServiceReferenceOverride{ServiceID: serviceID, Mode: entry.Mode})
			continue
		}
		if entry.Mode != "include" {
			return nil, nil, errors.New("location-only services must use mode include")
		}
		display, key, err := layer4ServiceLabelKey(*entry.ServiceLabel)
		if err != nil {
			return nil, nil, err
		}
		if seenLabels[key] {
			return nil, nil, errors.New("duplicate service override")
		}
		seenLabels[key] = true
		locationOnly = append(locationOnly, layer4LocationOnlyService{Display: display, Key: key})
	}
	return references, locationOnly, nil
}

func (a *App) writeLayer4LocationServices(w http.ResponseWriter, r *http.Request, projectID, locationID, userID pgtype.UUID) {
	editor, err := a.Queries.ListProjectLocationServiceEditorRowsForUser(r.Context(), sqlc.ListProjectLocationServiceEditorRowsForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID})
	if err != nil {
		serverError(w, r, err)
		return
	}
	only, err := a.Queries.ListProjectLocationOnlyServiceLabelsForUser(r.Context(), sqlc.ListProjectLocationOnlyServiceLabelsForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID})
	if err != nil {
		serverError(w, r, err)
		return
	}
	effective, err := a.Queries.ListEffectiveProjectLocationServiceLabelsForUser(r.Context(), sqlc.ListEffectiveProjectLocationServiceLabelsForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID})
	if err != nil {
		serverError(w, r, err)
		return
	}
	response := layer4LocationServicesResponse{
		Effective:    make([]string, 0, len(effective)),
		Project:      make([]layer4ProjectServiceEditorRecord, 0, len(editor)),
		LocationOnly: make([]string, 0, len(only)),
	}
	for _, row := range effective {
		response.Effective = append(response.Effective, row.Label)
	}
	for _, row := range editor {
		response.Project = append(response.Project, layer4ProjectServiceEditorRecord{ID: row.ServiceID.String(), Label: row.Label, Excluded: row.Excluded})
	}
	for _, row := range only {
		response.LocationOnly = append(response.LocationOnly, row.ServiceLabel.String)
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleGetProjectLocationServices(w http.ResponseWriter, r *http.Request) {
	projectID, locationID, userID, _, ok := a.layer4Location(w, r)
	if !ok {
		return
	}
	a.writeLayer4LocationServices(w, r, projectID, locationID, userID)
}

func (a *App) handlePutProjectLocationServices(w http.ResponseWriter, r *http.Request) {
	projectID, locationID, userID, _, ok := a.layer4Location(w, r)
	if !ok {
		return
	}
	var body layer4PutLocationServicesRequest
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	references, locationOnly, err := layer4ValidateServiceOverrides(body.Overrides)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		if _, err := queries.LockProjectLocationForQueryDraftForUser(r.Context(), sqlc.LockProjectLocationForQueryDraftForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "location not found")
			} else {
				serverError(w, r, err)
			}
			return err
		}
		if _, err := queries.DeleteProjectLocationServiceOverridesForUser(r.Context(), sqlc.DeleteProjectLocationServiceOverridesForUserParams{LocationID: locationID, ProjectID: projectID, UserID: userID}); err != nil {
			serverError(w, r, err)
			return err
		}
		for _, reference := range references {
			if _, err := queries.InsertProjectLocationServiceOverrideForUser(r.Context(), sqlc.InsertProjectLocationServiceOverrideForUserParams{ServiceID: reference.ServiceID, Mode: reference.Mode, LocationID: locationID, ProjectID: projectID, UserID: userID}); err != nil {
				if isLayer4ForeignKeyViolation(err) {
					writeJSONError(w, http.StatusUnprocessableEntity, "unknown service")
				} else if isLayer4UniqueViolation(err) {
					writeJSONError(w, http.StatusUnprocessableEntity, "duplicate service override")
				} else {
					serverError(w, r, err)
				}
				return err
			}
		}
		for _, include := range locationOnly {
			if _, err := queries.InsertProjectLocationServiceOverrideForUser(r.Context(), sqlc.InsertProjectLocationServiceOverrideForUserParams{
				ServiceLabel:           pgtype.Text{String: include.Display, Valid: true},
				NormalizedServiceLabel: pgtype.Text{String: include.Key, Valid: true},
				Mode:                   "include",
				LocationID:             locationID,
				ProjectID:              projectID,
				UserID:                 userID,
			}); err != nil {
				if isLayer4UniqueViolation(err) {
					writeJSONError(w, http.StatusUnprocessableEntity, "duplicate service override")
				} else {
					serverError(w, r, err)
				}
				return err
			}
		}
		return nil
	}) {
		return
	}
	a.writeLayer4LocationServices(w, r, projectID, locationID, userID)
}
