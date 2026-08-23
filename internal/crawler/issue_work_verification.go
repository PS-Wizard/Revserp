package crawler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func reconcileIssueWorkAttempts(ctx context.Context, queries *sqlc.Queries, crawlID pgtype.UUID) error {
	attempts, err := queries.ListIssueWorkAttemptsForVerification(ctx, crawlID)
	if err != nil {
		return fmt.Errorf("list issue work attempts for verification: %w", err)
	}
	for _, attempt := range attempts {
		result := "fixed"
		workStatus := "fixed"
		if !attempt.SourceCrawlID.Valid {
			result, workStatus = "not_verified", "awaiting_verification"
		} else {
			switch attempt.SubjectKind {
			case "group":
				if !attempt.SourceIssueGroupID.Valid {
					result, workStatus = "not_verified", "awaiting_verification"
					break
				}
				evidence, err := queries.GetDuplicateGroupVerificationEvidence(ctx, sqlc.GetDuplicateGroupVerificationEvidenceParams{
					IssueType: attempt.IssueType, SourceGroupID: attempt.SourceIssueGroupID, CrawlID: crawlID,
				})
				if err != nil {
					return fmt.Errorf("get duplicate group verification evidence: %w", err)
				}
				result, workStatus = classifyIssueWorkVerification(evidence.AllMembersObserved.Valid && evidence.AllMembersObserved.Bool, true, evidence.IssuePresent)
			case "page":
				evidence, err := queries.GetPageIssueVerificationEvidence(ctx, sqlc.GetPageIssueVerificationEvidenceParams{
					CrawlID: crawlID, Url: attempt.SubjectKey, Pillar: attempt.Pillar,
					Bucket: attempt.Bucket, IssueType: attempt.IssueType,
				})
				if err != nil {
					return fmt.Errorf("get page issue verification evidence: %w", err)
				}
				result, workStatus = classifyIssueWorkVerification(evidence.PageObserved, evidence.RequiredEvidenceObserved, evidence.IssuePresent)
			case "site":
				evidence, err := queries.GetSiteIssueVerificationEvidence(ctx, sqlc.GetSiteIssueVerificationEvidenceParams{
					CrawlID: crawlID, SourceCrawlID: attempt.SourceCrawlID, Pillar: attempt.Pillar, Bucket: attempt.Bucket, IssueType: attempt.IssueType,
				})
				if err != nil {
					return fmt.Errorf("get site issue verification evidence: %w", err)
				}
				result, workStatus = classifyIssueWorkVerification(evidence.CoverageObserved.Valid && evidence.CoverageObserved.Bool, true, evidence.IssuePresent)
			default:
				result, workStatus = "not_verified", "awaiting_verification"
			}
		}

		if err := queries.UpdateIssueWorkAttemptVerification(ctx, sqlc.UpdateIssueWorkAttemptVerificationParams{
			Status: result, VerificationCrawlID: crawlID, AttemptID: attempt.AttemptID,
		}); err != nil {
			return fmt.Errorf("update issue work attempt verification: %w", err)
		}
		if err := queries.UpdateIssueWorkItemStatusAfterVerification(ctx, sqlc.UpdateIssueWorkItemStatusAfterVerificationParams{
			WorkItemID: attempt.WorkItemID, AttemptID: attempt.AttemptID, Status: workStatus,
		}); err != nil {
			return fmt.Errorf("update issue work item status: %w", err)
		}
	}
	return nil
}

func classifyIssueWorkVerification(observed, requiredEvidenceObserved, issuePresent bool) (string, string) {
	if !observed || !requiredEvidenceObserved {
		return "not_verified", "awaiting_verification"
	}
	if issuePresent {
		return "still_open", "open"
	}
	return "fixed", "fixed"
}
