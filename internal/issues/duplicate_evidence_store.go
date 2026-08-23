package issues

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues/seo"
)

func persistDuplicateEvidence(ctx context.Context, queries *sqlc.Queries, crawlID pgtype.UUID, evidence seo.DuplicateEvidence) error {
	if err := queries.DeleteCrawlIssueGroupsForCrawl(ctx, crawlID); err != nil {
		return fmt.Errorf("delete duplicate groups: %w", err)
	}
	if err := queries.DeleteCrawlIssueRelationsForCrawl(ctx, crawlID); err != nil {
		return fmt.Errorf("delete duplicate relations: %w", err)
	}

	for _, group := range evidence.Groups {
		groupID, err := queries.CreateCrawlIssueGroup(ctx, sqlc.CreateCrawlIssueGroupParams{CrawlID: crawlID, IssueType: group.IssueType})
		if err != nil {
			return fmt.Errorf("create duplicate group: %w", err)
		}
		members := make([]sqlc.CreateCrawlIssueGroupMembersParams, 0, len(group.Members))
		pageIDs := make([]pgtype.UUID, 0, len(group.Members))
		for _, member := range group.Members {
			members = append(members, sqlc.CreateCrawlIssueGroupMembersParams{GroupID: groupID, CrawlPageID: member.CrawlPageID, Url: member.URL})
			pageIDs = append(pageIDs, member.CrawlPageID)
		}
		if len(members) > 0 {
			if _, err := queries.CreateCrawlIssueGroupMembers(ctx, members); err != nil {
				return fmt.Errorf("create duplicate group members: %w", err)
			}
		}
		if err := queries.LinkCrawlIssuesToIssueGroup(ctx, sqlc.LinkCrawlIssuesToIssueGroupParams{IssueGroupID: groupID, CrawlID: crawlID, IssueType: group.IssueType, Column4: pageIDs}); err != nil {
			return fmt.Errorf("link duplicate issues to group: %w", err)
		}
	}

	relations := make([]sqlc.CreateCrawlIssueRelationsParams, 0, len(evidence.Relations))
	for _, relation := range evidence.Relations {
		relations = append(relations, sqlc.CreateCrawlIssueRelationsParams{
			CrawlID: crawlID, IssueType: relation.IssueType,
			LeftCrawlPageID: relation.LeftPage.CrawlPageID, RightCrawlPageID: relation.RightPage.CrawlPageID,
			Similarity: pgtype.Float8{Float64: relation.Similarity, Valid: true},
		})
	}
	if len(relations) > 0 {
		if _, err := queries.CreateCrawlIssueRelations(ctx, relations); err != nil {
			return fmt.Errorf("create duplicate relations: %w", err)
		}
	}
	return nil
}
