package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/locationlandmarks"
	"github.com/ps-wizard/revserp/internal/textnormalization"
)

type l4itFixture struct {
	t        *testing.T
	pool     *pgxpool.Pool
	ctx      context.Context
	queries  *sqlc.Queries
	app      *App
	orgID    pgtype.UUID
	ownerID  pgtype.UUID
	otherID  pgtype.UUID
	projAID  pgtype.UUID
	projBID  pgtype.UUID
	locAID   pgtype.UUID
	locBID   pgtype.UUID
	extraLoc []pgtype.UUID
	prefix   string
	appName  string
	runIDs   []string
}

func l4itUUIDsToStrings(ids []pgtype.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}

func l4itOpenPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	raw := os.Getenv("LOCAL_SEO_LAYER4_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("LOCAL_SEO_LAYER4_TEST_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse layer4 test database url failed")
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "localhost" && host != "127.0.0.1" {
		t.Fatalf("refusing layer4 test target host %q, want localhost/127.0.0.1", host)
	}
	if parsed.Port() != "5432" {
		t.Fatalf("refusing layer4 test target port %q, want 5432", parsed.Port())
	}
	if strings.Trim(parsed.Path, "/") != "revserp_app" {
		t.Fatalf("refusing layer4 test target database %q, want revserp_app", strings.Trim(parsed.Path, "/"))
	}
	appName, err := l4itAppName()
	if err != nil {
		t.Fatalf("layer4 test application name: %v", err)
	}
	// Tag every pooled session so lock-wait probes can only ever observe this
	// fixture's own backends, never unrelated sessions on the shared database.
	query := parsed.Query()
	query.Set("application_name", appName)
	parsed.RawQuery = query.Encode()
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, parsed.String(), config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("layer4 test database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, appName
}

func l4itAppName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("l4it-%d-%x", os.Getpid(), b), nil
}

func l4itRandomUUID() (pgtype.UUID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return pgtype.UUID{}, err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return pgtype.UUID{Bytes: b, Valid: true}, nil
}

func l4itSetup(t *testing.T, pool *pgxpool.Pool, appName string) *l4itFixture {
	t.Helper()
	fx, err := l4itSetupFixture(t, pool, appName, -1)
	if err != nil {
		t.Fatalf("layer4-it setup: %v", err)
	}
	return fx
}

// l4itSetupFixture allocates every identifier and registers cleanup before the
// first INSERT, so a fatal or panic midway through setup can never leak phantom
// rows. failAfter >= 0 injects a returned error after that many inserts for the
// partial-cleanup proof; a non-nil fixture is always returned so callers can
// still clean up.
func l4itSetupFixture(t *testing.T, pool *pgxpool.Pool, appName string, failAfter int) (*l4itFixture, error) {
	t.Helper()
	ctx := context.Background()
	queries := sqlc.New(pool)
	prefix := fmt.Sprintf("l4it-%d", time.Now().UnixNano())

	ids := make([]pgtype.UUID, 7)
	for i := range ids {
		id, err := l4itRandomUUID()
		if err != nil {
			return nil, fmt.Errorf("allocate layer4-it fixture id: %w", err)
		}
		ids[i] = id
	}
	orgID, ownerID, otherID := ids[0], ids[1], ids[2]
	projAID, projBID, locAID, locBID := ids[3], ids[4], ids[5], ids[6]

	fx := &l4itFixture{
		t: t, pool: pool, ctx: ctx, queries: queries,
		app:     &App{DB: pool, Queries: queries},
		appName: appName,
		orgID:   orgID,
		ownerID: ownerID,
		otherID: otherID,
		projAID: projAID,
		projBID: projBID,
		locAID:  locAID,
		locBID:  locBID,
		prefix:  prefix,
	}
	t.Cleanup(func() { fx.cleanup() })

	inserts := []func() error{
		func() error {
			_, err := pool.Exec(ctx, `INSERT INTO organizations (id, name) VALUES ($1,$2)`, orgID, prefix)
			return err
		},
		func() error {
			_, err := pool.Exec(ctx, `INSERT INTO users (id, auth_provider, auth_subject, email) VALUES ($1,'layer4-integration-test',$2,$3)`,
				ownerID, prefix+"owner", prefix+"owner@example.com")
			return err
		},
		func() error {
			_, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, ownerID)
			return err
		},
		func() error {
			_, err := pool.Exec(ctx, `INSERT INTO users (id, auth_provider, auth_subject, email) VALUES ($1,'layer4-integration-test',$2,$3)`,
				otherID, prefix+"outsider", prefix+"outsider@example.com")
			return err
		},
		func() error {
			_, err := pool.Exec(ctx, `INSERT INTO projects (id, organization_id, name, base_url) VALUES ($1,$2,$3,'https://layer4-it.example')`,
				projAID, orgID, prefix+"-proj-a")
			return err
		},
		func() error {
			_, err := pool.Exec(ctx, `INSERT INTO projects (id, organization_id, name, base_url) VALUES ($1,$2,$3,'https://layer4-it.example')`,
				projBID, orgID, prefix+"-proj-b")
			return err
		},
		func() error {
			_, err := pool.Exec(ctx, `INSERT INTO project_locations (id, project_id, name, latitude, longitude) VALUES ($1,$2,$3,27.7172,85.3240)`,
				locAID, projAID, prefix+"-loc-a")
			return err
		},
		func() error {
			_, err := pool.Exec(ctx, `INSERT INTO project_locations (id, project_id, name, latitude, longitude) VALUES ($1,$2,$3,27.7172,85.3240)`,
				locBID, projBID, prefix+"-loc-b")
			return err
		},
	}
	for i, insert := range inserts {
		if i == failAfter {
			return fx, fmt.Errorf("layer4-it injected setup failure after %d inserts", failAfter)
		}
		if err := insert(); err != nil {
			return fx, fmt.Errorf("layer4-it setup insert %d: %w", i, err)
		}
	}
	if failAfter >= len(inserts) {
		return fx, fmt.Errorf("layer4-it injected setup failure after %d inserts", failAfter)
	}
	return fx, nil
}

func (fx *l4itFixture) locStrings() []string {
	ids := append([]pgtype.UUID{fx.locAID, fx.locBID}, fx.extraLoc...)
	return l4itUUIDsToStrings(ids)
}

func (fx *l4itFixture) scopedCounts() map[string]int {
	ctx := context.Background()
	out := map[string]int{}
	locs := fx.locStrings()
	projs := l4itUUIDsToStrings([]pgtype.UUID{fx.projAID, fx.projBID})
	queries := []struct {
		key string
		sql string
		arg []string
	}{
		{"project_location_queries", `SELECT count(*) FROM project_location_queries WHERE location_id = ANY($1::uuid[])`, locs},
		{"location_landmarks", `SELECT count(*) FROM location_landmarks WHERE location_id = ANY($1::uuid[])`, locs},
		{"project_location_services", `SELECT count(*) FROM project_location_services WHERE location_id = ANY($1::uuid[])`, locs},
		{"project_services", `SELECT count(*) FROM project_services WHERE project_id = ANY($1::uuid[])`, projs},
		{"local_visibility_runs", `SELECT count(*) FROM local_visibility_runs WHERE location_id = ANY($1::uuid[])`, locs},
	}
	for _, q := range queries {
		var n int
		if err := fx.pool.QueryRow(ctx, q.sql, q.arg).Scan(&n); err != nil {
			out[q.key] = -1
			continue
		}
		out[q.key] = n
	}
	return out
}

