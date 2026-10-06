package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestLayer4ServiceLabelKey(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantDisplay string
		wantKey     string
		wantErr     string
	}{
		{name: "trims and collapses", raw: "  Life   Insurance ", wantDisplay: "Life Insurance", wantKey: "life insurance"},
		{name: "keeps display casing", raw: "ACME Shoes", wantDisplay: "ACME Shoes", wantKey: "acme shoes"},
		{name: "empty", raw: "", wantErr: "service label must not be empty"},
		{name: "whitespace only", raw: "  \t ", wantErr: "service label must not be empty"},
		{name: "nul byte", raw: "life\x00insurance", wantErr: "service label must not contain nul"},
		{name: "invalid utf8", raw: "life\xffinsurance", wantErr: "service label must be valid utf-8"},
		{name: "too long", raw: strings.Repeat("a", 201), wantErr: "service label must fit within 200 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			display, key, err := layer4ServiceLabelKey(tc.raw)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("layer4ServiceLabelKey(%q) error = %v, want %q", tc.raw, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("layer4ServiceLabelKey(%q) unexpected error: %v", tc.raw, err)
			}
			if display != tc.wantDisplay || key != tc.wantKey {
				t.Fatalf("layer4ServiceLabelKey(%q) = (%q, %q), want (%q, %q)", tc.raw, display, key, tc.wantDisplay, tc.wantKey)
			}
		})
	}
}

func layer4OverrideEntry(serviceID, serviceLabel *string, mode string) layer4ServiceOverrideEntry {
	return layer4ServiceOverrideEntry{ServiceID: serviceID, ServiceLabel: serviceLabel, Mode: mode}
}

func layer4StringPointer(value string) *string {
	return &value
}

