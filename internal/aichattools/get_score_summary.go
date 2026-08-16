package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

const (
	scoreSummaryDefaultLimit = 10
	scoreSummaryMaxLimit     = 20

	scoreSourceBreakdown    = "breakdown"
	scoreSourceCrawlColumns = "crawl_columns"
)

const getScoreSummarySchema = `{
  "type": "object",
  "properties": {
    "pillar": {"type": "string", "enum": ["seo", "aeo", "pagespeed"], "description": "Scope the detail to one pillar"},
    "include_buckets": {"type": "boolean", "default": true, "description": "Include the top buckets per pillar"},
    "compare": {"type": "boolean", "default": true, "description": "Include the previous crawl's scores for comparison"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20, "default": 10, "description": "Max buckets per pillar"}
  },
  "additionalProperties": false
}`

// scoreBreakdownReader reads one crawl's persisted scoring snapshot through the
// user-membership join.
type scoreBreakdownReader interface {
	GetCrawlScoreBreakdownByCrawlForUser(ctx context.Context, arg sqlc.GetCrawlScoreBreakdownByCrawlForUserParams) (sqlc.CrawlScoreBreakdown, error)
}

// scoreHistoryReader lists a project's completed crawl snapshots, newest first.
type scoreHistoryReader interface {
	ListCompletedProjectCrawlScoreBreakdownsForUser(ctx context.Context, arg sqlc.ListCompletedProjectCrawlScoreBreakdownsForUserParams) ([]sqlc.ListCompletedProjectCrawlScoreBreakdownsForUserRow, error)
}

// crawlScoresReader reads one crawl's top-level score columns through the
// user-membership join; the fallback when no breakdown snapshot exists.
type crawlScoresReader interface {
	GetCrawlByIDForUser(ctx context.Context, arg sqlc.GetCrawlByIDForUserParams) (sqlc.GetCrawlByIDForUserRow, error)
}

// scoreSummaryExecutor runs one get_score_summary call against narrow reader
// interfaces so tests can substitute fakes without a database.
type scoreSummaryExecutor struct {
	current scoreBreakdownReader
	history scoreHistoryReader
	crawls  crawlScoresReader
}

func getScoreSummaryTool() Tool {
	return Tool{
		Def: Def{
			Name:        "get_score_summary",
			Label:       "Get score summary",
			Description: "Read the current crawl's score summary: the overall score, per-pillar scores with weights, penalties, and the top contributing buckets, optionally compared with the previous crawl. Use this before read_issues to explain why a score is where it is.",
			Schema:      json.RawMessage(getScoreSummarySchema),
		},
		Execute: executeGetScoreSummary,
	}
}

func executeGetScoreSummary(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.Queries == nil {
		return Result{}, errors.New("get_score_summary: scope has no queries")
	}
	exec := scoreSummaryExecutor{
		current: s.Queries,
		history: s.Queries,
		crawls:  s.Queries,
	}
	return exec.run(ctx, args, s.CrawlID, s.ProjectID, s.UserID)
}

// getScoreSummaryArgs is the raw, unvalidated argument set.
type getScoreSummaryArgs struct {
	Pillar         string
	IncludeBuckets bool
	Compare        bool
	Limit          int
}

// getScoreSummaryResponse is the JSON the model sees.
type getScoreSummaryResponse struct {
	CrawlID          string                `json:"crawl_id"`
	ScoringVersion   string                `json:"scoring_version,omitempty"`
	CoverageScale    float64               `json:"coverage_scale,omitempty"`
	TotalScoredPages int32                 `json:"total_scored_pages,omitempty"`
	OverallScore     int32                 `json:"overall_score"`
	Source           string                `json:"source"`
	Pillars          []scoreSummaryPillar  `json:"pillars"`
	Previous         *scoreSummaryPrevious `json:"previous,omitempty"`
}

