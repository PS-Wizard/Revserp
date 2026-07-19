package aitools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// testUUID builds a deterministic, distinguishable pgtype.UUID for tests.
func testUUID(seed byte) pgtype.UUID {
	var id pgtype.UUID
	id.Valid = true
	id.Bytes[15] = seed
	return id
}

type fakeBusinessProfileReader struct {
	gotArg sqlc.GetProjectBusinessProfileByProjectIDForUserParams
	row    sqlc.GetProjectBusinessProfileByProjectIDForUserRow
	err    error
}

func (f *fakeBusinessProfileReader) GetProjectBusinessProfileByProjectIDForUser(_ context.Context, arg sqlc.GetProjectBusinessProfileByProjectIDForUserParams) (sqlc.GetProjectBusinessProfileByProjectIDForUserRow, error) {
	f.gotArg = arg
	return f.row, f.err
}

func TestExecGetBusinessProfile_UsesScopeIDs(t *testing.T) {
	projectID := testUUID(1)
	userID := testUUID(2)
	fake := &fakeBusinessProfileReader{
		row: sqlc.GetProjectBusinessProfileByProjectIDForUserRow{BrandName: "Acme", WebsiteUrl: "https://acme.example"},
	}

	result, err := execGetBusinessProfile(context.Background(), projectID, userID, fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.gotArg.ProjectID != projectID || fake.gotArg.UserID != userID {
		t.Fatalf("expected reader to be called with scope IDs, got %+v", fake.gotArg)
	}

	var output businessProfileOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("could not unmarshal result content: %v", err)
	}
	if !output.HasBusinessProfile || output.BrandName != "Acme" {
		t.Errorf("unexpected output: %+v", output)
	}
}

func TestExecGetBusinessProfile_NoProfile(t *testing.T) {
	fake := &fakeBusinessProfileReader{err: pgx.ErrNoRows}

	result, err := execGetBusinessProfile(context.Background(), testUUID(1), testUUID(2), fake)
	if err != nil {
		t.Fatalf("no-rows should not be a Go error, got: %v", err)
	}

	var output businessProfileOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("could not unmarshal result content: %v", err)
	}
	if output.HasBusinessProfile {
		t.Errorf("expected has_business_profile=false, got %+v", output)
	}
	if result.Summary != "no business profile set" {
		t.Errorf("unexpected summary: %q", result.Summary)
	}
}
