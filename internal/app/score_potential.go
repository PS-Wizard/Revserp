package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueengine "github.com/ps-wizard/revserp/internal/issues"
)

// scorePotentialScores is one set of crawl scores in the response.
type scorePotentialScores struct {
	Overall   int32 `json:"overall"`
	SEO       int32 `json:"seo"`
	AEO       int32 `json:"aeo"`
	PageSpeed int32 `json:"pagespeed"`
}

// scorePotentialOpportunity is one bucket's fully-fixed hypothetical score.
type scorePotentialOpportunity struct {
	Bucket        string               `json:"bucket"`
	Pillar        string               `json:"pillar"`
	ScoresIfFixed scorePotentialScores `json:"scores_if_fixed"`
	Delta         scorePotentialScores `json:"delta"`
}

// scorePotentialScenario is one combined hypothetical scenario.
type scorePotentialScenario struct {
	Buckets       []string             `json:"buckets"`
	ScoresIfFixed scorePotentialScores `json:"scores_if_fixed"`
	Delta         scorePotentialScores `json:"delta"`
}

type scorePotentialScenarios struct {
	BestBucket  scorePotentialScenario `json:"best_bucket"`
	Top3        scorePotentialScenario `json:"top_3"`
	Recommended scorePotentialScenario `json:"recommended"`
}

type scorePotentialResponse struct {
	PotentialAvailable bool                        `json:"potential_available"`
	Reason             string                      `json:"reason,omitempty"`
	Current            *scorePotentialScores       `json:"current,omitempty"`
	Opportunities      []scorePotentialOpportunity `json:"opportunities,omitempty"`
	Scenarios          *scorePotentialScenarios    `json:"scenarios,omitempty"`
}

// handleGetProjectScorePotential returns the latest completed crawl's
// simulated scores for the project's summary page. It uses the normal
// project/user authorization path (never the admin scoring-preview route) and
// reuses the real scorer (BuildScoreBreakdownWithConfig) against the crawl's
// persisted signals, the active scoring config, and the stored Google PSI
// result.
func (a *App) handleGetProjectScorePotential(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}

	user := principal.User
	if _, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{ID: projectID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}

	crawlID, err := a.Queries.GetLatestCompletedCrawlForProject(r.Context(), projectID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSONError(w, http.StatusNotFound, "no completed crawl")
		return
	} else if err != nil {
		serverError(w, r, err)
		return
	}

	crawl, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}
		serverError(w, r, err)
		return
	}

	pageRows, err := a.Queries.ListCrawlPageSignalsForCrawl(r.Context(), crawlID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	issueRows, err := a.Queries.ListCrawlIssuesForCrawl(r.Context(), crawlID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	config, err := issueengine.LoadActiveScoringConfig(r.Context(), a.Queries)
	if err != nil {
		serverError(w, r, err)
		return
	}

	potential := issueengine.ComputePotential(
		issueengine.PageSignalsFromRows(pageRows),
		issueengine.IssueSignalsFromRows(issueRows),
		config,
		issueengine.ParseStoredGooglePSI(crawl.GooglePsiResults),
	)

	// If the recomputed baseline disagrees with the stored crawl score, the
	// scoring config changed after the crawl was scored. Potential scores
	// would be meaningless against a stale baseline, so opt out until the next
	// crawl is scored with the current config.
	if potentialBaselineDrifted(potential.Current, crawl) {
		writeJSON(w, http.StatusOK, scorePotentialResponse{
			PotentialAvailable: false,
			Reason:             "scoring_config_changed",
		})
		return
	}

	writeJSON(w, http.StatusOK, buildScorePotentialResponse(potential))
}

// potentialBaselineDrifted reports whether the recomputed baseline disagrees
// with the scores stored on the crawl beyond rounding tolerance. A drift
// means the active scoring config changed after the crawl was scored.
func potentialBaselineDrifted(baseline issueengine.Scores, crawl sqlc.GetCrawlByIDForUserRow) bool {
	tolerance := int32(1)
	return scoreDifference(baseline.Overall, crawl.OverallScore) > tolerance ||
		scoreDifference(baseline.SEO, crawl.SeoScore) > tolerance ||
		scoreDifference(baseline.AEO, crawl.AeoScore) > tolerance ||
		scoreDifference(baseline.PageSpeed, crawl.PagespeedScore) > tolerance
}

func scoreDifference(computed int32, stored pgtype.Int4) int32 {
	if !stored.Valid {
		return computed
	}
	difference := computed - stored.Int32
	if difference < 0 {
		return -difference
	}
	return difference
}

func buildScorePotentialResponse(potential issueengine.PotentialResult) scorePotentialResponse {
	opportunities := make([]scorePotentialOpportunity, 0, len(potential.Opportunities))
	for _, opportunity := range potential.Opportunities {
		opportunities = append(opportunities, scorePotentialOpportunity{
			Bucket:        opportunity.Bucket,
			Pillar:        opportunity.Pillar,
			ScoresIfFixed: toScorePotentialScores(opportunity.Scores),
			Delta:         toScorePotentialScores(opportunity.Delta),
		})
	}
	current := toScorePotentialScores(potential.Current)
	return scorePotentialResponse{
		PotentialAvailable: true,
		Current:            &current,
		Opportunities:      opportunities,
		Scenarios: &scorePotentialScenarios{
			BestBucket:  toScorePotentialScenario(potential.Best),
			Top3:        toScorePotentialScenario(potential.Top3),
			Recommended: toScorePotentialScenario(potential.Recommended),
		},
	}
}

func toScorePotentialScores(scores issueengine.Scores) scorePotentialScores {
	return scorePotentialScores{
		Overall:   scores.Overall,
		SEO:       scores.SEO,
		AEO:       scores.AEO,
		PageSpeed: scores.PageSpeed,
	}
}

func toScorePotentialScenario(scenario issueengine.PotentialScenario) scorePotentialScenario {
	return scorePotentialScenario{
		Buckets:       scenario.Buckets,
		ScoresIfFixed: toScorePotentialScores(scenario.Scores),
		Delta:         toScorePotentialScores(scenario.Delta),
	}
}
