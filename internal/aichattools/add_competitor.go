package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/projectcompetitors"
)

// addCompetitorName is the save-only project competitor roster tool: it saves
// a competitor name and URL to the roster and never starts a crawl.
const addCompetitorName = "add_competitor"

const addCompetitorSchema = `{
  "type": "object",
  "required": ["url"],
  "properties": {
    "name": {"type": "string", "description": "Competitor display name. Trimmed; defaults to the URL host when blank."},
    "url": {"type": "string", "description": "Competitor website URL. Must be a real, public URL: never invent or guess one. Saved as the canonical seed_url."}
  },
  "additionalProperties": false
}`

func addCompetitorTool() Tool {
	return Tool{
		Def: Def{
			Name:        addCompetitorName,
			Label:       "Add competitor",
			Description: "Save one competitor to the current project's competitor roster (name plus website URL). Save-only: the row is saved, not crawled, and no crawl or job is ever started. Use only real, researched competitor URLs; never invent or guess a URL. Project scoped only: location turns are denied so a location can never write the parent roster. Requires project membership; respects the plan competitor limit. Retrying an already-saved URL returns the existing row without a duplicate.",
			Schema:      json.RawMessage(addCompetitorSchema),
		},
		Execute: executeAddCompetitor,
	}
}

type addCompetitorArgs struct {
	Name string
	URL  string
}

// addCompetitorSaver persists one roster row. Production wires
// projectcompetitors.SaveProjectCompetitor; tests substitute a fake.
type addCompetitorSaver func(ctx context.Context, store projectcompetitors.CompetitorStore, projectID, userID pgtype.UUID, name, rawURL string) (projectcompetitors.SaveOutcome, error)

type addCompetitorExecutor struct {
	queries *sqlc.Queries
	db      Transactor
	save    addCompetitorSaver
}

func (e *addCompetitorExecutor) saver() addCompetitorSaver {
	if e.save != nil {
		return e.save
	}
	return projectcompetitors.SaveProjectCompetitor
}

func executeAddCompetitor(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.LocationID.Valid {
		return Result{Content: addCompetitorName + " error: location turns cannot add project competitors"}, nil
	}
	if s.Queries == nil || s.DB == nil {
		return Result{}, errors.New("add_competitor: scope has no queries or transaction support")
	}
	exec := addCompetitorExecutor{queries: s.Queries, db: s.DB}
	return exec.run(ctx, args, s.ProjectID, s.UserID)
}

func (e *addCompetitorExecutor) run(ctx context.Context, raw json.RawMessage, projectID, userID pgtype.UUID) (Result, error) {
	args, err := parseAddCompetitorArgs(raw)
	if err != nil {
		return Result{Content: addCompetitorName + " error: " + err.Error()}, nil
	}
	tx, err := e.db.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("%s: begin tx: %w", addCompetitorName, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	outcome, err := e.saver()(ctx, e.queries.WithTx(tx), projectID, userID, args.Name, args.URL)
	if err != nil {
		if projectcompetitors.IsInputError(err) {
			return Result{Content: addCompetitorName + " error: " + err.Error()}, nil
		}
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("%s: commit: %w", addCompetitorName, err)
	}
	return newAddCompetitorResult(outcome), nil
}

// newAddCompetitorResult renders one saved roster row with its plan capacity
// and the explicit saved-not-crawled marker the model must see.
func newAddCompetitorResult(outcome projectcompetitors.SaveOutcome) Result {
	remaining := int64(outcome.MaxCompetitors) - outcome.Total
	if remaining < 0 {
		remaining = 0
	}
	content, err := json.Marshal(map[string]any{
		"id":              outcome.Competitor.ID.String(),
		"name":            outcome.Competitor.Name,
		"seed_url":        outcome.Competitor.SeedUrl,
		"max_competitors": outcome.MaxCompetitors,
		"total":           outcome.Total,
		"remaining":       remaining,
		"already_existed": outcome.Existed,
		"crawled":         false,
		"status":          "saved, not crawled",
	})
	if err != nil {
		return Result{Content: `{"status":"saved, not crawled"}`, Summary: "saved competitor, not crawled"}
	}
	summary := fmt.Sprintf("saved competitor %s, not crawled (%d of %d used)", outcome.Competitor.Name, outcome.Total, outcome.MaxCompetitors)
	if outcome.Existed {
		summary = fmt.Sprintf("competitor %s already saved, not crawled (%d of %d used)", outcome.Competitor.Name, outcome.Total, outcome.MaxCompetitors)
	}
	return Result{Content: string(content), Summary: summary}
}

func parseAddCompetitorArgs(raw json.RawMessage) (addCompetitorArgs, error) {
	args := addCompetitorArgs{}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for key, value := range fields {
		if strings.TrimSpace(string(value)) == "null" {
			return args, fmt.Errorf("argument %q must be a string", key)
		}
		switch key {
		case "name":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.Name = v
		case "url":
			var v string
			if err := json.Unmarshal(value, &v); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			args.URL = v
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	if strings.TrimSpace(args.URL) == "" {
		return args, errors.New(`missing required argument "url"`)
	}
	return args, nil
}
