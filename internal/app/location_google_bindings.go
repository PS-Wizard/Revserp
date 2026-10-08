package app

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const (
	locationGoogleModeInherit = "inherit"
	locationGoogleModeOff     = "off"
	locationGoogleModeCustom  = "custom"
)

type putLocationGoogleBindingRequest struct {
	Mode               string `json:"mode"`
	GoogleConnectionID string `json:"google_connection_id"`
	SiteURL            string `json:"site_url"`
	PropertyID         string `json:"property_id"`
}

type locationGSCBinding struct {
	LocationID         pgtype.UUID
	Mode               string
	GoogleConnectionID pgtype.UUID
	SiteURL            pgtype.Text
	PermissionLevel    pgtype.Text
}

type locationGoogleAnalyticsBinding struct {
	LocationID          pgtype.UUID
	Mode                string
	GoogleConnectionID  pgtype.UUID
	PropertyID          pgtype.Text
	PropertyDisplayName pgtype.Text
	AccountDisplayName  pgtype.Text
}

type locationGSCEffectiveBinding struct {
	Source             string `json:"source"`
	GoogleConnectionID string `json:"google_connection_id"`
	GoogleAccountEmail string `json:"google_account_email,omitempty"`
	SiteURL            string `json:"site_url"`
	PermissionLevel    string `json:"permission_level,omitempty"`
}

type locationGoogleAnalyticsEffectiveBinding struct {
	Source              string `json:"source"`
	GoogleConnectionID  string `json:"google_connection_id"`
	GoogleAccountEmail  string `json:"google_account_email,omitempty"`
	PropertyID          string `json:"property_id"`
	PropertyDisplayName string `json:"property_display_name,omitempty"`
	AccountDisplayName  string `json:"account_display_name,omitempty"`
}

