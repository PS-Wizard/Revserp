package localvisibility

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/serper"
)

func TestResolveLocalPlaceRank(t *testing.T) {
	places := []serper.Place{
		{Position: 1, Title: "Target Shop", PlaceID: "other-place"},
		{Position: 2, Title: "Wrong Name", PlaceID: "target-place"},
		{Position: 0, Title: "Target Shop", PlaceID: "target-place"},
		{Position: -1, Title: "Target Shop", PlaceID: "target-place"},
	}
	for _, tc := range []struct {
		name      string
		places    []serper.Place
		target    string
		wantRank  int
		wantFound bool
	}{
		{"match by place id ignores title", places, "target-place", 2, true},
		{"title alone never matches", []serper.Place{{Position: 1, Title: "Target Shop"}}, "target-place", 0, false},
		{"empty target never matches", places, "", 0, false},
		{"absent place", places, "missing-place", 0, false},
		{"non-positive positions are not ranks", places[:0:0], "target-place", 0, false},
		{"empty response", nil, "target-place", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rank, found := resolveLocalPlaceRank(tc.places, tc.target)
			if rank != tc.wantRank || found != tc.wantFound {
				t.Fatalf("rank = %d,%v want %d,%v", rank, found, tc.wantRank, tc.wantFound)
			}
		})
	}
}

func validTestSnapshot() LocalRunSnapshot {
	points, err := BuildGeoGrid(40.0, -74.0, 5000)
	if err != nil {
		panic(err)
	}
	viewports := make([]string, len(points))
	for i, point := range points {
		viewports[i], err = serper.FormatMapsViewport(point.Latitude, point.Longitude)
		if err != nil {
			panic(err)
		}
	}
	return LocalRunSnapshot{
		Queries:              []string{"q1", "q2", "q3", "q4", "q5"},
		TargetPlaceID:        "target-place",
		Points:               points,
		Viewports:            viewports,
		Provider:             "serper",
		Endpoint:             "http://127.0.0.1:1/local-seo-test/maps",
		Zoom:                 MapsZoom,
		Language:             "en",
		GridGeometryVersion:  1,
		ComparisonVersion:    1,
		RequestedResultLimit: 20,
		ViewportToleranceM:   serper.MapsViewportToleranceM,
	}
}

