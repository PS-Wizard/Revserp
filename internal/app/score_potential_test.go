package app

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueengine "github.com/ps-wizard/revserp/internal/issues"
)

func storedScores(overall, seo, aeo, pagespeed int32) sqlc.GetCrawlByIDForUserRow {
	return sqlc.GetCrawlByIDForUserRow{
		OverallScore:   pgtype.Int4{Int32: overall, Valid: true},
		SeoScore:       pgtype.Int4{Int32: seo, Valid: true},
		AeoScore:       pgtype.Int4{Int32: aeo, Valid: true},
		PagespeedScore: pgtype.Int4{Int32: pagespeed, Valid: true},
	}
}

func TestPotentialBaselineDrifted(t *testing.T) {
	baseline := issueengine.Scores{Overall: 80, SEO: 85, AEO: 82, PageSpeed: 70}

	if potentialBaselineDrifted(baseline, storedScores(80, 85, 82, 70)) {
		t.Fatal("identical stored scores must not drift")
	}
	if potentialBaselineDrifted(baseline, storedScores(79, 85, 82, 70)) {
		t.Fatal("stored within rounding tolerance (1 point) must not drift")
	}
	if !potentialBaselineDrifted(baseline, storedScores(77, 85, 82, 70)) {
		t.Fatal("overall drift beyond tolerance must flag scoring_config_changed")
	}
	if !potentialBaselineDrifted(baseline, storedScores(80, 85, 70, 70)) {
		t.Fatal("aeo drift beyond tolerance must flag scoring_config_changed")
	}
	if !potentialBaselineDrifted(baseline, storedScores(0, 0, 0, 0)) {
		t.Fatal("missing stored scores must flag drift")
	}
}

func TestBuildScorePotentialResponse(t *testing.T) {
	potential := issueengine.PotentialResult{
		Current: issueengine.Scores{Overall: 90, SEO: 92, AEO: 91, PageSpeed: 80},
		Opportunities: []issueengine.BucketPotential{
			{Bucket: "meta_tags", Pillar: "seo", Scores: issueengine.Scores{Overall: 92, SEO: 96, AEO: 91, PageSpeed: 80}, Delta: issueengine.Scores{Overall: 2, SEO: 4}},
			{Bucket: "server_responsiveness", Pillar: "pagespeed", Scores: issueengine.Scores{Overall: 91, SEO: 92, AEO: 91, PageSpeed: 84}, Delta: issueengine.Scores{Overall: 1, PageSpeed: 4}},
		},
		Best:        issueengine.PotentialScenario{Buckets: []string{"meta_tags"}, Scores: issueengine.Scores{Overall: 92, SEO: 96, AEO: 91, PageSpeed: 80}, Delta: issueengine.Scores{Overall: 2, SEO: 4}},
		Top3:        issueengine.PotentialScenario{Buckets: []string{"meta_tags", "answerability", "server_responsiveness"}, Scores: issueengine.Scores{Overall: 93, SEO: 96, AEO: 95, PageSpeed: 84}, Delta: issueengine.Scores{Overall: 3, SEO: 4, AEO: 4, PageSpeed: 4}},
		Recommended: issueengine.PotentialScenario{Buckets: []string{"meta_tags", "server_responsiveness"}, Scores: issueengine.Scores{Overall: 93, SEO: 96, AEO: 91, PageSpeed: 84}, Delta: issueengine.Scores{Overall: 3, SEO: 4, PageSpeed: 4}},
	}

	response := buildScorePotentialResponse(potential)
	payload, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}

	var decoded scorePotentialResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.PotentialAvailable || decoded.Reason != "" {
		t.Fatalf("availability = %v reason = %q, want available", decoded.PotentialAvailable, decoded.Reason)
	}
	if decoded.Current == nil || decoded.Current.Overall != 90 || decoded.Current.PageSpeed != 80 {
		t.Fatalf("current = %+v", decoded.Current)
	}
	if len(decoded.Opportunities) != 2 {
		t.Fatalf("opportunities = %d, want 2", len(decoded.Opportunities))
	}
	if decoded.Opportunities[0].Bucket != "meta_tags" || decoded.Opportunities[0].Delta.Overall != 2 || decoded.Opportunities[0].Delta.SEO != 4 {
		t.Fatalf("opportunity[0] = %+v", decoded.Opportunities[0])
	}
	if decoded.Scenarios == nil {
		t.Fatal("scenarios missing from response")
	}
	if len(decoded.Scenarios.BestBucket.Buckets) != 1 || decoded.Scenarios.BestBucket.ScoresIfFixed.Overall != 92 {
		t.Fatalf("best_bucket = %+v", decoded.Scenarios.BestBucket)
	}
	if len(decoded.Scenarios.Top3.Buckets) != 3 {
		t.Fatalf("top_3 buckets = %v, want 3", decoded.Scenarios.Top3.Buckets)
	}
}
