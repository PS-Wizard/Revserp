package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/geography"
	"github.com/ps-wizard/revserp/internal/localvisibility"
)

func (a *App) locationSetupProjectUser(w http.ResponseWriter, r *http.Request) (pgtype.UUID, pgtype.UUID, bool) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, 400, "invalid project id")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	if _, err = a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: principal.User.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, 404, "project not found")
		} else {
			serverError(w, r, err)
		}
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	return projectID, principal.User.ID, true
}

func (a *App) handleGenerateLocationQueries(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.locationSetupProjectUser(w, r); !ok {
		return
	}
	var body struct {
		Service  string `json:"service"`
		// Services carries every service the location offers, so the
		// generator round-robins across them instead of going deep on one.
		// A lone Service is used when the caller sends only one.
		Services []string `json:"services"`
		Locality string   `json:"locality"`
		// Localities carries every distinct address level smallest first, so
		// each generated query searches a different scope. A lone Locality
		// is used when the caller has only one level.
		Localities []string `json:"localities"`
	}
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	services := body.Services
	if len(services) == 0 {
		services = []string{body.Service}
	}
	levels := body.Localities
	if len(levels) == 0 {
		levels = []string{body.Locality}
	}
	queries, err := localvisibility.GenerateMapQueries(services, levels)
	if err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"queries": queries})
}

type locationGeographyResponse struct {
	Results      []geography.GeocodedAddress `json:"results"`
	Cached       bool                        `json:"cached"`
	RefreshError string                      `json:"refresh_error,omitempty"`
}

func (a *App) locationGeographyResults(ctx context.Context, key string, refresh bool, fetch func() ([]geography.GeocodedAddress, error)) (locationGeographyResponse, error) {
	cached, cacheErr := a.Queries.GetLocationGeographyCache(ctx, key)
	if cacheErr != nil && !errors.Is(cacheErr, pgx.ErrNoRows) {
		return locationGeographyResponse{}, cacheErr
	}
	saved := []geography.GeocodedAddress{}
	if cacheErr == nil {
		if err := json.Unmarshal(cached.Results, &saved); err != nil {
			return locationGeographyResponse{}, fmt.Errorf("location geography cache: %w", err)
		}
		if !refresh {
			return locationGeographyResponse{Results: saved, Cached: true}, nil
		}
	}
	results, err := fetch()
	if err != nil {
		if cacheErr == nil {
			return locationGeographyResponse{Results: saved, Cached: true, RefreshError: err.Error()}, nil
		}
		return locationGeographyResponse{}, err
	}
	if results == nil {
		results = []geography.GeocodedAddress{}
	}
	raw, err := json.Marshal(results)
	if err != nil {
		return locationGeographyResponse{}, err
	}
	if err = a.Queries.SaveLocationGeographyCache(ctx, sqlc.SaveLocationGeographyCacheParams{CacheKey: key, Results: raw}); err != nil {
		return locationGeographyResponse{}, err
	}
	return locationGeographyResponse{Results: results}, nil
}

func (a *App) handleSearchLocationAddress(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.locationSetupProjectUser(w, r); !ok {
		return
	}
	var body struct {
		Address string `json:"address"`
		Refresh bool   `json:"refresh"`
	}
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	address := strings.Join(strings.Fields(body.Address), " ")
	if address == "" || len(address) > 1000 {
		writeJSONError(w, 400, "address must contain 1–1000 bytes")
		return
	}
	response, err := a.locationGeographyResults(r.Context(), "search:"+strings.ToLower(address), body.Refresh, func() ([]geography.GeocodedAddress, error) {
		if a.Nominatim == nil {
			return nil, errors.New("location geography provider is unavailable")
		}
		return a.Nominatim.SearchAddresses(r.Context(), address)
	})
	if err != nil {
		writeJSONError(w, 503, err.Error())
		return
	}
	writeJSON(w, 200, response)
}

func (a *App) handleReverseLocationAddress(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.locationSetupProjectUser(w, r); !ok {
		return
	}
	var body struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
		Refresh   bool     `json:"refresh"`
	}
	if !readStrictJSONOrRespond(w, r, &body) {
		return
	}
	if body.Latitude == nil || body.Longitude == nil {
		writeJSONError(w, 400, "latitude and longitude are required")
		return
	}
	lat, lng := *body.Latitude, *body.Longitude
	if math.IsNaN(lat) || math.IsInf(lat, 0) || lat < -90 || lat > 90 || math.IsNaN(lng) || math.IsInf(lng, 0) || lng < -180 || lng > 180 {
		writeJSONError(w, 400, "physical coordinates are invalid")
		return
	}
	response, err := a.locationGeographyResults(r.Context(), fmt.Sprintf("reverse:%.6f,%.6f", lat, lng), body.Refresh, func() ([]geography.GeocodedAddress, error) {
		if a.Nominatim == nil {
			return nil, errors.New("location geography provider is unavailable")
		}
		address, err := a.Nominatim.ReverseAddress(r.Context(), lat, lng)
		if err != nil {
			return nil, err
		}
		return []geography.GeocodedAddress{address}, nil
	})
	if err != nil {
		writeJSONError(w, 503, err.Error())
		return
	}
	writeJSON(w, 200, response)
}
