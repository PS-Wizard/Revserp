package localvisibility

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/joho/godotenv"
	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/serper"
)

type nepalLiveCellEvidence struct {
	QueryIndex  int             `json:"query_index"`
	PointIndex  int             `json:"point_index"`
	RequestedLL string          `json:"requested_ll"`
	EchoedLL    string          `json:"echoed_ll"`
	DriftM      *float64        `json:"viewport_drift_m"`
	CallStatus  string          `json:"call_status"`
	MatchStatus string          `json:"match_status"`
	Rank        *int32          `json:"rank"`
	Credits     int32           `json:"credits"`
	CreditKnown bool            `json:"credit_known"`
	Error       *string         `json:"error"`
	Response    json.RawMessage `json:"decoded_response"`
}

type nepalLiveRunEvidence struct {
	RunID             string                  `json:"run_id"`
	CapturedAt        time.Time               `json:"captured_at"`
	Status            string                  `json:"status"`
	ExpectedCredits   int32                   `json:"expected_credits"`
	ConfirmedCredits  int32                   `json:"confirmed_credits"`
	ReservedCredits   int32                   `json:"reserved_credits"`
	FoundObservations int                     `json:"found_observations"`
	FoundGridPoints   int                     `json:"found_grid_points"`
	Snapshot          LocalRunSnapshot        `json:"snapshot"`
	Cells             []nepalLiveCellEvidence `json:"cells"`
}

