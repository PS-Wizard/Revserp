package aitools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// businessProfileReader is the narrow DB port get_business_profile depends
// on, so it can be faked in tests without a real database.
type businessProfileReader interface {
	GetProjectBusinessProfileByProjectIDForUser(ctx context.Context, arg sqlc.GetProjectBusinessProfileByProjectIDForUserParams) (sqlc.GetProjectBusinessProfileByProjectIDForUserRow, error)
}

func businessProfileTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "get_business_profile",
			Description: "Get the current project's business profile: brand name, website URL, primary category, primary location, business description, and seed prompts. Takes no arguments.",
			Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
		Execute: func(ctx context.Context, _ json.RawMessage, s Scope) (Result, error) {
			return execGetBusinessProfile(ctx, s.ProjectID, s.UserID, s.Queries)
		},
	}
}

type businessProfileOutput struct {
	HasBusinessProfile bool   `json:"has_business_profile"`
	BrandName          string `json:"brand_name,omitempty"`
	WebsiteURL         string `json:"website_url,omitempty"`
	PrimaryCategory    string `json:"primary_category,omitempty"`
	PrimaryLocation    string `json:"primary_location,omitempty"`
	Description        string `json:"business_description,omitempty"`
	SeedPrompts        []byte `json:"seed_prompts,omitempty"`
}

func execGetBusinessProfile(ctx context.Context, projectID pgtype.UUID, userID pgtype.UUID, reader businessProfileReader) (Result, error) {
	row, err := reader.GetProjectBusinessProfileByProjectIDForUser(ctx, sqlc.GetProjectBusinessProfileByProjectIDForUserParams{
		ProjectID: projectID,
		UserID:    userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return jsonResult(businessProfileOutput{HasBusinessProfile: false}, "no business profile set")
		}
		return Result{}, err
	}

	output := businessProfileOutput{
		HasBusinessProfile: true,
		BrandName:          row.BrandName,
		WebsiteURL:         row.WebsiteUrl,
		PrimaryCategory:    textValue(row.PrimaryCategory),
		PrimaryLocation:    textValue(row.PrimaryLocation),
		Description:        capText(textValue(row.BusinessDescription), 1000),
	}
	if len(row.SeedPrompts) > 0 {
		output.SeedPrompts = row.SeedPrompts
	}
	return jsonResult(output, "business profile for "+row.BrandName)
}

// textValue extracts a nullable Postgres text field, trimmed.
func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

// jsonResult marshals v as the tool's Content and pairs it with summary.
func jsonResult(v any, summary string) (Result, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded), Summary: summary}, nil
}
