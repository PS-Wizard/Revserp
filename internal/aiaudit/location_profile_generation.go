package aiaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/businessprofile"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/websitescope"
)

// LocationProfileGenerationJobType is the ai_worker_jobs job type the setup
// worker enqueues for per-location profile adaptation.
const LocationProfileGenerationJobType = "location_profile_generation"

type locationProfilePageRef struct {
	URL             string
	Title           string
	MetaDescription string
}

type locationParentPageEvidence struct {
	pages  []locationProfilePageRef
	scoped bool
}

type locationAdaptedProfileFields struct {
	BusinessDescription  string
	ProductDescription   string
	TargetAudience       string
	PrimaryCategory      string
	PrimaryLocation      string
	BusinessCompetitors  []string
	CompetitorsMentioned bool
}

func (w *Worker) handleLocationProfileGeneration(ctx context.Context, job sqlc.ClaimNextPendingAIWorkerJobRow) error {
	if !job.LocationID.Valid {
		return fmt.Errorf("location profile job %s has no location scope", job.ID.String())
	}
	locationID := pgtype.UUID{Bytes: job.LocationID.Bytes, Valid: true}

	parent, err := w.queries.GetProjectBusinessProfileByProjectID(ctx, job.ProjectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("no parent business profile for project %s", job.ProjectID.String())
		}
		return fmt.Errorf("load parent business profile: %w", err)
	}
	local, err := w.locationProfileReader().GetLocationBusinessProfile(ctx, locationID, job.ProjectID)
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

	evidence, err := w.loadLocationProfileParentPages(ctx, job.ProjectID, locationID)
	if err != nil {
		return err
	}
	differences, err := loadLocationSetupDifferences(ctx, w.pool, locationID, job.ProjectID)
	if err != nil {
		return err
	}

	provider, err := w.promptProvider()
	if err != nil {
		return fmt.Errorf("create ai provider: %w", err)
	}
	raw, err := provider.GenerateText(ctx, buildLocationProfileGenerationPrompt(parent, local, target, evidence, differences))
	if err != nil {
		return fmt.Errorf("generate location profile: %w", err)
	}
	parsed, err := parseLocationProfileGenerationOutput(raw)
	if err != nil {
		return err
	}
	planned := planLocationProfileUpdate(local, target, parsed)
	if err := w.saveLocationProfileAdaptation(ctx, locationID, job.ProjectID, planned); err != nil {
		return err
	}
	return nil
}

func (w *Worker) loadLocationProfileParentPages(ctx context.Context, projectID, locationID pgtype.UUID) (locationParentPageEvidence, error) {
	crawlID, err := w.queries.GetLatestCompletedCrawlForProject(ctx, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return locationParentPageEvidence{}, nil
		}
		return locationParentPageEvidence{}, fmt.Errorf("load parent crawl: %w", err)
	}
	scopeURL, scopeMatch, err := loadLocationWebsiteScope(ctx, w.pool, locationID)
	if err != nil {
		return locationParentPageEvidence{}, err
	}
	var rows pgx.Rows
	if scopeURL != "" {
		rows, err = w.pool.Query(ctx,
			`SELECT url, COALESCE(title,''), COALESCE(meta_description,'') FROM crawl_pages
			 WHERE crawl_id = $1 AND url LIKE $2 ESCAPE '\' ORDER BY depth NULLS LAST, url LIMIT 50`,
			crawlID, locationScopeLikePrefix(scopeURL))
	} else {
		rows, err = w.pool.Query(ctx,
			`SELECT url, COALESCE(title,''), COALESCE(meta_description,'') FROM crawl_pages
			 WHERE crawl_id = $1 ORDER BY depth NULLS LAST, url LIMIT 5`,
			crawlID)
	}
	if err != nil {
		return locationParentPageEvidence{}, fmt.Errorf("list parent pages: %w", err)
	}
	defer rows.Close()
	evidence := locationParentPageEvidence{scoped: scopeURL != ""}
	for rows.Next() {
		var ref locationProfilePageRef
		if err := rows.Scan(&ref.URL, &ref.Title, &ref.MetaDescription); err != nil {
			return locationParentPageEvidence{}, fmt.Errorf("scan parent page: %w", err)
		}
		if strings.TrimSpace(ref.URL) == "" {
			continue
		}
		if scopeURL != "" && !websitescope.MatchWebsiteScopePage(scopeMatch, scopeURL, ref.URL) {
			continue
		}
		evidence.pages = append(evidence.pages, ref)
		if len(evidence.pages) >= 5 {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return locationParentPageEvidence{}, fmt.Errorf("read parent pages: %w", err)
	}
	return evidence, nil
}

