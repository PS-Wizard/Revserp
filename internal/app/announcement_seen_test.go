package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestValidAnnouncementID(t *testing.T) {
	valid := []string{"v1.2.3", "a", "A-._0", "release-2026.10", strings.Repeat("x", 100)}
	for _, id := range valid {
		if !validAnnouncementID(id) {
			t.Errorf("validAnnouncementID(%q) = false, want true", id)
		}
	}
	invalid := []string{
		"",
		strings.Repeat("x", 101),
		".v1", "-v1", "_v1",
		"v 1", "v/1", "v:1", "v@1", "v#1",
		"väl", "v1\n",
	}
	for _, id := range invalid {
		if validAnnouncementID(id) {
			t.Errorf("validAnnouncementID(%q) = true, want false", id)
		}
	}
}

func announcementSeenTestApp() *App {
	return &App{SessionManager: internalauth.NewSessionManager(nil, nil, nil, "", "", time.Hour, false)}
}

func TestAnnouncementSeenRequiresAuth(t *testing.T) {
	router := announcementSeenTestApp().Router()
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/me/announcements/seen", nil),
		httptest.NewRequest(http.MethodPut, "/me/announcements/v1.2.3/seen", nil),
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", request.Method, request.URL.Path, response.Code)
		}
	}
}

func markAnnouncementSeenDirect(t *testing.T, app *App, userID pgtype.UUID, announcementID string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/me/announcements/seen", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("announcementID", announcementID)
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
	ctx = withPrincipal(ctx, Principal{User: sqlc.User{ID: userID}})
	response := httptest.NewRecorder()
	app.handleMarkAnnouncementSeen(response, request.WithContext(ctx))
	return response
}

func TestMarkAnnouncementSeenRejectsInvalidIDs(t *testing.T) {
	var userID pgtype.UUID
	if err := userID.Scan("00000000-0000-0000-0000-0000000000a1"); err != nil {
		t.Fatalf("scan user id: %v", err)
	}
	app := &App{}
	for _, id := range []string{"", ".v1", "-v1", "_v1", "v 1", "v/1", "v:1", "väl", strings.Repeat("x", 101)} {
		if response := markAnnouncementSeenDirect(t, app, userID, id); response.Code != http.StatusBadRequest {
			t.Errorf("PUT id %q = %d, want 400 (body %s)", id, response.Code, response.Body.String())
		}
	}
}

type announcementSeenListResponse struct {
	SeenAnnouncements []announcementSeenReceipt `json:"seen_announcements"`
	UserCreatedAt     string                    `json:"user_created_at"`
}

func decodeAnnouncementSeen(t *testing.T, response *httptest.ResponseRecorder) announcementSeenListResponse {
	t.Helper()
	var body announcementSeenListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode seen announcements: %v (body %q)", err, response.Body.String())
	}
	return body
}

func putAnnouncementSeen(t *testing.T, fixture sessionFixture, id string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/me/announcements/"+id+"/seen", nil)
	request.AddCookie(&http.Cookie{Name: "revserp_session", Value: fixture.rawCookie})
	response := httptest.NewRecorder()
	fixture.app.Router().ServeHTTP(response, request)
	return response
}

func getAnnouncementSeen(t *testing.T, fixture sessionFixture) *httptest.ResponseRecorder {
	t.Helper()
	return get(t, fixture.app.Router(), "/me/announcements/seen", fixture.rawCookie)
}

