package projectkeywords

import (
	"strings"
	"testing"
)

func TestBuildProjectKeywordListsIncludesCompetitorSelections(t *testing.T) {
	rows := []StoredProjectKeyword{
		{ID: "r1", Keyword: "revserp shoes", Normalized: "revserp shoes", Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceRevserp},
		{ID: "u1", Keyword: "ACME", Normalized: "acme", Kind: ProjectKeywordKindBrand, Source: ProjectKeywordSourceUser},
		{ID: "c1", Keyword: "trail boots", Normalized: "trail boots", Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceCompetitor},
		{ID: "c2", Keyword: "acme", Normalized: "acme", Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceCompetitor},
	}
	lists := BuildProjectKeywordLists(rows)
	if len(lists.UserDefined) != 1 || len(lists.RevserpSuggested) != 1 {
		t.Fatalf("sources = %d/%d, want 1/1: competitor rows stay out of per-source lists",
			len(lists.UserDefined), len(lists.RevserpSuggested))
	}
	if len(lists.Combined) != 3 {
		t.Fatalf("combined = %#v, want 3 entries", lists.Combined)
	}
	first := lists.Combined[0]
	if first.Keyword != "ACME" || first.Kind != ProjectKeywordKindBrand {
		t.Fatalf("first combined = %#v, want user ACME/brand to win", first)
	}
	if len(first.Sources) != 2 || first.Sources[0] != ProjectKeywordSourceUser || first.Sources[1] != ProjectKeywordSourceCompetitor {
		t.Fatalf("first sources = %v, want [user competitor]", first.Sources)
	}
	last := lists.Combined[2]
	if last.Keyword != "trail boots" || last.Kind != ProjectKeywordKindNonBrand {
		t.Fatalf("last combined = %#v, want competitor trail boots/non_brand", last)
	}
	if len(last.Sources) != 1 || last.Sources[0] != ProjectKeywordSourceCompetitor {
		t.Fatalf("last sources = %v, want [competitor]", last.Sources)
	}
	if got := strings.Join(CombinedKeywordTexts(lists), "|"); got != "ACME|revserp shoes|trail boots" {
		t.Fatalf("texts = %q, want user, revserp, then competitor order", got)
	}
}

func TestBuildProjectKeywordListsStartsEmptyWithoutSelections(t *testing.T) {
	rows := []StoredProjectKeyword{
		{ID: "u1", Keyword: "ACME", Normalized: "acme", Kind: ProjectKeywordKindBrand, Source: ProjectKeywordSourceUser},
		{ID: "r1", Keyword: "trail boots", Normalized: "trail boots", Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceRevserp},
	}
	lists := BuildProjectKeywordLists(rows)
	if len(lists.Combined) != 2 {
		t.Fatalf("combined = %#v, want 2 entries without selections", lists.Combined)
	}
	for _, entry := range lists.Combined {
		for _, source := range entry.Sources {
			if source == ProjectKeywordSourceCompetitor {
				t.Fatalf("combined = %#v, want no competitor badge without selections", lists.Combined)
			}
		}
	}
}

func TestBuildProjectKeywordListsMergesDuplicateCompetitorBadges(t *testing.T) {
	rows := []StoredProjectKeyword{
		{ID: "c1", Keyword: "trail boots", Normalized: "trail boots", Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceCompetitor},
		{ID: "c2", Keyword: "Trail  Boots", Normalized: "trail boots", Kind: ProjectKeywordKindNonBrand, Source: ProjectKeywordSourceCompetitor},
	}
	lists := BuildProjectKeywordLists(rows)
	if len(lists.Combined) != 1 {
		t.Fatalf("combined = %#v, want 1 merged entry", lists.Combined)
	}
	if len(lists.Combined[0].Sources) != 1 || lists.Combined[0].Sources[0] != ProjectKeywordSourceCompetitor {
		t.Fatalf("sources = %v, want a single competitor badge", lists.Combined[0].Sources)
	}
	if len(lists.UserDefined) != 0 || len(lists.RevserpSuggested) != 0 {
		t.Fatalf("per-source = %d/%d, want 0/0", len(lists.UserDefined), len(lists.RevserpSuggested))
	}
}
