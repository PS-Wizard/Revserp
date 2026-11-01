package app

import (
	"net/http"
	"time"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
)

type usageQuota struct {
	Used  int64 `json:"used"`
	Limit int32 `json:"limit"`
}

type mapsCreditUsage struct {
	Remaining int64 `json:"remaining"`
	Reserved  int64 `json:"reserved"`
	Spent     int64 `json:"spent"`
}

type meUsageResponse struct {
	OrganizationID           string          `json:"organization_id"`
	PeriodStart              string          `json:"period_start"`
	PeriodEnd                string          `json:"period_end"`
	AIMessages               usageQuota      `json:"ai_messages"`
	AIVisibilityAudits       usageQuota      `json:"ai_visibility_audits"`
	Projects                 usageQuota      `json:"projects"`
	MaxCompetitorsPerProject int32           `json:"max_competitors_per_project"`
	MapsCredits              mapsCreditUsage `json:"maps_credits"`
}

// handleMeUsage returns the quota usage of the caller's active workspace.
// Any member may read it; the workspace is resolved exactly like handleMe.
func (a *App) handleMeUsage(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	session, ok := internalauth.SessionFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	activeOrgID := resolveActiveOrganizationID(session.ActiveOrgID, principal.Organizations)
	if !activeOrgID.Valid {
		writeJSONError(w, http.StatusNotFound, "no workspace")
		return
	}

	ctx := r.Context()
	features, err := a.Queries.GetOrganizationFeatures(ctx, activeOrgID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	usage, err := a.Queries.GetAIWorkspaceMonthlyUsage(ctx, activeOrgID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	projectCount, err := a.Queries.CountProjectsForOrganization(ctx, activeOrgID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	budget, err := a.Queries.GetOrganizationMapsCreditBudget(ctx, activeOrgID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	now := time.Now().UTC()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)

	writeJSON(w, http.StatusOK, meUsageResponse{
		OrganizationID: activeOrgID.String(),
		PeriodStart:    periodStart.Format("2006-01-02"),
		PeriodEnd:      periodEnd.Format("2006-01-02"),
		AIMessages: usageQuota{
			Used:  int64(usage.UsedMessages),
			Limit: features.AiMonthlyMessageLimit,
		},
		AIVisibilityAudits: usageQuota{
			Used:  int64(usage.UsedAudits),
			Limit: features.AiVisibilityAuditMonthlyLimit,
		},
		Projects: usageQuota{
			Used:  projectCount,
			Limit: features.MaxProjects,
		},
		MaxCompetitorsPerProject: features.MaxCompetitors,
		MapsCredits: mapsCreditUsage{
			Remaining: budget.RemainingCredits,
			Reserved:  budget.ReservedCredits,
			Spent:     budget.SpentCredits,
		},
	})
}