func locationScopeLikePrefix(scopeURL string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(scopeURL)
	return escaped + "%"
}

type locationScopeRowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadLocationWebsiteScope(ctx context.Context, db locationScopeRowQuerier, locationID pgtype.UUID) (string, string, error) {
	var (
		url   pgtype.Text
		match string
	)
	if err := db.QueryRow(ctx,
		`SELECT url, match FROM location_website_scopes WHERE location_id = $1 ORDER BY revision DESC LIMIT 1`,
		locationID).Scan(&url, &match); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", nil
		}
		return "", "", fmt.Errorf("load location website scope: %w", err)
	}
	if !websitescope.ValidScopeMatch(match) || match == websitescope.ScopeMatchNone {
		return "", "", nil
	}
	if !url.Valid || strings.TrimSpace(url.String) == "" {
		return "", "", nil
	}
	return strings.TrimSpace(url.String), match, nil
}

type locationSetupDifferencesQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadLocationSetupDifferences(ctx context.Context, db locationSetupDifferencesQuerier, locationID, projectID pgtype.UUID) (string, error) {
	var differences string
	if err := db.QueryRow(ctx,
		`SELECT differences FROM location_setup WHERE location_id = $1 AND project_id = $2 LIMIT 1`,
		locationID, projectID).Scan(&differences); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("load location setup differences: %w", err)
	}
	return strings.TrimSpace(differences), nil
}

