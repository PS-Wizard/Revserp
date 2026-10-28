package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	guuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/keywordanalysis"
	"github.com/ps-wizard/revserp/internal/organizationaccess"
)

// setKeywordAnalysisName is the AI estimate writer for project and competitor
// keyword metrics. Estimates are model guesses grounded in crawled content,
// never measured search data.
const setKeywordAnalysisName = "set_keyword_analysis"

const setKeywordAnalysisSchema = `{
  "type": "object",
  "required": ["analyses"],
  "properties": {
    "competitor_id": {"type": "string", "description": "Optional competitor roster UUID. When omitted the estimates attach to the current project; when set it must be a roster competitor of the current project. Never invent one."},
    "analyses": {"type": "array", "minItems": 1, "maxItems": 60, "description": "Complete estimate list: one entry per keyword phrase with best-effort numeric estimates for all five metrics. Replaces only the listed phrases.",
      "items": {"type": "object", "required": ["keyword", "kind", "rank", "traffic", "volume", "kd", "cpc"],
        "properties": {
          "keyword": {"type": "string", "maxLength": 200},
          "kind": {"type": "string", "enum": ["brand", "non_brand"]},
          "rank": {"type": "integer", "minimum": 1, "maximum": 100},
          "traffic": {"type": "integer", "minimum": 0},
          "volume": {"type": "integer", "minimum": 0},
          "kd": {"type": "number", "minimum": 0, "maximum": 100},
          "cpc": {"type": "number", "minimum": 0},
          "results_count": {"type": "integer", "minimum": 0}
        },
        "additionalProperties": false}}
  },
  "additionalProperties": false
}`

type setKeywordAnalysisArgs struct {
	CompetitorID string
	Analyses     []keywordanalysis.Estimate
}

func setKeywordAnalysisTool() Tool {
	return Tool{
		Def: Def{
			Name:        setKeywordAnalysisName,
			Label:       "Set keyword analysis",
			Description: "Save AI-generated metric estimates (rank 1-100, traffic, volume, kd 0-100, cpc) for project, competitor, or location keywords. Estimates are best-effort planning guesses grounded in crawled content, never measured search data: every entry must include best-effort numeric estimates for all five metrics, never null, blank, or missing. No Search Console connection or measured data is required: always estimate every metric from crawled content and general knowledge, never leave a metric blank or ask the user to connect anything. Project turns omit competitor_id for the current project or pass a roster competitor UUID for one competitor; location turns never pass competitor_id and the estimates attach to the turn location. Requires organization owner or manager.",
			Schema:      json.RawMessage(setKeywordAnalysisSchema),
		},
		Execute: executeSetKeywordAnalysis,
	}
}

func executeSetKeywordAnalysis(ctx context.Context, args json.RawMessage, s Scope) (Result, error) {
	if s.Queries == nil || s.DB == nil {
		return Result{}, errors.New("set_keyword_analysis: scope has no queries or transaction support")
	}
	exec := setKeywordAnalysisExecutor{queries: s.Queries, db: s.DB}
	if s.LocationID.Valid {
		return exec.runLocation(ctx, args, s.ProjectID, s.LocationID, s.UserID)
	}
	return exec.run(ctx, args, s.ProjectID, s.UserID)
}

type setKeywordAnalysisQuerier interface {
	GetProjectByIDForUserForBusinessProfileUpdate(ctx context.Context, arg sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams) (sqlc.Project, error)
	GetOrganizationMember(ctx context.Context, arg sqlc.GetOrganizationMemberParams) (sqlc.OrganizationMember, error)
	GetUserByID(ctx context.Context, id pgtype.UUID) (sqlc.GetUserByIDRow, error)
	GetProjectCompetitorByID(ctx context.Context, id pgtype.UUID) (sqlc.GetProjectCompetitorByIDRow, error)
}

type setKeywordAnalysisExecutor struct {
	queries *sqlc.Queries
	db      Transactor
}

func emitKeywordAnalysisUpdated(ctx context.Context, exec sqlExecer, orgID, projectID pgtype.UUID, competitorID string, count int) error {
	payloadMap := map[string]interface{}{"analyses": count}
	if competitorID != "" {
		payloadMap["competitor_id"] = competitorID
	}
	payload, err := json.Marshal(payloadMap)
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx,
		`SELECT insert_organization_event($1, $2, 'keyword_analysis.updated', NULL, $3::jsonb)`,
		orgID, projectID, string(payload))
	return err
}