func TestLayer1NepalLiveRun(t *testing.T) {
	if os.Getenv("LOCAL_SEO_RUN_LIVE") != "1" {
		t.Skip("one authorized 135-credit live run requires LOCAL_SEO_RUN_LIVE=1")
	}
	pool, ctx := newLocalRunTestPool(t)
	_ = godotenv.Load("../../.env")
	cfg := config.Load()
	if cfg.SerperAPIKey == "" {
		t.Fatal("SERPER_KEY is required; no live calls performed")
	}
	label := fmt.Sprintf("nepal-live-%d", time.Now().UnixNano())
	var userID, orgID, projectID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO users(auth_provider,auth_subject,email) VALUES('local-seo-live',$1,$2) RETURNING id`, label, label+"@example.invalid").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO organizations(name) VALUES($1) RETURNING id`, label).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_members(org_id,user_id,role) VALUES($1,$2,'owner')`, orgID, userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO projects(organization_id,name,base_url) VALUES($1,$2,'https://example.invalid') RETURNING id`, orgID, label).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	q := sqlc.New(pool)
	var locationID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO project_locations (project_id, name, place_id, latitude, longitude, localities)
		VALUES ($1, 'Nepal Life Insurance Company Ltd. live fixture', 'ChIJlSym_K4Z6zkRWAt9oU_H4rA', 27.715444, 85.340306, '[]') RETURNING id`,
		projectID).Scan(&locationID); err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"life insurance", "life insurance near me", "life insurance Kathmandu", "insurance company near me", "life insurance Baneshwor"} {
		if _, err := pool.Exec(ctx, `INSERT INTO project_location_queries (location_id, text, normalized, ordinal, enabled, kind, source, origin)
			VALUES ($1, $2, $3, $4, TRUE, 'map', 'manual', 'service')`, locationID, text, strings.ToLower(text), i); err != nil {
			t.Fatal(err)
		}
	}
	store := LocalVisibilityStore{Pool: pool, MapsEndpoint: cfg.SerperMapsEndpoint}
	run, err := store.EnqueueRun(ctx, userID, projectID, locationID, 5000, 135)
	if err != nil {
		t.Fatalf("reserve before live spend: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ai_worker_jobs SET status='running',started_at=now() WHERE local_run_id=$1 AND status='pending'`, run.ID); err != nil {
		t.Fatal(err)
	}
	executeErr := ExecuteLocalVisibilityRun(ctx, pool, cfg, run.ID, projectID)
	saved, err := q.GetLocalVisibilityRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot LocalRunSnapshot
	if err := json.Unmarshal(saved.Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	evidence := nepalLiveRunEvidence{RunID: run.ID.String(), CapturedAt: time.Now().UTC(), Status: saved.Status, ExpectedCredits: saved.ExpectedCredits, ConfirmedCredits: saved.CreditsUsed, ReservedCredits: saved.ReservedCredits, Snapshot: snapshot, Cells: []nepalLiveCellEvidence{}}
	rows, err := pool.Query(ctx, `SELECT c.query_index,c.point_index,COALESCE(r.call_status,'pending'),COALESCE(r.match_status,'unknown'),r.rank,COALESCE(r.credits,0),COALESCE(r.credit_known,FALSE),r.error,r.raw_response FROM local_run_cells c LEFT JOIN local_visibility_results r USING(run_id,query_index,point_index) WHERE c.run_id=$1 ORDER BY c.query_index,c.point_index`, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundPoints := map[int]bool{}
	for rows.Next() {
		var cell nepalLiveCellEvidence
		var rank pgtype.Int4
		var message pgtype.Text
		var raw []byte
		if err := rows.Scan(&cell.QueryIndex, &cell.PointIndex, &cell.CallStatus, &cell.MatchStatus, &rank, &cell.Credits, &cell.CreditKnown, &message, &raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		cell.RequestedLL = snapshot.Viewports[cell.PointIndex]
		cell.Response = raw
		if rank.Valid {
			value := rank.Int32
			cell.Rank = &value
			evidence.FoundObservations++
			foundPoints[cell.PointIndex] = true
		}
		if message.Valid {
			value := message.String
			cell.Error = &value
		}
		var decoded serper.MapsResponse
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &decoded); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			cell.EchoedLL = decoded.LL
			cell.DriftM = decoded.ViewportDriftM
		}
		evidence.Cells = append(evidence.Cells, cell)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	evidence.FoundGridPoints = len(foundPoints)
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("testdata", 0755); err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join("testdata", "nepal-grid-live.json")
	if err := os.WriteFile(fixturePath, append(encoded, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
	if executeErr == nil {
		_, err = pool.Exec(ctx, `UPDATE ai_worker_jobs SET status='completed',completed_at=now() WHERE local_run_id=$1`, run.ID)
	} else {
		_, err = pool.Exec(ctx, `UPDATE ai_worker_jobs SET status='failed',error_message=$2,completed_at=now() WHERE local_run_id=$1`, run.ID, executeErr.Error())
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("run=%s location=%s project=%s status=%s credits=%d/%d reserved=%d observations=%d points=%d/9 fixture=%s", run.ID.String(), locationID.String(), projectID.String(), saved.Status, saved.CreditsUsed, saved.ExpectedCredits, saved.ReservedCredits, evidence.FoundObservations, evidence.FoundGridPoints, fixturePath)
	for _, cell := range evidence.Cells {
		t.Logf("q%d p%d requested=%s echoed=%s drift=%v credits=%d known=%t status=%s match=%s rank=%v", cell.QueryIndex, cell.PointIndex, cell.RequestedLL, cell.EchoedLL, cell.DriftM, cell.Credits, cell.CreditKnown, cell.CallStatus, cell.MatchStatus, cell.Rank)
	}
	if executeErr != nil {
		t.Fatalf("live run failed or partial; evidence retained, no automatic rerun: %v", executeErr)
	}
	if len(evidence.Cells) != len(evidence.Snapshot.Queries)*GridPointCount || saved.CreditsUsed != saved.ExpectedCredits || saved.ReservedCredits != 0 {
		t.Fatal("live outcome/count/settlement mismatch; evidence retained")
	}
}

func TestRecordedNepalGridLiveEvidence(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "nepal-grid-live.json"))
	if os.IsNotExist(err) {
		t.Skip("authorized live run has not been recorded")
	}
	if err != nil {
		t.Fatal(err)
	}
	var evidence nepalLiveRunEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	expected, err := ExpectedRunCredits(len(evidence.Snapshot.Queries), len(evidence.Snapshot.Points))
	if err != nil || int(evidence.ExpectedCredits) != expected {
		t.Fatalf("recorded expected cost invalid: %v", err)
	}
	if len(evidence.Cells) != len(evidence.Snapshot.Queries)*GridPointCount {
		t.Fatal("recorded run lacks planned cells")
	}
	total := int32(0)
	for _, cell := range evidence.Cells {
		total += cell.Credits
		if cell.CallStatus == "pending" {
			t.Fatalf("unperformed recorded cell q%d p%d", cell.QueryIndex, cell.PointIndex)
		}
		if cell.CreditKnown && cell.Credits != MapsCreditsPerCall {
			t.Fatalf("provider pricing changed in recorded cell: %+v", cell)
		}
		if cell.CallStatus != "request_failed" {
			if cell.DriftM == nil || *cell.DriftM > serper.MapsViewportToleranceM {
				t.Fatalf("accepted viewport without drift proof: %+v", cell)
			}
		}
		if cell.MatchStatus == "found" && (cell.Rank == nil || *cell.Rank <= 0) {
			t.Fatal("found rank evidence missing")
		}
		if cell.MatchStatus != "found" && cell.Rank != nil {
			t.Fatal("invented recorded rank")
		}
	}
	if total != evidence.ConfirmedCredits {
		t.Fatal("recorded credit accounting mismatch")
	}
}
