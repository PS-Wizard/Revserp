package seo

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func TestExactDuplicateEvidenceCarriesMembers(t *testing.T) {
	t.Parallel()
	pages := []shared.PageFact{
		{ID: testDuplicateUUID(1), URL: "https://example.com/a", ContentSHA256: "same"},
		{ID: testDuplicateUUID(2), URL: "https://example.com/b", ContentSHA256: "same"},
		{ID: testDuplicateUUID(3), URL: "https://example.com/c", ContentSHA256: "other"},
	}
	issues, evidence, err := deriveDuplicateContentIssuesWithEvidence(context.Background(), pages)
	if err != nil {
		t.Fatalf("deriveDuplicateContentIssuesWithEvidence: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2", len(issues))
	}
	if len(evidence.Groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(evidence.Groups))
	}
	if len(evidence.Groups[0].Members) != 2 {
		t.Fatalf("got %d members, want 2", len(evidence.Groups[0].Members))
	}
}

func testDuplicateUUID(last byte) pgtype.UUID {
	var bytes [16]byte
	bytes[15] = last
	return pgtype.UUID{Bytes: bytes, Valid: true}
}