type scoreSummaryPillar struct {
	ID                   string               `json:"id"`
	Label                string               `json:"label"`
	Score                int32                `json:"score"`
	Weight               float64              `json:"weight"`
	WeightedContribution float64              `json:"weighted_contribution"`
	TotalPenalty         float64              `json:"total_penalty"`
	IssueRowCount        int32                `json:"issue_row_count"`
	AffectedURLCount     int32                `json:"affected_url_count"`
	TopBuckets           []scoreSummaryBucket `json:"top_buckets,omitempty"`
}

type scoreSummaryBucket struct {
	ID                   string  `json:"id"`
	Label                string  `json:"label"`
	Score                int32   `json:"score"`
	WeightedContribution float64 `json:"weighted_contribution"`
	TotalPenalty         float64 `json:"total_penalty"`
	IssueRowCount        int32   `json:"issue_row_count"`
	AffectedURLCount     int32   `json:"affected_url_count"`
}

type scoreSummaryPrevious struct {
	CrawlID      string                  `json:"crawl_id"`
	CompletedAt  string                  `json:"completed_at,omitempty"`
	OverallScore int32                   `json:"overall_score"`
	Pillars      []scoreSummaryPillarRef `json:"pillars"`
}

type scoreSummaryPillarRef struct {
	ID    string `json:"id"`
	Score int32  `json:"score"`
}

// run executes one get_score_summary call. The response is bounded by
// construction (at most three pillars, each with a capped bucket list), so it
// never spends the turn row budget; read_issues owns row fetching.
func (e *scoreSummaryExecutor) run(ctx context.Context, raw json.RawMessage, crawlID, projectID, userID pgtype.UUID) (Result, error) {
	args, err := parseGetScoreSummaryArgs(raw)
	if err != nil {
		return Result{Content: "get_score_summary error: " + err.Error()}, nil
	}
	if args.Pillar != "" && !slices.Contains(validReadIssuesPillars, args.Pillar) {
		return Result{Content: fmt.Sprintf("get_score_summary error: unknown pillar %q; valid pillars: %s", args.Pillar, strings.Join(validReadIssuesPillars, ", "))}, nil
	}

	current, source, err := e.currentScores(ctx, crawlID, userID)
	if err != nil {
		return Result{}, err
	}

	var previous *scoreSummaryPrevious
	if args.Compare {
		previous, err = e.previousScores(ctx, crawlID, projectID, userID, args.Pillar)
		if err != nil {
			return Result{}, err
		}
	}

	response := getScoreSummaryResponse{
		CrawlID:      crawlID.String(),
		OverallScore: current.overall,
		Source:       source,
		Pillars:      shapeScoreSummaryPillars(current.pillars, args.Pillar, args.IncludeBuckets, args.Limit),
		Previous:     previous,
	}
	if source == scoreSourceBreakdown {
		response.ScoringVersion = current.version
		response.CoverageScale = current.coverageScale
		response.TotalScoredPages = current.scoredPages
	}

	content, err := json.Marshal(response)
	if err != nil {
		return Result{}, fmt.Errorf("get_score_summary: marshal response: %w", err)
	}
	return Result{
		Content: string(content),
		Summary: scoreSummaryLine(response),
	}, nil
}

// currentScores returns the current crawl's scores from the breakdown snapshot
// when one exists, falling back to the crawl's top-level score columns.
func (e *scoreSummaryExecutor) currentScores(ctx context.Context, crawlID, userID pgtype.UUID) (scoreSet, string, error) {
	row, err := e.current.GetCrawlScoreBreakdownByCrawlForUser(ctx, sqlc.GetCrawlScoreBreakdownByCrawlForUserParams{
		CrawlID: crawlID,
		UserID:  userID,
	})
	if err == nil {
		snapshot, err := unmarshalScoreSnapshot(row.BreakdownJson)
		if err != nil {
			return scoreSet{}, "", fmt.Errorf("get_score_summary: parse breakdown: %w", err)
		}
		return scoreSet{overall: snapshot.OverallScore, version: snapshot.ScoringVersion, coverageScale: snapshot.CoverageScale, scoredPages: snapshot.TotalScoredPages, pillars: snapshot.Pillars}, scoreSourceBreakdown, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return scoreSet{}, "", fmt.Errorf("get_score_summary: breakdown: %w", err)
	}

	crawl, err := e.crawls.GetCrawlByIDForUser(ctx, sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return scoreSet{}, "", fmt.Errorf("get_score_summary: crawl not found")
		}
		return scoreSet{}, "", fmt.Errorf("get_score_summary: crawl scores: %w", err)
	}
	return scoreSet{overall: crawl.OverallScore.Int32, pillars: crawlPillarScores(crawl)}, scoreSourceCrawlColumns, nil
}