func TestLayer4ValidateServiceOverrides(t *testing.T) {
	validID := "11111111-1111-1111-1111-111111111111"
	t.Run("exclude reference and location-only include", func(t *testing.T) {
		references, locationOnly, err := layer4ValidateServiceOverrides([]layer4ServiceOverrideEntry{
			layer4OverrideEntry(layer4StringPointer(validID), nil, "exclude"),
			layer4OverrideEntry(nil, layer4StringPointer("  Deep  Clean "), "include"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(references) != 1 || references[0].ServiceID.String() != validID || references[0].Mode != "exclude" {
			t.Fatalf("references = %+v, want one exclude reference of %s", references, validID)
		}
		if len(locationOnly) != 1 || locationOnly[0].Display != "Deep Clean" || locationOnly[0].Key != "deep clean" {
			t.Fatalf("locationOnly = %+v, want normalized Deep Clean", locationOnly)
		}
	})
	t.Run("include reference is retained with mode", func(t *testing.T) {
		references, locationOnly, err := layer4ValidateServiceOverrides([]layer4ServiceOverrideEntry{
			layer4OverrideEntry(layer4StringPointer(validID), nil, "include"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(references) != 1 || references[0].ServiceID.String() != validID || references[0].Mode != "include" {
			t.Fatalf("include reference must be retained with mode, got references=%+v", references)
		}
		if len(locationOnly) != 0 {
			t.Fatalf("include reference must not become location-only, got %+v", locationOnly)
		}
	})
	t.Run("include and exclude references keep their modes", func(t *testing.T) {
		excludeID := "22222222-2222-2222-2222-222222222222"
		references, _, err := layer4ValidateServiceOverrides([]layer4ServiceOverrideEntry{
			layer4OverrideEntry(layer4StringPointer(validID), nil, "include"),
			layer4OverrideEntry(layer4StringPointer(excludeID), nil, "exclude"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(references) != 2 {
			t.Fatalf("references = %+v, want two", references)
		}
		modes := map[string]string{}
		for _, reference := range references {
			modes[reference.ServiceID.String()] = reference.Mode
		}
		if modes[validID] != "include" || modes[excludeID] != "exclude" {
			t.Fatalf("reference modes = %v, want include/exclude preserved", modes)
		}
	})
	t.Run("repeated service reference same mode dedupes", func(t *testing.T) {
		references, locationOnly, err := layer4ValidateServiceOverrides([]layer4ServiceOverrideEntry{
			layer4OverrideEntry(layer4StringPointer(validID), nil, "exclude"),
			layer4OverrideEntry(layer4StringPointer(validID), nil, "exclude"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(references) != 1 || references[0].ServiceID.String() != validID || references[0].Mode != "exclude" {
			t.Fatalf("repeated same-mode reference must dedupe, got references=%+v", references)
		}
		if len(locationOnly) != 0 {
			t.Fatalf("unexpected location-only entries: %+v", locationOnly)
		}
	})
	t.Run("empty array clears overrides", func(t *testing.T) {
		references, locationOnly, err := layer4ValidateServiceOverrides([]layer4ServiceOverrideEntry{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(references) != 0 || len(locationOnly) != 0 {
			t.Fatalf("empty overrides must clear, got references=%v locationOnly=%v", references, locationOnly)
		}
	})
	invalid := []struct {
		name    string
		entries []layer4ServiceOverrideEntry
		wantErr string
	}{
		{name: "missing overrides", entries: nil, wantErr: "overrides is required"},
		{name: "bad mode", entries: []layer4ServiceOverrideEntry{layer4OverrideEntry(layer4StringPointer(validID), nil, "remove")}, wantErr: "override mode must be include or exclude"},
		{name: "both set", entries: []layer4ServiceOverrideEntry{layer4OverrideEntry(layer4StringPointer(validID), layer4StringPointer("x"), "exclude")}, wantErr: "exactly one of service_id, service_label must be set"},
		{name: "neither set", entries: []layer4ServiceOverrideEntry{layer4OverrideEntry(nil, nil, "exclude")}, wantErr: "exactly one of service_id, service_label must be set"},
		{name: "bad uuid", entries: []layer4ServiceOverrideEntry{layer4OverrideEntry(layer4StringPointer("nope"), nil, "exclude")}, wantErr: "invalid service_id"},
		{name: "location-only exclude", entries: []layer4ServiceOverrideEntry{layer4OverrideEntry(nil, layer4StringPointer("x"), "exclude")}, wantErr: "location-only services must use mode include"},
		{name: "empty label", entries: []layer4ServiceOverrideEntry{layer4OverrideEntry(nil, layer4StringPointer("  "), "include")}, wantErr: "service label must not be empty"},
		{name: "duplicate normalized label", entries: []layer4ServiceOverrideEntry{layer4OverrideEntry(nil, layer4StringPointer("Deep Clean"), "include"), layer4OverrideEntry(nil, layer4StringPointer("deep  clean"), "include")}, wantErr: "duplicate service override"},
		{name: "mixed duplicate ids", entries: []layer4ServiceOverrideEntry{layer4OverrideEntry(layer4StringPointer(validID), nil, "exclude"), layer4OverrideEntry(layer4StringPointer(validID), nil, "include")}, wantErr: "conflicting override modes for service_id " + validID},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := layer4ValidateServiceOverrides(tc.entries); err == nil || err.Error() != tc.wantErr {
				t.Fatalf("layer4ValidateServiceOverrides error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestLayer4ParseSelectedIDs(t *testing.T) {
	id := "33333333-3333-3333-3333-333333333333"
	selected, err := layer4ParseSelectedIDs([]string{id, id})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(selected) != 1 {
		t.Fatalf("duplicate ids must dedupe, got %d", len(selected))
	}
	if _, err := layer4ParseSelectedIDs(nil); err == nil || err.Error() != "selected_ids is required" {
		t.Fatalf("nil selected_ids must be required, got %v", err)
	}
	if _, err := layer4ParseSelectedIDs([]string{"nope"}); err == nil || err.Error() != "invalid landmark id" {
		t.Fatalf("bad uuid must fail, got %v", err)
	}
}

func TestLayer4LocationQueryRecordNullLandmark(t *testing.T) {
	var landmarkID pgtype.UUID
	record := newLayer4LocationQueryRecord(sqlc.ProjectLocationQuery{Text: "plumber near baluwatar", Ordinal: 2, Enabled: true, Kind: "map", Source: "generated", Origin: "service", LandmarkID: landmarkID})
	if record.LandmarkID != nil {
		t.Fatalf("null landmark_id must stay null, got %q", *record.LandmarkID)
	}
	if record.Ordinal != 2 || !record.Enabled || record.Kind != "map" {
		t.Fatalf("record fields lost: %+v", record)
	}
}

func TestLayer4LandmarkRecordDefaults(t *testing.T) {
	record := newLayer4LandmarkRecord(sqlc.LocationLandmark{
		Name: "Boudha", Latitude: 27.7, Longitude: 85.3, StraightLineM: 1370,
		Provider: "google_places", ProviderRef: "places/ChIJ1",
		FetchedAt: pgtype.Timestamptz{Time: time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC), Valid: true},
	})
	if record.Categories == nil || len(record.Categories) != 0 {
		t.Fatalf("nil categories must become an empty array, got %#v", record.Categories)
	}
	if record.FetchedAt != "2026-10-06T07:00:00Z" {
		t.Fatalf("fetched_at = %q, want RFC3339 UTC", record.FetchedAt)
	}
	if record.Selected {
		t.Fatalf("fresh record must default selected false")
	}
}

func TestLayer4InvalidIDsAreBadRequest(t *testing.T) {
	call := func(handler func(http.ResponseWriter, *http.Request), projectID, locationID, serviceID string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/layer4-test", nil)
		routeContext := chi.NewRouteContext()
		if projectID != "" {
			routeContext.URLParams.Add("projectID", projectID)
		}
		if locationID != "" {
			routeContext.URLParams.Add("locationID", locationID)
		}
		if serviceID != "" {
			routeContext.URLParams.Add("serviceID", serviceID)
		}
		request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
		recorder := httptest.NewRecorder()
		handler(recorder, request)
		return recorder
	}
	app := &App{}
	if recorder := call(app.handleListProjectServices, "nope", "", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad project id status = %d, want 400", recorder.Code)
	}
	if recorder := call(app.handleGetProjectLocationServices, "nope", "also-bad", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad project id status = %d, want 400", recorder.Code)
	}
	if recorder := call(app.handleGetProjectLocationServices, "44444444-4444-4444-4444-444444444444", "bad", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad location id status = %d, want 400", recorder.Code)
	}
	if recorder := call(app.handleRenameProjectService, "44444444-4444-4444-4444-444444444444", "", "bad"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad service id status = %d, want 400", recorder.Code)
	}
	if recorder := call(app.handleListProjectLocationQueries, "44444444-4444-4444-4444-444444444444", "bad", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad location id status = %d, want 400", recorder.Code)
	}
	if recorder := call(app.handleRefreshProjectLocationLandmarks, "44444444-4444-4444-4444-444444444444", "bad", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad location id status = %d, want 400", recorder.Code)
	}
}

func TestLayer4RoutesRegistered(t *testing.T) {
	router, ok := (&App{}).Router().(chi.Router)
	if !ok {
		t.Fatal("app router does not expose chi routes")
	}
	found := map[string]bool{}
	if err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		found[method+" "+route] = true
		return nil
	}); err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	for _, route := range []string{
		"GET /projects/{projectID}/services",
		"POST /projects/{projectID}/services",
		"PATCH /projects/{projectID}/services/{serviceID}",
		"DELETE /projects/{projectID}/services/{serviceID}",
		"GET /projects/{projectID}/locations/{locationID}/services",
		"PUT /projects/{projectID}/locations/{locationID}/services",
		"GET /projects/{projectID}/locations/{locationID}/queries",
		"GET /projects/{projectID}/locations/{locationID}/landmarks",
		"POST /projects/{projectID}/locations/{locationID}/landmarks/refresh",
		"PUT /projects/{projectID}/locations/{locationID}/landmarks/selection",
	} {
		if !found[route] {
			t.Fatalf("route %q is not registered", route)
		}
	}
}
