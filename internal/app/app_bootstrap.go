package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type appBootstrapResponse struct {
	Me                meResponse        `json:"me"`
	Projects          []projectResponse `json:"projects"`
	ActiveProject     *projectResponse  `json:"active_project"`
	Crawls            []crawlResponse   `json:"crawls"`
	SelectedCrawlID   string            `json:"selected_crawl_id,omitempty"`
	Breakdown         json.RawMessage   `json:"breakdown,omitempty"`
	SessionExpiresAt  string            `json:"session_expires_at"`
	SessionRenewAfter string            `json:"session_renew_after"`
}

// handleAppBootstrap collapses the frontend's 4 serial calls (/me,
// /organizations/{org}/projects, /projects/{id}/crawls, /crawls/{id}/score-breakdown)
// into a single authenticated request executed inside one transaction.
func (a *App) handleAppBootstrap(w http.ResponseWriter, r *http.Request) {
	identity, ok := internalauth.IdentityFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	session, ok := internalauth.SessionFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	requestedProjectID := strings.TrimSpace(r.URL.Query().Get("project"))
	requestedCrawlID := strings.TrimSpace(r.URL.Query().Get("crawl"))

	resp := appBootstrapResponse{
		SessionExpiresAt:  session.ExpiresAt.Format(time.RFC3339),
		SessionRenewAfter: a.SessionManager.RenewalStartsAt(session.ExpiresAt).Format(time.RFC3339),
	}

	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		// Step 1: resolve user + orgs; persist active org if it changed.
		user, orgs, err := a.ensureUserAndOrganizations(r, queries, identity)
		if err != nil {
			serverError(w, r, err)
			return err
		}

		activeOrgID := resolveActiveOrganizationID(session.ActiveOrgID, orgs)
		if activeOrgID.Valid && activeOrgID != session.ActiveOrgID {
			if err := a.SessionManager.UpdateActiveOrganization(r.Context(), session.SessionID, activeOrgID); err != nil {
				serverError(w, r, err)
				return err
			}
		}

		resp.Me = newMeResponse(user, orgs, activeOrgID)
		resp.Me.SessionExpiresAt = resp.SessionExpiresAt
		resp.Me.SessionRenewAfter = resp.SessionRenewAfter
		a.attachActiveOrgFeatures(r.Context(), &resp.Me, activeOrgID)

		// Step 2: list projects for the active org (membership implied by org ownership).
		var projects []sqlc.Project
		if activeOrgID.Valid {
			if _, err := queries.GetOrganizationMember(r.Context(), sqlc.GetOrganizationMemberParams{
				OrgID:  activeOrgID,
				UserID: user.ID,
			}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				serverError(w, r, err)
				return err
			}
			projects, err = queries.ListProjectsForOrganization(r.Context(), activeOrgID)
			if err != nil {
				serverError(w, r, err)
				return err
			}
		}

		resp.Projects = make([]projectResponse, 0, len(projects))
		for _, p := range projects {
			resp.Projects = append(resp.Projects, newProjectResponse(p))
		}

		// Step 3: resolve active project — requested match or first.
		var activeProject *sqlc.Project
		for i := range projects {
			if requestedProjectID != "" && projects[i].ID.String() == requestedProjectID {
				activeProject = &projects[i]
				break
			}
		}
		if activeProject == nil && len(projects) > 0 {
			activeProject = &projects[0]
		}

		if activeProject != nil {
			pr := newProjectResponse(*activeProject)
			resp.ActiveProject = &pr

			// Step 4: list crawls for the active project (limit 50, no status filter).
			crawls, err := queries.ListCrawlsForProject(r.Context(), sqlc.ListCrawlsForProjectParams{
				ProjectID: activeProject.ID,
				// Column2 is the optional status filter; the query is
				// ($2 = '' OR status = $2). An empty string means "all
				// statuses" — nil would make the predicate NULL and drop
				// every row. Mirror handleListCrawls which passes "".
				Column2: "",
				Limit:   50,
				Offset:  0,
			})
			if err != nil {
				serverError(w, r, err)
				return err
			}

			resp.Crawls = make([]crawlResponse, 0, len(crawls))
			for _, c := range crawls {
				resp.Crawls = append(resp.Crawls, newCrawlResponseFromListRow(c))
			}

			// Step 5: pick selected completed crawl, mirroring loader logic.
			// Sort completed crawls by timestamp descending, pick requested or first.
			var completedCrawls []sqlc.ListCrawlsForProjectRow
			for _, c := range crawls {
				if c.Status == "completed" {
					completedCrawls = append(completedCrawls, c)
				}
			}
			// Sort descending by completed_at > started_at > created_at (same as frontend getCrawlTimestamp).
			sortCrawlsDescending(completedCrawls)

			var selectedCrawlID pgtype.UUID
			for _, c := range completedCrawls {
				if requestedCrawlID != "" && c.ID.String() == requestedCrawlID {
					selectedCrawlID = c.ID
					break
				}
			}
			if !selectedCrawlID.Valid && len(completedCrawls) > 0 {
				selectedCrawlID = completedCrawls[0].ID
			}

			// Step 6: load breakdown; tolerate not-found (leave breakdown null).
			if selectedCrawlID.Valid {
				resp.SelectedCrawlID = selectedCrawlID.String()

				breakdown, err := queries.GetCrawlScoreBreakdownByCrawlForUser(r.Context(), sqlc.GetCrawlScoreBreakdownByCrawlForUserParams{
					CrawlID: selectedCrawlID,
					UserID:  user.ID,
				})
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					serverError(w, r, err)
					return err
				}
				if err == nil && len(breakdown.BreakdownJson) > 0 {
					resp.Breakdown = json.RawMessage(breakdown.BreakdownJson)
				}
			}
		}

		return nil
	}) {
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

// sortCrawlsDescending sorts crawls in place by timestamp descending,
// matching the frontend's getCrawlTimestamp logic (completed_at ?? started_at ?? created_at).
func sortCrawlsDescending(crawls []sqlc.ListCrawlsForProjectRow) {
	for i := 1; i < len(crawls); i++ {
		for j := i; j > 0 && crawlTimestamp(crawls[j]) > crawlTimestamp(crawls[j-1]); j-- {
			crawls[j], crawls[j-1] = crawls[j-1], crawls[j]
		}
	}
}

func crawlTimestamp(c sqlc.ListCrawlsForProjectRow) int64 {
	if c.CompletedAt.Valid {
		return c.CompletedAt.Time.UTC().UnixMilli()
	}
	if c.StartedAt.Valid {
		return c.StartedAt.Time.UTC().UnixMilli()
	}
	if c.CreatedAt.Valid {
		return c.CreatedAt.Time.UTC().UnixMilli()
	}
	return time.Time{}.UnixMilli()
}
