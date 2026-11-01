package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywordanalysis"
	"github.com/ps-wizard/revserp/internal/projectkeywords"
)

// listCompetitorsName is the read-only project competitor roster tool: it
// lists saved competitors with their crawl status and keyword counts, and
// never starts a crawl.
const listCompetitorsName = "list_competitors"

const listCompetitorsSchema = `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

// competitorRosterStore is the narrow data surface both competitor read tools
// need. Membership is enforced by the project join in the roster queries and
// rechecked through GetProjectByIDForUser, so a stale or forged scope cannot
// read another project's roster.
type competitorRosterStore interface {
	projectKeywordAccessChecker
	ListProjectCompetitorsForUser(ctx context.Context, arg sqlc.ListProjectCompetitorsForUserParams) ([]sqlc.ProjectCompetitor, error)
	GetLatestCompletedCrawlForProject(ctx context.Context, projectID pgtype.UUID) (pgtype.UUID, error)
	GetCompetitorChildCrawlForParent(ctx context.Context, arg sqlc.GetCompetitorChildCrawlForParentParams) (sqlc.GetCompetitorChildCrawlForParentRow, error)
	ListKeywordCoveragePagesForCrawl(ctx context.Context, crawlID pgtype.UUID) ([]sqlc.ListKeywordCoveragePagesForCrawlRow, error)
}

// competitorAnalysesLister reuses keywordanalysis.ListCompetitorAnalyses.
// Production wires the live implementation; tests substitute a fake.
type competitorAnalysesLister interface {
	ListCompetitorAnalyses(ctx context.Context, competitorID pgtype.UUID) ([]keywordanalysis.Analysis, error)
}

type liveCompetitorAnalyses struct {
	queries *sqlc.Queries
}

func (l liveCompetitorAnalyses) ListCompetitorAnalyses(ctx context.Context, competitorID pgtype.UUID) ([]keywordanalysis.Analysis, error) {
	return keywordanalysis.ListCompetitorAnalyses(ctx, l.queries, competitorID)
}

func listCompetitorsTool() Tool {
	return Tool{
		Def: Def{
			Name:        listCompetitorsName,
			Label:       "List competitors",
			Description: "List the current project's saved competitor roster: id, name, seed URL, crawl status (completed, queued, running, failed, or not_crawled when no competitor crawl exists yet), and brand/non-brand keyword counts from the stored AI estimates. Save-only rows appear before their first crawl. Use it to see which competitors are set and which are crawled before calling get_competitor_keywords. Project scoped only: location turns are denied.",
			Schema:      json.RawMessage(listCompetitorsSchema),
		},
		Execute: executeListCompetitors,
	}
}

type listCompetitorsExecutor struct {
	store    competitorRosterStore
	analyses competitorAnalysesLister
}

type listCompetitorsEntry struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	SeedURL          string `json:"seed_url"`
	CrawlStatus      string `json:"crawl_status"`
	BrandKeywords    int    `json:"brand_keywords"`
	NonBrandKeywords int    `json:"non_brand_keywords"`
}

type listCompetitorsResponse struct {
	Competitors []listCompetitorsEntry `json:"competitors"`
	Total       int                    `json:"total"`
}

func executeListCompetitors(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.LocationID.Valid {
		return Result{Content: listCompetitorsName + " error: location turns cannot list project competitors"}, nil
	}
	if s.Queries == nil {
		return Result{}, errors.New("list_competitors: scope has no queries")
	}
	exec := listCompetitorsExecutor{store: s.Queries, analyses: liveCompetitorAnalyses{queries: s.Queries}}
	return exec.run(ctx, args, s.ProjectID, s.UserID)
}

// run executes one list_competitors call. Membership is rechecked through the
// project join even though the scope already carries the project, so a stale
// or forged scope cannot read another project's roster.
func (e *listCompetitorsExecutor) run(ctx context.Context, raw json.RawMessage, projectID, userID pgtype.UUID) (Result, error) {
	if err := rejectProjectKeywordsArgs(raw); err != nil {
		return Result{Content: listCompetitorsName + " error: " + err.Error()}, nil
	}
	if _, err := e.store.GetProjectByIDForUser(ctx, sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{Content: listCompetitorsName + " error: project not found or access denied"}, nil
		}
		return Result{}, fmt.Errorf("%s: check project access: %w", listCompetitorsName, err)
	}
	roster, err := e.store.ListProjectCompetitorsForUser(ctx, sqlc.ListProjectCompetitorsForUserParams{UserID: userID, ProjectID: projectID})
	if err != nil {
		return Result{}, fmt.Errorf("%s: list competitors: %w", listCompetitorsName, err)
	}
	// The latest completed parent crawl anchors the child-crawl lookup: a
	// competitor crawl always hangs off one parent crawl.
	var parentCrawlID pgtype.UUID
	if id, err := e.store.GetLatestCompletedCrawlForProject(ctx, projectID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return Result{}, fmt.Errorf("%s: read latest crawl: %w", listCompetitorsName, err)
		}
	} else {
		parentCrawlID = id
	}
	entries := make([]listCompetitorsEntry, 0, len(roster))
	crawled := 0
	for _, competitor := range roster {
		status := "not_crawled"
		if parentCrawlID.Valid {
			if child, err := e.store.GetCompetitorChildCrawlForParent(ctx, sqlc.GetCompetitorChildCrawlForParentParams{ParentCrawlID: parentCrawlID, CompetitorID: competitor.ID}); err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					return Result{}, fmt.Errorf("%s: read competitor crawl: %w", listCompetitorsName, err)
				}
			} else {
				status = child.Status
			}
		}
		brand, nonBrand, err := e.countCompetitorKeywords(ctx, competitor.ID)
		if err != nil {
			return Result{}, err
		}
		if status == "completed" {
			crawled++
		}
		entries = append(entries, listCompetitorsEntry{
			ID:               competitor.ID.String(),
			Name:             competitor.Name,
			SeedURL:          competitor.SeedUrl,
			CrawlStatus:      status,
			BrandKeywords:    brand,
			NonBrandKeywords: nonBrand,
		})
	}
	content, err := json.Marshal(listCompetitorsResponse{Competitors: entries, Total: len(entries)})
	if err != nil {
		return Result{}, fmt.Errorf("%s: marshal response: %w", listCompetitorsName, err)
	}
	summary := fmt.Sprintf("competitors: %d (%d crawled)", len(entries), crawled)
	if len(entries) == 0 {
		summary = "no competitors saved yet"
	}
	return Result{Content: string(content), Summary: summary}, nil
}

func (e *listCompetitorsExecutor) countCompetitorKeywords(ctx context.Context, competitorID pgtype.UUID) (brand, nonBrand int, err error) {
	analyses, err := e.analyses.ListCompetitorAnalyses(ctx, competitorID)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: list competitor keywords: %w", listCompetitorsName, err)
	}
	for _, analysis := range analyses {
		switch analysis.Kind {
		case projectkeywords.ProjectKeywordKindBrand:
			brand++
		case projectkeywords.ProjectKeywordKindNonBrand:
			nonBrand++
		}
	}
	return brand, nonBrand, nil
}
