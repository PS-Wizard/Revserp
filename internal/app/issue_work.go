package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// handleMarkCrawlIssueWorkDone records that the current user finished their
// part of the work for a crawl issue. It resolves or creates the shared work
// item and its active attempt, then adds the user as an attempt contributor.
// Route: POST /crawl-issues/{issueID}/work-done (registration left to routes.go).
func (a *App) handleMarkCrawlIssueWorkDone(w http.ResponseWriter, r *http.Request) {
	issueID, err := parseUUIDParam(chi.URLParam(r, "issueID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid issue id")
		return
	}

	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}

	user := principal.User
	issue, err := a.Queries.GetWorkableCrawlIssueByIDForUser(r.Context(), sqlc.GetWorkableCrawlIssueByIDForUserParams{
		IssueID: issueID,
		UserID:  user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	q := a.Queries.WithTx(tx)

	subjectKind := "page"
	subjectKey := issue.Url
	if issue.IssueGroupID.Valid {
		subjectKind = "group"
		members, err := q.ListCrawlIssueGroupMembersForGroup(r.Context(), issue.IssueGroupID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		subjectKey = duplicateGroupWorkKey(members)
	}
	if isSitewideWorkspaceIssue(issue.IssueType) {
		subjectKind = "site"
	}
	item, err := q.UpsertIssueWorkItem(r.Context(), sqlc.UpsertIssueWorkItemParams{
		ProjectID:          issue.ProjectID,
		SubjectKind:        subjectKind,
		SubjectKey:         subjectKey,
		Pillar:             issue.Pillar,
		Bucket:             issue.Bucket,
		IssueType:          issue.IssueType,
		SourceCrawlIssueID: issue.ID,
		SourceIssueGroupID: issue.IssueGroupID,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	// The upsert keeps the work-item row locked until this transaction commits,
	// which serializes the active-attempt check and insert for this identity.

	attempt, err := q.GetActiveIssueWorkAttemptForWorkItem(r.Context(), item.ID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		attempt, err = q.CreateIssueWorkAttempt(r.Context(), sqlc.CreateIssueWorkAttemptParams{
			WorkItemID:    item.ID,
			SourceCrawlID: issue.CrawlID,
		})
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	}

	if attempt.LockedAt.Valid {
		writeJSONError(w, http.StatusConflict, "verification already started")
		return
	}

	if _, err := q.AddIssueWorkAttemptContributor(r.Context(), sqlc.AddIssueWorkAttemptContributorParams{
		AttemptID: attempt.ID,
		UserID:    user.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusConflict, "verification already started")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := q.UpdateIssueWorkItemStatus(r.Context(), sqlc.UpdateIssueWorkItemStatusParams{ID: item.ID, Status: "awaiting_verification"}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	contributors, err := q.ListIssueWorkAttemptContributors(r.Context(), attempt.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, newIssueWorkStateResponse(item, attempt, contributors))
}

// handleRemoveOwnIssueWorkContribution removes the current user's pending contribution
// from an attempt. Only allowed before the attempt locks.
// Route: DELETE /issue-work-attempts/{attemptID}/contributors/me.
func (a *App) handleRemoveOwnIssueWorkContribution(w http.ResponseWriter, r *http.Request) {
	attemptID, err := parseUUIDParam(chi.URLParam(r, "attemptID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid attempt id")
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
	row, err := queries.GetIssueWorkAttemptByIDForUser(r.Context(), sqlc.GetIssueWorkAttemptByIDForUserParams{ID: attemptID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "attempt not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if row.LockedAt.Valid || row.Status == "fixed" {
		writeJSONError(w, http.StatusConflict, "attempt is locked")
		return
	}

	removed, err := queries.RemoveIssueWorkAttemptContributor(r.Context(), sqlc.RemoveIssueWorkAttemptContributorParams{AttemptID: attemptID, UserID: user.ID})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if removed == 0 {
		writeJSONError(w, http.StatusNotFound, "contribution not found")
		return
	}
	contributors, err := queries.ListIssueWorkAttemptContributors(r.Context(), attemptID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if len(contributors) == 0 {
		deleted, err := queries.DeleteEmptyUnlockedIssueWorkAttempt(r.Context(), attemptID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if deleted > 0 {
			if err := queries.UpdateIssueWorkItemStatus(r.Context(), sqlc.UpdateIssueWorkItemStatusParams{ID: row.WorkItemID, Status: "open"}); err != nil {
				writeJSONError(w, http.StatusInternalServerError, "internal server error")
				return
			}
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, newIssueWorkStateResponse(sqlc.IssueWorkItem{}, sqlc.IssueWorkAttempt{ID: row.ID, WorkItemID: row.WorkItemID, SourceCrawlID: row.SourceCrawlID, Status: row.Status, VerificationCrawlID: row.VerificationCrawlID, CreatedAt: row.CreatedAt, LockedAt: row.LockedAt, VerifiedAt: row.VerifiedAt}, contributors))
}

// handleGetProjectWorkReport returns verified work attempts for a project in a
// date range. Without user_id it is the deduplicated organization report; with
// user_id it is the per-user report including only their contributions.
// Route: GET /projects/{projectID}/work-report?from&to&user_id.
func (a *App) handleGetProjectWorkReport(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}

	user := principal.User
	if _, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: user.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	from := time.Time{}
	if raw := r.URL.Query().Get("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid from timestamp")
			return
		}
		from = parsed
	} else {
		from = from.UTC().AddDate(-100, 0, 0)
	}
	to := time.Now().UTC()
	if raw := r.URL.Query().Get("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid to timestamp")
			return
		}
		to = parsed
	}

	var userID pgtype.UUID
	if raw := r.URL.Query().Get("user_id"); raw != "" {
		parsed, err := parseUUIDParam(raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid user id")
			return
		}
		userID = parsed
	}

	rows, err := a.Queries.ListVerifiedIssueWorkAttemptsForProject(r.Context(), sqlc.ListVerifiedIssueWorkAttemptsForProjectParams{
		ProjectID: projectID,
		From:      pgtype.Timestamptz{Time: from, Valid: true},
		To:        pgtype.Timestamptz{Time: to, Valid: true},
		UserID:    userID,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	items := make([]workReportItemResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, workReportItemResponse{
			AttemptID:           row.AttemptID.String(),
			Status:              row.Status,
			VerifiedAt:          formatTimestamptz(row.VerifiedAt),
			VerificationCrawlID: row.VerificationCrawlID.String(),
			WorkItemID:          row.WorkItemID.String(),
			SubjectKind:         row.SubjectKind,
			SubjectKey:          row.SubjectKey,
			Pillar:              row.Pillar,
			Bucket:              row.Bucket,
			IssueType:           row.IssueType,
			ContributorEmails:   row.ContributorEmails,
		})
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type issueWorkContributorResponse struct {
	UserID       string `json:"user_id"`
	MarkedDoneAt string `json:"marked_done_at"`
}

type issueWorkStateResponse struct {
	WorkItemID   string                         `json:"work_item_id,omitempty"`
	AttemptID    string                         `json:"attempt_id"`
	Status       string                         `json:"status"`
	Locked       bool                           `json:"locked"`
	Contributors []issueWorkContributorResponse `json:"contributors"`
}

type workReportItemResponse struct {
	AttemptID           string   `json:"attempt_id"`
	Status              string   `json:"status"`
	VerifiedAt          string   `json:"verified_at,omitempty"`
	VerificationCrawlID string   `json:"verification_crawl_id,omitempty"`
	WorkItemID          string   `json:"work_item_id"`
	SubjectKind         string   `json:"subject_kind"`
	SubjectKey          string   `json:"subject_key"`
	Pillar              string   `json:"pillar"`
	Bucket              string   `json:"bucket"`
	IssueType           string   `json:"issue_type"`
	ContributorEmails   []string `json:"contributor_emails"`
}

func formatTimestamptz(value pgtype.Timestamptz) string {
	if !value.Valid {
		return ""
	}
	return value.Time.UTC().Format(time.RFC3339)
}

func contributorResponses(rows []sqlc.IssueWorkAttemptContributor) []issueWorkContributorResponse {
	out := make([]issueWorkContributorResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, issueWorkContributorResponse{
			UserID:       row.UserID.String(),
			MarkedDoneAt: formatTimestamptz(row.MarkedDoneAt),
		})
	}
	return out
}

func newIssueWorkStateResponse(
	item sqlc.IssueWorkItem,
	attempt sqlc.IssueWorkAttempt,
	contributors []sqlc.IssueWorkAttemptContributor,
) issueWorkStateResponse {
	response := issueWorkStateResponse{
		AttemptID:    attempt.ID.String(),
		Status:       attempt.Status,
		Locked:       attempt.LockedAt.Valid,
		Contributors: contributorResponses(contributors),
	}
	if item.ID.Valid {
		response.WorkItemID = item.ID.String()
	}
	return response
}

func isSitewideWorkspaceIssue(issueType string) bool {
	switch issueType {
	case "weak_open_graph_coverage", "missing_website_schema", "missing_org_identity_schema", "missing_about_page", "missing_contact_page", "missing_policy_page", "homepage_missing_org_contact_trust_signals":
		return true
	default:
		return false
	}
}

func duplicateGroupWorkKey(members []sqlc.CrawlIssueGroupMember) string {
	urls := make([]string, 0, len(members))
	for _, member := range members {
		urls = append(urls, member.Url)
	}
	sort.Strings(urls)
	sum := sha256.Sum256([]byte(strings.Join(urls, "\n")))
	return hex.EncodeToString(sum[:])
}
