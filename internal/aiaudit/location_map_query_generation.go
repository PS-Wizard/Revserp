package aiaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/localvisibility"
	"github.com/ps-wizard/revserp/internal/textnormalization"
)

// LocationMapQueryGenerationJobType is the ai_worker_jobs job type the setup
// worker enqueues for the per-location AI Maps starter set.
const LocationMapQueryGenerationJobType = "map_query_generation"

type locationMapQueryInsert struct {
	Text       string
	Normalized string
	Ordinal    int32
	Enabled    bool
}

func (w *Worker) handleLocationMapQueryGeneration(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	if !job.LocationID.Valid {
		return fmt.Errorf("map query job %s has no location scope", job.ID.String())
	}
	locationID := pgtype.UUID{Bytes: job.LocationID.Bytes, Valid: true}

	profile, err := w.locationProfileReader().GetLocationBusinessProfile(ctx, locationID, job.ProjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no location business profile for %s", locationID.String())
		}
		return fmt.Errorf("load location business profile: %w", err)
	}
	target, err := w.locationTargetReader().GetLocationTarget(ctx, locationID, job.ProjectID)
	if err != nil {
		return fmt.Errorf("load location target: %w", err)
	}
	if strings.TrimSpace(target.Locality) == "" && strings.TrimSpace(target.Address) == "" {
		return fmt.Errorf("location %s has no verified locality or address", locationID.String())
	}
	landmarks, err := w.locationLandmarkReader().GetLocationLandmarks(ctx, locationID, job.ProjectID)
	if err != nil {
		return fmt.Errorf("load location landmarks: %w", err)
	}

	provider, err := w.promptProvider()
	if err != nil {
		return fmt.Errorf("create ai provider: %w", err)
	}
	raw, err := provider.GenerateText(ctx, buildLocationMapQueryGenerationPrompt(profile, target, landmarks))
	if err != nil {
		return fmt.Errorf("generate location map queries: %w", err)
	}
	queries, err := parseLocationMapQueryOutput(raw)
	if err != nil {
		return err
	}

	return w.saveLocationMapQueryStarter(ctx, locationID, job.ProjectID, queries)
}

func (w *Worker) saveLocationMapQueryStarter(ctx context.Context, locationID, projectID pgtype.UUID, queries []string) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin map query save: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var locked pgtype.UUID
	if err := tx.QueryRow(ctx,
		`SELECT id FROM project_locations WHERE id = $1 AND project_id = $2 FOR NO KEY UPDATE OF project_locations`,
		locationID, projectID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no location %s for project %s", locationID.String(), projectID.String())
		}
		return fmt.Errorf("lock location for map query save: %w", err)
	}
	rows, err := tx.Query(ctx,
		`SELECT id, location_id, text, normalized, ordinal, enabled, kind, source, origin, landmark_id, created_at, updated_at
		 FROM project_location_queries WHERE location_id = $1 AND kind = 'map' ORDER BY ordinal, id FOR UPDATE`,
		locationID)
	if err != nil {
		return fmt.Errorf("list location map queries: %w", err)
	}
	var existing []sqlc.ProjectLocationQuery
	for rows.Next() {
		var row sqlc.ProjectLocationQuery
		if err := rows.Scan(&row.ID, &row.LocationID, &row.Text, &row.Normalized, &row.Ordinal,
			&row.Enabled, &row.Kind, &row.Source, &row.Origin, &row.LandmarkID, &row.CreatedAt, &row.UpdatedAt); err != nil {
			rows.Close()
			return err
		}
		existing = append(existing, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	plan := planLocationMapQueryRefresh(queries, existing)
	for _, id := range plan.deleteIDs {
		if _, err := tx.Exec(ctx,
			`DELETE FROM project_location_queries WHERE id = $1 AND location_id = $2 AND kind = 'map' AND source = 'generated'`,
			id, locationID); err != nil {
			return fmt.Errorf("replace location map query: %w", err)
		}
	}
	for _, insert := range plan.inserts {
		if _, err := tx.Exec(ctx,
			`INSERT INTO project_location_queries (location_id, text, normalized, ordinal, enabled, kind, source, origin, landmark_id)
			 VALUES ($1,$2,$3,$4,$5,'map','generated','service',NULL)
			 ON CONFLICT (location_id, kind, normalized) DO NOTHING`,
			locationID, insert.Text, insert.Normalized, insert.Ordinal, insert.Enabled); err != nil {
			return fmt.Errorf("save location map query: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit map query save: %w", err)
	}
	return nil
}

func buildLocationMapQueryGenerationPrompt(profile LocationBusinessProfile, target LocationTarget, landmarks []LocationLandmark) string {
	var sb strings.Builder
	sb.WriteString(`You are a Google Maps query writer. Write the starter Maps searches a customer would type for this location. Output JSON only: {"queries":["..."]}.`)
	sb.WriteString("\n\nLocation business facts (only these services exist):\n")
	if profile.BrandName != "" {
		sb.WriteString("Brand: " + profile.BrandName + "\n")
	}
	if profile.PrimaryCategory != "" {
		sb.WriteString("Category: " + profile.PrimaryCategory + "\n")
	}
	if profile.BusinessDescription != "" {
		sb.WriteString("Description: " + profile.BusinessDescription + "\n")
	}
	if profile.ProductDescription != "" {
		sb.WriteString("Products: " + profile.ProductDescription + "\n")
	}
	if strings.TrimSpace(profile.BusinessDescription) == "" && strings.TrimSpace(profile.ProductDescription) == "" && len(profile.Services) > 0 {
		sb.WriteString("Historical service labels (background only; never override the facts above): " + strings.Join(profile.Services, ", ") + "\n")
	}
	sb.WriteString("\nVerified geography (use exactly this locality; never invent another place):\n")
	if target.Address != "" {
		sb.WriteString("Address: " + target.Address + "\n")
	}
	sb.WriteString("Locality: " + target.Locality + "\n")
	if len(landmarks) > 0 {
		sb.WriteString("\nSaved nearby landmarks (may anchor at most two queries; never invent others):\n")
		for i, landmark := range landmarks {
			if i >= 10 {
				break
			}
			sb.WriteString("- " + landmark.Name + "\n")
		}
	} else {
		sb.WriteString("\nSaved nearby landmarks: none. Do not invent place names.\n")
	}
	sb.WriteString("\nRules:\n")
	sb.WriteString("- Write up to 10 of the strongest realistic location-grounded Maps phrases a customer would type, ordered best first, not questions: no question marks. Fewer is fine; never pad with weak phrases.\n")
	sb.WriteString("- Short phrases win: \"dentist\", \"dentist near me\", \"dentist in Kathmandu\".\n")
	sb.WriteString("- Only services in the facts above; no invented services, locations, or volumes.\n")
	sb.WriteString("- Prefer \"{service} in {locality}\" phrasing; at most two \"near {landmark}\" queries.\n")
	return sb.String()
}

func parseLocationMapQueryOutput(raw string) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)
	var decoded struct {
		Queries []string `json:"queries"`
	}
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, fmt.Errorf("decode location map queries: %w", err)
	}
	return validateLocationMapQueries(decoded.Queries)
}

