package aiaudit

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectsetup"
)

func TestFinalizePromptGenerationSuccessSetsSetupMarker(t *testing.T) {
	projectID := pgtype.UUID{Bytes: [16]byte{12}, Valid: true}
	orgID := pgtype.UUID{Bytes: [16]byte{13}, Valid: true}
	crawlID := pgtype.UUID{Bytes: [16]byte{14}, Valid: true}
	requester := pgtype.UUID{Bytes: [16]byte{15}, Valid: true}
	setupID := pgtype.UUID{Bytes: [16]byte{16}, Valid: true}
	auditID := pgtype.UUID{Bytes: [16]byte{17}, Valid: true}
	fake := &fakeSetupQueries{
		setup:       sqlc.ProjectSetup{ID: setupID, ProjectID: projectID, OrganizationID: orgID, CrawlID: crawlID, RequestedByUserID: requester, Status: projectsetup.StatusPromptGeneration},
		features:    sqlc.GetOrganizationFeaturesByProjectIDRow{AiVisibilityAuditMonthlyLimit: 10},
		reserveUsed: 1,
		audit:       sqlc.AiAudit{ID: auditID, ProjectID: projectID, CrawlID: crawlID, Status: "queued", ProjectSetupID: setupID},
	}

	if err := finalizePromptGenerationSuccess(context.Background(), fake, finalizationJob(projectID)); err != nil {
		t.Fatalf("finalizePromptGenerationSuccess: %v", err)
	}
	if fake.auditArg.ProjectSetupID != setupID {
		t.Fatalf("audit ProjectSetupID = %s, want setup %s", fake.auditArg.ProjectSetupID.String(), setupID.String())
	}
}
