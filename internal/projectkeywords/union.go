package projectkeywords

// StoredProjectKeyword is one project_keywords row in store-independent form so
// the union builds without a database.
type StoredProjectKeyword struct {
	ID         string
	Keyword    string
	Normalized string
	Kind       string
	Source     string
}

// BuildProjectKeywordLists groups stored rows by source and derives the
// deduplicated combined union. Combined entries merge on the Go-folded key, so
// rows stored under an older key form still collapse instead of duplicating.
// One phrase in several sources keeps the winning text and classification
// (user first, then revserp, then competitor) with every source badge.
// Checked competitor rows never appear in the per-source lists; they only
// extend Combined with the competitor badge.
func BuildProjectKeywordLists(rows []StoredProjectKeyword) KeywordLists {
	lists := KeywordLists{
		UserDefined:      []Keyword{},
		RevserpSuggested: []Keyword{},
		Combined:         []CombinedKeyword{},
	}
	combinedByKey := make(map[string]int, len(rows))
	for pass := 0; pass < 3; pass++ {
		for _, row := range rows {
			want := (pass == 0 && row.Source == ProjectKeywordSourceUser) ||
				(pass == 1 && row.Source == ProjectKeywordSourceRevserp) ||
				(pass == 2 && row.Source == ProjectKeywordSourceCompetitor)
			if !want {
				continue
			}
			entry := Keyword{ID: row.ID, Keyword: row.Keyword, Kind: row.Kind}
			switch row.Source {
			case ProjectKeywordSourceUser:
				lists.UserDefined = append(lists.UserDefined, entry)
			case ProjectKeywordSourceRevserp:
				lists.RevserpSuggested = append(lists.RevserpSuggested, entry)
			}
			if idx, dup := combinedByKey[projectKeywordUnionKey(row)]; dup {
				sources := lists.Combined[idx].Sources
				already := false
				for _, s := range sources {
					if s == row.Source {
						already = true
						break
					}
				}
				if !already {
					lists.Combined[idx].Sources = append(sources, row.Source)
				}
				continue
			}
			combinedByKey[projectKeywordUnionKey(row)] = len(lists.Combined)
			lists.Combined = append(lists.Combined, CombinedKeyword{
				Keyword: row.Keyword,
				Kind:    row.Kind,
				Sources: []string{row.Source},
			})
		}
	}
	return lists
}

func projectKeywordUnionKey(row StoredProjectKeyword) string {
	if key := NormalizeProjectKeywordKey(row.Normalized); key != "" {
		return key
	}
	return NormalizeProjectKeywordKey(row.Keyword)
}

// CombinedKeywordTexts returns combined display phrases in union order for keyword coverage.
func CombinedKeywordTexts(lists KeywordLists) []string {
	texts := make([]string, 0, len(lists.Combined))
	for _, entry := range lists.Combined {
		texts = append(texts, entry.Keyword)
	}
	return texts
}