func newAnnouncementSeenTestQueries(t *testing.T) (*sqlc.Queries, *pgxpool.Pool, context.Context) {
	t.Helper()
	testDatabaseURL := os.Getenv("ANNOUNCEMENT_SEEN_TEST_DATABASE_URL")
	if testDatabaseURL == "" {
		t.Skip("ANNOUNCEMENT_SEEN_TEST_DATABASE_URL is not set")
	}
	if appDatabaseURL := os.Getenv("DATABASE_URL"); appDatabaseURL != "" && appDatabaseURL == testDatabaseURL {
		t.Fatal("refusing to run announcement seen DB test against the application DATABASE_URL")
	}
	parsed, err := url.Parse(testDatabaseURL)
	if err != nil {
		t.Fatalf("parse ANNOUNCEMENT_SEEN_TEST_DATABASE_URL: %v", err)
	}
	if name := strings.ToLower(strings.TrimPrefix(parsed.Path, "/")); !strings.Contains(name, "test") {
		t.Fatalf("refusing to run announcement seen DB test against non-test database %q", parsed.Path)
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, testDatabaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("announcement seen test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	var usersTable, seenTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.users'), to_regclass('public.user_announcement_seen')`).Scan(&usersTable, &seenTable); err != nil || usersTable == nil || seenTable == nil {
		t.Skip("announcement seen test database is missing migrated tables")
	}
	return sqlc.New(pool), pool, ctx
}

func TestAnnouncementSeenPersistence(t *testing.T) {
	queries, pool, ctx := newAnnouncementSeenTestQueries(t)
	fixture := newSessionFixtureWithQueries(t, queries, pool, ctx)

	initial := getAnnouncementSeen(t, fixture)
	if initial.Code != http.StatusOK {
		t.Fatalf("initial GET = %d (%s)", initial.Code, initial.Body.String())
	}
	decoded := decodeAnnouncementSeen(t, initial)
	if decoded.SeenAnnouncements == nil || len(decoded.SeenAnnouncements) != 0 {
		t.Fatalf("initial receipts = %+v, want empty non-null []", decoded.SeenAnnouncements)
	}
	if _, err := time.Parse(time.RFC3339, decoded.UserCreatedAt); err != nil {
		t.Fatalf("user_created_at %q is not RFC3339: %v", decoded.UserCreatedAt, err)
	}
	if cache := initial.Header().Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("GET Cache-Control = %q, want no-store", cache)
	}

	if response := putAnnouncementSeen(t, fixture, "v1.2.3"); response.Code != http.StatusOK {
		t.Fatalf("PUT v1.2.3 = %d (%s)", response.Code, response.Body.String())
	} else if strings.TrimSpace(response.Body.String()) != `{"ok":true}` {
		t.Fatalf("PUT v1.2.3 body = %s, want {\"ok\":true}", response.Body.String())
	}

	first := decodeAnnouncementSeen(t, getAnnouncementSeen(t, fixture))
	if len(first.SeenAnnouncements) != 1 || first.SeenAnnouncements[0].AnnouncementID != "v1.2.3" {
		t.Fatalf("receipts after first PUT = %+v", first.SeenAnnouncements)
	}
	if _, err := time.Parse(time.RFC3339, first.SeenAnnouncements[0].SeenAt); err != nil {
		t.Fatalf("seen_at %q is not RFC3339: %v", first.SeenAnnouncements[0].SeenAt, err)
	}

	time.Sleep(20 * time.Millisecond)
	if response := putAnnouncementSeen(t, fixture, "v1.2.3"); response.Code != http.StatusOK {
		t.Fatalf("repeat PUT = %d (%s)", response.Code, response.Body.String())
	}
	if repeat := decodeAnnouncementSeen(t, getAnnouncementSeen(t, fixture)); len(repeat.SeenAnnouncements) != 1 ||
		repeat.SeenAnnouncements[0].SeenAt != first.SeenAnnouncements[0].SeenAt {
		t.Fatalf("repeat PUT moved first seen_at from %+v to %+v", first.SeenAnnouncements, repeat.SeenAnnouncements)
	}

	if response := putAnnouncementSeen(t, fixture, "v9.9.9"); response.Code != http.StatusOK {
		t.Fatalf("PUT v9.9.9 = %d (%s)", response.Code, response.Body.String())
	}
	both := decodeAnnouncementSeen(t, getAnnouncementSeen(t, fixture))
	if len(both.SeenAnnouncements) != 2 {
		t.Fatalf("receipts after second release = %+v, want 2 rows", both.SeenAnnouncements)
	}
	byID := map[string]string{}
	for _, receipt := range both.SeenAnnouncements {
		byID[receipt.AnnouncementID] = receipt.SeenAt
	}
	if byID["v1.2.3"] != first.SeenAnnouncements[0].SeenAt || byID["v9.9.9"] == "" {
		t.Fatalf("seeing v9.9.9 disturbed v1.2.3: %+v", both.SeenAnnouncements)
	}

	other := newSessionFixtureWithQueries(t, queries, pool, ctx)
	if second := decodeAnnouncementSeen(t, getAnnouncementSeen(t, other)); len(second.SeenAnnouncements) != 0 {
		t.Fatalf("second user receipts = %+v, want empty", second.SeenAnnouncements)
	}

	for _, id := range []string{".bad", "-bad", "_bad", "has space", strings.Repeat("x", 101)} {
		if response := putAnnouncementSeen(t, fixture, id); response.Code != http.StatusBadRequest {
			t.Errorf("PUT id %q = %d, want 400 (body %s)", id, response.Code, response.Body.String())
		}
	}
	if after := decodeAnnouncementSeen(t, getAnnouncementSeen(t, fixture)); len(after.SeenAnnouncements) != 2 {
		t.Fatalf("rejected writes mutated receipts: %+v", after.SeenAnnouncements)
	}
}