func (e *setKeywordAnalysisExecutor) run(ctx context.Context, raw json.RawMessage, projectID, userID pgtype.UUID) (Result, error) {
	args, err := parseSetKeywordAnalysisArgs(raw)
	if err != nil {
		return Result{Content: setKeywordAnalysisName + " error: " + err.Error()}, nil
	}
	tx, err := e.db.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("%s: begin tx: %w", setKeywordAnalysisName, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := e.queries.WithTx(tx)
	res, err := e.apply(ctx, args, projectID, userID, qtx, qtx, tx)
	if err != nil {
		if isModelError(err) {
			return Result{Content: setKeywordAnalysisName + " error: " + err.Error()}, nil
		}
		if errors.Is(err, keywordanalysis.ErrEstimateInvalid) || errors.Is(err, keywordanalysis.ErrEstimateConflict) {
			return Result{Content: setKeywordAnalysisName + " error: " + err.Error()}, nil
		}
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("%s: commit: %w", setKeywordAnalysisName, err)
	}
	return res, nil
}

// runLocation saves estimates for the turn location. The location branch
// reuses the same validation and emits keyword_analysis.updated carrying the
// location id, so location estimate views invalidate exactly one location.
func (e *setKeywordAnalysisExecutor) runLocation(ctx context.Context, raw json.RawMessage, projectID, locationID, userID pgtype.UUID) (Result, error) {
	args, err := parseSetKeywordAnalysisArgs(raw)
	if err != nil {
		return Result{Content: setKeywordAnalysisName + " error: " + err.Error()}, nil
	}
	if args.CompetitorID != "" {
		return Result{Content: setKeywordAnalysisName + " error: competitor_id is not valid in a location turn"}, nil
	}
	tx, err := e.db.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("%s: begin tx: %w", setKeywordAnalysisName, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := e.queries.WithTx(tx)
	locked, err := qtx.LockProjectLocationForQueryDraftForUser(ctx, sqlc.LockProjectLocationForQueryDraftForUserParams{
		LocationID: locationID,
		ProjectID:  projectID,
		UserID:     userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, &modelError{msg: "location not found or access denied"}
		}
		return Result{}, fmt.Errorf("%s: lock location: %w", setKeywordAnalysisName, err)
	}
	if err := organizationaccess.RequireOrganizationManager(ctx, qtx, locked.OrganizationID, userID); err != nil {
		if errors.Is(err, organizationaccess.ErrForbidden) {
			return Result{}, &modelError{msg: "only organization owners and managers can set keyword analysis"}
		}
		return Result{}, fmt.Errorf("%s: check organization manager: %w", setKeywordAnalysisName, err)
	}
	validated, err := keywordanalysis.ValidateCompleteEstimates(args.Analyses)
	if err != nil {
		return Result{}, &modelError{msg: err.Error()}
	}
	if err := keywordanalysis.UpsertLocationAnalyses(ctx, qtx, locationID, validated); err != nil {
		return Result{}, fmt.Errorf("%s: save location analysis: %w", setKeywordAnalysisName, err)
	}
	if err := emitLocationKeywordAnalysisUpdated(ctx, tx, locked.OrganizationID, projectID, locationID, len(validated)); err != nil {
		return Result{}, fmt.Errorf("%s: emit keyword_analysis.updated: %w", setKeywordAnalysisName, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("%s: commit: %w", setKeywordAnalysisName, err)
	}
	return newSetKeywordAnalysisResult("location", len(validated)), nil
}

func emitLocationKeywordAnalysisUpdated(ctx context.Context, exec sqlExecer, orgID, projectID, locationID pgtype.UUID, count int) error {
	payload, err := json.Marshal(map[string]interface{}{
		"location_id": locationID.String(),
		"analyses":    count,
	})
	if err != nil {
		return err
	}
	_, err = exec.Exec(ctx,
		`SELECT insert_organization_event($1, $2, 'keyword_analysis.updated', $3, $4::jsonb)`,
		orgID, projectID, locationID, string(payload))
	return err
}

func (e *setKeywordAnalysisExecutor) apply(ctx context.Context, args setKeywordAnalysisArgs, projectID, userID pgtype.UUID, q setKeywordAnalysisQuerier, qtx *sqlc.Queries, exec sqlExecer) (Result, error) {
	project, err := q.GetProjectByIDForUserForBusinessProfileUpdate(ctx, sqlc.GetProjectByIDForUserForBusinessProfileUpdateParams{ID: projectID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, &modelError{msg: "project not found or access denied"}
		}
		return Result{}, fmt.Errorf("%s: lock project: %w", setKeywordAnalysisName, err)
	}
	if err := organizationaccess.RequireOrganizationManager(ctx, q, project.OrganizationID, userID); err != nil {
		if errors.Is(err, organizationaccess.ErrForbidden) {
			return Result{}, &modelError{msg: "only organization owners and managers can set keyword analysis"}
		}
		return Result{}, fmt.Errorf("%s: check organization manager: %w", setKeywordAnalysisName, err)
	}
	validated, err := keywordanalysis.ValidateCompleteEstimates(args.Analyses)
	if err != nil {
		return Result{}, &modelError{msg: err.Error()}
	}
	if args.CompetitorID == "" {
		if err := keywordanalysis.UpsertProjectAnalyses(ctx, qtx, projectID, validated); err != nil {
			return Result{}, fmt.Errorf("%s: save project analysis: %w", setKeywordAnalysisName, err)
		}
		if err := emitKeywordAnalysisUpdated(ctx, exec, project.OrganizationID, projectID, "", len(validated)); err != nil {
			return Result{}, fmt.Errorf("%s: emit keyword_analysis.updated: %w", setKeywordAnalysisName, err)
		}
		return newSetKeywordAnalysisResult("project", len(validated)), nil
	}
	competitorUUID, err := guuid.Parse(args.CompetitorID)
	if err != nil {
		return Result{}, &modelError{msg: "competitor_id must be a UUID"}
	}
	competitorID := pgtype.UUID{Bytes: competitorUUID, Valid: true}
	competitor, err := q.GetProjectCompetitorByID(ctx, competitorID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, &modelError{msg: "competitor not found"}
		}
		return Result{}, fmt.Errorf("%s: load competitor: %w", setKeywordAnalysisName, err)
	}
	if competitor.ProjectID != projectID {
		return Result{}, &modelError{msg: "competitor not found"}
	}
	if err := keywordanalysis.UpsertCompetitorAnalyses(ctx, qtx, competitorID, validated); err != nil {
		return Result{}, fmt.Errorf("%s: save competitor analysis: %w", setKeywordAnalysisName, err)
	}
	if err := emitKeywordAnalysisUpdated(ctx, exec, project.OrganizationID, projectID, args.CompetitorID, len(validated)); err != nil {
		return Result{}, fmt.Errorf("%s: emit keyword_analysis.updated: %w", setKeywordAnalysisName, err)
	}
	return newSetKeywordAnalysisResult("competitor", len(validated)), nil
}

func newSetKeywordAnalysisResult(scope string, count int) Result {
	content, err := json.Marshal(map[string]any{"scope": scope, "analyses": count})
	if err != nil {
		return Result{Content: `{"status":"saved"}`, Summary: "saved keyword analysis"}
	}
	return Result{Content: string(content), Summary: fmt.Sprintf("saved %d keyword estimates for %s", count, scope)}
}

func parseSetKeywordAnalysisArgs(raw json.RawMessage) (setKeywordAnalysisArgs, error) {
	args := setKeywordAnalysisArgs{}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for key, value := range fields {
		switch key {
		case "competitor_id":
			var id *string
			if err := json.Unmarshal(value, &id); err != nil {
				return args, fmt.Errorf("argument %q must be a string", key)
			}
			if id != nil {
				args.CompetitorID = *id
			}
		case "analyses":
			var rows []struct {
				Keyword      *string  `json:"keyword"`
				Kind         *string  `json:"kind"`
				Rank         *int32   `json:"rank"`
				Traffic      *int64   `json:"traffic"`
				Volume       *int64   `json:"volume"`
				KD           *float64 `json:"kd"`
				CPC          *float64 `json:"cpc"`
				ResultsCount *int64   `json:"results_count"`
			}
			if err := json.Unmarshal(value, &rows); err != nil {
				return args, fmt.Errorf("argument %q must be an array of estimate objects", key)
			}
			for _, row := range rows {
				estimate := keywordanalysis.Estimate{
					Rank: row.Rank, Traffic: row.Traffic, Volume: row.Volume,
					KD: row.KD, CPC: row.CPC, ResultsCount: row.ResultsCount,
				}
				if row.Keyword != nil {
					estimate.Keyword = *row.Keyword
				}
				if row.Kind != nil {
					estimate.Kind = *row.Kind
				}
				args.Analyses = append(args.Analyses, estimate)
			}
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	if !fieldPresent(fields, "analyses") {
		return args, errors.New("analyses is required as a non-empty list")
	}
	return args, nil
}