func TestValidateLocalRunSnapshot(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		if err := validateLocalRunSnapshot(validTestSnapshot(), 135); err != nil {
			t.Fatalf("valid snapshot: %v", err)
		}
	})
	mutate := func(f func(*LocalRunSnapshot)) LocalRunSnapshot {
		s := validTestSnapshot()
		f(&s)
		return s
	}
	for _, tc := range []struct {
		name     string
		snapshot LocalRunSnapshot
		expected int32
	}{
		{"wrong query count", mutate(func(s *LocalRunSnapshot) { s.Queries = s.Queries[:4] }), 135},
		{"duplicate queries", mutate(func(s *LocalRunSnapshot) { s.Queries[1] = "Q1" }), 135},
		{"unnormalized query", mutate(func(s *LocalRunSnapshot) { s.Queries[1] = " q2" }), 135},
		{"wrong point count", mutate(func(s *LocalRunSnapshot) { s.Points = s.Points[:8] }), 135},
		{"duplicate point index", mutate(func(s *LocalRunSnapshot) { s.Points[1].PointIndex = 0 }), 135},
		{"bad ring", mutate(func(s *LocalRunSnapshot) { s.Points[1].Ring = "mid" }), 135},
		{"bad sector", mutate(func(s *LocalRunSnapshot) { s.Points[1].Sector = "UP" }), 135},
		{"wrong viewport count", mutate(func(s *LocalRunSnapshot) { s.Viewports = s.Viewports[:8] }), 135},
		{"viewport mismatch", mutate(func(s *LocalRunSnapshot) { s.Viewports[3] = s.Viewports[4] }), 135},
		{"wrong zoom", mutate(func(s *LocalRunSnapshot) { s.Zoom = 15 }), 135},
		{"wrong viewport tolerance", mutate(func(s *LocalRunSnapshot) { s.ViewportToleranceM = 100 }), 135},
		{"missing viewport tolerance", mutate(func(s *LocalRunSnapshot) { s.ViewportToleranceM = 0 }), 135},
		{"wrong provider", mutate(func(s *LocalRunSnapshot) { s.Provider = "other" }), 135},
		{"empty endpoint", mutate(func(s *LocalRunSnapshot) { s.Endpoint = "" }), 135},
		{"wrong language", mutate(func(s *LocalRunSnapshot) { s.Language = "de" }), 135},
		{"wrong grid version", mutate(func(s *LocalRunSnapshot) { s.GridGeometryVersion = 2 }), 135},
		{"wrong comparison version", mutate(func(s *LocalRunSnapshot) { s.ComparisonVersion = 2 }), 135},
		{"wrong result limit", mutate(func(s *LocalRunSnapshot) { s.RequestedResultLimit = 10 }), 135},
		{"cost mismatch", validTestSnapshot(), 134},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateLocalRunSnapshot(tc.snapshot, tc.expected); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestLocalCellOutcome(t *testing.T) {
	var runID pgtype.UUID
	runID.Valid = true
	echo := func() serper.MapsResponse {
		return serper.MapsResponse{LL: "@40.000000,-74.000000,14z", Credits: 3, Places: []serper.Place{
			{Position: 1, Title: "Neighbor", PlaceID: "other"},
			{Position: 2, Title: "Wrong Name", PlaceID: "target"},
		}}
	}
	t.Run("found by place id", func(t *testing.T) {
		out := localCellOutcome(runID, 0, 0, "target", echo(), nil)
		if out.CallStatus != "success_nonempty" || out.MatchStatus != "found" || !out.Rank.Valid || out.Rank.Int32 != 2 {
			t.Fatalf("outcome = %#v", out)
		}
		if !out.CreditKnown || out.Credits != 3 || len(out.RawResponse) == 0 {
			t.Fatalf("charge not preserved: %#v", out)
		}
	})
	t.Run("absent without rank", func(t *testing.T) {
		out := localCellOutcome(runID, 0, 0, "missing", echo(), nil)
		if out.CallStatus != "success_nonempty" || out.MatchStatus != "absent" || out.Rank.Valid {
			t.Fatalf("outcome = %#v", out)
		}
	})
	t.Run("empty response is absent", func(t *testing.T) {
		resp := echo()
		resp.Places = nil
		out := localCellOutcome(runID, 0, 0, "target", resp, nil)
		if out.CallStatus != "success_empty" || out.MatchStatus != "absent" || out.Rank.Valid {
			t.Fatalf("outcome = %#v", out)
		}
	})
	t.Run("echo mismatch keeps credits but no rank", func(t *testing.T) {
		out := localCellOutcome(runID, 0, 0, "target", echo(), fmt.Errorf("serper maps at: viewport mismatch"))
		if out.CallStatus != "request_failed" || out.MatchStatus != "unknown" || out.Rank.Valid {
			t.Fatalf("outcome = %#v", out)
		}
		if !out.CreditKnown || out.Credits != 3 || len(out.RawResponse) == 0 || !out.Error.Valid {
			t.Fatalf("charge not preserved: %#v", out)
		}
	})
	t.Run("transport failure keeps reservation", func(t *testing.T) {
		out := localCellOutcome(runID, 0, 0, "target", serper.MapsResponse{}, fmt.Errorf("connection reset"))
		if out.CallStatus != "request_failed" || out.MatchStatus != "unknown" {
			t.Fatalf("outcome = %#v", out)
		}
		if out.CreditKnown || out.Credits != 0 {
			t.Fatalf("unknown charge must retain reservation: %#v", out)
		}
	})
}

// Local run DB tests use a mocked Serper endpoint (never a paid call) and are
// gated on LOCAL_SEO_TEST_DATABASE_URL, skipping when the isolated database is
// absent.

type localRunFixture struct {
	userID     pgtype.UUID
	projectID  pgtype.UUID
	locationID pgtype.UUID
	placeID    string
}

func newLocalRunTestPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	databaseURL := os.Getenv("LOCAL_SEO_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("LOCAL_SEO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, databaseURL, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("local visibility test database unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"public.local_visibility_runs", "public.project_locations", "public.ai_worker_jobs"} {
		var regclass string
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&regclass); err != nil || regclass == "" {
			t.Skipf("table %s is not migrated in the test database", table)
		}
	}
	return pool, ctx
}

func newLocalRunFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) localRunFixture {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var userID, orgID, projectID, locationID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('test', $1, $2) RETURNING id`,
		"lv-user-"+suffix, "lv-user-"+suffix+"@example.invalid").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ($1) RETURNING id`, "lv-org-"+suffix).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, userID); err != nil {
		t.Fatalf("create membership: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1, $2, 'https://lv-test.example.invalid') RETURNING id`,
		orgID, "lv-project-"+suffix).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_maps_credit_budgets (organization_id, remaining_credits) VALUES ($1, 1000000) ON CONFLICT (organization_id) DO UPDATE SET remaining_credits = EXCLUDED.remaining_credits`, orgID); err != nil {
		t.Fatalf("fund org budget: %v", err)
	}
	var platform struct{ remaining, reserved, spent int64 }
	if err := pool.QueryRow(ctx, `SELECT remaining_credits, reserved_credits, spent_credits FROM platform_maps_credit_budget WHERE id = TRUE`).Scan(
		&platform.remaining, &platform.reserved, &platform.spent); err != nil {
		t.Fatalf("read platform budget: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE platform_maps_credit_budget SET remaining_credits = remaining_credits + 1000000 WHERE id = TRUE`); err != nil {
		t.Fatalf("fund platform budget: %v", err)
	}
	placeID := "lv-target-" + suffix
	queriesJSON, _ := json.Marshal([]string{"lv q1 " + suffix, "lv q2 " + suffix, "lv q3 " + suffix, "lv q4 " + suffix, "lv q5 " + suffix})
	if err := pool.QueryRow(ctx, `INSERT INTO project_locations (project_id, name, place_id, latitude, longitude, queries)
		VALUES ($1, $2, $3, 40.0, -74.0, $4) RETURNING id`,
		projectID, "lv-location-"+suffix, placeID, queriesJSON).Scan(&locationID); err != nil {
		t.Fatalf("create location: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `UPDATE local_visibility_runs SET status = 'failed', reserved_credits = 0 WHERE location_id IN (SELECT l.id FROM project_locations l JOIN projects p ON p.id=l.project_id WHERE p.organization_id=$1)`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `UPDATE platform_maps_credit_budget SET remaining_credits = $1, reserved_credits = $2, spent_credits = $3 WHERE id = TRUE`,
			platform.remaining, platform.reserved, platform.spent)
	})
	return localRunFixture{userID: userID, projectID: projectID, locationID: locationID, placeID: placeID}
}

func startLocalMapsStub(t *testing.T, hits *atomic.Int64, targetPlaceID string, credits int, badEcho bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			LL string `json:"ll"`
		}
		_ = json.Unmarshal(raw, &body)
		echo := body.LL
		if badEcho {
			echo = "@0.000000,0.000000,14z"
		}
		resp := fmt.Sprintf(`{"ll":%q,"places":[{"position":1,"title":"Neighbor Shop","placeId":"other-place"},{"position":2,"title":"Wrong Name","placeId":%q}],"credits":%d}`,
			echo, targetPlaceID, credits)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(server.Close)
	return server
}

func enqueueLocalStubRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f localRunFixture, mapsURL string) pgtype.UUID {
	t.Helper()
	store := LocalVisibilityStore{Pool: pool, MapsEndpoint: mapsURL}
	run, err := store.EnqueueRun(ctx, f.userID, f.projectID, f.locationID, 5000)
	if err != nil {
		t.Fatalf("enqueue run: %v", err)
	}
	return run.ID
}

func localTestConfig(mapsURL string) config.Config {
	return config.Config{
		SerperAPIKey:          "test-key",
		SerperMapsEndpoint:    mapsURL,
		SerperPlacesEndpoint:  "http://127.0.0.1:1/",
		SerperReviewsEndpoint: "http://127.0.0.1:1/",
	}
}

