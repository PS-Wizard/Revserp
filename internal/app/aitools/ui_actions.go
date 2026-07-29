package aitools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/ai"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

const maxProjectNameLength = 200

var navigateDestinations = map[string]bool{
	"audit_summary": true, "audit_seo": true, "audit_aeo": true, "audit_pagespeed": true, "site_graph": true, "search_console": true, "visibility": true,
}

func navigateTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "navigate",
			Description: "Navigate within Revserp to an audit section, Search Console, or Visibility. Use site_graph for the sitemap/site graph. This cannot open the comparison view — for a competitor analysis or a comparison against another project, use compare_projects instead.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"destination":{"type":"string","enum":["audit_summary","audit_seo","audit_aeo","audit_pagespeed","site_graph","search_console","visibility"]}},"required":["destination"],"additionalProperties":false}`),
		},
		Execute: func(_ context.Context, args json.RawMessage, _ Scope) (Result, error) {
			var parsed struct {
				Destination string `json:"destination"`
			}
			if err := strictObject(args, &parsed, "destination"); err != nil || !navigateDestinations[parsed.Destination] {
				return Result{}, errors.New("destination must be a supported Revserp destination")
			}
			return Result{Content: `{"destination":` + mustJSON(parsed.Destination) + `}`, Summary: "navigated", Destination: parsed.Destination}, nil
		},
	}
}

type projectReader interface {
	ListProjectsForOrganizationForUser(context.Context, sqlc.ListProjectsForOrganizationForUserParams) ([]sqlc.Project, error)
}

func listProjectsTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "list_projects",
			Description: "List the projects in the current organization that the user can access. Returns each project's name and base URL. Use switch_project with an exact name to change the active project. Takes no arguments.",
			Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
		Execute: func(ctx context.Context, _ json.RawMessage, s Scope) (Result, error) {
			return execListProjects(ctx, s.UserID, s.OrgID, s.Queries)
		},
	}
}

type projectRowOutput struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
}

type listProjectsOutput struct {
	Projects []projectRowOutput `json:"projects"`
}

func execListProjects(ctx context.Context, userID, orgID pgtype.UUID, reader projectReader) (Result, error) {
	if !userID.Valid || !orgID.Valid || reader == nil {
		return Result{}, errors.New("project access is unavailable")
	}
	projects, err := reader.ListProjectsForOrganizationForUser(ctx, sqlc.ListProjectsForOrganizationForUserParams{OrganizationID: orgID, UserID: userID})
	if err != nil {
		return Result{}, err
	}
	output := listProjectsOutput{Projects: make([]projectRowOutput, 0, len(projects))}
	for _, project := range projects {
		output.Projects = append(output.Projects, projectRowOutput{Name: project.Name, BaseURL: project.BaseUrl})
	}
	return jsonResult(output, fmt.Sprintf("%d projects", len(output.Projects)))
}

func switchProjectTool() Tool {
	return Tool{
		Def: ai.ToolDef{
			Name:        "switch_project",
			Description: "Switch to a visible project by its exact project name (case-insensitive). Do not use a project ID.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`),
		},
		Execute: func(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
			return execSwitchProject(ctx, args, s.UserID, s.OrgID, s.Queries)
		},
	}
}

func execSwitchProject(ctx context.Context, args json.RawMessage, userID, orgID pgtype.UUID, reader projectReader) (Result, error) {
	var parsed struct {
		Name string `json:"name"`
	}
	if err := strictObject(args, &parsed, "name"); err != nil {
		return Result{}, errors.New("arguments must be exactly an object with a project name")
	}
	name := strings.TrimSpace(parsed.Name)
	if name == "" || len(name) > maxProjectNameLength {
		return Result{}, errors.New("project name must be nonempty and within the allowed length")
	}
	if !userID.Valid || !orgID.Valid || reader == nil {
		return Result{}, errors.New("project access is unavailable")
	}
	projects, err := reader.ListProjectsForOrganizationForUser(ctx, sqlc.ListProjectsForOrganizationForUserParams{OrganizationID: orgID, UserID: userID})
	if err != nil {
		return Result{}, err
	}
	var matches []sqlc.Project
	for _, project := range projects {
		if strings.EqualFold(project.Name, name) {
			matches = append(matches, project)
		}
	}
	switch len(matches) {
	case 0:
		return Result{}, fmt.Errorf("no visible project named %q", name)
	case 1:
		id := matches[0].ID.String()
		return Result{Content: `{"name":` + mustJSON(matches[0].Name) + `}`, Summary: "project switched", ProjectID: id}, nil
	default:
		return Result{}, fmt.Errorf("multiple visible projects match %q; use the exact visible name", name)
	}
}

func strictObject(raw json.RawMessage, target any, names ...string) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return errors.New("arguments must be an object")
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return errors.New("arguments must be an object")
	}

	allowed := make(map[string]struct{}, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return errors.New("arguments must be an object")
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("arguments must be an object")
		}
		if _, ok := allowed[name]; !ok {
			return errors.New("unexpected arguments")
		}
		if _, ok := seen[name]; ok {
			return errors.New("duplicate argument")
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errors.New("arguments must be an object")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return errors.New("arguments must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("unexpected arguments")
	}
	for _, name := range names {
		if _, ok := seen[name]; !ok {
			return errors.New("missing argument")
		}
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return err
	}
	return nil
}

func mustJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
