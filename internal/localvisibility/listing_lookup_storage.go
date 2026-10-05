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
const ListingLookupCredits int64 = 3

var ErrListingAlreadyBound = errors.New("listing lookup: location already has a bound listing")

// ListingLookupSelection is one deliberate free-candidate choice to resolve.
type ListingLookupSelection struct {
	Query        string
	CandidateKey string
	Latitude     float64
	Longitude    float64
}

func (s ListingLookupSelection) validated() (query, key string, latitude, longitude float64, err error) {
	query = strings.TrimSpace(s.Query)
	key = strings.TrimSpace(s.CandidateKey)
	if query == "" {
		return "", "", 0, 0, fmt.Errorf("listing lookup: query is required")
	}
	if key == "" {
		return "", "", 0, 0, fmt.Errorf("listing lookup: candidate key is required")
	}
	latitude, longitude = s.Latitude, s.Longitude
	probe := serper.MapsListingPlace{Latitude: &latitude, Longitude: &longitude}
	if !probe.HasMapCoordinates() {
		return "", "", 0, 0, fmt.Errorf("listing lookup: invalid source coordinates")
	}
	return query, key, latitude, longitude, nil
}

// ListingLookupStore preserves paid lookup evidence independently of listing selection.
type ListingLookupStore struct{ Pool *pgxpool.Pool }

func (s ListingLookupStore) ReserveListingLookup(ctx context.Context, userID, projectID, locationID pgtype.UUID, selection ListingLookupSelection) (sqlc.LocalListingLookup, bool, error) {
	if !userID.Valid || !projectID.Valid || !locationID.Valid {
		return sqlc.LocalListingLookup{}, false, fmt.Errorf("listing lookup: invalid ids")
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
	if latest, err := q.GetLatestListingLookupForUser(ctx, sqlc.GetLatestListingLookupForUserParams{ID: locationID, ID_2: projectID, UserID: userID}); err == nil {
		if latest.CandidateKey == key && latest.ExpectedCredits == int32(ListingLookupCredits) && latest.Status != "running" && latest.ReservedCredits == 0 {
			_ = tx.Rollback(ctx)
			return latest, false, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return sqlc.LocalListingLookup{}, false, err
	}
	if _, err = q.ReservePlatformMapsCredits(ctx, ListingLookupCredits); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrMapsBudgetUnavailable
		}
		return sqlc.LocalListingLookup{}, false, err
	}
	if _, err = q.ReserveOrganizationMapsCredits(ctx, sqlc.ReserveOrganizationMapsCreditsParams{OrganizationID: location.OrganizationID, Credits: ListingLookupCredits}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrMapsBudgetUnavailable
		}
		return sqlc.LocalListingLookup{}, false, err
	}
	lookup, err := q.CreateLocationListingLookup(ctx, sqlc.CreateLocationListingLookupParams{
		LocationID:      locationID,
		Query:           query,
		ExpectedCredits: int32(ListingLookupCredits),
		CandidateKey:    key,
		SourceLatitude:  latitude,
		SourceLongitude: longitude,
	})
	if err != nil {
		return sqlc.LocalListingLookup{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return sqlc.LocalListingLookup{}, false, err
	}
	return lookup, true, nil
}

// RecordListingLookup never releases an uncertain provider charge or overwrites a recorded outcome.
func (s ListingLookupStore) RecordListingLookup(ctx context.Context, lookupID pgtype.UUID, response serper.MapsListingResponse, providerErr error) (sqlc.LocalListingLookup, error) {
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
	expected := int64(locked.ExpectedCredits)
	held := locked.ReservedCredits
	known := response.Credits > 0
	reserved, spent := held, int64(0)
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
	if known && spent != expected && providerErr == nil {
		status = "failed"
		message = pgtype.Text{String: "Provider charged an unexpected listing lookup price; actual spend was recorded", Valid: true}
	}
	if status == "completed" && len(response.Places) > 0 {
		bindable := false
		if locked.ExpectedCredits == int32(ListingLookupCredits) {
			for _, place := range response.Places {
				if strings.TrimSpace(place.PlaceID) != "" && place.HasMapCoordinates() {
					bindable = true
					break
				}
			}
		} else {
			for _, place := range response.Places {
				if strings.TrimSpace(place.PlaceID) != "" {
					bindable = true
					break
				}
			}
		}
		if !bindable {
			status = "failed"
			message = pgtype.Text{String: "Listing search returned results without a bindable placeId", Valid: true}
		}
	}
	released := held - reserved
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
