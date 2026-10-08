package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// stubLocationRow is a canned pgx.Row. Values are positional and must match
// the generated Scan arity and types of the dispatched query.
type stubLocationRow struct {
	values []any
	err    error
}

func (r stubLocationRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("stub row arity mismatch: %d dest, %d values", len(dest), len(r.values))
	}
	for i, d := range dest {
		switch t := d.(type) {
		case *pgtype.UUID:
			*t = r.values[i].(pgtype.UUID)
		case *pgtype.Text:
			*t = r.values[i].(pgtype.Text)
		case *string:
			*t = r.values[i].(string)
		case *float64:
			*t = r.values[i].(float64)
		case *int32:
			*t = r.values[i].(int32)
		case *[]byte:
			*t = r.values[i].([]byte)
		case *pgtype.Timestamptz:
			*t = r.values[i].(pgtype.Timestamptz)
		default:
			return fmt.Errorf("stub row: unsupported dest %T", d)
		}
	}
	return nil
}

// stubLocationDBTX routes generated location queries to canned rows without
// a database. A nil row group reads as ErrNoRows for that query kind.
type stubLocationDBTX struct {
	location []any
	profile  []any
	role     string
	member   bool
	upserted *sqlc.UpsertLocationBusinessProfileParams
}

func (s *stubLocationDBTX) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "location_business_profiles") && strings.Contains(sql, "INSERT"):
		return stubLocationRow{values: []any{
			testLocationID, testProjectID, testLocationID,
			"Acme Springfield", "https://acme.example/springfield",
			text("Cafe"), text(""), text("Family cafe."), text("Loaf"),
			text(""), []byte(`["Big Roast"]`), []byte(`[]`), []byte(`["Catering"]`),
			pgtype.Timestamptz{}, pgtype.Timestamptz{},
		}}
	case strings.Contains(sql, "FROM location_business_profiles"):
		if s.profile == nil {
			return stubLocationRow{err: pgx.ErrNoRows}
		}
		return stubLocationRow{values: s.profile}
	case strings.Contains(sql, "FROM project_locations"):
		if s.location == nil {
			return stubLocationRow{err: pgx.ErrNoRows}
		}
		return stubLocationRow{values: s.location}
	case strings.Contains(sql, "FROM organization_members"):
		if !s.member {
			return stubLocationRow{err: pgx.ErrNoRows}
		}
		return stubLocationRow{values: []any{testProjectID, testUserID, s.role, pgtype.Timestamptz{}}}
	default:
		return stubLocationRow{err: fmt.Errorf("stub db: unexpected query")}
	}
}

func (s *stubLocationDBTX) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("stub db: no exec")
}

func (s *stubLocationDBTX) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return nil, errors.New("stub db: no query")
}

func (s *stubLocationDBTX) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("stub db: no copy")
}

func stubLocationRowValues() []any {
	return []any{
		testLocationID, testProjectID, "Downtown", text("place-1"), 1.5, 2.5,
		pgtype.Timestamptz{Time: time.Now()}, pgtype.Timestamptz{Time: time.Now()},
		"addr", "Springfield", []byte(`[]`), int32(5000), testProjectID,
	}
}

func stubProfileRowValues() []any {
	return []any{
		testLocationID, testProjectID, testLocationID,
		"Acme Springfield", "https://acme.example/springfield",
		text("Cafe"), text("Springfield"), text("Family cafe."), text("Stone-baked loaf, 400g"),
		text("Locals"), []byte(`["Big Roast"]`), []byte(`[]`), []byte(`["Catering","Repairs"]`),
		pgtype.Timestamptz{}, pgtype.Timestamptz{},
	}
}

func locationScope(stub *stubLocationDBTX) Scope {
	return Scope{
		Queries:    sqlc.New(stub),
		ProjectID:  testProjectID,
		LocationID: testLocationID,
		UserID:     testUserID,
	}
}

func TestExecuteGetBusinessProfileDispatchesLocal(t *testing.T) {
	stub := &stubLocationDBTX{location: stubLocationRowValues(), profile: stubProfileRowValues(), member: true, role: "owner"}
	result, err := executeGetBusinessProfile(context.Background(), json.RawMessage(`{}`), locationScope(stub))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var response businessProfileLocalResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("content is not local profile JSON: %v\ncontent: %s", err, result.Content)
	}
	if response.BrandName != "Acme Springfield" || response.LocationID != testLocationID.String() {
		t.Fatalf("served tool must answer from the local profile: %+v", response)
	}
}

func TestExecuteGetBusinessProfileLocalAbsentNeverFallsToParent(t *testing.T) {
	stub := &stubLocationDBTX{location: stubLocationRowValues(), member: true, role: "owner"}
	result, err := executeGetBusinessProfile(context.Background(), json.RawMessage(`{}`), locationScope(stub))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Content, "for this location") {
		t.Fatalf("absent local profile must stay local, got: %s", result.Content)
	}
	if strings.Contains(result.Content, "for this project yet") {
		t.Fatalf("must never fall through to the parent message, got: %s", result.Content)
	}
}

func TestExecuteGetBusinessProfileForeignLocationNeverFallsToParent(t *testing.T) {
	stub := &stubLocationDBTX{member: true, role: "owner"}
	result, err := executeGetBusinessProfile(context.Background(), json.RawMessage(`{}`), locationScope(stub))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Content, "not found in this project") {
		t.Fatalf("foreign location must stay local, got: %s", result.Content)
	}
}

func TestExecuteUpdateBusinessProfileDispatchesLocal(t *testing.T) {
	stub := &stubLocationDBTX{location: stubLocationRowValues(), profile: stubProfileRowValues(), member: true, role: "owner"}
	result, err := executeUpdateBusinessProfile(context.Background(), json.RawMessage(`{"primary_category":"Bakery"}`), locationScope(stub))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	parsed := parseUpdateResult(t, result)
	if parsed["location_id"] != testLocationID.String() {
		t.Fatalf("served tool must answer from the local profile: %v", parsed)
	}
	if parsed["primary_category"] != "Cafe" {
		t.Fatalf("upsert echo must come from the local row: %v", parsed)
	}
}

func TestExecuteUpdateBusinessProfileLocalDeniedNeverFallsToParent(t *testing.T) {
	stub := &stubLocationDBTX{location: stubLocationRowValues(), profile: stubProfileRowValues(), member: true, role: "member"}
	result, err := executeUpdateBusinessProfile(context.Background(), json.RawMessage(`{"brand_name":"X"}`), locationScope(stub))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Content, "only organization owners") {
		t.Fatalf("non-owner must be denied locally, got: %s", result.Content)
	}
	foreign := &stubLocationDBTX{member: true, role: "owner"}
	result, err = executeUpdateBusinessProfile(context.Background(), json.RawMessage(`{"brand_name":"X"}`), locationScope(foreign))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Content, "location not found or access denied") {
		t.Fatalf("foreign location must stay local, got: %s", result.Content)
	}
}