func buildLocationProfileGenerationPrompt(parent sqlc.GetProjectBusinessProfileByProjectIDRow, local LocationBusinessProfile, target LocationTarget, evidence locationParentPageEvidence, differences string) string {
	var sb strings.Builder
	sb.WriteString(`You are a location profile adapter. Adapt the independent location profile copy below using the parent facts. Output one JSON object only, no markdown, no commentary.`)
	sb.WriteString("\n\nParent business profile (background facts, never write back to the parent):\n")
	sb.WriteString("Brand: " + parent.BrandName + "\n")
	if parent.WebsiteUrl != "" {
		sb.WriteString("Website: " + parent.WebsiteUrl + "\n")
	}
	if parent.PrimaryCategory.Valid && strings.TrimSpace(parent.PrimaryCategory.String) != "" {
		sb.WriteString("Category: " + strings.TrimSpace(parent.PrimaryCategory.String) + "\n")
	}
	if parent.BusinessDescription.Valid && strings.TrimSpace(parent.BusinessDescription.String) != "" {
		sb.WriteString("Description: " + strings.TrimSpace(parent.BusinessDescription.String) + "\n")
	}
	if parent.ProductDescription.Valid && strings.TrimSpace(parent.ProductDescription.String) != "" {
		sb.WriteString("Products: " + strings.TrimSpace(parent.ProductDescription.String) + "\n")
	}
	if parent.TargetAudience.Valid && strings.TrimSpace(parent.TargetAudience.String) != "" {
		sb.WriteString("Audience: " + strings.TrimSpace(parent.TargetAudience.String) + "\n")
	}
	if competitors, err := businessprofile.DecodeBusinessCompetitors(parent.BusinessCompetitors); err == nil && len(competitors) > 0 {
		sb.WriteString("Competitors: " + strings.Join(competitors, ", ") + "\n")
	}
	sb.WriteString("\nCurrent location profile copy (adapt these fields, keep anything the output leaves blank):\n")
	sb.WriteString("Brand: " + local.BrandName + "\n")
	if local.PrimaryCategory != "" {
		sb.WriteString("Category: " + local.PrimaryCategory + "\n")
	}
	if local.PrimaryLocation != "" {
		sb.WriteString("Location: " + local.PrimaryLocation + "\n")
	}
	if local.BusinessDescription != "" {
		sb.WriteString("Description: " + local.BusinessDescription + "\n")
	}
	if local.ProductDescription != "" {
		sb.WriteString("Products: " + local.ProductDescription + "\n")
	}
	if local.TargetAudience != "" {
		sb.WriteString("Audience: " + local.TargetAudience + "\n")
	}
	if len(local.BusinessCompetitors) > 0 {
		sb.WriteString("Competitors: " + strings.Join(local.BusinessCompetitors, ", ") + "\n")
	}
	sb.WriteString("\nSelected Maps identity (authoritative geography; the parent Location is background only and never overrides this):\n")
	if target.Name != "" {
		sb.WriteString("Name: " + target.Name + "\n")
	}
	if target.Address != "" {
		sb.WriteString("Address: " + target.Address + "\n")
	}
	if target.Locality != "" {
		sb.WriteString("Locality: " + target.Locality + "\n")
	}
	if len(target.Localities) > 0 {
		sb.WriteString("Localities: " + strings.Join(target.Localities, ", ") + "\n")
	}
	if strings.TrimSpace(differences) != "" {
		sb.WriteString("\nOwner-stated local differences (apply above all else):\n" + strings.TrimSpace(differences) + "\n")
	}
	switch {
	case len(evidence.pages) > 0 && evidence.scoped:
		sb.WriteString("\nSelected website-scope pages (in-scope stored evidence only, not a location-scoped audit):\n")
		for _, page := range evidence.pages {
			sb.WriteString("- " + page.URL)
			if strings.TrimSpace(page.Title) != "" {
				sb.WriteString(" (" + strings.TrimSpace(page.Title) + ")")
			}
			if strings.TrimSpace(page.MetaDescription) != "" {
				sb.WriteString(" — " + strings.TrimSpace(page.MetaDescription))
			}
			sb.WriteString("\n")
		}
	case len(evidence.pages) > 0:
		sb.WriteString("\nStored parent site pages (full-site context only, not a location-scoped audit):\n")
		for _, page := range evidence.pages {
			sb.WriteString("- " + page.URL)
			if strings.TrimSpace(page.Title) != "" {
				sb.WriteString(" (" + strings.TrimSpace(page.Title) + ")")
			}
			if strings.TrimSpace(page.MetaDescription) != "" {
				sb.WriteString(" — " + strings.TrimSpace(page.MetaDescription))
			}
			sb.WriteString("\n")
		}
	case evidence.scoped:
		sb.WriteString("\nSelected website scope has no matching stored pages. Use the parent homepage context above without claiming a scoped audit.\n")
	default:
		sb.WriteString("\nStored parent site pages: none available. Use the parent homepage context above without claiming a scoped audit.\n")
	}
	sb.WriteString("\nRules:\n")
	sb.WriteString("- Localize description, products, audience, category, and location for this Maps identity; keep the brand recognizable.\n")
	sb.WriteString("- The products description is canonical; there is no structured services list to consult.\n")
	sb.WriteString("- When the differences name competitors, adapt business_competitors to the local set; otherwise leave it blank to keep the copy.\n")
	sb.WriteString("- Never invent a different city, street, or place name; never change the website URL.\n")
	sb.WriteString("- business_description is required and non-blank; an empty string for any other field keeps the current copy.\n")
	sb.WriteString(`- Respond with exactly: {"business_description":"...","product_description":"...","target_audience":"...","primary_category":"...","primary_location":"...","business_competitors":["..."]}` + "\n")
	return sb.String()
}

