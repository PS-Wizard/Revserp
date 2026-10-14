// Package projectcompetitors owns the save-only project competitor roster.
// The project_competitors table is the only authoritative competitor roster;
// saving a row here never starts a competitor crawl.
package projectcompetitors

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// InputError is a member-visible validation failure: the caller maps it to a
// model or HTTP 4xx response instead of an infrastructure error.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// IsInputError reports whether err is a member-visible validation failure.
func IsInputError(err error) bool {
	var input *InputError
	return errors.As(err, &input)
}

// CompetitorStore is the roster surface SaveProjectCompetitor needs. It is
// implemented by *sqlc.Queries, including a transaction-bound copy, so tests
// substitute fakes without a database.
type CompetitorStore interface {
	GetProjectByIDForUserForBusinessProfileUpdate(ctx context.Context, arg sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams) (sqlc.Project, error)
	GetOrganizationFeaturesByProjectID(ctx context.Context, arg sqlc.GetOrganizationFeaturesByProjectIDParams) (sqlc.GetOrganizationFeaturesByProjectIDRow, error)
	CountProjectCompetitors(ctx context.Context, projectID pgtype.UUID) (int64, error)
	InsertProjectCompetitor(ctx context.Context, arg sqlc.InsertProjectCompetitorParams) (sqlc.ProjectCompetitor, error)
	ListProjectCompetitorsForUser(ctx context.Context, arg sqlc.ListProjectCompetitorsForUserParams) ([]sqlc.ProjectCompetitor, error)
}

// SaveOutcome is one saved roster row plus the plan capacity around it.
type SaveOutcome struct {
	Competitor     sqlc.ProjectCompetitor
	MaxCompetitors int32
	Total          int64
	// Existed is true when the canonical URL was already on the roster: the
	// existing row is returned and no duplicate is created.
	Existed bool
}

// SaveProjectCompetitor validates and saves one roster row without starting
// any crawl. Call it inside a transaction: the project row lock serializes
// concurrent additions so the plan limit stays atomic. A retry with an
// identical canonical URL returns the existing row with Existed true.
func SaveProjectCompetitor(ctx context.Context, store CompetitorStore, projectID, userID pgtype.UUID, name, rawURL string) (SaveOutcome, error) {
	seedURL := strings.TrimSpace(rawURL)
	if seedURL == "" {
		return SaveOutcome{}, &InputError{Msg: "seed_url is required"}
	}
	normalizedSeedURL, err := crawler.NormalizeURL(seedURL, nil)
	if err != nil {
		return SaveOutcome{}, &InputError{Msg: "invalid seed_url"}
	}
	if err := crawler.ValidatePublicHost(ctx, normalizedSeedURL.Hostname()); err != nil {
		return SaveOutcome{}, &InputError{Msg: "seed_url must not point to a private or internal host"}
	}

	project, err := store.GetProjectByIDForUserForBusinessProfileUpdate(ctx, sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams{
		ID:     projectID,
		UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SaveOutcome{}, &InputError{Msg: "project not found or access denied"}
		}
		return SaveOutcome{}, err
	}

	projectBaseURL, err := url.Parse(project.BaseUrl)
	if err != nil {
		return SaveOutcome{}, &InputError{Msg: "project base_url is invalid"}
	}
	if crawler.IsInternalURL(projectBaseURL, normalizedSeedURL) {
		return SaveOutcome{}, &InputError{Msg: "competitor must not share the project host"}
	}

	features, err := store.GetOrganizationFeaturesByProjectID(ctx, sqlc.GetOrganizationFeaturesByProjectIDParams{
		ProjectID: projectID,
		UserID:    userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SaveOutcome{}, &InputError{Msg: "forbidden"}
		}
		return SaveOutcome{}, err
	}

	// A PostgreSQL unique violation aborts the transaction, so find existing
	// rows before inserting.
	existing, findErr := findCompetitorBySeedURL(ctx, store, projectID, userID, normalizedSeedURL.String())
	if findErr != nil && !errors.Is(findErr, pgx.ErrNoRows) {
		return SaveOutcome{}, findErr
	}

	count, err := store.CountProjectCompetitors(ctx, projectID)
	if err != nil {
		return SaveOutcome{}, err
	}
	if findErr == nil {
		return SaveOutcome{Competitor: existing, MaxCompetitors: features.MaxCompetitors, Total: count, Existed: true}, nil
	}
	if count >= int64(features.MaxCompetitors) {
		return SaveOutcome{}, &InputError{Msg: "competitor_limit_reached"}
	}

	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		trimmedName = normalizedSeedURL.Hostname()
	}

	competitor, err := store.InsertProjectCompetitor(ctx, sqlc.InsertProjectCompetitorParams{
		ProjectID: projectID,
		SeedUrl:   normalizedSeedURL.String(),
		Name:      trimmedName,
	})
	if err != nil {
		return SaveOutcome{}, err
	}
	return SaveOutcome{Competitor: competitor, MaxCompetitors: features.MaxCompetitors, Total: count + 1}, nil
}

func findCompetitorBySeedURL(ctx context.Context, store CompetitorStore, projectID, userID pgtype.UUID, seedURL string) (sqlc.ProjectCompetitor, error) {
	rows, err := store.ListProjectCompetitorsForUser(ctx, sqlc.ListProjectCompetitorsForUserParams{
		UserID:    userID,
		ProjectID: projectID,
	})
	if err != nil {
		return sqlc.ProjectCompetitor{}, err
	}
	for _, row := range rows {
		if row.SeedUrl == seedURL {
			return row, nil
		}
	}
	return sqlc.ProjectCompetitor{}, pgx.ErrNoRows
}