func TestExecuteLocalVisibilityRunCompletes(t *testing.T) {
	pool, ctx := newLocalRunTestPool(t)
	f := newLocalRunFixture(t, ctx, pool)
	var hits atomic.Int64
	server := startLocalMapsStub(t, &hits, f.placeID, 3, false)
	runID := enqueueLocalStubRun(t, ctx, pool, f, server.URL)

	if err := ExecuteLocalVisibilityRun(ctx, pool, localTestConfig(server.URL), runID, f.projectID); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if hits.Load() != 45 {
		t.Fatalf("provider calls = %d, want 45", hits.Load())
	}
	queries := sqlc.New(pool)
	run, err := queries.GetLocalVisibilityRun(ctx, runID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if run.Status != "completed" || run.ReservedCredits != 0 || run.CreditsUsed != 135 {
		t.Fatalf("run = status %s reserved %d used %d, want completed/0/135", run.Status, run.ReservedCredits, run.CreditsUsed)
	}
	cells, err := queries.GetLocalRunCells(ctx, runID)
	if err != nil {
		t.Fatalf("cells: %v", err)
	}
	if len(cells) != 45 {
		t.Fatalf("cells = %d, want 45", len(cells))
	}
	for _, cell := range cells {
		if cell.CallStatus != "success_nonempty" || cell.MatchStatus != "found" || !cell.Rank.Valid || cell.Rank.Int32 != 2 {
			t.Fatalf("cell q%d p%d = %+v, want found rank 2", cell.QueryIndex, cell.PointIndex, cell)
		}
	}
}

func TestExecuteLocalVisibilityRunEchoMismatchFails(t *testing.T) {
	pool, ctx := newLocalRunTestPool(t)
	f := newLocalRunFixture(t, ctx, pool)
	var hits atomic.Int64
	server := startLocalMapsStub(t, &hits, f.placeID, 3, true)
	runID := enqueueLocalStubRun(t, ctx, pool, f, server.URL)

	if err := ExecuteLocalVisibilityRun(ctx, pool, localTestConfig(server.URL), runID, f.projectID); err == nil {
		t.Fatal("want error for failed run")
	}
	queries := sqlc.New(pool)
	run, err := queries.GetLocalVisibilityRun(ctx, runID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	// Charges were known, so every cell settled: no reservation retained.
	if run.Status != "failed" || run.ReservedCredits != 0 || run.CreditsUsed != 135 {
		t.Fatalf("run = status %s reserved %d used %d, want failed/0/135", run.Status, run.ReservedCredits, run.CreditsUsed)
	}
	cells, err := queries.GetLocalRunCells(ctx, runID)
	if err != nil {
		t.Fatalf("cells: %v", err)
	}
	for _, cell := range cells {
		if cell.CallStatus != "request_failed" || cell.MatchStatus != "unknown" || cell.Rank.Valid || !cell.CreditKnown {
			t.Fatalf("cell q%d p%d = %+v, want failed/unknown with known charge", cell.QueryIndex, cell.PointIndex, cell)
		}
	}
}

func TestExecuteLocalVisibilityRunStopsOnCreditDrift(t *testing.T) {
	pool, ctx := newLocalRunTestPool(t)
	f := newLocalRunFixture(t, ctx, pool)
	var hits atomic.Int64
	server := startLocalMapsStub(t, &hits, f.placeID, 5, false)
	runID := enqueueLocalStubRun(t, ctx, pool, f, server.URL)

	if err := ExecuteLocalVisibilityRun(ctx, pool, localTestConfig(server.URL), runID, f.projectID); err == nil {
		t.Fatal("want error for partial run")
	}
	if hits.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1 stopped call", hits.Load())
	}
	run, err := sqlc.New(pool).GetLocalVisibilityRun(ctx, runID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if run.Status != "partial" || run.CreditsUsed != 5 {
		t.Fatalf("run = status %s used %d, want partial/5", run.Status, run.CreditsUsed)
	}
}

func TestExecuteLocalVisibilityRunRejectsForeignProject(t *testing.T) {
	pool, ctx := newLocalRunTestPool(t)
	f := newLocalRunFixture(t, ctx, pool)
	var hits atomic.Int64
	server := startLocalMapsStub(t, &hits, f.placeID, 3, false)
	runID := enqueueLocalStubRun(t, ctx, pool, f, server.URL)

	foreign := pgtype.UUID{Bytes: [16]byte{9}, Valid: true}
	if err := ExecuteLocalVisibilityRun(ctx, pool, localTestConfig(server.URL), runID, foreign); err == nil {
		t.Fatal("want error for foreign project")
	}
	if hits.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", hits.Load())
	}
	run, err := sqlc.New(pool).GetLocalVisibilityRun(ctx, runID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if run.Status != "queued" {
		t.Fatalf("run status = %s, want still queued", run.Status)
	}
}

// startLocalMapsFailingStub answers the first call with a found listing and
// every later call with HTTP 500. Transport failures carry no decoded
// charge, so those cells stay unknown with their reservations retained.
func startLocalMapsFailingStub(t *testing.T, hits *atomic.Int64, targetPlaceID string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				LL string `json:"ll"`
			}
			_ = json.Unmarshal(raw, &body)
			resp := fmt.Sprintf(`{"ll":%q,"places":[{"position":2,"title":"Wrong Name","placeId":%q}],"credits":3}`,
				body.LL, targetPlaceID)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(resp))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestExecuteLocalVisibilityRunUnknownChargesRetainReservations(t *testing.T) {
	pool, ctx := newLocalRunTestPool(t)
	f := newLocalRunFixture(t, ctx, pool)
	var hits atomic.Int64
	server := startLocalMapsFailingStub(t, &hits, f.placeID)
	runID := enqueueLocalStubRun(t, ctx, pool, f, server.URL)

	if err := ExecuteLocalVisibilityRun(ctx, pool, localTestConfig(server.URL), runID, f.projectID); err == nil {
		t.Fatal("want error for partial run")
	}
	if hits.Load() != 45 {
		t.Fatalf("provider calls = %d, want 45 with no retry", hits.Load())
	}
	queries := sqlc.New(pool)
	run, err := queries.GetLocalVisibilityRun(ctx, runID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	// One settled call plus 44 unknown charges whose reservations stay held.
	if run.Status != "partial" || run.ReservedCredits != 132 || run.CreditsUsed != 3 {
		t.Fatalf("run = status %s reserved %d used %d, want partial/132/3", run.Status, run.ReservedCredits, run.CreditsUsed)
	}
	cells, err := queries.GetLocalRunCells(ctx, runID)
	if err != nil {
		t.Fatalf("cells: %v", err)
	}
	for _, cell := range cells {
		if cell.QueryIndex == 0 && cell.PointIndex == 0 {
			if cell.CallStatus != "success_nonempty" || cell.MatchStatus != "found" {
				t.Fatalf("first cell = %+v, want found", cell)
			}
			continue
		}
		if cell.CallStatus != "request_failed" || cell.MatchStatus != "unknown" || cell.Rank.Valid || cell.CreditKnown || cell.Credits != 0 {
			t.Fatalf("cell q%d p%d = %+v, want unknown with retained reservation", cell.QueryIndex, cell.PointIndex, cell)
		}
	}
}

func TestExecuteLocalVisibilityRunCancelsWithoutRetry(t *testing.T) {
	pool, ctx := newLocalRunTestPool(t)
	f := newLocalRunFixture(t, ctx, pool)
	var hits atomic.Int64
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				LL string `json:"ll"`
			}
			_ = json.Unmarshal(raw, &body)
			resp := fmt.Sprintf(`{"ll":%q,"places":[{"position":2,"title":"Wrong Name","placeId":%q}],"credits":3}`,
				body.LL, f.placeID)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(resp))
			return
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })
	runID := enqueueLocalStubRun(t, ctx, pool, f, server.URL)

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ExecuteLocalVisibilityRun(runCtx, pool, localTestConfig(server.URL), runID, f.projectID)
	}()
	deadline := time.Now().Add(15 * time.Second)
	for hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hits.Load() < 2 {
		t.Fatal("second provider call never started")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want error for cancelled run")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("execute did not return after cancel")
	}
	time.Sleep(300 * time.Millisecond)
	if hits.Load() != 2 {
		t.Fatalf("provider calls = %d, want exactly 2 with no retry after cancel", hits.Load())
	}
	queries := sqlc.New(pool)
	run, err := queries.GetLocalVisibilityRun(ctx, runID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	// The started-but-unrecorded cell keeps its reservation (its charge is
	// unknown); the 43 never-started cells release theirs.
	if run.Status != "partial" || run.ReservedCredits != 3 || run.CreditsUsed != 3 {
		t.Fatalf("run = status %s reserved %d used %d, want partial/3/3", run.Status, run.ReservedCredits, run.CreditsUsed)
	}
	cells, err := queries.GetLocalRunCells(ctx, runID)
	if err != nil {
		t.Fatalf("cells: %v", err)
	}
	for _, cell := range cells {
		if cell.QueryIndex == 0 && cell.PointIndex == 1 {
			if cell.CallStatus != "pending" || !cell.StartedAt.Valid {
				t.Fatalf("cancelled cell = %+v, want started but still pending", cell)
			}
		}
	}
}

