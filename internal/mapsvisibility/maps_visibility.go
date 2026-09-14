package mapsvisibility

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/config"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/serper"
)

// HandleMapsVisibilityCheck runs one Serper maps check for a claimed job.
// The job's payload is the maps question; the run row is created by the API
// handler that enqueued the job, so this handler only claims execution.
func HandleMapsVisibilityCheck(ctx context.Context, queries *sqlc.Queries, cfg config.Config, projectID pgtype.UUID) error {
	check, err := queries.GetLatestMapsVisibilityCheckByProject(ctx, projectID)
	if err != nil {
		return fmt.Errorf("load maps visibility check: %w", err)
	}
	if check.Status != "queued" {
		return fmt.Errorf("maps visibility check %s is %s, expected queued", check.ID.String(), check.Status)
	}

	profile, err := queries.GetProjectBusinessProfileByProjectID(ctx, projectID)
	if err != nil {
		markFailed(ctx, queries, check.ID, fmt.Errorf("load business profile: %w", err))
		return fmt.Errorf("load business profile: %w", err)
	}

	if err := queries.MarkMapsVisibilityCheckRunning(ctx, check.ID); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}

	// Resolve the project's Google listing once and cache it on the project:
	// re-tests then cost only the maps call, not a places call too.
	listingRef, err := resolveListingRef(ctx, queries, cfg, projectID, profile)
	if err != nil {
		markFailed(ctx, queries, check.ID, fmt.Errorf("resolve listing: %w", err))
		return fmt.Errorf("resolve listing: %w", err)
	}

	client := serper.NewClient(cfg.SerperAPIKey, cfg.SerperMapsEndpoint, cfg.SerperPlacesEndpoint, cfg.SerperReviewsEndpoint)
	response, err := client.Maps(ctx, check.Question)
	if err != nil {
		markFailed(ctx, queries, check.ID, fmt.Errorf("serper maps: %w", err))
		return fmt.Errorf("serper maps: %w", err)
	}

	// Rank is viewport-relative: store the returned ll/zoom verbatim so every
	// later read can see the sampling context the rank was captured under.
	zoom := parseZoom(response.LL)
	rank, basis := findOurRank(response.Places, listingRef)

	resultsJSON, err := json.Marshal(response)
	if err != nil {
		markFailed(ctx, queries, check.ID, fmt.Errorf("marshal results: %w", err))
		return fmt.Errorf("marshal results: %w", err)
	}
	listingJSON, err := json.Marshal(listingRefJSON(listingRef))
	if err != nil {
		markFailed(ctx, queries, check.ID, fmt.Errorf("marshal listing: %w", err))
		return fmt.Errorf("marshal listing: %w", err)
	}

	if err := queries.MarkMapsVisibilityCheckCompleted(ctx, sqlc.MarkMapsVisibilityCheckCompletedParams{
		ID:            check.ID,
		Ll:            pgtype.Text{String: response.LL, Valid: response.LL != ""},
		Zoom:          pgtype.Int4{Int32: int32(zoom), Valid: zoom > 0},
		OurRank:       pgtype.Int4{Int32: int32(rank), Valid: rank > 0},
		OurMatchBasis: pgtype.Text{String: basis, Valid: basis != ""},
		CreditsUsed:   pgtype.Int4{Int32: int32(response.Credits), Valid: response.Credits > 0},
		Results:       resultsJSON,
		Listing:       listingJSON,
	}); err != nil {
		return fmt.Errorf("mark completed: %w", err)
	}

	log.Printf("maps visibility: check %s completed rank=%d basis=%s credits=%d", check.ID.String(), rank, basis, response.Credits)
	return nil
}

type listingRef struct {
	CID      string `json:"cid,omitempty"`
	PlaceID  string `json:"place_id,omitempty"`
	Title    string `json:"title,omitempty"`
	Website  string `json:"website,omitempty"`
	Address  string `json:"address,omitempty"`
	Resolved bool   `json:"resolved"`
}

func listingRefJSON(ref *sqlc.MapsListingRef) map[string]any {
	if ref == nil {
		return map[string]any{"resolved": false}
	}
	cid := textString(ref.Cid)
	placeID := textString(ref.PlaceID)
	title := textString(ref.Title)
	website := textString(ref.Website)
	address := textString(ref.Address)
	return map[string]any{
		"resolved": cid != "" || placeID != "",
		"cid":      cid,
		"place_id": placeID,
		"title":    title,
		"website":  website,
		"address":  address,
	}
}

