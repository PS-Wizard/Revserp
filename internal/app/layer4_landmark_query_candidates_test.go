package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func landmarkQueryUUID(n byte) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, n}, Valid: true}
}

func landmarkQueryLandmark(id byte, name string) sqlc.LocationLandmark {
	return sqlc.LocationLandmark{ID: landmarkQueryUUID(id), Name: name}
}

func TestGenerateLandmarkQueryCandidates(t *testing.T) {
	landmarks := []sqlc.LocationLandmark{
		landmarkQueryLandmark(1, "Baluwatar"),
		landmarkQueryLandmark(2, "Pashupatinath"),
	}
	got, err := GenerateLandmarkQueryCandidates([]string{"Plumber", "Electrician"}, landmarks)
	if err != nil {
		t.Fatalf("GenerateLandmarkQueryCandidates error: %v", err)
	}
	want := []LandmarkQueryCandidate{
		{Text: "Plumber near Baluwatar", LandmarkID: landmarkQueryUUID(1)},
		{Text: "Electrician near Baluwatar", LandmarkID: landmarkQueryUUID(1)},
		{Text: "Plumber near Pashupatinath", LandmarkID: landmarkQueryUUID(2)},
		{Text: "Electrician near Pashupatinath", LandmarkID: landmarkQueryUUID(2)},
	}
	if len(got) != len(want) {
		t.Fatalf("candidates = %+v, want %d entries", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestGenerateLandmarkQueryCandidatesDedupsNamesAndText(t *testing.T) {
	landmarks := []sqlc.LocationLandmark{
		landmarkQueryLandmark(1, "Baluwatar"),
		landmarkQueryLandmark(2, "  Baluwatar  "),
	}
	got, err := GenerateLandmarkQueryCandidates([]string{"Plumber", "plumber", "  "}, landmarks)
	if err != nil {
		t.Fatalf("GenerateLandmarkQueryCandidates error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("candidates = %+v, want 1 deduped entry", got)
	}
	if got[0].Text != "Plumber near Baluwatar" || got[0].LandmarkID != landmarkQueryUUID(1) {
		t.Fatalf("deduped candidate = %+v", got[0])
	}
}

func TestGenerateLandmarkQueryCandidatesEmptyInputs(t *testing.T) {
	if got, err := GenerateLandmarkQueryCandidates(nil, []sqlc.LocationLandmark{landmarkQueryLandmark(1, "Baluwatar")}); err != nil || len(got) != 0 {
		t.Fatalf("no services = %+v err=%v, want empty no error", got, err)
	}
	if got, err := GenerateLandmarkQueryCandidates([]string{"Plumber"}, nil); err != nil || len(got) != 0 {
		t.Fatalf("no landmarks = %+v err=%v, want empty no error", got, err)
	}
}

func TestGenerateLandmarkQueryCandidatesHasNoResultCap(t *testing.T) {
	landmarks := make([]sqlc.LocationLandmark, 12)
	for i := range landmarks {
		landmarks[i] = landmarkQueryLandmark(byte(i+1), fmt.Sprintf("Landmark %d", i+1))
	}
	got, err := GenerateLandmarkQueryCandidates([]string{"Plumber"}, landmarks)
	if err != nil || len(got) != 12 {
		t.Fatalf("candidates = %d err=%v, want 12 uncapped", len(got), err)
	}
}

func TestGenerateLandmarkQueryCandidatesRejectsOversizedText(t *testing.T) {
	if _, err := GenerateLandmarkQueryCandidates([]string{"Plumber"}, []sqlc.LocationLandmark{landmarkQueryLandmark(1, strings.Repeat("x", 600))}); err == nil {
		t.Fatalf("oversized candidate must error")
	}
}

func TestPlanLandmarkQueryInsertsDefaultsOffAndAppends(t *testing.T) {
	candidates := []LandmarkQueryCandidate{
		{Text: "Plumber near Baluwatar", LandmarkID: landmarkQueryUUID(1)},
		{Text: "Electrician near Baluwatar", LandmarkID: landmarkQueryUUID(1)},
	}
	existing := []sqlc.ProjectLocationQuery{
		{ID: landmarkQueryUUID(90), Text: "old map", Normalized: "old map", Ordinal: 2, Kind: "map"},
		{ID: landmarkQueryUUID(91), Text: "old ai", Normalized: "old ai", Ordinal: 9, Kind: "ai_question"},
	}
	got := planLandmarkQueryInserts(candidates, existing, landmarkQueryUUID(100), landmarkQueryUUID(101), landmarkQueryUUID(102))
	if len(got) != 2 {
		t.Fatalf("inserts = %+v, want 2", got)
	}
	for i, insert := range got {
		if insert.Enabled {
			t.Fatalf("insert %d enabled = true, want default off", i)
		}
		if insert.Kind != "map" || insert.Source != "generated" || insert.Origin != "landmark" {
			t.Fatalf("insert %d contract = %+v", i, insert)
		}
		if insert.ProjectID != landmarkQueryUUID(100) || insert.LocationID != landmarkQueryUUID(101) || insert.UserID != landmarkQueryUUID(102) {
			t.Fatalf("insert %d scope = %+v", i, insert)
		}
	}
	if got[0].Ordinal != 3 || got[1].Ordinal != 4 {
		t.Fatalf("ordinals = %d, %d, want 3, 4 appended after max map ordinal 2 (ai ordinal 9 ignored)", got[0].Ordinal, got[1].Ordinal)
	}
	if got[0].Normalized != "plumber near baluwatar" || got[0].LandmarkID != landmarkQueryUUID(1) {
		t.Fatalf("insert[0] = %+v", got[0])
	}
}

func TestPlanLandmarkQueryInsertsPreservesExistingByTextKey(t *testing.T) {
	// Migration 095 copied legacy raw text into normalized, so a stale
	// Normalized must not hide an existing row from canonical dedup.
	existing := []sqlc.ProjectLocationQuery{
		{ID: landmarkQueryUUID(90), Text: "Plumber near Baluwatar", Normalized: "Plumber near Baluwatar", Ordinal: 0, Enabled: false, Kind: "map", Source: "generated", Origin: "landmark"},
	}
	candidates := []LandmarkQueryCandidate{
		{Text: "Plumber near Baluwatar", LandmarkID: landmarkQueryUUID(1)},
	}
	got := planLandmarkQueryInserts(candidates, existing, landmarkQueryUUID(100), landmarkQueryUUID(101), landmarkQueryUUID(102))
	if len(got) != 0 {
		t.Fatalf("inserts = %+v, want none: existing id and enabled choice must survive", got)
	}
	// A second refresh over the rows the first refresh inserted still inserts
	// nothing, which is what keeps record ids stable across refreshes.
	first := planLandmarkQueryInserts([]LandmarkQueryCandidate{{Text: "Electrician near Baluwatar", LandmarkID: landmarkQueryUUID(2)}}, existing, landmarkQueryUUID(100), landmarkQueryUUID(101), landmarkQueryUUID(102))
	if len(first) != 1 {
		t.Fatalf("first refresh inserts = %+v, want 1", first)
	}
	after := []sqlc.ProjectLocationQuery{{
		ID: landmarkQueryUUID(93), Text: first[0].Text, Normalized: first[0].Normalized, Ordinal: first[0].Ordinal,
		Enabled: false, Kind: "map", Source: "generated", Origin: "landmark",
	}}
	again := planLandmarkQueryInserts([]LandmarkQueryCandidate{{Text: "Electrician near Baluwatar", LandmarkID: landmarkQueryUUID(2)}}, after, landmarkQueryUUID(100), landmarkQueryUUID(101), landmarkQueryUUID(102))
	if len(again) != 0 {
		t.Fatalf("repeat refresh inserts = %+v, want none", again)
	}
}