// previousScores finds the most recent completed crawl with a breakdown whose
// id differs from the current crawl, or nil when there is none. Pillar scores
// are filtered to the same pillar scope as the main response.
func (e *scoreSummaryExecutor) previousScores(ctx context.Context, crawlID, projectID, userID pgtype.UUID, pillar string) (*scoreSummaryPrevious, error) {
	rows, err := e.history.ListCompletedProjectCrawlScoreBreakdownsForUser(ctx, sqlc.ListCompletedProjectCrawlScoreBreakdownsForUserParams{
		ProjectID: projectID,
		UserID:    userID,
		Limit:     2,
	})
	if err != nil {
		return nil, fmt.Errorf("get_score_summary: history: %w", err)
	}
	for _, row := range rows {
		if row.CrawlID == crawlID {
			continue
		}
		snapshot, err := unmarshalScoreSnapshot(row.BreakdownJson)
		if err != nil {
			return nil, fmt.Errorf("get_score_summary: parse previous breakdown: %w", err)
		}
		previous := &scoreSummaryPrevious{
			CrawlID:      row.CrawlID.String(),
			OverallScore: snapshot.OverallScore,
			Pillars:      make([]scoreSummaryPillarRef, 0, len(snapshot.Pillars)),
		}
		if row.CompletedAt.Valid {
			previous.CompletedAt = row.CompletedAt.Time.UTC().Format(time.RFC3339)
		}
		for _, pillarBreakdown := range snapshot.Pillars {
			if pillar != "" && pillarBreakdown.ID != pillar {
				continue
			}
			previous.Pillars = append(previous.Pillars, scoreSummaryPillarRef{ID: pillarBreakdown.ID, Score: pillarBreakdown.Score})
		}
		return previous, nil
	}
	return nil, nil
}

// scoreSet is one crawl's scores in executor-internal shape; the source field
// of the response distinguishes breakdown detail from crawl-column fallback.
type scoreSet struct {
	overall       int32
	version       string
	coverageScale float64
	scoredPages   int32
	pillars       []shared.PillarScoreBreakdown
}

func crawlPillarScores(crawl sqlc.GetCrawlByIDForUserRow) []shared.PillarScoreBreakdown {
	return []shared.PillarScoreBreakdown{
		{ID: "seo", Label: "SEO", Score: crawl.SeoScore.Int32},
		{ID: "aeo", Label: "AEO", Score: crawl.AeoScore.Int32},
		{ID: "pagespeed", Label: "PageSpeed", Score: crawl.PagespeedScore.Int32},
	}
}