func isLocationMapQueryQuestion(display string) bool {
	first := strings.ToLower(display)
	for _, word := range []string{"who", "what", "where", "when", "why", "how", "is", "are", "can", "does", "do", "which"} {
		if first == word || strings.HasPrefix(first, word+" ") {
			return true
		}
	}
	return false
}

func validateLocationMapQueries(queries []string) ([]string, error) {
	out := make([]string, 0, len(queries))
	seen := make(map[string]struct{}, len(queries))
	for _, query := range queries {
		display := textnormalization.NormalizeTextDisplay(query)
		if display == "" || strings.Contains(display, "?") {
			continue
		}
		if isLocationMapQueryQuestion(display) {
			continue
		}
		if len(display) > 80 || len(strings.Fields(display)) > 8 {
			continue
		}
		if err := localvisibility.ValidateMapQueryCandidate(display); err != nil {
			continue
		}
		key := textnormalization.NormalizeTextKey(display)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, display)
		if len(out) >= localvisibility.MapQueryStarterLimit {
			break
		}
	}
	if len(out) == 0 {
		return nil, errors.New("map query generation returned no usable Maps phrases")
	}
	return out, nil
}

type locationMapQueryRefresh struct {
	inserts   []locationMapQueryInsert
	deleteIDs []pgtype.UUID
}

func planLocationMapQueryRefresh(generated []string, existing []sqlc.ProjectLocationQuery) locationMapQueryRefresh {
	want := make([]locationMapQueryInsert, 0, len(generated))
	wantKeys := make(map[string]struct{}, len(generated))
	for _, query := range generated {
		if len(want) >= localvisibility.MapQueryStarterLimit {
			break
		}
		display := textnormalization.NormalizeTextDisplay(query)
		if display == "" {
			continue
		}
		key := textnormalization.NormalizeTextKey(display)
		if _, dup := wantKeys[key]; dup {
			continue
		}
		wantKeys[key] = struct{}{}
		want = append(want, locationMapQueryInsert{Text: display, Normalized: key})
	}
	present := make(map[string]struct{}, len(existing)+len(wantKeys))
	var next int32
	mapCount := 0
	var plan locationMapQueryRefresh
	for _, row := range existing {
		if row.Kind != "map" {
			continue
		}
		mapCount++
		present[textnormalization.NormalizeTextKey(row.Text)] = struct{}{}
		present[row.Normalized] = struct{}{}
		if row.Ordinal >= next {
			next = row.Ordinal + 1
		}
		if row.Source != "generated" {
			continue
		}
		if _, ok := wantKeys[row.Normalized]; ok {
			continue
		}
		if _, ok := wantKeys[textnormalization.NormalizeTextKey(row.Text)]; ok {
			continue
		}
		plan.deleteIDs = append(plan.deleteIDs, row.ID)
	}
	initial := mapCount == 0
	for _, item := range want {
		if _, dup := present[item.Normalized]; dup {
			continue
		}
		present[item.Normalized] = struct{}{}
		item.Ordinal = next
		item.Enabled = initial
		plan.inserts = append(plan.inserts, item)
		next++
		if len(plan.inserts) >= localvisibility.MapQueryStarterLimit {
			break
		}
	}
	return plan
}