type l4itScopedQuery struct {
	sql string
	arg []string
}

type l4itScopedCheck struct {
	key string
	sql string
	arg []string
}

// ownedDeletes removes every row the fixture could have created, derived only
// from preallocated ids so it is safe after any partial setup.
func (fx *l4itFixture) ownedDeletes() []l4itScopedQuery {
	locs := fx.locStrings()
	projs := l4itUUIDsToStrings([]pgtype.UUID{fx.projAID, fx.projBID})
	users := l4itUUIDsToStrings([]pgtype.UUID{fx.ownerID, fx.otherID})
	orgs := []string{fx.orgID.String()}
	return []l4itScopedQuery{
		{`DELETE FROM project_location_queries WHERE location_id = ANY($1::uuid[])`, locs},
		{`DELETE FROM location_landmarks WHERE location_id = ANY($1::uuid[])`, locs},
		{`DELETE FROM project_location_services WHERE location_id = ANY($1::uuid[])`, locs},
		{`DELETE FROM project_services WHERE project_id = ANY($1::uuid[])`, projs},
		{`DELETE FROM local_visibility_runs WHERE location_id = ANY($1::uuid[])`, locs},
		{`DELETE FROM project_locations WHERE id = ANY($1::uuid[])`, locs},
		{`DELETE FROM projects WHERE id = ANY($1::uuid[])`, projs},
		{`DELETE FROM organization_members WHERE user_id = ANY($1::uuid[])`, users},
		{`DELETE FROM users WHERE id = ANY($1::uuid[])`, users},
		{`DELETE FROM organizations WHERE id = ANY($1::uuid[])`, orgs},
	}
}

func (fx *l4itFixture) ownedChecks() []l4itScopedCheck {
	locs := fx.locStrings()
	projs := l4itUUIDsToStrings([]pgtype.UUID{fx.projAID, fx.projBID})
	users := l4itUUIDsToStrings([]pgtype.UUID{fx.ownerID, fx.otherID})
	orgs := []string{fx.orgID.String()}
	return []l4itScopedCheck{
		{"project_location_queries", `SELECT count(*) FROM project_location_queries WHERE location_id = ANY($1::uuid[])`, locs},
		{"location_landmarks", `SELECT count(*) FROM location_landmarks WHERE location_id = ANY($1::uuid[])`, locs},
		{"project_location_services", `SELECT count(*) FROM project_location_services WHERE location_id = ANY($1::uuid[])`, locs},
		{"project_services", `SELECT count(*) FROM project_services WHERE project_id = ANY($1::uuid[])`, projs},
		{"local_visibility_runs", `SELECT count(*) FROM local_visibility_runs WHERE location_id = ANY($1::uuid[])`, locs},
		{"project_locations", `SELECT count(*) FROM project_locations WHERE id = ANY($1::uuid[])`, locs},
		{"projects", `SELECT count(*) FROM projects WHERE id = ANY($1::uuid[])`, projs},
		{"organization_members", `SELECT count(*) FROM organization_members WHERE user_id = ANY($1::uuid[])`, users},
		{"users", `SELECT count(*) FROM users WHERE id = ANY($1::uuid[])`, users},
		{"organizations", `SELECT count(*) FROM organizations WHERE id = ANY($1::uuid[])`, orgs},
	}
}

func (fx *l4itFixture) ownedCounts(ctx context.Context) map[string]int {
	out := make(map[string]int, 10)
	for _, c := range fx.ownedChecks() {
		var n int
		if err := fx.pool.QueryRow(ctx, c.sql, c.arg).Scan(&n); err != nil {
			out[c.key] = -1
			continue
		}
		out[c.key] = n
	}
	return out
}

func (fx *l4itFixture) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, s := range fx.ownedDeletes() {
		if _, err := fx.pool.Exec(ctx, s.sql, s.arg); err != nil {
			fx.t.Errorf("layer4-it cleanup delete: %v", err)
		}
	}
	counts := fx.ownedCounts(ctx)
	for _, c := range fx.ownedChecks() {
		n := counts[c.key]
		if n < 0 {
			fx.t.Errorf("layer4-it cleanup verify %s: query failed", c.key)
			continue
		}
		if n != 0 {
			fx.t.Errorf("layer4-it cleanup leftover %s rows = %d, want 0", c.key, n)
			continue
		}
		fx.t.Logf("layer4-it cleanup %s rows = 0", c.key)
	}
}

