package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

const getProjectKeywordsName = "get_project_keywords"

const getProjectKeywordsSchema = `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

type projectKeywordAccessChecker interface {
	GetProjectByIDForUser(ctx context.Context, arg sqlc.GetProjectByIDForUserParams) (sqlc.Project, error)
}

type getProjectKeywordsExecutor struct {
	access   projectKeywordAccessChecker
	keywords projectKeywordService
}

func getProjectKeywordsTool() Tool {
	return Tool{
		Def: Def{
			Name:        getProjectKeywordsName,
			Label:       "Get project keywords",
			Description: "Read the project's keyword lists: user-defined keywords (added manually, never erased by AI), REVSerp-suggested keywords (brand and non-brand, written only by update_project_keywords or the bootstrap), and the combined deduplicated union of both with per-entry source badges. Use it for keyword research context and before refreshing suggestions. To change suggestions, call update_project_keywords with complete brand_keywords and non_brand_keywords lists grounded in live Search Console data or crawled content.",
			Schema:      json.RawMessage(getProjectKeywordsSchema),
		},
		Execute: executeGetProjectKeywords,
	}
}

func executeGetProjectKeywords(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.LocationID.Valid {
		return executeGetProjectKeywordsLocal(ctx, args, s)
	}
	if s.Queries == nil {
		return Result{}, errors.New("get_project_keywords: scope has no queries")
	}
	keywords := projectKeywordService(contractProjectKeywordService{})
	exec := getProjectKeywordsExecutor{access: s.Queries, keywords: keywords}
	return exec.run(ctx, args, s.ProjectID, s.UserID, s.Queries)
}

// run executes one get_project_keywords call. Membership is rechecked through
// the project join even though the scope already carries the project, so a
// stale or forged scope cannot read another project's lists.
func (e *getProjectKeywordsExecutor) run(ctx context.Context, raw json.RawMessage, projectID, userID pgtype.UUID, queries *sqlc.Queries) (Result, error) {
	if err := rejectProjectKeywordsArgs(raw); err != nil {
		return Result{Content: getProjectKeywordsName + " error: " + err.Error()}, nil
	}
	if _, err := e.access.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{Content: getProjectKeywordsName + " error: project not found or access denied"}, nil
		}
		return Result{}, fmt.Errorf("%s: check project access: %w", getProjectKeywordsName, err)
	}
	lists, err := e.keywords.LoadProjectKeywordLists(ctx, queries, projectID)
	if err != nil {
		return Result{}, fmt.Errorf("%s: load keyword lists: %w", getProjectKeywordsName, err)
	}
	content, err := json.Marshal(nonNullProjectKeywordLists(lists))
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal keyword lists: %w", getProjectKeywordsName, err)
	}
	summary := fmt.Sprintf("keywords: %d user-defined, %d suggested, %d combined",
		len(lists.UserDefined), len(lists.RevserpSuggested), len(lists.Combined))
	return Result{Content: string(content), Summary: summary}, nil
}

// rejectProjectKeywordsArgs rejects every argument including tenant IDs: the
// project comes from the server-side scope, never the model.
func rejectProjectKeywordsArgs(raw json.RawMessage) error {
	fields, err := strictJSONFields(raw)
	if err != nil {
		return err
	}
	for key := range fields {
		return fmt.Errorf("unknown argument %q", key)
	}
	return nil
}

func nonNullProjectKeywordLists(lists projectkeywords.KeywordLists) projectkeywords.KeywordLists {
	if lists.UserDefined == nil {
		lists.UserDefined = []projectkeywords.Keyword{}
	}
	if lists.RevserpSuggested == nil {
		lists.RevserpSuggested = []projectkeywords.Keyword{}
	}
	if lists.Combined == nil {
		lists.Combined = []projectkeywords.CombinedKeyword{}
	}
	for i := range lists.Combined {
		if lists.Combined[i].Sources == nil {
			lists.Combined[i].Sources = []string{}
		}
	}
	return lists
}
