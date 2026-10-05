package localvisibility

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/serper"
)

// ListingLookupCredits is the reservation for one deliberate Google listing search.
const ListingLookupCredits int64 = 1

var ErrListingAlreadyBound = errors.New("listing lookup: location already has a bound listing")

// ListingLookupStore preserves paid lookup evidence independently of listing selection.
type ListingLookupStore struct{ Pool *pgxpool.Pool }

func (s ListingLookupStore) ReserveListingLookup(ctx context.Context, userID, projectID, locationID pgtype.UUID) (sqlc.LocalListingLookup, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := sqlc.New(tx)
	location, err := q.GetLocationForListingLookup(ctx, sqlc.GetLocationForListingLookupParams{ID: locationID, ID_2: projectID, UserID: userID})
	if err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	if location.PlaceID.Valid {
		return sqlc.LocalListingLookup{}, ErrListingAlreadyBound
	}
	query := strings.TrimSpace(location.Name + " " + location.Address)
	if _, err = q.ReservePlatformMapsCredits(ctx, ListingLookupCredits); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrMapsBudgetUnavailable
		}
		return sqlc.LocalListingLookup{}, err
	}
	if _, err = q.ReserveOrganizationMapsCredits(ctx, sqlc.ReserveOrganizationMapsCreditsParams{OrganizationID: location.OrganizationID, Credits: ListingLookupCredits}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrMapsBudgetUnavailable
		}
		return sqlc.LocalListingLookup{}, err
	}
	lookup, err := q.CreateLocationListingLookup(ctx, sqlc.CreateLocationListingLookupParams{LocationID: locationID, Query: query})
	if err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	return lookup, nil
}

// RecordListingLookup never releases an uncertain provider charge or overwrites a recorded outcome.
func (s ListingLookupStore) RecordListingLookup(ctx context.Context, lookupID pgtype.UUID, response serper.PlacesResponse, providerErr error) (sqlc.LocalListingLookup, error) {
	raw, err := json.Marshal(response)
	if err != nil {
		return sqlc.LocalListingLookup{}, fmt.Errorf("listing lookup response evidence: %w", err)
	}
	original, err := sqlc.New(s.Pool).GetLocationListingLookup(ctx, lookupID)
	if err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := sqlc.New(tx)
	// Match the platform -> organization -> evidence lock order used by grid settlement.
	if err = q.SettlePlatformMapsCredits(ctx, sqlc.SettlePlatformMapsCreditsParams{}); err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	if err = q.SettleOrganizationMapsCredits(ctx, sqlc.SettleOrganizationMapsCreditsParams{OrganizationID: original.OrganizationID}); err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	locked, err := q.LockLocationListingLookup(ctx, lookupID)
	if err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	if locked.Status != "running" {
		return locked, nil
	}
	known := response.Credits > 0
	reserved, spent := ListingLookupCredits, int64(0)
	status := "uncertain"
	message := pgtype.Text{}
	if known {
		reserved = 0
		spent = int64(response.Credits)
		status = "completed"
	}
	if providerErr != nil {
		message = pgtype.Text{String: providerErr.Error(), Valid: true}
		if known {
			status = "failed"
		}
	}
	if !known && providerErr == nil {
		message = pgtype.Text{String: "Provider charge is unconfirmed; the lookup reservation remains held", Valid: true}
	}
	if known && spent != ListingLookupCredits && providerErr == nil {
		status = "failed"
		message = pgtype.Text{String: "Provider charged an unexpected listing lookup price; actual spend was recorded", Valid: true}
	}
	if status == "completed" && len(response.Places) > 0 {
		bindable := false
		for _, place := range response.Places {
			if strings.TrimSpace(place.PlaceID) != "" {
				bindable = true
				break
			}
		}
		if !bindable {
			status = "failed"
			message = pgtype.Text{String: "Listing search returned results without a bindable placeId", Valid: true}
		}
	}
	released := ListingLookupCredits - reserved
	if err = q.SettlePlatformMapsCredits(ctx, sqlc.SettlePlatformMapsCreditsParams{Released: released, Spent: spent}); err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	if err = q.SettleOrganizationMapsCredits(ctx, sqlc.SettleOrganizationMapsCreditsParams{OrganizationID: original.OrganizationID, Released: released, Spent: spent}); err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	result, err := q.CompleteLocationListingLookup(ctx, sqlc.CompleteLocationListingLookupParams{ID: lookupID, Status: status, ReservedCredits: reserved, CreditsUsed: spent, CreditKnown: known, RawResponse: raw, Error: message})
	if err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return sqlc.LocalListingLookup{}, err
	}
	return result, nil
}