func l4itPgxCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func l4itCreateService(t *testing.T, fx *l4itFixture, projectID pgtype.UUID, label string) sqlc.ProjectService {
	t.Helper()
	display, key, err := layer4ServiceLabelKey(label)
	if err != nil {
		t.Fatalf("label key: %v", err)
	}
	service, err := fx.queries.CreateProjectServiceForUser(fx.ctx, sqlc.CreateProjectServiceForUserParams{
		Label: display, NormalizedLabel: key, ProjectID: projectID, UserID: fx.ownerID,
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	return service
}

func l4itInsertQuery(t *testing.T, fx *l4itFixture, loc, proj pgtype.UUID, text, kind, source, origin string, ordinal int32, landmark pgtype.UUID) sqlc.ProjectLocationQuery {
	t.Helper()
	row, err := fx.queries.InsertProjectLocationQueryForUser(fx.ctx, sqlc.InsertProjectLocationQueryForUserParams{
		Text: text, Normalized: textnormalization.NormalizeTextKey(text), Ordinal: ordinal,
		Enabled: true, Kind: kind, Source: source, Origin: origin,
		LandmarkID: landmark, LocationID: loc, ProjectID: proj, UserID: fx.ownerID,
	})
	if err != nil {
		t.Fatalf("insert query: %v", err)
	}
	return row
}

func l4itUpsertLandmark(t *testing.T, fx *l4itFixture, loc, proj pgtype.UUID, ref, name string) sqlc.LocationLandmark {
	t.Helper()
	row, err := fx.queries.UpsertLocationLandmarkForUser(fx.ctx, sqlc.UpsertLocationLandmarkForUserParams{
		Name: name, Latitude: 27.7172, Longitude: 85.3240, StraightLineM: 400,
		Provider: "google_places", ProviderRef: ref, Categories: []string{"amenity"},
		FetchedAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
		LocationID: loc, ProjectID: proj, UserID: fx.ownerID,
	})
	if err != nil {
		t.Fatalf("upsert landmark: %v", err)
	}
	return row
}

func TestLocationLayer4Integration(t *testing.T) {
	pool, appName := l4itOpenPool(t)
	fx := l4itSetup(t, pool, appName)
	t.Logf("layer4-it start counts: %v", fx.scopedCounts())

	t.Run("servicesOverridesDraftQueriesLandmarks", func(t *testing.T) {
		svcA := l4itCreateService(t, fx, fx.projAID, fx.prefix+" Plumbing")
		renamed, err := fx.queries.RenameProjectServiceForUser(fx.ctx, sqlc.RenameProjectServiceForUserParams{
			Label: "Renamed " + fx.prefix, NormalizedLabel: textnormalization.NormalizeTextKey("Renamed " + fx.prefix),
			ServiceID: svcA.ID, ProjectID: fx.projAID, UserID: fx.ownerID,
		})
		if err != nil {
			t.Fatalf("rename service: %v", err)
		}
		services, err := fx.queries.ListProjectServicesForUser(fx.ctx, sqlc.ListProjectServicesForUserParams{ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(services) != 1 || services[0].Label != renamed.Label {
			t.Fatalf("list services = %d rows err=%v", len(services), err)
		}
		editor, err := fx.queries.ListProjectLocationServiceEditorRowsForUser(fx.ctx, sqlc.ListProjectLocationServiceEditorRowsForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(editor) != 1 || editor[0].Excluded {
			t.Fatalf("editor rows = %+v err=%v", editor, err)
		}
		only, err := fx.queries.ListProjectLocationOnlyServiceLabelsForUser(fx.ctx, sqlc.ListProjectLocationOnlyServiceLabelsForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(only) != 0 {
			t.Fatalf("location-only = %d err=%v", len(only), err)
		}
		effective, err := fx.queries.ListEffectiveProjectLocationServiceLabelsForUser(fx.ctx, sqlc.ListEffectiveProjectLocationServiceLabelsForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(effective) != 1 {
			t.Fatalf("effective = %d err=%v", len(effective), err)
		}
		excluded, err := fx.queries.InsertProjectLocationServiceOverrideForUser(fx.ctx, sqlc.InsertProjectLocationServiceOverrideForUserParams{
			ServiceID: svcA.ID, Mode: "exclude", LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil {
			t.Fatalf("insert exclude override: %v", err)
		}
		if excluded.Mode != "exclude" {
			t.Fatalf("override mode = %q", excluded.Mode)
		}
		effective, err = fx.queries.ListEffectiveProjectLocationServiceLabelsForUser(fx.ctx, sqlc.ListEffectiveProjectLocationServiceLabelsForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(effective) != 0 {
			t.Fatalf("effective after exclude = %d err=%v", len(effective), err)
		}
		cleared, err := fx.queries.DeleteProjectLocationServiceOverridesForUser(fx.ctx, sqlc.DeleteProjectLocationServiceOverridesForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || cleared != 1 {
			t.Fatalf("delete overrides affected = %d err=%v", cleared, err)
		}
		deleted, err := fx.queries.DeleteProjectServiceForUser(fx.ctx, sqlc.DeleteProjectServiceForUserParams{
			ServiceID: svcA.ID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || deleted != 1 {
			t.Fatalf("delete service affected = %d err=%v", deleted, err)
		}

		q1 := l4itInsertQuery(t, fx, fx.locAID, fx.projAID, fx.prefix+" plumber map", "map", "manual", "service", 0, pgtype.UUID{})
		q2 := l4itInsertQuery(t, fx, fx.locAID, fx.projAID, fx.prefix+" plumber ai", "ai_question", "generated", "landmark", 0, pgtype.UUID{})
		listed, err := fx.queries.ListProjectLocationQueriesForUser(fx.ctx, sqlc.ListProjectLocationQueriesForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(listed) != 2 {
			t.Fatalf("list queries = %d err=%v", len(listed), err)
		}
		locked, err := fx.queries.LockProjectLocationQueriesForUser(fx.ctx, sqlc.LockProjectLocationQueriesForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(locked) != 2 {
			t.Fatalf("lock queries = %d err=%v", len(locked), err)
		}
		enabled, err := fx.queries.ListEnabledMapQueriesForUser(fx.ctx, sqlc.ListEnabledMapQueriesForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(enabled) != 1 {
			t.Fatalf("enabled map queries = %d err=%v", len(enabled), err)
		}
		updated, err := fx.queries.UpdateProjectLocationQueryForUser(fx.ctx, sqlc.UpdateProjectLocationQueryForUserParams{
			Text: fx.prefix + " plumber map v2", Normalized: textnormalization.NormalizeTextKey(fx.prefix + " plumber map v2"),
			Ordinal: 0, Enabled: true, Kind: "map", Source: "manual", Origin: "service",
			LandmarkID: pgtype.UUID{}, ID: q1.ID, LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || updated.Text != fx.prefix+" plumber map v2" {
			t.Fatalf("update query = %+v err=%v", updated, err)
		}
		if _, err := fx.queries.UpdateProjectLocationQueryForUser(fx.ctx, sqlc.UpdateProjectLocationQueryForUserParams{
			Text: "x", Normalized: "x", Ordinal: 0, Enabled: true, Kind: "map", Source: "manual", Origin: "service",
			LandmarkID: pgtype.UUID{}, ID: q1.ID, LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("cross-route update err = %v, want no rows", err)
		}
		disabled, err := fx.queries.DisableProjectLocationQueryForUser(fx.ctx, sqlc.DisableProjectLocationQueryForUserParams{
			ID: q2.ID, LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || disabled.Enabled {
			t.Fatalf("disable generated = %+v err=%v", disabled, err)
		}
		if _, err := fx.queries.DisableProjectLocationQueryForUser(fx.ctx, sqlc.DisableProjectLocationQueryForUserParams{
			ID: q1.ID, LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("disable manual err = %v, want no rows", err)
		}
		gen, err := fx.queries.InsertMissingGeneratedProjectLocationQueryForUser(fx.ctx, sqlc.InsertMissingGeneratedProjectLocationQueryForUserParams{
			Text: fx.prefix + " generated", Normalized: textnormalization.NormalizeTextKey(fx.prefix + " generated"),
			Ordinal: 1, Kind: "map", Origin: "locality", LandmarkID: pgtype.UUID{},
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil {
			t.Fatalf("insert missing generated: %v", err)
		}
		if gen.Source != "generated" {
			t.Fatalf("missing generated source = %q", gen.Source)
		}
		pruned, err := fx.queries.DeleteObsoleteGeneratedProjectLocationQueryForUser(fx.ctx, sqlc.DeleteObsoleteGeneratedProjectLocationQueryForUserParams{
			ProjectID: fx.projAID, LocationID: fx.locAID, Kind: "map",
			Normalized: textnormalization.NormalizeTextKey(fx.prefix + " generated"), UserID: fx.ownerID})
		if err != nil || pruned != 1 {
			t.Fatalf("delete obsolete affected = %d err=%v", pruned, err)
		}
		removed, err := fx.queries.DeleteProjectLocationQueryForUser(fx.ctx, sqlc.DeleteProjectLocationQueryForUserParams{
			ID: q2.ID, LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || removed != 0 {
			t.Fatalf("delete generated affected = %d err=%v, want 0", removed, err)
		}
		removed, err = fx.queries.DeleteProjectLocationQueryForUser(fx.ctx, sqlc.DeleteProjectLocationQueryForUserParams{
			ID: q1.ID, LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || removed != 1 {
			t.Fatalf("delete manual affected = %d err=%v", removed, err)
		}
		if _, err := fx.queries.DeleteProjectLocationQueryForUser(fx.ctx, sqlc.DeleteProjectLocationQueryForUserParams{
			ID: q2.ID, LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID}); err != nil {
			t.Fatalf("cross-route delete: %v", err)
		} else if n, _ := fx.queries.ListProjectLocationQueriesForUser(fx.ctx, sqlc.ListProjectLocationQueriesForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); len(n) != 1 {
			t.Fatalf("sibling queries = %d, want 1", len(n))
		}
		pruned, err = fx.queries.DeleteObsoleteGeneratedProjectLocationQueryForUser(fx.ctx, sqlc.DeleteObsoleteGeneratedProjectLocationQueryForUserParams{
			ProjectID: fx.projAID, LocationID: fx.locAID, Kind: "ai_question",
			Normalized: textnormalization.NormalizeTextKey(fx.prefix + " plumber ai"), UserID: fx.ownerID})
		if err != nil || pruned != 1 {
			t.Fatalf("delete remaining generated affected = %d err=%v", pruned, err)
		}
		if n, _ := fx.queries.ListProjectLocationQueriesForUser(fx.ctx, sqlc.ListProjectLocationQueriesForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); len(n) != 0 {
			t.Fatalf("draft queries after cleanup = %d, want 0", len(n))
		}

		lm := l4itUpsertLandmark(t, fx, fx.locAID, fx.projAID, fx.prefix+"-ref-1", fx.prefix+" Cafe")
		listed2, err := fx.queries.ListLocationLandmarksForUser(fx.ctx, sqlc.ListLocationLandmarksForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(listed2) != 1 {
			t.Fatalf("list landmarks = %d err=%v", len(listed2), err)
		}
		sel, err := fx.queries.UpdateLocationLandmarkSelectionForUser(fx.ctx, sqlc.UpdateLocationLandmarkSelectionForUserParams{
			Selected: true, ID: lm.ID, LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || !sel.Selected {
			t.Fatalf("select landmark = %+v err=%v", sel, err)
		}
		if _, err := fx.queries.UpdateLocationLandmarkSelectionForUser(fx.ctx, sqlc.UpdateLocationLandmarkSelectionForUserParams{
			Selected: false, ID: lm.ID, LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("cross-route selection err = %v, want no rows", err)
		}
		lockedLoc, err := fx.queries.LockProjectLocationForLandmarkRefreshForUser(fx.ctx, sqlc.LockProjectLocationForLandmarkRefreshForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || lockedLoc.ID != fx.locAID {
			t.Fatalf("lock location for refresh: %v", err)
		}
		kept, err := fx.queries.DeleteRemovedLocationLandmarksForUser(fx.ctx, sqlc.DeleteRemovedLocationLandmarksForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID, KeepProviderRefs: []string{fx.prefix + "-ref-1"}})
		if err != nil || kept != 0 {
			t.Fatalf("prune kept affected = %d err=%v", kept, err)
		}

		outsiderQueries, err := fx.queries.ListProjectLocationQueriesForUser(fx.ctx, sqlc.ListProjectLocationQueriesForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.otherID})
		if err != nil || len(outsiderQueries) != 0 {
			t.Fatalf("outsider queries = %d err=%v", len(outsiderQueries), err)
		}
		if _, err := fx.queries.InsertProjectLocationQueryForUser(fx.ctx, sqlc.InsertProjectLocationQueryForUserParams{
			Text: "x", Normalized: "x", Ordinal: 9, Enabled: true, Kind: "map", Source: "manual", Origin: "service",
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.otherID}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("outsider insert err = %v, want no rows", err)
		}
		if _, err := fx.queries.LockProjectLocationForQueryDraftForUser(fx.ctx, sqlc.LockProjectLocationForQueryDraftForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.otherID}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("outsider draft lock err = %v, want no rows", err)
		}

		o1 := l4itInsertQuery(t, fx, fx.locAID, fx.projAID, fx.prefix+" ordinal one", "map", "manual", "service", 0, pgtype.UUID{})
		o2 := l4itInsertQuery(t, fx, fx.locAID, fx.projAID, fx.prefix+" ordinal two", "map", "manual", "service", 1, pgtype.UUID{})
		func() {
			tx, err := pool.Begin(fx.ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback(fx.ctx) }()
			if _, err := tx.Exec(fx.ctx, `UPDATE project_location_queries SET ordinal = 1 WHERE id = $1`, o1.ID); err == nil {
				t.Fatalf("immediate ordinal collision must fail")
			} else if l4itPgxCode(err) != "23505" {
				t.Fatalf("collision code = %q, want 23505", l4itPgxCode(err))
			}
		}()
		func() {
			tx, err := pool.Begin(fx.ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback(fx.ctx) }()
			if _, err := tx.Exec(fx.ctx, `SET CONSTRAINTS project_location_queries_location_id_kind_ordinal_key DEFERRED`); err != nil {
				t.Fatalf("defer ordinal constraint: %v", err)
			}
			if _, err := tx.Exec(fx.ctx, `UPDATE project_location_queries SET ordinal = 99 WHERE id = $1`, o1.ID); err != nil {
				t.Fatalf("stage ordinal: %v", err)
			}
			if _, err := tx.Exec(fx.ctx, `UPDATE project_location_queries SET ordinal = 0 WHERE id = $1`, o2.ID); err != nil {
				t.Fatalf("swap ordinal: %v", err)
			}
			if _, err := tx.Exec(fx.ctx, `UPDATE project_location_queries SET ordinal = 1 WHERE id = $1`, o1.ID); err != nil {
				t.Fatalf("finish swap: %v", err)
			}
			if err := tx.Commit(fx.ctx); err != nil {
				t.Fatalf("commit ordinal swap: %v", err)
			}
		}()
		final, err := fx.queries.ListProjectLocationQueriesForUser(fx.ctx, sqlc.ListProjectLocationQueriesForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(final) != 2 {
			t.Fatalf("post-swap queries = %d err=%v", len(final), err)
		}
		for _, row := range []sqlc.ProjectLocationQuery{o1, o2} {
			if _, err := fx.queries.DeleteProjectLocationQueryForUser(fx.ctx, sqlc.DeleteProjectLocationQueryForUserParams{
				ID: row.ID, LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); err != nil {
				t.Fatalf("delete ordinal row: %v", err)
			}
		}

		var runID pgtype.UUID
		if err := pool.QueryRow(fx.ctx, `INSERT INTO local_visibility_runs (location_id, status, radius_m, snapshot, expected_credits, reserved_credits, credits_used)
			VALUES ($1,'completed',5000,'{}',5,0,5) RETURNING id`, fx.locAID).Scan(&runID); err != nil {
			t.Fatalf("insert run fixture: %v", err)
		}
		fx.runIDs = append(fx.runIDs, runID.String())
		var settled int
		if err := pool.QueryRow(fx.ctx, `SELECT count(*) FROM local_visibility_runs WHERE location_id = $1 AND status = 'completed' AND reserved_credits = 0`, fx.locAID).Scan(&settled); err != nil || settled != 1 {
			t.Fatalf("settled run count = %d err=%v", settled, err)
		}
		t.Logf("layer4-it proof1 settled fixture runs: %d", settled)
	})

	t.Run("effectiveDefaultExcludeLocalOnly", func(t *testing.T) {
		svcDefault := l4itCreateService(t, fx, fx.projAID, fx.prefix+" ModelA")
		svcExcluded := l4itCreateService(t, fx, fx.projAID, fx.prefix+" Excluded")
		l4itCreateService(t, fx, fx.projBID, fx.prefix+" Other Project")
		if got := textnormalization.NormalizeTextKey("L4IT\u00a0Deep  Clean"); got != "l4it deep clean" {
			t.Fatalf("nbsp canon = %q", got)
		}
		if _, err := fx.queries.InsertProjectLocationServiceOverrideForUser(fx.ctx, sqlc.InsertProjectLocationServiceOverrideForUserParams{
			ServiceID: svcExcluded.ID, Mode: "exclude", LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); err != nil {
			t.Fatalf("exclude: %v", err)
		}
		if _, err := fx.queries.InsertProjectLocationServiceOverrideForUser(fx.ctx, sqlc.InsertProjectLocationServiceOverrideForUserParams{
			ServiceID: svcDefault.ID, Mode: "include", LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); err != nil {
			t.Fatalf("explicit include: %v", err)
		}
		display, key, _ := layer4ServiceLabelKey("L4IT\u00a0Only Service")
		if _, err := fx.queries.InsertProjectLocationServiceOverrideForUser(fx.ctx, sqlc.InsertProjectLocationServiceOverrideForUserParams{
			ServiceLabel:           pgtype.Text{String: display, Valid: true},
			NormalizedServiceLabel: pgtype.Text{String: key, Valid: true},
			Mode:                   "include", LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); err != nil {
			t.Fatalf("location-only include: %v", err)
		}
		effective, err := fx.queries.ListEffectiveProjectLocationServiceLabelsForUser(fx.ctx, sqlc.ListEffectiveProjectLocationServiceLabelsForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil {
			t.Fatalf("effective: %v", err)
		}
		labels := map[string]bool{}
		for _, row := range effective {
			labels[row.Label] = true
		}
		if !labels[svcDefault.Label] || !labels[display] {
			t.Fatalf("effective missing default include or location-only: %v", labels)
		}
		if labels[svcExcluded.Label] {
			t.Fatalf("effective contains excluded: %v", labels)
		}
		for label := range labels {
			if strings.Contains(label, "Other Project") {
				t.Fatalf("cross-project leak: %v", labels)
			}
		}
		req := localVisibilityRequest(t, http.MethodGet, fx.ownerID,
			map[string]string{"projectID": fx.projAID.String(), "locationID": fx.locAID.String()}, "")
		rr := httptest.NewRecorder()
		fx.app.handleGetProjectLocationServices(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("handler services status = %d body=%s", rr.Code, rr.Body.String())
		}
		var body struct {
			Effective    []string `json:"effective"`
			LocationOnly []string `json:"location_only"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode services: %v", err)
		}
		if len(body.Effective) != len(effective) || len(body.LocationOnly) != 1 {
			t.Fatalf("handler effective = %d location_only = %d", len(body.Effective), len(body.LocationOnly))
		}
	})

	t.Run("foreignServiceRollbackAndLandmarkGuards", func(t *testing.T) {
		seedSvc := l4itCreateService(t, fx, fx.projAID, fx.prefix+" Seed")
		if _, err := fx.queries.InsertProjectLocationServiceOverrideForUser(fx.ctx, sqlc.InsertProjectLocationServiceOverrideForUserParams{
			ServiceID: seedSvc.ID, Mode: "exclude", LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); err != nil {
			t.Fatalf("seed override: %v", err)
		}
		foreignSvc := l4itCreateService(t, fx, fx.projBID, fx.prefix+" Foreign")
		putBody := fmt.Sprintf(`{"overrides":[{"service_id":%q,"mode":"include"}]}`, foreignSvc.ID.String())
		req := localVisibilityRequest(t, http.MethodPut, fx.ownerID,
			map[string]string{"projectID": fx.projAID.String(), "locationID": fx.locAID.String()}, putBody)
		rr := httptest.NewRecorder()
		fx.app.handlePutProjectLocationServices(rr, req)
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "unknown service") {
			t.Fatalf("foreign put status = %d body=%s, want 422 unknown service", rr.Code, rr.Body.String())
		}
		remaining, err := fx.queries.ListProjectLocationServiceEditorRowsForUser(fx.ctx, sqlc.ListProjectLocationServiceEditorRowsForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil {
			t.Fatalf("editor after rollback: %v", err)
		}
		kept := 0
		for _, row := range remaining {
			if row.ServiceID == seedSvc.ID && row.Excluded {
				kept++
			}
			if row.ServiceID == foreignSvc.ID {
				t.Fatalf("foreign override persisted after rollback")
			}
		}
		if kept != 1 {
			t.Fatalf("seed override lost, rows = %+v", remaining)
		}
		func() {
			tx, err := pool.Begin(fx.ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback(fx.ctx) }()
			qtx := fx.queries.WithTx(tx)
			if _, err := qtx.LockProjectLocationForQueryDraftForUser(fx.ctx, sqlc.LockProjectLocationForQueryDraftForUserParams{
				LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); err != nil {
				t.Fatalf("tx lock: %v", err)
			}
			if _, err := qtx.InsertProjectLocationServiceOverrideForUser(fx.ctx, sqlc.InsertProjectLocationServiceOverrideForUserParams{
				ServiceID: foreignSvc.ID, Mode: "include", LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); l4itPgxCode(err) != "23503" {
				t.Fatalf("tx foreign insert code = %q err=%v, want 23503", l4itPgxCode(err), err)
			}
		}()
		after, err := fx.queries.ListProjectLocationServiceEditorRowsForUser(fx.ctx, sqlc.ListProjectLocationServiceEditorRowsForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(after) != len(remaining) {
			t.Fatalf("override count changed by failed tx: %d vs %d", len(after), len(remaining))
		}

		lmA := l4itUpsertLandmark(t, fx, fx.locAID, fx.projAID, fx.prefix+"-ref-guard", fx.prefix+" Guard")
		lmB := l4itUpsertLandmark(t, fx, fx.locBID, fx.projBID, fx.prefix+"-ref-other", fx.prefix+" Other")
		sibling := l4itInsertQuery(t, fx, fx.locAID, fx.projAID, fx.prefix+" sibling", "map", "manual", "service", 0, pgtype.UUID{})
		if _, err := fx.queries.InsertProjectLocationQueryForUser(fx.ctx, sqlc.InsertProjectLocationQueryForUserParams{
			Text: fx.prefix + " bad link", Normalized: textnormalization.NormalizeTextKey(fx.prefix + " bad link"),
			Ordinal: 1, Enabled: true, Kind: "map", Source: "manual", Origin: "landmark",
			LandmarkID: lmB.ID, LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("wrong-landmark insert err = %v, want no rows", err)
		}
		linked := l4itInsertQuery(t, fx, fx.locAID, fx.projAID, fx.prefix+" linked", "map", "manual", "landmark", 1, lmA.ID)
		if !linked.LandmarkID.Valid || linked.LandmarkID != lmA.ID {
			t.Fatalf("linked landmark = %+v", linked.LandmarkID)
		}
		if _, err := fx.queries.UpdateProjectLocationQueryForUser(fx.ctx, sqlc.UpdateProjectLocationQueryForUserParams{
			Text: fx.prefix + " hijack", Normalized: textnormalization.NormalizeTextKey(fx.prefix + " hijack"),
			Ordinal: 0, Enabled: true, Kind: "map", Source: "manual", Origin: "service",
			LandmarkID: pgtype.UUID{}, ID: sibling.ID, LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("cross-route sibling update err = %v, want no rows", err)
		}
		check, err := fx.queries.ListProjectLocationQueriesForUser(fx.ctx, sqlc.ListProjectLocationQueriesForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(check) != 2 || check[0].Text != fx.prefix+" sibling" {
			t.Fatalf("sibling mutated: %+v err=%v", check, err)
		}
		for _, row := range check {
			if _, err := fx.queries.DeleteProjectLocationQueryForUser(fx.ctx, sqlc.DeleteProjectLocationQueryForUserParams{
				ID: row.ID, LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); err != nil {
				t.Fatalf("cleanup query: %v", err)
			}
		}
	})

	t.Run("landmarkCacheRefresh", func(t *testing.T) {
		before, err := fx.queries.ListLocationLandmarksForUser(fx.ctx, sqlc.ListLocationLandmarksForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil {
			t.Fatalf("list before: %v", err)
		}
		beforeSelected := map[string]bool{}
		for _, row := range before {
			beforeSelected[row.ID.String()] = row.Selected
		}
		ref := fx.prefix + "-ref-cache"
		first := l4itUpsertLandmark(t, fx, fx.locAID, fx.projAID, ref, fx.prefix+" Cache One")
		if _, err := fx.queries.UpdateLocationLandmarkSelectionForUser(fx.ctx, sqlc.UpdateLocationLandmarkSelectionForUserParams{
			Selected: true, ID: first.ID, LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); err != nil {
			t.Fatalf("select: %v", err)
		}
		second, err := fx.queries.UpsertLocationLandmarkForUser(fx.ctx, sqlc.UpsertLocationLandmarkForUserParams{
			Name: fx.prefix + " Cache One Moved", Latitude: 27.718, Longitude: 85.325, StraightLineM: 410,
			Provider: "google_places", ProviderRef: ref, Categories: []string{"museum"},
			FetchedAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || second.ID != first.ID || !second.Selected || second.Name != fx.prefix+" Cache One Moved" {
			t.Fatalf("re-upsert = %+v err=%v, want same id selected preserved", second, err)
		}
		other := l4itUpsertLandmark(t, fx, fx.locAID, fx.projAID, fx.prefix+"-ref-prune", fx.prefix+" Prune Me")
		pruned, err := fx.queries.DeleteRemovedLocationLandmarksForUser(fx.ctx, sqlc.DeleteRemovedLocationLandmarksForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID,
			KeepProviderRefs: []string{ref, fx.prefix + "-ref-1", fx.prefix + "-ref-guard"}})
		if err != nil || pruned < 1 {
			t.Fatalf("prune affected = %d err=%v", pruned, err)
		}
		_ = other
		kept, err := fx.queries.ListLocationLandmarksForUser(fx.ctx, sqlc.ListLocationLandmarksForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil {
			t.Fatalf("list kept: %v", err)
		}
		for _, row := range kept {
			if row.ProviderRef == ref && !row.Selected {
				t.Fatalf("matched selected flag lost on prune survivor")
			}
		}
		snapshot, err := fx.queries.ListLocationLandmarksForUser(fx.ctx, sqlc.ListLocationLandmarksForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		func() {
			tx, err := pool.Begin(fx.ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback(fx.ctx) }()
			qtx := fx.queries.WithTx(tx)
			if _, err := qtx.UpsertLocationLandmarkForUser(fx.ctx, sqlc.UpsertLocationLandmarkForUserParams{
				Name: "mid-refresh", Latitude: 27.7, Longitude: 85.3, StraightLineM: 1,
				Provider: "google_places", ProviderRef: fx.prefix + "-ref-midfail", Categories: []string{},
				FetchedAt:  pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
				LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID}); err != nil {
				t.Fatalf("mid-refresh upsert: %v", err)
			}
			if _, err := qtx.DeleteRemovedLocationLandmarksForUser(fx.ctx, sqlc.DeleteRemovedLocationLandmarksForUserParams{
				LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID,
				KeepProviderRefs: []string{fx.prefix + "-ref-midfail"}}); err != nil {
				t.Fatalf("mid-refresh prune: %v", err)
			}
		}()
		afterFail, err := fx.queries.ListLocationLandmarksForUser(fx.ctx, sqlc.ListLocationLandmarksForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(afterFail) != len(snapshot) {
			t.Fatalf("rollback changed cache: %d vs %d err=%v", len(afterFail), len(snapshot), err)
		}
		for i := range snapshot {
			if snapshot[i].ID != afterFail[i].ID || snapshot[i].Selected != afterFail[i].Selected {
				t.Fatalf("rollback changed selection state")
			}
		}

		outage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer outage.Close()
		broken := locationlandmarks.NewClient(locationlandmarks.Config{APIKey: "test-key", BaseURL: outage.URL})
		if _, err := broken.Discover(fx.ctx, 27.7172, 85.3240); err == nil {
			t.Fatalf("fake outage must fail discovery")
		}
		if _, err := pool.Exec(fx.ctx, `UPDATE project_locations SET place_id = $1 WHERE id = $2`, fx.prefix+"-synth-place", fx.locAID); err != nil {
			t.Fatalf("bind synthetic place: %v", err)
		}
		outageApp := &App{DB: pool, Queries: fx.queries, Landmarks: broken}
		req := localVisibilityRequest(t, http.MethodPost, fx.ownerID,
			map[string]string{"projectID": fx.projAID.String(), "locationID": fx.locAID.String()}, "")
		rr := httptest.NewRecorder()
		outageApp.handleRefreshProjectLocationLandmarks(rr, req)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("outage refresh status = %d body=%s, want 502", rr.Code, rr.Body.String())
		}
		postOutage, err := fx.queries.ListLocationLandmarksForUser(fx.ctx, sqlc.ListLocationLandmarksForUserParams{
			LocationID: fx.locAID, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(postOutage) != len(afterFail) {
			t.Fatalf("outage wrote cache: %d vs %d", len(postOutage), len(afterFail))
		}

		selBody, _ := json.Marshal(map[string]any{"selected_ids": []string{"00000000-0000-0000-0000-000000000000"}})
		selReq := localVisibilityRequest(t, http.MethodPut, fx.ownerID,
			map[string]string{"projectID": fx.projAID.String(), "locationID": fx.locAID.String()}, string(selBody))
		selRR := httptest.NewRecorder()
		fx.app.handlePutProjectLocationLandmarkSelection(selRR, selReq)
		if selRR.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unknown landmark status = %d, want 422", selRR.Code)
		}

		manual := l4itInsertQuery(t, fx, fx.locBID, fx.projBID, fx.prefix+" manual edit", "map", "manual", "service", 0, pgtype.UUID{})
		lmLink := l4itUpsertLandmark(t, fx, fx.locBID, fx.projBID, fx.prefix+"-ref-link", fx.prefix+" Link")
		if _, err := fx.queries.UpdateProjectLocationQueryForUser(fx.ctx, sqlc.UpdateProjectLocationQueryForUserParams{
			Text: fx.prefix + " manual edited", Normalized: textnormalization.NormalizeTextKey(fx.prefix + " manual edited"),
			Ordinal: 0, Enabled: true, Kind: "map", Source: "manual", Origin: "landmark",
			LandmarkID: lmLink.ID, ID: manual.ID, LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID}); err != nil {
			t.Fatalf("link manual: %v", err)
		}
		if _, err := fx.queries.DeleteRemovedLocationLandmarksForUser(fx.ctx, sqlc.DeleteRemovedLocationLandmarksForUserParams{
			LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID, KeepProviderRefs: []string{}}); err != nil {
			t.Fatalf("remove landmark: %v", err)
		}
		keptQueries, err := fx.queries.ListProjectLocationQueriesForUser(fx.ctx, sqlc.ListProjectLocationQueriesForUserParams{
			LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID})
		if err != nil || len(keptQueries) != 1 || keptQueries[0].Text != fx.prefix+" manual edited" || keptQueries[0].LandmarkID.Valid {
			t.Fatalf("null link did not preserve edited manual: %+v err=%v", keptQueries, err)
		}
		if _, err := fx.queries.DeleteProjectLocationQueryForUser(fx.ctx, sqlc.DeleteProjectLocationQueryForUserParams{
			ID: keptQueries[0].ID, LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID}); err != nil {
			t.Fatalf("cleanup null-link query: %v", err)
		}

		var raceLoc pgtype.UUID
		if err := pool.QueryRow(fx.ctx, `INSERT INTO project_locations (project_id, name, latitude, longitude, place_id)
			VALUES ($1,$2,27.7172,85.3240,$3) RETURNING id`, fx.projAID, fx.prefix+"-race", fx.prefix+"-race-place").Scan(&raceLoc); err != nil {
			t.Fatalf("race location: %v", err)
		}
		fx.extraLoc = append(fx.extraLoc, raceLoc)
		raceBefore, err := fx.queries.GetProjectLocationForUser(fx.ctx, sqlc.GetProjectLocationForUserParams{
			ID: raceLoc, ID_2: fx.projAID, UserID: fx.ownerID})
		if err != nil {
			t.Fatalf("race read: %v", err)
		}
		txA, err := pool.Begin(fx.ctx)
		if err != nil {
			t.Fatalf("begin race tx: %v", err)
		}
		qa := fx.queries.WithTx(txA)
		if _, err := qa.LockProjectLocationForLandmarkRefreshForUser(fx.ctx, sqlc.LockProjectLocationForLandmarkRefreshForUserParams{
			LocationID: raceLoc, ProjectID: fx.projAID, UserID: fx.ownerID}); err != nil {
			_ = txA.Rollback(fx.ctx)
			t.Fatalf("race lock: %v", err)
		}
		if _, err := txA.Exec(fx.ctx, `UPDATE project_locations SET latitude = 27.7300, longitude = 85.3400 WHERE id = $1`, raceLoc); err != nil {
			_ = txA.Rollback(fx.ctx)
			t.Fatalf("race mutate: %v", err)
		}
		emptyDiscovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"places":[]}`))
		}))
		defer emptyDiscovery.Close()
		raceApp := &App{DB: pool, Queries: fx.queries,
			Landmarks: locationlandmarks.NewClient(locationlandmarks.Config{APIKey: "test-key", BaseURL: emptyDiscovery.URL})}
		done := make(chan int, 1)
		go func() {
			r := localVisibilityRequest(t, http.MethodPost, fx.ownerID,
				map[string]string{"projectID": fx.projAID.String(), "locationID": raceLoc.String()}, "")
			rec := httptest.NewRecorder()
			raceApp.handleRefreshProjectLocationLandmarks(rec, r)
			done <- rec.Code
		}()
		deadline := time.Now().Add(8 * time.Second)
		waiter := false
		for time.Now().Before(deadline) {
			var n int
			if err := pool.QueryRow(fx.ctx, `SELECT count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND application_name = $1
				AND wait_event_type = 'Lock' AND query ILIKE '%FOR NO KEY UPDATE OF l%'`, fx.appName).Scan(&n); err == nil && n > 0 {
				waiter = true
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !waiter {
			_ = txA.Rollback(fx.ctx)
			t.Fatalf("refresh handler never blocked on the held row lock (application_name %q)", fx.appName)
		}
		if err := txA.Commit(fx.ctx); err != nil {
			t.Fatalf("commit race tx: %v", err)
		}
		var status int
		select {
		case status = <-done:
		case <-time.After(15 * time.Second):
			t.Fatalf("refresh handler did not return after lock release")
		}
		if status != http.StatusConflict {
			t.Fatalf("rebind race status = %d, want 409", status)
		}
		raceAfter, err := fx.queries.ListLocationLandmarksForUser(fx.ctx, sqlc.ListLocationLandmarksForUserParams{
			LocationID: raceLoc, ProjectID: fx.projAID, UserID: fx.ownerID})
		if err != nil || len(raceAfter) != 0 {
			t.Fatalf("race wrote cache: %d err=%v", len(raceAfter), err)
		}
		if _, err := pool.Exec(fx.ctx, `UPDATE project_locations SET latitude = $1, longitude = $2 WHERE id = $3`,
			raceBefore.Latitude, raceBefore.Longitude, raceLoc); err != nil {
			t.Fatalf("restore race coords: %v", err)
		}
	})

	t.Run("emptyDraftConcurrency", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(fx.ctx, 30*time.Second)
		defer cancel()
		tx1, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin tx1: %v", err)
		}
		defer func() { _ = tx1.Rollback(ctx) }()
		q1 := sqlc.New(tx1)
		if _, err := q1.LockProjectLocationForQueryDraftForUser(ctx, sqlc.LockProjectLocationForQueryDraftForUserParams{
			LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID}); err != nil {
			t.Fatalf("tx1 draft lock: %v", err)
		}
		first, err := q1.InsertProjectLocationQueryForUser(ctx, sqlc.InsertProjectLocationQueryForUserParams{
			Text: fx.prefix + " draft one", Normalized: textnormalization.NormalizeTextKey(fx.prefix + " draft one"),
			Ordinal: 0, Enabled: true, Kind: "map", Source: "manual", Origin: "service",
			LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID})
		if err != nil {
			t.Fatalf("tx1 insert: %v", err)
		}
		type secondResult struct {
			rows []sqlc.ProjectLocationQuery
			err  error
		}
		secondCh := make(chan secondResult, 1)
		attemptCh := make(chan int32, 1)
		go func() {
			tx2, err := pool.Begin(ctx)
			if err != nil {
				secondCh <- secondResult{err: err}
				return
			}
			defer func() { _ = tx2.Rollback(ctx) }()
			var pid int32
			if err := tx2.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				secondCh <- secondResult{err: err}
				return
			}
			attemptCh <- pid
			q2 := sqlc.New(tx2)
			if _, err := q2.LockProjectLocationForQueryDraftForUser(ctx, sqlc.LockProjectLocationForQueryDraftForUserParams{
				LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID}); err != nil {
				secondCh <- secondResult{err: err}
				return
			}
			rows, err := q2.ListProjectLocationQueriesForUser(ctx, sqlc.ListProjectLocationQueriesForUserParams{
				LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID})
			if err != nil {
				secondCh <- secondResult{err: err}
				return
			}
			seen := false
			for _, row := range rows {
				if row.ID == first.ID {
					seen = true
				}
			}
			if !seen {
				secondCh <- secondResult{err: errors.New("second lock missed first committed row")}
				return
			}
			if _, err := q2.DeleteProjectLocationQueryForUser(ctx, sqlc.DeleteProjectLocationQueryForUserParams{
				ID: first.ID, LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID}); err != nil {
				secondCh <- secondResult{err: err}
				return
			}
			replacement, err := q2.InsertProjectLocationQueryForUser(ctx, sqlc.InsertProjectLocationQueryForUserParams{
				Text: fx.prefix + " draft two", Normalized: textnormalization.NormalizeTextKey(fx.prefix + " draft two"),
				Ordinal: 0, Enabled: true, Kind: "map", Source: "manual", Origin: "service",
				LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID})
			if err != nil {
				secondCh <- secondResult{err: err}
				return
			}
			if err := tx2.Commit(ctx); err != nil {
				secondCh <- secondResult{err: err}
				return
			}
			secondCh <- secondResult{rows: []sqlc.ProjectLocationQuery{replacement}}
		}()
		var pid int32
		select {
		case pid = <-attemptCh:
		case <-time.After(15 * time.Second):
			t.Fatalf("second draft never attempted the lock")
		}
		if _, err := pool.Exec(ctx, `SELECT id FROM project_locations WHERE id = $1 FOR NO KEY UPDATE NOWAIT`, fx.locBID); l4itPgxCode(err) != "55P03" {
			t.Fatalf("nowait probe code = %q err=%v, want 55P03 while tx1 holds the draft lock", l4itPgxCode(err), err)
		}
		waitDeadline := time.Now().Add(8 * time.Second)
		observed := false
		for time.Now().Before(waitDeadline) {
			var n int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE pid = $1 AND wait_event_type = 'Lock'`, pid).Scan(&n); err == nil && n > 0 {
				observed = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !observed {
			t.Fatalf("second backend pid %d never entered lock wait", pid)
		}
		t.Logf("layer4-it draft lockwait observed backend pid %d", pid)
		select {
		case res := <-secondCh:
			t.Fatalf("second draft proceeded before first commit: %+v", res)
		default:
		}
		if err := tx1.Commit(ctx); err != nil {
			t.Fatalf("commit tx1: %v", err)
		}
		var res secondResult
		select {
		case res = <-secondCh:
		case <-time.After(15 * time.Second):
			t.Fatalf("second draft did not proceed after first commit")
		}
		if res.err != nil {
			t.Fatalf("second draft: %v", res.err)
		}
		final, err := fx.queries.ListProjectLocationQueriesForUser(fx.ctx, sqlc.ListProjectLocationQueriesForUserParams{
			LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID})
		if err != nil || len(final) != 1 || final[0].Text != fx.prefix+" draft two" {
			t.Fatalf("post-replace draft = %+v err=%v", final, err)
		}
		if _, err := fx.queries.DeleteProjectLocationQueryForUser(fx.ctx, sqlc.DeleteProjectLocationQueryForUserParams{
			ID: final[0].ID, LocationID: fx.locBID, ProjectID: fx.projBID, UserID: fx.ownerID}); err != nil {
			t.Fatalf("cleanup draft: %v", err)
		}
	})

	t.Logf("layer4-it end counts: %v", fx.scopedCounts())
}

// TestLocationLayer4PartialSetupCleanup proves that a setup failure midway
// removes every row it may have created. It uses the error-returning setup path
// instead of t.Fatal so the suite stays green while exercising real fixtures.
func TestLocationLayer4PartialSetupCleanup(t *testing.T) {
	pool, appName := l4itOpenPool(t)
	for _, failAfter := range []int{0, 1, 3, 5, 8} {
		failAfter := failAfter
		t.Run(fmt.Sprintf("failAfter%d", failAfter), func(t *testing.T) {
			fx, err := l4itSetupFixture(t, pool, appName, failAfter)
			if err == nil {
				t.Fatalf("expected injected setup failure after %d inserts", failAfter)
			}
			if fx == nil {
				t.Fatalf("expected fixture with preallocated ids after %d inserts", failAfter)
			}
			fx.cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			counts := fx.ownedCounts(ctx)
			for _, c := range fx.ownedChecks() {
				if n := counts[c.key]; n != 0 {
					t.Fatalf("partial setup cleanup left %s rows = %d, want 0", c.key, n)
				}
			}
			t.Logf("layer4-it partial setup failAfter=%d cleanup verified 0 rows in %d owned tables", failAfter, len(counts))
		})
	}
}
