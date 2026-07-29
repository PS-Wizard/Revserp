package aitools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// compareReader is the narrow DB port compare_projects depends on. It composes
// the project lookup (for resolving the competitor by name) with the same
// score-summary reads get_score_summary uses, once per side.
type compareReader interface {
	projectReader
	scoreSummaryReader
	ListCrawlsForProject(ctx context.Context, arg sqlc.ListCrawlsForProjectParams) ([]sqlc.ListCrawlsForProjectRow, error)
}

func compareProjectsTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name: "compare_projects",
			// "Competitor analysis" is named explicitly: users ask for this by
			// that phrase, and without it the model reaches for `navigate` (which
			// has no compare destination) or plain get_score_summary.
			Description: "Run a competitor analysis: compare the current project against another project side by side, and open Revserp's comparison view. Takes the competitor's exact project name (case-insensitive), never an ID — use list_projects to see the available names. Returns both sides' overall, per-pillar, and per-bucket scores so the comparison can be explained. The competitor's most recent completed crawl is used.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"competitor":{"type":"string"}},"required":["competitor"],"additionalProperties":false}`),
		},
		Execute: func(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
			return execCompareProjects(ctx, args, s, s.Queries)
		},
	}
}

type compareSideOutput struct {
	ProjectName string             `json:"project_name"`
	BaseURL     string             `json:"base_url"`
	Scores      scoreSummaryOutput `json:"scores"`
}

type compareOutput struct {
	Current    compareSideOutput `json:"current"`
	Competitor compareSideOutput `json:"competitor"`
}

func execCompareProjects(ctx context.Context, args json.RawMessage, s Scope, reader compareReader) (Result, error) {
	var parsed struct {
		Competitor string `json:"competitor"`
	}
	if err := strictObject(args, &parsed, "competitor"); err != nil {
		return Result{}, errors.New("arguments must be exactly an object with a competitor project name")
	}
	name := strings.TrimSpace(parsed.Competitor)
	if name == "" || len(name) > maxProjectNameLength {
		return Result{}, errors.New("competitor project name must be nonempty and within the allowed length")
	}
	if !s.UserID.Valid || !s.OrgID.Valid || reader == nil {
		return Result{}, errors.New("project access is unavailable")
	}
	if !s.ProjectID.Valid || !s.CrawlID.Valid {
		return Result{}, errors.New("open a project with a completed crawl before running a competitor analysis")
	}

	// Membership is enforced here, by listing only what this user can see in the
	// active org. Everything downstream reads a project that survived this list.
	projects, err := reader.ListProjectsForOrganizationForUser(ctx, sqlc.ListProjectsForOrganizationForUserParams{
		OrganizationID: s.OrgID,
		UserID:         s.UserID,
	})
	if err != nil {
		return Result{}, err
	}

	var competitor *sqlc.Project
	var current *sqlc.Project
	matches := 0
	for i := range projects {
		if projects[i].ID == s.ProjectID {
			current = &projects[i]
		}
		if strings.EqualFold(projects[i].Name, name) {
			competitor = &projects[i]
			matches++
		}
	}
	switch {
	case matches == 0:
		return Result{}, fmt.Errorf("no visible project named %q", name)
	case matches > 1:
		return Result{}, fmt.Errorf("multiple visible projects match %q; use the exact visible name", name)
	case current == nil:
		return Result{}, errors.New("the active project is unavailable")
	case competitor.ID == s.ProjectID:
		return Result{}, errors.New("a competitor analysis needs two different projects; name a project other than the active one")
	}

	competitorCrawlID, err := latestCompletedCrawlID(ctx, reader, competitor.ID)
	if err != nil {
		return Result{}, err
	}
	if !competitorCrawlID.Valid {
		return Result{}, fmt.Errorf("%q has no completed crawl to compare against; run a crawl on it first", competitor.Name)
	}

	currentScores, err := compareSideScores(ctx, s.CrawlID, s.UserID, reader)
	if err != nil {
		return Result{}, err
	}
	competitorScores, err := compareSideScores(ctx, competitorCrawlID, s.UserID, reader)
	if err != nil {
		return Result{}, err
	}

	output := compareOutput{
		Current: compareSideOutput{
			ProjectName: current.Name,
			BaseURL:     current.BaseUrl,
			Scores:      currentScores,
		},
		Competitor: compareSideOutput{
			ProjectName: competitor.Name,
			BaseURL:     competitor.BaseUrl,
			Scores:      competitorScores,
		},
	}

	result, err := jsonResult(output, fmt.Sprintf("compared against %s", competitor.Name))
	if err != nil {
		return Result{}, err
	}
	result.CompareProjectID = competitor.ID.String()
	result.CompareCrawlID = competitorCrawlID.String()
	return result, nil
}

// latestCompletedCrawlID mirrors the frontend's default crawl selection and the
// agent loop's own project-switch behavior: newest completed crawl, or invalid
// when the project has never produced one.
func latestCompletedCrawlID(ctx context.Context, reader compareReader, projectID pgtype.UUID) (pgtype.UUID, error) {
	crawls, err := reader.ListCrawlsForProject(ctx, sqlc.ListCrawlsForProjectParams{
		ProjectID: projectID,
		Column2:   "completed",
		Limit:     1,
		Offset:    0,
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	if len(crawls) == 0 {
		return pgtype.UUID{}, nil
	}
	return crawls[0].ID, nil
}

// compareSideScores reuses get_score_summary's reader so both sides of a
// comparison are shaped exactly like a normal score summary. The underlying
// queries are user-scoped, so a crawl the caller cannot see cannot be read here
// even though the crawl ID did not come from Scope.
func compareSideScores(ctx context.Context, crawlID, userID pgtype.UUID, reader scoreSummaryReader) (scoreSummaryOutput, error) {
	result, err := execGetScoreSummary(ctx, crawlID, userID, reader)
	if err != nil {
		return scoreSummaryOutput{}, err
	}
	var scores scoreSummaryOutput
	if err := json.Unmarshal([]byte(result.Content), &scores); err != nil {
		return scoreSummaryOutput{}, err
	}
	return scores, nil
}
