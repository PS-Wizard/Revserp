package projectkeywords

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// LoadProjectKeywordLists reads one project's stored keywords and derives the
// combined union. It accepts a transaction-scoped Queries.
func LoadProjectKeywordLists(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID) (KeywordLists, error) {
	rows, err := queries.ListProjectKeywordsByProject(ctx, projectID)
	if err != nil {
		return KeywordLists{}, fmt.Errorf("project keywords load: %w", err)
	}
	stored := make([]StoredProjectKeyword, 0, len(rows))
	for _, row := range rows {
		stored = append(stored, StoredProjectKeyword{
			ID:         row.ID.String(),
			Keyword:    row.Keyword,
			Normalized: row.NormalizedKeyword,
			Kind:       row.Kind,
			Source:     row.Source,
		})
	}
	return BuildProjectKeywordLists(stored), nil
}

// ReplaceSuggestedKeywords atomically replaces the REVSerp-suggested brand and
// non-brand lists, leaving user rows untouched. All input validates BEFORE any
// delete. The caller must hold the project row lock (for example via
// GetProjectByIDForUserForBusinessProfileUpdate) so concurrent replacements
// and limit checks serialize.
func ReplaceSuggestedKeywords(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID, brandKeywords, nonBrandKeywords []string) error {
	brand, nonBrand, err := NormalizeSuggestedProjectKeywords(brandKeywords, nonBrandKeywords)
	if err != nil {
		return err
	}
	if err := queries.DeleteRevserpProjectKeywordsByProject(ctx, projectID); err != nil {
		return fmt.Errorf("project keywords replace revserp: %w", err)
	}
	for _, keyword := range brand {
		if err := insertProjectKeyword(ctx, queries, projectID, keyword, ProjectKeywordKindBrand, ProjectKeywordSourceRevserp); err != nil {
			return err
		}
	}
	for _, keyword := range nonBrand {
		if err := insertProjectKeyword(ctx, queries, projectID, keyword, ProjectKeywordKindNonBrand, ProjectKeywordSourceRevserp); err != nil {
			return err
		}
	}
	return nil
}

// AddUserProjectKeyword stores one user phrase, returning the row and whether
// it was created. The same phrase and kind is idempotent; the same phrase
// under the opposite kind is a conflict. The caller must hold the project row
// lock so the per-kind limit check and insert serialize.
func AddUserProjectKeyword(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID, phrase, kind string) (Keyword, bool, error) {
	if err := ValidateProjectKeywordKind(kind); err != nil {
		return Keyword{}, false, err
	}
	display, err := ValidateProjectKeywordPhrase(phrase)
	if err != nil {
		return Keyword{}, false, err
	}
	key := NormalizeProjectKeywordKey(display)
	existing, err := queries.GetProjectKeywordByProjectSourceNormalized(ctx, sqlc.GetProjectKeywordByProjectSourceNormalizedParams{
		ProjectID:         projectID,
		Source:            ProjectKeywordSourceUser,
		NormalizedKeyword: key,
	})
	if err == nil {
		if existing.Kind != kind {
			return Keyword{}, false, fmt.Errorf("%w: %q is already %s", ErrProjectKeywordConflict, existing.Keyword, existing.Kind)
		}
		return Keyword{ID: existing.ID.String(), Keyword: existing.Keyword, Kind: existing.Kind}, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Keyword{}, false, fmt.Errorf("project keyword lookup: %w", err)
	}
	counts, err := queries.CountProjectKeywordsByProject(ctx, projectID)
	if err != nil {
		return Keyword{}, false, fmt.Errorf("project keyword count: %w", err)
	}
	for _, count := range counts {
		if count.Source == ProjectKeywordSourceUser && count.Kind == kind && count.KeywordCount >= MaxProjectKeywordsPerKindPerSource {
			return Keyword{}, false, fmt.Errorf("%w: %s list holds %d phrases", ErrProjectKeywordLimit, kind, count.KeywordCount)
		}
	}
	row, err := insertProjectKeywordRow(ctx, queries, projectID, display, key, kind, ProjectKeywordSourceUser)
	if err != nil {
		return Keyword{}, false, err
	}
	return Keyword{ID: row.ID.String(), Keyword: row.Keyword, Kind: row.Kind}, true, nil
}

// DeleteUserProjectKeyword deletes one user phrase scoped to its project.
// Missing rows, other projects, and revserp rows surface pgx.ErrNoRows.
func DeleteUserProjectKeyword(ctx context.Context, queries *sqlc.Queries, projectID, keywordID pgtype.UUID) error {
	_, err := queries.DeleteUserProjectKeyword(ctx, sqlc.DeleteUserProjectKeywordParams{
		ID:        keywordID,
		ProjectID: projectID,
	})
	if err != nil {
		return fmt.Errorf("project keyword delete: %w", err)
	}
	return nil
}

func insertProjectKeyword(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID, display, kind, source string) error {
	_, err := insertProjectKeywordRow(ctx, queries, projectID, display, NormalizeProjectKeywordKey(display), kind, source)
	return err
}

func insertProjectKeywordRow(ctx context.Context, queries *sqlc.Queries, projectID pgtype.UUID, display, key, kind, source string) (sqlc.ProjectKeyword, error) {
	row, err := queries.InsertProjectKeyword(ctx, sqlc.InsertProjectKeywordParams{
		ProjectID:         projectID,
		Keyword:           display,
		NormalizedKeyword: key,
		Kind:              kind,
		Source:            source,
	})
	if err != nil {
		return sqlc.ProjectKeyword{}, fmt.Errorf("project keyword insert: %w", err)
	}
	return row, nil
}
