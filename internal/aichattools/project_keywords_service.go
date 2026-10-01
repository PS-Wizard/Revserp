package aichattools

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

type projectKeywordService interface {
	LoadProjectKeywordLists(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID) (projectkeywords.KeywordLists, error)
	ReplaceSuggestedKeywords(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID, brandKeywords, nonBrandKeywords []string) error
}

type contractProjectKeywordService struct{}

func (contractProjectKeywordService) LoadProjectKeywordLists(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID) (projectkeywords.KeywordLists, error) {
	return projectkeywords.LoadProjectKeywordLists(ctx, queries, projectID)
}

func (contractProjectKeywordService) ReplaceSuggestedKeywords(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID, brandKeywords, nonBrandKeywords []string) error {
	return projectkeywords.ReplaceSuggestedKeywords(ctx, queries, projectID, brandKeywords, nonBrandKeywords)
}

type sqlExecer interface {
	Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
}

// emitProjectKeywordsUpdated writes the project_keywords.updated organization
// event inside the keyword-write transaction, so the row becomes visible
// exactly on commit and the existing SSE stream (pg_notify trigger on
// organization_events) delivers it. No trigger on project_keywords may also
// fire for this event, or subscribers would see it twice.
func emitProjectKeywordsUpdated(ctx context.Context, exec sqlExecer, orgID, projectID pgtype.UUID, brandCount, nonBrandCount int) error {
	payload, err := json.Marshal(map[string]interface{}{
		"brand_keywords":     brandCount,
		"non_brand_keywords": nonBrandCount,
		"source":             "revserp",
	})
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx,
		`SELECT insert_organization_event($1, $2, 'project_keywords.updated', NULL, $3::jsonb)`,
		orgID, projectID, string(payload))
	return err
}
