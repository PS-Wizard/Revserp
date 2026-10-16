package locationsetup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/localvisibility"
)

const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"

	StepPending   = "pending"
	StepRunning   = "running"
	StepCompleted = "completed"
	StepFailed    = "failed"
	StepSkipped   = "skipped"

	StepPrepare        = "prepare"
	StepProfile        = "profile"
	StepQuestions      = "questions"
	StepMapQueries     = "map_queries"
	StepAIVisibility   = "ai_visibility"
	StepMapsVisibility = "maps_visibility"

	JobLocationProfileGeneration = "location_profile_generation"
	JobMapQueryGeneration        = "map_query_generation"
	JobPromptGeneration          = "prompt_generation"
	JobVisibilityRun             = "visibility_run"
	JobLocalVisibility           = "local_visibility"

	DefaultMapsRadiusM   = 5000
	MaxDifferencesLength = 2000
)

var OrderedSteps = []string{StepPrepare, StepProfile, StepQuestions, StepMapQueries, StepAIVisibility, StepMapsVisibility}

const (
	SkipAIVisibilityDisabled = "visibility audits are disabled for this workspace"
	SkipAIVisibilityQuota    = "visibility audit monthly limit reached"
	SkipAIVisibilityAccess   = "requester workspace access changed"
	SkipMapsNoQueries        = "no enabled map queries"
	SkipMapsUnbound          = "bind a Google Maps listing before starting a ranking run"
	SkipMapsBudget           = "maps spending allowance is unavailable"
)

// MaxDisclosedMapsCredits bounds the pre-start quote: starter cap x grid x per-call cost.
const MaxDisclosedMapsCredits = localvisibility.MapQueryStarterLimit * localvisibility.GridPointCount * localvisibility.MapsCreditsPerCall

// DB is satisfied by *pgxpool.Pool and pgx.Tx.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Setup is one location_setup row. EstimatedMapsCredits is the disclosed
// maximum before the Maps run is reserved, and the actual quote after.
type Setup struct {
	ID                   pgtype.UUID
	OrganizationID       pgtype.UUID
	ProjectID            pgtype.UUID
	LocationID           pgtype.UUID
	RequestedByUserID    pgtype.UUID
	Differences          string
	Status               string
	Error                string
	Steps                map[string]string
	Reasons              map[string]string
	AIAuditID            pgtype.UUID
	MapsRunID            pgtype.UUID
	EstimatedMapsCredits *int32
}

// MapsQueriesJobStatus is the regenerate/status endpoint state.
type MapsQueriesJobStatus struct {
	Status string
	Error  string
}

// DeferLaunch is a transient launch refusal: the step stays pending with the
// reason recorded, and the reconciler retries it later.
type DeferLaunch struct{ Reason string }

func (e *DeferLaunch) Error() string { return e.Reason }

func OverallStatus(steps map[string]string) string {
	for _, id := range OrderedSteps {
		if steps[id] == StepFailed {
			return StatusFailed
		}
	}
	for _, id := range OrderedSteps {
		if s := steps[id]; s != StepCompleted && s != StepSkipped {
			return StatusRunning
		}
	}
	return StatusCompleted
}

func NormalizeDifferences(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > MaxDifferencesLength {
		return "", fmt.Errorf("differences must be at most %d characters", MaxDifferencesLength)
	}
	if !utf8.ValidString(trimmed) {
		return "", errors.New("differences must be valid UTF-8")
	}
	return trimmed, nil
}

func ApplyDifferences(stored, incoming string) string {
	if stored != "" || incoming == "" {
		return stored
	}
	return incoming
}

// Launchable reports which setup-owned launches still need doing. Completed
// and skipped steps never relaunch; branch launches wait for profile.
func Launchable(steps map[string]string) (profile, questions, mapQueries, aiAudit, mapsRun bool) {
	if steps[StepProfile] != StepCompleted {
		return true, false, false, false, false
	}
	if s := steps[StepQuestions]; s != StepCompleted && s != StepSkipped {
		questions = true
	}
	if s := steps[StepMapQueries]; s != StepCompleted && s != StepSkipped {
		mapQueries = true
	}
	if steps[StepQuestions] == StepCompleted {
		if s := steps[StepAIVisibility]; s != StepCompleted && s != StepSkipped {
			aiAudit = true
		}
	}
	if steps[StepMapQueries] == StepCompleted {
		if s := steps[StepMapsVisibility]; s != StepCompleted && s != StepSkipped {
			mapsRun = true
		}
	}
	return false, questions, mapQueries, aiAudit, mapsRun
}

// DiscloseMapsCredits quotes the pre-start estimate: saved enabled queries
// when any exist, otherwise the bounded starter maximum.
func DiscloseMapsCredits(savedEnabled int) int32 {
	if savedEnabled > 0 {
		if credits, err := localvisibility.ExpectedRunCredits(savedEnabled, localvisibility.GridPointCount); err == nil {
			return int32(credits)
		}
	}
	return int32(MaxDisclosedMapsCredits)
}

var _ = pgx.ErrNoRows
var _ = pgxpool.Pool{}