func (a *App) handleGetLocationGSCBinding(w http.ResponseWriter, r *http.Request) {
	location, _, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	accounts, err := listGoogleAccountConnectionsByOrganizationID(r.Context(), a.DB, location.OrganizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	projectConnection, hasProjectConnection, err := getProjectGSCConnectionByProjectID(r.Context(), a.Queries, location.ProjectID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	binding, hasBinding, err := getLocationGSCBinding(r.Context(), a.DB, location.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, newLocationGSCBindingResponse(accounts, projectConnection, hasProjectConnection, binding, hasBinding))
}

func (a *App) handlePutLocationGSCBinding(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	if err := requireOrganizationOwner(r.Context(), a.Queries, location.OrganizationID, userID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}
	var requestBody putLocationGoogleBindingRequest
	if !readJSONOrRespond(w, r, &requestBody) {
		return
	}
	mode, err := parseLocationGoogleBindingMode(requestBody.Mode)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	binding := locationGSCBinding{LocationID: location.ID, Mode: mode}
	if mode == locationGoogleModeCustom {
		connectionID, err := parseUUIDParam(strings.TrimSpace(requestBody.GoogleConnectionID))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid google connection id")
			return
		}
		account, found, err := getGoogleAccountConnectionByID(r.Context(), a.DB, connectionID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if !found || !uuidEqual(account.OrganizationID, location.OrganizationID) {
			writeJSONError(w, http.StatusNotFound, "google account not found")
			return
		}
		siteURL := strings.TrimSpace(requestBody.SiteURL)
		if siteURL == "" {
			writeJSONError(w, http.StatusBadRequest, "site_url is required")
			return
		}
		permissionLevel, ok := a.verifyGSCSiteForLocation(w, r, account, siteURL)
		if !ok {
			return
		}
		binding.GoogleConnectionID = account.ID
		binding.SiteURL = pgText(siteURL)
		binding.PermissionLevel = pgText(permissionLevel)
	}
	if err := upsertLocationGSCBinding(r.Context(), a.DB, binding); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": mode})
}

func (a *App) handleDeleteLocationGSCBinding(w http.ResponseWriter, r *http.Request) {
	a.removeLocationOwnGoogleBinding(w, r, deleteLocationGSCBinding)
}

func (a *App) handleGetLocationGoogleAnalyticsBinding(w http.ResponseWriter, r *http.Request) {
	location, _, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	accounts, err := listGoogleAccountConnectionsByOrganizationID(r.Context(), a.DB, location.OrganizationID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	selection, hasSelection, err := getProjectGoogleAnalyticsConnectionByProjectID(r.Context(), a.Queries, location.ProjectID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	binding, hasBinding, err := getLocationGoogleAnalyticsBinding(r.Context(), a.DB, location.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, newLocationGoogleAnalyticsBindingResponse(accounts, selection, hasSelection, binding, hasBinding))
}

func (a *App) handlePutLocationGoogleAnalyticsBinding(w http.ResponseWriter, r *http.Request) {
	location, userID, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	if err := requireOrganizationOwner(r.Context(), a.Queries, location.OrganizationID, userID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}
	var requestBody putLocationGoogleBindingRequest
	if !readJSONOrRespond(w, r, &requestBody) {
		return
	}
	mode, err := parseLocationGoogleBindingMode(requestBody.Mode)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	binding := locationGoogleAnalyticsBinding{LocationID: location.ID, Mode: mode}
	if mode == locationGoogleModeCustom {
		connectionID, err := parseUUIDParam(strings.TrimSpace(requestBody.GoogleConnectionID))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid google connection id")
			return
		}
		account, found, err := getGoogleAccountConnectionByID(r.Context(), a.DB, connectionID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if !found || !uuidEqual(account.OrganizationID, location.OrganizationID) {
			writeJSONError(w, http.StatusNotFound, "google account not found")
			return
		}
		propertyID := strings.TrimSpace(requestBody.PropertyID)
		if propertyID == "" {
			writeJSONError(w, http.StatusBadRequest, "property_id is required")
			return
		}
		displayName, accountDisplayName, ok := a.verifyGoogleAnalyticsPropertyForLocation(w, r, account, propertyID)
		if !ok {
			return
		}
		binding.GoogleConnectionID = account.ID
		binding.PropertyID = pgText(propertyID)
		binding.PropertyDisplayName = pgText(displayName)
		binding.AccountDisplayName = pgText(accountDisplayName)
	}
	if err := upsertLocationGoogleAnalyticsBinding(r.Context(), a.DB, binding); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": mode})
}

func (a *App) handleDeleteLocationGoogleAnalyticsBinding(w http.ResponseWriter, r *http.Request) {
	a.removeLocationOwnGoogleBinding(w, r, deleteLocationGoogleAnalyticsBinding)
}

func (a *App) removeLocationOwnGoogleBinding(w http.ResponseWriter, r *http.Request, remove func(context.Context, googleAccountDB, pgtype.UUID) error) {
	location, userID, ok := a.locationSetupLocation(w, r)
	if !ok {
		return
	}
	if err := requireOrganizationOwner(r.Context(), a.Queries, location.OrganizationID, userID); err != nil {
		writeInvitePermissionError(w, err)
		return
	}
	if err := remove(r.Context(), a.DB, location.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) verifyGSCSiteForLocation(w http.ResponseWriter, r *http.Request, account sqlc.GoogleConnection, siteURL string) (string, bool) {
	account, accessToken, err := a.ensureFreshGoogleConnection(r.Context(), a.Queries, account)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to refresh google connection")
		return "", false
	}
	sites, err := a.GSCService.FetchSites(r.Context(), accessToken)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch search console sites")
		return "", false
	}
	for siteIndex := range sites {
		if sites[siteIndex].SiteURL == siteURL {
			return sites[siteIndex].PermissionLevel, true
		}
	}
	writeJSONError(w, http.StatusBadRequest, "selected property is not available for this google account")
	return "", false
}

func (a *App) verifyGoogleAnalyticsPropertyForLocation(w http.ResponseWriter, r *http.Request, account sqlc.GoogleConnection, propertyID string) (string, string, bool) {
	if !hasGoogleScope(account.Scope, googleAnalyticsReadOnlyScope) {
		writeJSONError(w, http.StatusBadRequest, "google analytics requires reconnect")
		return "", "", false
	}
	account, accessToken, err := a.ensureFreshGoogleConnection(r.Context(), a.Queries, account)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to refresh google connection")
		return "", "", false
	}
	properties, err := a.GAService.ListProperties(r.Context(), accessToken)
	if err != nil {
		writeGoogleAPIError(w, err, http.StatusBadRequest, "failed to fetch analytics properties")
		return "", "", false
	}
	for propertyIndex := range properties {
		if properties[propertyIndex].PropertyID == propertyID {
			return properties[propertyIndex].DisplayName, properties[propertyIndex].AccountDisplayName, true
		}
	}
	writeJSONError(w, http.StatusBadRequest, "selected property is not available for this google account")
	return "", "", false
}

func parseLocationGoogleBindingMode(mode string) (string, error) {
	switch strings.TrimSpace(mode) {
	case locationGoogleModeInherit:
		return locationGoogleModeInherit, nil
	case locationGoogleModeOff:
		return locationGoogleModeOff, nil
	case locationGoogleModeCustom:
		return locationGoogleModeCustom, nil
	}
	return "", errors.New("mode must be inherit, off, or custom")
}

func resolveLocationServiceSource(mode string, hasProjectBinding, hasLocationBinding bool) (string, bool) {
	switch mode {
	case locationGoogleModeOff:
		return "", false
	case locationGoogleModeCustom:
		if hasLocationBinding {
			return locationGoogleModeCustom, true
		}
		return "", false
	default:
		if hasProjectBinding {
			return locationGoogleModeInherit, true
		}
		return "", false
	}
}

func googleAccountEmailByID(accounts []sqlc.GoogleConnection, id pgtype.UUID) string {
	for _, account := range accounts {
		if uuidEqual(account.ID, id) {
			return textValue(account.GoogleAccountEmail)
		}
	}
	return ""
}

func newLocationGSCBindingResponse(accounts []sqlc.GoogleConnection, projectConnection sqlc.ProjectGscConnection, hasProjectConnection bool, binding locationGSCBinding, hasBinding bool) map[string]any {
	mode := locationGoogleModeInherit
	if hasBinding {
		mode = binding.Mode
	}
	source, ok := resolveLocationServiceSource(mode, hasProjectConnection, hasBinding && binding.Mode == locationGoogleModeCustom)
	var effective *locationGSCEffectiveBinding
	if ok {
		if source == locationGoogleModeCustom {
			effective = &locationGSCEffectiveBinding{
				Source:             "location",
				GoogleConnectionID: binding.GoogleConnectionID.String(),
				GoogleAccountEmail: googleAccountEmailByID(accounts, binding.GoogleConnectionID),
				SiteURL:            textValue(binding.SiteURL),
				PermissionLevel:    textValue(binding.PermissionLevel),
			}
		} else {
			effective = &locationGSCEffectiveBinding{
				Source:             "project",
				GoogleConnectionID: projectConnection.GoogleConnectionID.String(),
				GoogleAccountEmail: googleAccountEmailByID(accounts, projectConnection.GoogleConnectionID),
				SiteURL:            projectConnection.SiteUrl,
				PermissionLevel:    textValue(projectConnection.PermissionLevel),
			}
		}
	}
	var projectSummary *map[string]string
	if hasProjectConnection {
		projectSummary = &map[string]string{
			"google_connection_id": projectConnection.GoogleConnectionID.String(),
			"site_url":             projectConnection.SiteUrl,
		}
	}
	return map[string]any{
		"mode":               mode,
		"configured":         hasBinding,
		"effective":          effective,
		"project":            projectSummary,
		"google_connections": newProjectGoogleAccountResponses(accounts),
	}
}

func newLocationGoogleAnalyticsBindingResponse(accounts []sqlc.GoogleConnection, selection sqlc.ProjectGoogleAnalyticsConnection, hasSelection bool, binding locationGoogleAnalyticsBinding, hasBinding bool) map[string]any {
	mode := locationGoogleModeInherit
	if hasBinding {
		mode = binding.Mode
	}
	source, ok := resolveLocationServiceSource(mode, hasSelection, hasBinding && binding.Mode == locationGoogleModeCustom)
	var effective *locationGoogleAnalyticsEffectiveBinding
	if ok {
		if source == locationGoogleModeCustom {
			effective = &locationGoogleAnalyticsEffectiveBinding{
				Source:              "location",
				GoogleConnectionID:  binding.GoogleConnectionID.String(),
				GoogleAccountEmail:  googleAccountEmailByID(accounts, binding.GoogleConnectionID),
				PropertyID:          textValue(binding.PropertyID),
				PropertyDisplayName: textValue(binding.PropertyDisplayName),
				AccountDisplayName:  textValue(binding.AccountDisplayName),
			}
		} else {
			effective = &locationGoogleAnalyticsEffectiveBinding{
				Source:              "project",
				GoogleConnectionID:  selection.GoogleConnectionID.String(),
				GoogleAccountEmail:  googleAccountEmailByID(accounts, selection.GoogleConnectionID),
				PropertyID:          selection.PropertyID,
				PropertyDisplayName: selection.PropertyDisplayName,
				AccountDisplayName:  textValue(selection.AccountDisplayName),
			}
		}
	}
	var projectSummary *map[string]string
	if hasSelection {
		projectSummary = &map[string]string{
			"google_connection_id": selection.GoogleConnectionID.String(),
			"property_id":          selection.PropertyID,
		}
	}
	return map[string]any{
		"mode":               mode,
		"configured":         hasBinding,
		"effective":          effective,
		"project":            projectSummary,
		"google_connections": newProjectGoogleAccountResponses(accounts),
	}
}

func getLocationGSCBinding(ctx context.Context, db googleAccountDB, locationID pgtype.UUID) (locationGSCBinding, bool, error) {
	var binding locationGSCBinding
	err := db.QueryRow(ctx, `SELECT location_id, mode, google_connection_id, site_url, permission_level
		FROM location_gsc_connections WHERE location_id = $1 LIMIT 1`, locationID).Scan(
		&binding.LocationID, &binding.Mode, &binding.GoogleConnectionID, &binding.SiteURL, &binding.PermissionLevel,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return locationGSCBinding{}, false, nil
		}
		return locationGSCBinding{}, false, err
	}
	return binding, true, nil
}

func deleteLocationGSCBinding(ctx context.Context, db googleAccountDB, locationID pgtype.UUID) error {
	_, err := db.Exec(ctx, `DELETE FROM location_gsc_connections WHERE location_id = $1`, locationID)
	return err
}

func upsertLocationGSCBinding(ctx context.Context, db googleAccountDB, binding locationGSCBinding) error {
	_, err := db.Exec(ctx, `INSERT INTO location_gsc_connections (location_id, mode, google_connection_id, site_url, permission_level, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (location_id) DO UPDATE SET
			mode = excluded.mode,
			google_connection_id = excluded.google_connection_id,
			site_url = excluded.site_url,
			permission_level = excluded.permission_level,
			updated_at = now()`,
		binding.LocationID, binding.Mode, uuidOrNull(binding.GoogleConnectionID), binding.SiteURL, binding.PermissionLevel,
	)
	return err
}

func getLocationGoogleAnalyticsBinding(ctx context.Context, db googleAccountDB, locationID pgtype.UUID) (locationGoogleAnalyticsBinding, bool, error) {
	var binding locationGoogleAnalyticsBinding
	err := db.QueryRow(ctx, `SELECT location_id, mode, google_connection_id, property_id, property_display_name, account_display_name
		FROM location_google_analytics_connections WHERE location_id = $1 LIMIT 1`, locationID).Scan(
		&binding.LocationID, &binding.Mode, &binding.GoogleConnectionID,
		&binding.PropertyID, &binding.PropertyDisplayName, &binding.AccountDisplayName,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return locationGoogleAnalyticsBinding{}, false, nil
		}
		return locationGoogleAnalyticsBinding{}, false, err
	}
	return binding, true, nil
}

func deleteLocationGoogleAnalyticsBinding(ctx context.Context, db googleAccountDB, locationID pgtype.UUID) error {
	_, err := db.Exec(ctx, `DELETE FROM location_google_analytics_connections WHERE location_id = $1`, locationID)
	return err
}

func upsertLocationGoogleAnalyticsBinding(ctx context.Context, db googleAccountDB, binding locationGoogleAnalyticsBinding) error {
	_, err := db.Exec(ctx, `INSERT INTO location_google_analytics_connections (location_id, mode, google_connection_id, property_id, property_display_name, account_display_name, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (location_id) DO UPDATE SET
			mode = excluded.mode,
			google_connection_id = excluded.google_connection_id,
			property_id = excluded.property_id,
			property_display_name = excluded.property_display_name,
			account_display_name = excluded.account_display_name,
			updated_at = now()`,
		binding.LocationID, binding.Mode, uuidOrNull(binding.GoogleConnectionID),
		binding.PropertyID, binding.PropertyDisplayName, binding.AccountDisplayName,
	)
	return err
}