func parseLocationProfileGenerationOutput(raw string) (locationAdaptedProfileFields, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return locationAdaptedProfileFields{}, errors.New("location profile generation returned no JSON object")
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &decoded); err != nil {
		return locationAdaptedProfileFields{}, fmt.Errorf("decode location profile generation: %w", err)
	}
	strField := func(key string) string {
		var value string
		if raw, ok := decoded[key]; ok && json.Unmarshal(raw, &value) == nil {
			return strings.TrimSpace(value)
		}
		return ""
	}
	out := locationAdaptedProfileFields{
		BusinessDescription: strField("business_description"),
		ProductDescription:  strField("product_description"),
		TargetAudience:      strField("target_audience"),
		PrimaryCategory:     strField("primary_category"),
		PrimaryLocation:     strField("primary_location"),
	}
	if rawCompetitors, ok := decoded["business_competitors"]; ok {
		var list []string
		if err := json.Unmarshal(rawCompetitors, &list); err == nil {
			out.BusinessCompetitors = businessprofile.NormalizeBusinessCompetitors(list)
			out.CompetitorsMentioned = true
		} else {
			var single string
			if err := json.Unmarshal(rawCompetitors, &single); err == nil && strings.TrimSpace(single) != "" {
				out.BusinessCompetitors = businessprofile.NormalizeBusinessCompetitors(strings.Split(single, ","))
				out.CompetitorsMentioned = true
			}
		}
	}
	if out.BusinessDescription == "" {
		return locationAdaptedProfileFields{}, errors.New("location profile generation returned a blank business_description")
	}
	if isBusinessProfileBootstrapPlaceholder(out.BusinessDescription) {
		return locationAdaptedProfileFields{}, errors.New("location profile generation returned a placeholder business_description")
	}
	return out, nil
}

func locationTargetPrimaryLocation(target LocationTarget) string {
	if strings.TrimSpace(target.Address) != "" {
		return strings.TrimSpace(target.Address)
	}
	if strings.TrimSpace(target.Locality) != "" {
		return strings.TrimSpace(target.Locality)
	}
	for _, level := range target.Localities {
		if strings.TrimSpace(level) != "" {
			return strings.TrimSpace(level)
		}
	}
	return ""
}

func planLocationProfileUpdate(local LocationBusinessProfile, target LocationTarget, parsed locationAdaptedProfileFields) locationAdaptedProfileFields {
	if parsed.ProductDescription == "" {
		parsed.ProductDescription = local.ProductDescription
	}
	if parsed.TargetAudience == "" {
		parsed.TargetAudience = local.TargetAudience
	}
	if parsed.PrimaryCategory == "" {
		parsed.PrimaryCategory = local.PrimaryCategory
	}
	if authoritative := locationTargetPrimaryLocation(target); authoritative != "" {
		parsed.PrimaryLocation = authoritative
	} else if parsed.PrimaryLocation == "" {
		parsed.PrimaryLocation = local.PrimaryLocation
	}
	if !parsed.CompetitorsMentioned {
		parsed.BusinessCompetitors = local.BusinessCompetitors
	}
	return parsed
}

func locationProfileText(value string) pgtype.Text {
	if strings.TrimSpace(value) == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func (w *Worker) saveLocationProfileAdaptation(ctx context.Context, locationID, projectID pgtype.UUID, planned locationAdaptedProfileFields) error {
	competitors, err := json.Marshal(planned.BusinessCompetitors)
	if err != nil {
		return fmt.Errorf("encode location competitors: %w", err)
	}
	tag, err := w.pool.Exec(ctx,
		`UPDATE location_business_profiles SET primary_category = $3, primary_location = $4,
		 business_description = $5, product_description = $6, target_audience = $7,
		 business_competitors = $8, updated_at = now()
		 WHERE location_id = $1 AND project_id = $2`,
		locationID, projectID,
		locationProfileText(planned.PrimaryCategory), locationProfileText(planned.PrimaryLocation),
		locationProfileText(planned.BusinessDescription), locationProfileText(planned.ProductDescription),
		locationProfileText(planned.TargetAudience), competitors)
	if err != nil {
		return fmt.Errorf("save location profile adaptation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no location business profile for %s", locationID.String())
	}
	return nil
}
