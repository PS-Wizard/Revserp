package localvisibility

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/serper"
)

// StartPlacesListingLookup stores a zero-credit attempt or reuses its recorded result.
func (s ListingLookupStore) StartPlacesListingLookup(ctx context.Context, userID, projectID, locationID pgtype.UUID, selection ListingLookupSelection) (sqlc.LocalListingLookup, bool, error) {
	if !userID.Valid || !projectID.Valid || !locationID.Valid {
		return sqlc.LocalListingLookup{}, false, fmt.Errorf("Places listing lookup: invalid ids")
	}
	query, key, latitude, longitude, err := selection.validated()
	if err != nil {
		return sqlc.LocalListingLookup{}, false, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return sqlc.LocalListingLookup{}, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := sqlc.New(tx)
	location, err := q.GetLocationForListingLookup(ctx, sqlc.GetLocationForListingLookupParams{ID: locationID, ID_2: projectID, UserID: userID})
	if err != nil {
		return sqlc.LocalListingLookup{}, false, err
	}
	if location.PlaceID.Valid {
		return sqlc.LocalListingLookup{}, false, ErrListingAlreadyBound
	}
	latest, err := q.GetLatestListingLookupForUser(ctx, sqlc.GetLatestListingLookupForUserParams{ID: locationID, ID_2: projectID, UserID: userID})
	if err == nil && latest.CandidateKey == key && latest.ExpectedCredits == 0 && latest.CreditKnown && latest.ReservedCredits == 0 && (latest.Status == "completed" || latest.Status == "failed") {
		return latest, false, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.LocalListingLookup{}, false, err
	}
	lookup, err := q.CreatePlacesListingLookup(ctx, sqlc.CreatePlacesListingLookupParams{
		LocationID: locationID, Query: query, CandidateKey: key,
		SourceLatitude: latitude, SourceLongitude: longitude,
	})
	if err != nil {
		return sqlc.LocalListingLookup{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlc.LocalListingLookup{}, false, err
	}
	return lookup, true, nil
}

// RecordPlacesListingLookup preserves zero-credit evidence without changing spending budgets.
func (s ListingLookupStore) RecordPlacesListingLookup(ctx context.Context, lookupID pgtype.UUID, response serper.MapsListingResponse, providerErr error) (sqlc.LocalListingLookup, error) {
	if response.Credits != 0 {
		return sqlc.LocalListingLookup{}, fmt.Errorf("Places listing lookup: unexpected application credits")
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return sqlc.LocalListingLookup{}, fmt.Errorf("Places listing lookup evidence: %w", err)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := sqlc.New(tx)
	locked, err := q.LockLocationListingLookup(ctx, lookupID)
	if err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	if locked.ExpectedCredits != 0 || locked.ReservedCredits != 0 {
		return sqlc.LocalListingLookup{}, fmt.Errorf("Places listing lookup: cannot change paid lookup evidence")
	}
	if locked.Status != "running" {
		return locked, nil
	}
	status := "completed"
	message := pgtype.Text{}
	if providerErr != nil {
		status = "failed"
		message = pgtype.Text{String: providerErr.Error(), Valid: true}
	} else if len(response.Places) > 0 {
		bindable := false
		for _, place := range response.Places {
			if strings.TrimSpace(place.PlaceID) != "" && place.HasMapCoordinates() {
				bindable = true
				break
			}
		}
		if !bindable {
			status = "failed"
			message = pgtype.Text{String: "Places listing lookup returned no bindable placeId and coordinates", Valid: true}
		}
	}
	recorded, err := q.CompleteLocationListingLookup(ctx, sqlc.CompleteLocationListingLookupParams{
		ID: lookupID, Status: status, CreditKnown: true, RawResponse: raw, Error: message,
	})
	if err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	return recorded, nil
}
