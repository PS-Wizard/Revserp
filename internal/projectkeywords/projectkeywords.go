// Package projectkeywords owns project keyword normalization, the combined
// union of user and revserp keyword sources, and project keyword row helpers.
package projectkeywords

import "errors"

const (
	// ProjectKeywordKindBrand marks brand phrases in the project keyword store.
	ProjectKeywordKindBrand = "brand"
	// ProjectKeywordKindNonBrand marks non-brand phrases in the project keyword store.
	ProjectKeywordKindNonBrand = "non_brand"
	// ProjectKeywordSourceUser marks user-defined project keywords, which generated replacements never mutate.
	ProjectKeywordSourceUser = "user"
	// ProjectKeywordSourceRevserp marks Revserp-suggested project keywords.
	ProjectKeywordSourceRevserp = "revserp"
	// MaxProjectKeywordsPerKindPerSource caps each kind list per source.
	MaxProjectKeywordsPerKindPerSource = 10
	// MaxProjectKeywordRunes bounds one phrase in Unicode characters.
	MaxProjectKeywordRunes = 200
)

// Keyword is one stored project keyword row for the keyword-lists API.
type Keyword struct {
	ID      string `json:"id"`
	Keyword string `json:"keyword"`
	Kind    string `json:"kind"`
}

// CombinedKeyword is one deduplicated phrase across both keyword sources.
type CombinedKeyword struct {
	Keyword string   `json:"keyword"`
	Kind    string   `json:"kind"`
	Sources []string `json:"sources"`
}

// KeywordLists is the keyword-lists payload: per-source rows plus their combined union.
type KeywordLists struct {
	UserDefined      []Keyword         `json:"user_defined"`
	RevserpSuggested []Keyword         `json:"revserp_suggested"`
	Combined         []CombinedKeyword `json:"combined"`
}

var (
	// ErrProjectKeywordInvalid marks a blank, overlong, or mistyped phrase.
	ErrProjectKeywordInvalid = errors.New("project keyword invalid")
	// ErrProjectKeywordConflict marks a user phrase already stored under the opposite kind.
	ErrProjectKeywordConflict = errors.New("project keyword conflict")
	// ErrProjectKeywordLimit marks more than 10 phrases for one kind and source.
	ErrProjectKeywordLimit = errors.New("project keyword limit")
)
