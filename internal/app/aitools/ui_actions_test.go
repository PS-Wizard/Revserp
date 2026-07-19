package aitools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestNavigateTool(t *testing.T) {
	tool := navigateTool()
	for destination := range navigateDestinations {
		result, err := tool.Execute(context.Background(), []byte(`{"destination":"`+destination+`"}`), Scope{})
		if err != nil || result.Destination != destination {
			t.Errorf("%s: %+v, %v", destination, result, err)
		}
	}
	for _, args := range []string{`{"destination":"/evil"}`, `{"destination":"unknown"}`, `[]`, `{`, `{}`, `{"destination":"audit_seo","destination":"audit_aeo"}`} {
		if _, err := tool.Execute(context.Background(), []byte(args), Scope{}); err == nil {
			t.Errorf("expected rejection for %s", args)
		}
	}
}

type fakeProjectReader struct {
	projects []sqlc.Project
	arg      sqlc.ListProjectsForOrganizationForUserParams
	err      error
}

func (r *fakeProjectReader) ListProjectsForOrganizationForUser(_ context.Context, arg sqlc.ListProjectsForOrganizationForUserParams) ([]sqlc.Project, error) {
	r.arg = arg
	return r.projects, r.err
}

func TestListProjects(t *testing.T) {
	user, org := testUUID(1), testUUID(2)
	reader := &fakeProjectReader{projects: []sqlc.Project{{ID: testUUID(3), Name: "Acme", BaseUrl: "https://acme.test"}}}
	result, err := execListProjects(context.Background(), user, org, reader)
	if err != nil || reader.arg.UserID != user || reader.arg.OrganizationID != org {
		t.Fatalf("got %+v, %v, arg=%+v", result, err, reader.arg)
	}
	if !strings.Contains(result.Content, "Acme") || !strings.Contains(result.Content, "https://acme.test") {
		t.Fatalf("expected project name and base url in content: %s", result.Content)
	}
	// list_projects never carries a tenant-scope UI action.
	if result.ProjectID != "" || result.Destination != "" {
		t.Fatalf("list_projects must not emit an action: %+v", result)
	}

	// Missing scope / db errors must fail safely rather than panic.
	if _, err := execListProjects(context.Background(), user, org, &fakeProjectReader{err: errors.New("db down")}); err == nil {
		t.Fatal("expected reader error to propagate")
	}
	if _, err := execListProjects(context.Background(), pgtype.UUID{}, org, reader); err == nil {
		t.Fatal("expected failure with an invalid user scope")
	}
}

func TestSwitchProject(t *testing.T) {
	user, org := testUUID(1), testUUID(2)
	reader := &fakeProjectReader{projects: []sqlc.Project{{ID: testUUID(3), Name: "Acme"}}}
	result, err := execSwitchProject(context.Background(), []byte(`{"name":" acME "}`), user, org, reader)
	if err != nil || result.ProjectID != testUUID(3).String() || reader.arg.UserID != user || reader.arg.OrganizationID != org {
		t.Fatalf("got %+v, %v, arg=%+v", result, err, reader.arg)
	}
	for _, projects := range [][]sqlc.Project{nil, {{ID: testUUID(4), Name: "acme"}, {ID: testUUID(5), Name: "ACME"}}} {
		reader.projects = projects
		result, err = execSwitchProject(context.Background(), []byte(`{"name":"acme"}`), user, org, reader)
		if err == nil || result.ProjectID != "" {
			t.Errorf("expected safe failure, got %+v, %v", result, err)
		}
	}
	reader.err = errors.New("db unavailable")
	if _, err := execSwitchProject(context.Background(), []byte(`{"name":"acme"}`), user, org, reader); err == nil {
		t.Fatal("expected reader error")
	}
	for _, args := range []string{`{}`, `[]`, `{"name":" "}`, `{"name":1}`, `{"name":"acme","id":"x"}`, `{"name":"acme","name":"other"}`} {
		if _, err := execSwitchProject(context.Background(), []byte(args), user, org, reader); err == nil {
			t.Errorf("expected invalid args %s", args)
		}
	}
}