func unmarshalScoreSnapshot(raw []byte) (shared.ScoreBreakdownSnapshot, error) {
	var snapshot shared.ScoreBreakdownSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

// shapeScoreSummaryPillars filters to one pillar when requested, then folds
// each pillar's buckets into a top-N list ordered by total penalty (biggest
// score killers first), with human labels.
func shapeScoreSummaryPillars(pillars []shared.PillarScoreBreakdown, pillar string, includeBuckets bool, limit int) []scoreSummaryPillar {
	shaped := make([]scoreSummaryPillar, 0, len(pillars))
	for _, pillarBreakdown := range pillars {
		if pillar != "" && pillarBreakdown.ID != pillar {
			continue
		}
		out := scoreSummaryPillar{
			ID:                   pillarBreakdown.ID,
			Label:                pillarBreakdown.Label,
			Score:                pillarBreakdown.Score,
			Weight:               round2(pillarBreakdown.Weight),
			WeightedContribution: round2(pillarBreakdown.WeightedContribution),
			TotalPenalty:         round2(pillarBreakdown.TotalPenalty),
			IssueRowCount:        pillarBreakdown.IssueRowCount,
			AffectedURLCount:     pillarBreakdown.AffectedURLCount,
		}
		if includeBuckets {
			buckets := make([]scoreSummaryBucket, 0, len(pillarBreakdown.Buckets))
			for _, bucket := range pillarBreakdown.Buckets {
				buckets = append(buckets, scoreSummaryBucket{
					ID:                   bucket.ID,
					Label:                bucket.Label,
					Score:                bucket.Score,
					WeightedContribution: round2(bucket.WeightedContribution),
					TotalPenalty:         round2(bucket.TotalPenalty),
					IssueRowCount:        bucket.IssueRowCount,
					AffectedURLCount:     bucket.AffectedURLCount,
				})
			}
			sort.Slice(buckets, func(i, j int) bool {
				if buckets[i].TotalPenalty != buckets[j].TotalPenalty {
					return buckets[i].TotalPenalty > buckets[j].TotalPenalty
				}
				return buckets[i].ID < buckets[j].ID
			})
			out.TopBuckets = buckets[:min(len(buckets), limit)]
		}
		shaped = append(shaped, out)
	}
	return shaped
}

// scoreSummaryLine renders the human one-liner shown on the tool chip.
func scoreSummaryLine(response getScoreSummaryResponse) string {
	parts := make([]string, 0, 3)
	for _, pillar := range response.Pillars {
		parts = append(parts, fmt.Sprintf("%s %d", pillar.ID, pillar.Score))
	}
	line := fmt.Sprintf("overall %d/100", response.OverallScore)
	if len(parts) > 0 {
		line += " — " + strings.Join(parts, ", ")
	}
	if response.Previous != nil {
		previous := response.Previous.OverallScore
		// A pillar-scoped call reports that pillar's previous score; an
		// unscoped call reports the previous overall score.
		if len(response.Pillars) == 1 && len(response.Previous.Pillars) == 1 {
			previous = response.Previous.Pillars[0].Score
		}
		line += fmt.Sprintf(" (prev %d)", previous)
	}
	if response.Source == scoreSourceCrawlColumns {
		line += " (crawl scores only)"
	}
	return line
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

// parseGetScoreSummaryArgs parses the tool arguments strictly: unknown keys,
// duplicate keys, and trailing data are rejected. Empty input yields defaults.
func parseGetScoreSummaryArgs(raw json.RawMessage) (getScoreSummaryArgs, error) {
	args := getScoreSummaryArgs{IncludeBuckets: true, Compare: true, Limit: scoreSummaryDefaultLimit}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for key, value := range fields {
		switch key {
		case "pillar":
			if err := json.Unmarshal(value, &args.Pillar); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.Pillar = strings.ToLower(strings.TrimSpace(args.Pillar))
		case "include_buckets":
			if err := json.Unmarshal(value, &args.IncludeBuckets); err != nil {
				return args, fmt.Errorf("argument %q must be a boolean", key)
			}
		case "compare":
			if err := json.Unmarshal(value, &args.Compare); err != nil {
				return args, fmt.Errorf("argument %q must be a boolean", key)
			}
		case "limit":
			if err := json.Unmarshal(value, &args.Limit); err != nil {
				return args, fmt.Errorf("argument %q must be an integer", key)
			}
			if args.Limit < 1 {
				return args, fmt.Errorf("argument %q must be at least 1", key)
			}
			if args.Limit > scoreSummaryMaxLimit {
				args.Limit = scoreSummaryMaxLimit
			}
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	return args, nil
}