func TestFinishRunIsIdempotent(t *testing.T) {
	pool, ctx := newLocalRunTestPool(t)
	f := newLocalRunFixture(t, ctx, pool)
	var hits atomic.Int64
	server := startLocalMapsStub(t, &hits, f.placeID, 3, false)
	runID := enqueueLocalStubRun(t, ctx, pool, f, server.URL)
	if err := ExecuteLocalVisibilityRun(ctx, pool, localTestConfig(server.URL), runID, f.projectID); err != nil {
		t.Fatalf("execute: %v", err)
	}
	store := LocalVisibilityStore{Pool: pool}
	queries := sqlc.New(pool)
	before, err := queries.GetLocalVisibilityRun(ctx, runID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	var platformBefore, orgBefore struct{ remaining, reserved, spent int64 }
	if err := pool.QueryRow(ctx, `SELECT remaining_credits, reserved_credits, spent_credits FROM platform_maps_credit_budget WHERE id = TRUE`).Scan(
		&platformBefore.remaining, &platformBefore.reserved, &platformBefore.spent); err != nil {
		t.Fatalf("read platform budget: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT remaining_credits, reserved_credits, spent_credits FROM organization_maps_credit_budgets
		WHERE organization_id = (SELECT organization_id FROM projects WHERE id = (SELECT project_id FROM project_locations WHERE id = $1))`, f.locationID).Scan(
		&orgBefore.remaining, &orgBefore.reserved, &orgBefore.spent); err != nil {
		t.Fatalf("read org budget: %v", err)
	}
	if err := store.FinishRun(ctx, runID); err != nil {
		t.Fatalf("repeat finish: %v", err)
	}
	after, err := queries.GetLocalVisibilityRun(ctx, runID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if after.Status != before.Status || after.ReservedCredits != before.ReservedCredits ||
		after.CreditsUsed != before.CreditsUsed || after.Error != before.Error {
		t.Fatalf("run changed on repeat finish: %#v vs %#v", after, before)
	}
	var platformAfter, orgAfter struct{ remaining, reserved, spent int64 }
	if err := pool.QueryRow(ctx, `SELECT remaining_credits, reserved_credits, spent_credits FROM platform_maps_credit_budget WHERE id = TRUE`).Scan(
		&platformAfter.remaining, &platformAfter.reserved, &platformAfter.spent); err != nil {
		t.Fatalf("reread platform budget: %v", err)
	}
	if platformAfter != platformBefore {
		t.Fatalf("platform budget changed on repeat finish: %#v vs %#v", platformAfter, platformBefore)
	}
	if err := pool.QueryRow(ctx, `SELECT remaining_credits, reserved_credits, spent_credits FROM organization_maps_credit_budgets
		WHERE organization_id = (SELECT organization_id FROM projects WHERE id = (SELECT project_id FROM project_locations WHERE id = $1))`, f.locationID).Scan(
		&orgAfter.remaining, &orgAfter.reserved, &orgAfter.spent); err != nil {
		t.Fatalf("reread org budget: %v", err)
	}
	if orgAfter != orgBefore {
		t.Fatalf("org budget changed on repeat finish: %#v vs %#v", orgAfter, orgBefore)
	}
}

func TestRecordCellOutcomeDuplicateChargesOnce(t *testing.T) {
	pool, ctx := newLocalRunTestPool(t)
	f := newLocalRunFixture(t, ctx, pool)
	store := LocalVisibilityStore{Pool: pool}
	queries := sqlc.New(pool)
	var hits atomic.Int64
	server := startLocalMapsStub(t, &hits, f.placeID, 3, false)
	runID := enqueueLocalStubRun(t, ctx, pool, f, server.URL)
	if _, err := queries.StartLocalVisibilityRun(ctx, runID); err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := queries.StartLocalRunCell(ctx, sqlc.StartLocalRunCellParams{RunID: runID}); err != nil {
		t.Fatalf("start cell: %v", err)
	}
	var spentBefore int64
	if err := pool.QueryRow(ctx, `SELECT spent_credits FROM platform_maps_credit_budget WHERE id = TRUE`).Scan(&spentBefore); err != nil {
		t.Fatalf("read platform spent: %v", err)
	}
	outcome := localCellOutcome(runID, 0, 0, f.placeID, serper.MapsResponse{
		LL:      "@40.000000,-74.000000,14z",
		Credits: 3,
		Places:  []serper.Place{{Position: 2, Title: "Wrong Name", PlaceID: f.placeID}},
	}, nil)
	if err := store.RecordCellOutcome(ctx, outcome); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := store.RecordCellOutcome(ctx, outcome); err == nil {
		t.Fatal("want error for duplicate outcome")
	}
	run, err := queries.GetLocalVisibilityRun(ctx, runID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if run.CreditsUsed != 3 {
		t.Fatalf("credits used = %d, want exactly 3", run.CreditsUsed)
	}
	var spentAfter int64
	if err := pool.QueryRow(ctx, `SELECT spent_credits FROM platform_maps_credit_budget WHERE id = TRUE`).Scan(&spentAfter); err != nil {
		t.Fatalf("reread platform spent: %v", err)
	}
	if spentAfter != spentBefore+3 {
		t.Fatalf("platform spent = %d, want %d", spentAfter, spentBefore+3)
	}
}

func TestLocalVisibilityDeletionBlocksUnsettledSpend(t *testing.T) {
	pool, ctx := newLocalRunTestPool(t)
	f := newLocalRunFixture(t, ctx, pool)
	platformBefore, err := sqlc.New(pool).GetPlatformMapsCreditBudget(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runID := enqueueLocalStubRun(t, ctx, pool, f, "http://127.0.0.1:1/maps")
	for _, statement := range []string{
		`DELETE FROM project_locations WHERE id=$1`,
		`DELETE FROM projects WHERE id=$1`,
	} {
		id := f.locationID
		if statement == `DELETE FROM projects WHERE id=$1` {
			id = f.projectID
		}
		if _, err := pool.Exec(ctx, statement, id); err == nil {
			t.Fatal("active spending deletion succeeded")
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE local_visibility_runs SET status='partial' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM project_locations WHERE id=$1`, f.locationID); err == nil {
		t.Fatal("unresolved credit deletion succeeded")
	}
	if _, err := pool.Exec(ctx, `UPDATE local_visibility_runs SET status='queued' WHERE id=$1`, runID); err != nil {
		t.Fatal(err)
	}
	if err := (LocalVisibilityStore{Pool: pool}).FinishRun(ctx, runID); err != nil {
		t.Fatal(err)
	}
	platformAfter, err := sqlc.New(pool).GetPlatformMapsCreditBudget(ctx)
	if err != nil || platformAfter.ReservedCredits != platformBefore.ReservedCredits {
		t.Fatalf("unused platform reservation stranded: %+v %v", platformAfter, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM project_locations WHERE id=$1`, f.locationID); err != nil {
		t.Fatalf("settled deletion blocked: %v", err)
	}
	var remainingRuns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM local_visibility_runs WHERE id=$1`, runID).Scan(&remainingRuns); err != nil || remainingRuns != 0 {
		t.Fatalf("settled location did not cascade run: %d %v", remainingRuns, err)
	}
}
