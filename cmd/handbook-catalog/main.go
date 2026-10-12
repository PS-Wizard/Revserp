package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/ps-wizard/revserp/internal/issues/aeo"
	pagespeed "github.com/ps-wizard/revserp/internal/issues/page_speed"
	"github.com/ps-wizard/revserp/internal/issues/seo"
	"github.com/ps-wizard/revserp/internal/issues/shared"
)

type handbookDetectorPillar struct {
	Buckets    []string          `json:"buckets"`
	IssueTypes []string          `json:"issueTypes"`
	Scopes     map[string]string `json:"scopes"`
}

func buildHandbookDetectorPillar(buckets, issueTypes map[string]float64) handbookDetectorPillar {
	scopes := make(map[string]string, len(issueTypes))
	for issueType := range issueTypes {
		scope := "page"
		if shared.IsSitewideIssue(issueType) {
			scope = "site"
		} else if shared.IsGooglePSIOriginIssue(issueType) {
			scope = "origin"
		}
		scopes[issueType] = scope
	}
	return handbookDetectorPillar{
		Buckets:    slices.Sorted(maps.Keys(buckets)),
		IssueTypes: slices.Sorted(maps.Keys(issueTypes)),
		Scopes:     scopes,
	}
}

func main() {
	manifest := map[string]handbookDetectorPillar{
		seo.PillarID:       buildHandbookDetectorPillar(seo.BucketWeights, seo.IssuePenaltyByType),
		aeo.PillarID:       buildHandbookDetectorPillar(aeo.BucketWeights, aeo.IssuePenaltyByType),
		pagespeed.PillarID: buildHandbookDetectorPillar(pagespeed.BucketWeights, pagespeed.IssuePenaltyByType),
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		fmt.Fprintln(os.Stderr, "Handbook detector manifest export failed:", err)
		os.Exit(1)
	}
}
