package app

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	internalauth "github.com/ps-wizard/revserp/internal/auth"
)

// Router builds the HTTP router.
func (a *App) Router() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   a.Config.CORSAllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/health", a.handleHealth)
	r.Post("/auth/signup", a.handleSignUp)
	r.Post("/auth/login", a.handleLogin)
	r.Post("/auth/oauth/exchange", a.handleOAuthExchange)
	r.Post("/auth/logout", a.handleLogout)
	r.Get("/auth/google/callback", a.handleGoogleOAuthCallback)
	r.Get("/invites/{token}", a.handleGetInvite)

	r.Group(func(protected chi.Router) {
		protected.Use(internalauth.RequireSession(a.SessionManager))
		protected.Get("/me", a.handleMe)
		protected.Post("/me/active-organization", a.handleSetActiveOrganization)
		protected.Get("/internal/scoring-config", a.handleGetScoringConfig)
		protected.Put("/internal/scoring-config", a.handlePutScoringConfig)
		protected.Post("/internal/scoring-config/preview", a.handlePreviewScoringConfig)
		protected.Post("/organizations/{organizationID}/leave", a.handleLeaveOrganization)
		protected.Post("/organizations/{organizationID}/projects", a.handleCreateProject)
		protected.Get("/organizations/{organizationID}/projects", a.handleListProjects)
		protected.Post("/organizations/{organizationID}/invites", a.handleCreateOrganizationInvite)
		protected.Get("/organizations/{organizationID}/invites", a.handleListOrganizationInvites)
		protected.Post("/organizations/{organizationID}/invites/{inviteID}/revoke", a.handleRevokeOrganizationInvite)
		protected.Get("/projects/{projectID}", a.handleGetProject)
		protected.Delete("/projects/{projectID}", a.handleDeleteProject)
		protected.Get("/projects/{projectID}/business-profile", a.handleProjectBusinessProfile)
		protected.Put("/projects/{projectID}/business-profile", a.handleUpsertProjectBusinessProfile)
		protected.Post("/projects/{projectID}/ai-audits", a.handleCreateAIAudit)
		protected.Get("/projects/{projectID}/ai-audits", a.handleListAIAudits)
		protected.Get("/ai-audits/{auditID}", a.handleGetAIAudit)
		protected.Post("/projects/{projectID}/gsc/connect/start", a.handleStartProjectGSCConnect)
		protected.Get("/projects/{projectID}/gsc/status", a.handleProjectGSCStatus)
		protected.Post("/projects/{projectID}/gsc/select-site", a.handleSelectProjectGSCSite)
		protected.Post("/projects/{projectID}/gsc/disconnect", a.handleDisconnectProjectGSC)
		protected.Get("/projects/{projectID}/gsc/overview", a.handleProjectGSCOverview)
		protected.Post("/projects/{projectID}/crawls", a.handleCreateCrawl)
		protected.Get("/projects/{projectID}/crawls", a.handleListCrawls)
		protected.Get("/projects/{projectID}/bucket-trends", a.handleGetProjectBucketTrends)
		protected.Get("/crawls/{crawlID}", a.handleGetCrawl)
		protected.Delete("/crawls/{crawlID}", a.handleDeleteCrawl)
		protected.Get("/crawls/{crawlID}/score-breakdown", a.handleGetCrawlScoreBreakdown)
		protected.Get("/crawls/{crawlID}/score-breakdown/export.csv", a.handleExportCrawlScoreBreakdownCSV)
		protected.Get("/crawls/{crawlID}/score-breakdown/export.xlsx", a.handleExportCrawlScoreBreakdownXLSX)
		protected.Get("/crawls/{crawlID}/score-breakdown/{pillar}/{bucket}/{issueType}/urls", a.handleListScoreBreakdownIssueURLs)
		protected.Get("/crawls/{baselineCrawlID}/compare/{currentCrawlID}/score-breakdown", a.handleGetCrawlScoreBreakdownCompare)
		protected.Get("/crawls/{baselineCrawlID}/compare/{currentCrawlID}/score-breakdown/{pillar}/{bucket}/{issueType}/urls", a.handleListScoreBreakdownCompareIssueURLs)
		protected.Post("/crawls/{crawlID}/ai/fix", a.handleAIFix)
		protected.Post("/crawls/{crawlID}/pages", a.handleCreateCrawlPage)
		protected.Get("/crawls/{crawlID}/pages", a.handleListCrawlPages)
		protected.Get("/crawl-pages/{pageID}", a.handleGetCrawlPage)
		protected.Post("/crawls/{crawlID}/links", a.handleCreateCrawlLink)
		protected.Get("/crawls/{crawlID}/links", a.handleListCrawlLinks)
		protected.Get("/crawl-links/{linkID}", a.handleGetCrawlLink)
		protected.Post("/crawls/{crawlID}/issues", a.handleCreateCrawlIssue)
		protected.Get("/crawls/{crawlID}/issues", a.handleListCrawlIssues)
		protected.Get("/crawl-issues/{issueID}", a.handleGetCrawlIssue)
		protected.Post("/invites/{token}/accept", a.handleAcceptInvite)
	})

	return r
}