func textString(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// resolveListingRef returns the cached listing reference, resolving it once
// through the places API when missing. The website match against the
// profile's own domain wins; otherwise the first result stands in (brands
// with many branches often lead with the wrong city, but it is the best
// available signal). A brand with no listing resolves to nil — the pack
// then reports "not matched".
func resolveListingRef(
	ctx context.Context,
	queries *sqlc.Queries,
	cfg config.Config,
	projectID pgtype.UUID,
	profile sqlc.GetProjectBusinessProfileByProjectIDRow,
) (*sqlc.MapsListingRef, error) {
	if ref, err := queries.GetMapsListingRef(ctx, projectID); err == nil {
		return &ref, nil
	}

	location := profile.PrimaryLocation.String
	query := fmt.Sprintf("%s %s", profile.BrandName, location)
	client := serper.NewClient(cfg.SerperAPIKey, cfg.SerperMapsEndpoint, cfg.SerperPlacesEndpoint, cfg.SerperReviewsEndpoint)
	response, err := client.Places(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(response.Places) == 0 {
		log.Printf("maps visibility: places resolve found no listing for project %s (query %q)", projectID.String(), query)
		return nil, nil
	}

	brandHost := hostOf(profile.WebsiteUrl)
	chosen := response.Places[0]
	if brandHost != "" {
		for _, place := range response.Places {
			if sameHost(place.Website, brandHost) {
				chosen = place
				break
			}
		}
	}

	ref, err := queries.UpsertMapsListingRef(ctx, sqlc.UpsertMapsListingRefParams{
		ProjectID: projectID,
		Cid:       pgtype.Text{String: chosen.CID, Valid: chosen.CID != ""},
		PlaceID:   pgtype.Text{String: chosen.PlaceID, Valid: chosen.PlaceID != ""},
		Title:     pgtype.Text{String: chosen.Title, Valid: chosen.Title != ""},
		Website:   pgtype.Text{String: chosen.Website, Valid: chosen.Website != ""},
		Address:   pgtype.Text{String: chosen.Address, Valid: chosen.Address != ""},
	})
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

func findOurRank(places []serper.Place, ref *sqlc.MapsListingRef) (int, string) {
	if ref == nil {
		return 0, ""
	}
	cid := textString(ref.Cid)
	placeID := textString(ref.PlaceID)
	website := textString(ref.Website)
	title := textString(ref.Title)
	if cid == "" && placeID == "" && title == "" {
		return 0, ""
	}
	for _, place := range places {
		if cid != "" && place.CID == cid {
			return place.Position, "cid"
		}
		if placeID != "" && place.PlaceID == placeID {
			return place.Position, "place_id"
		}
		if website != "" && sameHost(place.Website, website) {
			return place.Position, "domain"
		}
		if title != "" && strings.EqualFold(place.Title, title) {
			return place.Position, "title"
		}
	}
	return 0, ""
}

// sameHost compares two website fields by their host part, ignoring scheme,
// "www.", and path/tracking noise. Empty never matches.
func sameHost(website, brandWebsite string) bool {
	a := hostOf(website)
	b := hostOf(brandWebsite)
	if a == "" || b == "" {
		return false
	}
	return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

// parseZoom extracts the zoom level from serper's "@lat,lng,Zz" viewport.
func parseZoom(raw string) int {
	withoutAt := strings.TrimPrefix(raw, "@")
	parts := strings.Split(withoutAt, ",")
	if len(parts) != 3 {
		return 0
	}
	zoom := 0
	for _, r := range parts[2] {
		if r >= '0' && r <= '9' {
			zoom = zoom*10 + int(r-'0')
		}
	}
	return zoom
}

func markFailed(ctx context.Context, queries *sqlc.Queries, id pgtype.UUID, err error) {
	if markErr := queries.MarkMapsVisibilityCheckFailed(ctx, sqlc.MarkMapsVisibilityCheckFailedParams{
		ID:    id,
		Error: pgtype.Text{String: err.Error(), Valid: true},
	}); markErr != nil {
		log.Printf("maps visibility: mark failed error for %s: %v", id.String(), markErr)
	}
}

var _ = config.Config{}

// hostOf normalizes a website URL to a comparable host-ish string. Listing
// websites often carry tracking paths, so a suffix match on the domain part is
// the practical comparison for a fallback match.
func hostOf(raw string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	trimmed = strings.TrimPrefix(trimmed, "www.")
	if index := strings.IndexByte(trimmed, '/'); index >= 0 {
		trimmed = trimmed[:index]
	}
	return strings.ToLower(trimmed)
}
