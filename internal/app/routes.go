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
	r.Use(middleware.Compress(5))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   a.Config.CORSAllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token", "Last-Event-ID"},
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
	r.Post("/agent/setup", a.handleRedeemAgentSetup)

	r.Group(func(protected chi.Router) {
		protected.Use(internalauth.RequireSession(a.SessionManager))
		protected.Use(a.requireActiveUser)
		protected.Post("/auth/session/renew", a.handleRenewSession)
		protected.Group(func(app chi.Router) {
			app.Use(a.requirePrincipal)
			app.Get("/me", a.handleMe)
			app.Get("/app-bootstrap", a.handleAppBootstrap)
			app.Post("/me/active-organization", a.handleSetActiveOrganization)
			app.Get("/api-keys", a.handleListAPIKeys)
			app.Post("/api-keys/{apiKeyID}/revoke", a.handleRevokeAPIKey)
			app.Post("/agent/setup-codes", a.handleCreateAgentSetupCode)
			app.Get("/internal/scoring-config", a.platformAdminOnly(a.handleGetScoringConfig))
			app.Put("/internal/scoring-config", a.platformAdminOnly(a.handlePutScoringConfig))
			app.Post("/internal/scoring-config/preview", a.platformAdminOnly(a.handlePreviewScoringConfig))
			app.Post("/organizations/{organizationID}/leave", a.handleLeaveOrganization)
			app.Post("/organizations/{organizationID}/projects", a.handleCreateProject)
			app.Get("/organizations/{organizationID}/projects", a.handleListProjects)
			app.Get("/organizations/{organizationID}/crawls/active", a.handleListActiveOrganizationCrawls)
			app.Post("/organizations/{organizationID}/invites", a.handleCreateOrganizationInvite)
			app.Get("/organizations/{organizationID}/invites", a.handleListOrganizationInvites)
			app.Post("/organizations/{organizationID}/invites/{inviteID}/revoke", a.handleRevokeOrganizationInvite)
			app.Get("/projects/{projectID}", a.handleGetProject)
			app.Delete("/projects/{projectID}", a.handleDeleteProject)
			app.Get("/projects/{projectID}/business-profile", a.handleProjectBusinessProfile)
			app.Put("/projects/{projectID}/business-profile", a.handleUpsertProjectBusinessProfile)
			app.Get("/projects/{projectID}/ai-questions", a.handleGetProjectAIQuestions)
			app.Post("/projects/{projectID}/ai-audits", a.handleCreateAIAudit)
			app.Get("/projects/{projectID}/ai-audits", a.handleListAIAudits)
			app.Get("/ai-audits/{auditID}", a.handleGetAIAudit)

			app.Group(func(gated chi.Router) {
				gated.Use(a.requireFeature(FeatureAIChat, featuresByProjectParam))
				gated.Post("/projects/{projectID}/ai/conversations", a.handleCreateAIConversation)
				gated.Get("/projects/{projectID}/ai/conversations", a.handleListAIConversations)
			})
			app.Group(func(gated chi.Router) {
				gated.Use(a.requireFeature(FeatureAIChat, featuresByConversationParam))
				gated.Get("/ai/conversations/{conversationID}", a.handleGetAIConversation)
				gated.Delete("/ai/conversations/{conversationID}", a.handleDeleteAIConversation)
				gated.Post("/ai/conversations/{conversationID}/turns", a.handleSubmitAITurn)
			})
			app.Get("/ai/turns/{turnID}", a.handleGetAITurn)
			app.Post("/ai/turns/{turnID}/cancel", a.handleCancelAITurn)
			app.Get("/ai/turns/{turnID}/events", a.handleGetAITurnEvents)

			// Feature-gated route groups. Each group resolves the governing workspace
			// once in middleware and rejects before reaching a handler, so hiding a
			// surface in the UI is never the only thing standing in the way.
			app.Group(func(gated chi.Router) {
				gated.Use(a.requireFeature(FeatureAutoCrawl, featuresByProjectParam))
				gated.Get("/projects/{projectID}/auto-crawl", a.handleGetAutoCrawlSettings)
				gated.Put("/projects/{projectID}/auto-crawl", a.handlePutAutoCrawlSettings)
			})

			app.Group(func(gated chi.Router) {
				gated.Use(a.requireFeature(FeatureGSCConnector, featuresByProjectParam))
				gated.Post("/projects/{projectID}/gsc/connect/start", a.handleStartProjectGSCConnect)
				gated.Get("/projects/{projectID}/gsc/status", a.handleProjectGSCStatus)
				gated.Post("/projects/{projectID}/gsc/select-site", a.handleSelectProjectGSCSite)
				gated.Post("/projects/{projectID}/gsc/disconnect", a.handleDisconnectProjectGSC)
				gated.Get("/projects/{projectID}/gsc/overview", a.handleProjectGSCOverview)
				gated.Get("/projects/{projectID}/gsc/queries", a.handleProjectGSCQueries)
			})

			app.Post("/projects/{projectID}/crawls", a.handleCreateCrawl)
			app.Get("/projects/{projectID}/crawls", a.handleListCrawls)
			app.Get("/projects/{projectID}/bucket-trends", a.handleGetProjectBucketTrends)
			app.Get("/projects/{projectID}/score-potential", a.handleGetProjectScorePotential)
			app.Get("/crawls/{crawlID}", a.handleGetCrawl)
			app.Delete("/crawls/{crawlID}", a.handleDeleteCrawl)
			app.Post("/crawls/{crawlID}/cancel", a.handleCancelCrawl)
			app.Get("/crawls/{crawlID}/score-breakdown", a.handleGetCrawlScoreBreakdown)
			app.Get("/crawls/{crawlID}/page-health", a.handleGetCrawlPageHealth)
			app.Get("/crawls/{crawlID}/commentary", a.handleGetCrawlCommentary)
			app.Get("/crawls/{crawlID}/score-breakdown/export.csv", a.handleExportCrawlScoreBreakdownCSV)
			app.Get("/crawls/{crawlID}/score-breakdown/export.xlsx", a.handleExportCrawlScoreBreakdownXLSX)
			app.Get("/crawls/{crawlID}/score-breakdown/{pillar}/{bucket}/{issueType}/urls", a.handleListScoreBreakdownIssueURLs)
			app.Get("/crawls/{baselineCrawlID}/compare/{currentCrawlID}/score-breakdown", a.handleGetCrawlScoreBreakdownCompare)
			app.Get("/crawls/{baselineCrawlID}/compare/{currentCrawlID}/score-breakdown/{pillar}/{bucket}/{issueType}/urls", a.handleListScoreBreakdownCompareIssueURLs)
			app.Get("/crawls/{crawlID}/issue-workspace/summary", a.handleGetIssueWorkspaceSummary)
			app.Get("/crawls/{crawlID}/issue-workspace/changes", a.handleListIssueWorkspaceChanges)
			app.Get("/crawls/{crawlID}/issue-workspace/pages/search", a.handleSearchIssueWorkspacePages)
			app.Get("/crawls/{crawlID}/issue-workspace/page", a.handleGetIssueWorkspacePage)
			app.Post("/crawls/{crawlID}/pages", a.handleCreateCrawlPage)
			app.Get("/crawls/{crawlID}/pages", a.handleListCrawlPages)
			app.Get("/crawls/{crawlID}/pages/search", a.handleSearchCrawlPages)
			app.Get("/crawls/{crawlID}/pages/by-url", a.handleGetCrawlPageByURL)
			app.Get("/crawls/{crawlID}/pages/{pageID}/health", a.handleGetCrawlPageHealthDetail)
			app.Get("/crawl-pages/{pageID}", a.handleGetCrawlPage)
			app.Post("/crawls/{crawlID}/links", a.handleCreateCrawlLink)
			app.Get("/crawls/{crawlID}/links", a.handleListCrawlLinks)
			app.Get("/crawls/{crawlID}/site-graph", a.handleGetCrawlSiteGraph)
			app.Get("/crawl-links/{linkID}", a.handleGetCrawlLink)
			app.Post("/crawls/{crawlID}/issues", a.handleCreateCrawlIssue)
			app.Get("/crawls/{crawlID}/issues", a.handleListCrawlIssues)
			app.Get("/crawl-issues/{issueID}", a.handleGetCrawlIssue)
			app.Post("/crawl-issues/{issueID}/work-done", a.handleMarkCrawlIssueWorkDone)
			app.Delete("/issue-work-attempts/{attemptID}/contributors/me", a.handleRemoveOwnIssueWorkContribution)
			app.Get("/projects/{projectID}/work-report", a.handleGetProjectWorkReport)
			app.Post("/invites/{token}/accept", a.handleAcceptInvite)

			// Admin routes — platform admin only
			app.Group(func(admin chi.Router) {
				admin.Use(a.requirePlatformAdmin)
				admin.Get("/admin/users", a.handleAdminListUsers)
				admin.Post("/admin/users/{userID}/make-admin", a.handleAdminMakeAdmin)
				admin.Post("/admin/users/{userID}/remove-admin", a.handleAdminRemoveAdmin)
				admin.Post("/admin/users/{userID}/suspend", a.handleAdminSuspendUser)
				admin.Post("/admin/users/{userID}/unsuspend", a.handleAdminUnsuspendUser)
				admin.Delete("/admin/users/{userID}", a.handleAdminDeleteUser)
				admin.Get("/admin/organizations", a.handleAdminListOrganizations)
				admin.Get("/admin/features", a.handleAdminListFeatures)
				admin.Put("/admin/features", a.handleAdminPutFeatures)
				admin.Get("/admin/ai-config", a.handleAdminGetAIConfig)
				admin.Put("/admin/ai-config", a.handleAdminPutAIConfig)
				admin.Post("/admin/ai-config/reset", a.handleAdminResetAIConfig)
				admin.Post("/admin/scoring-config/preview", a.handleAdminPreviewScoringConfig)
				admin.Get("/admin/organizations/{orgID}/projects", a.handleAdminListOrgProjects)
				admin.Get("/admin/projects/{projectID}/crawls", a.handleAdminListProjectCrawls)
				admin.Get("/admin/crawls/{crawlID}/score-breakdown", a.handleAdminGetCrawlScoreBreakdown)
			})
		})
	})

	r.Route("/v1", func(v1 chi.Router) {
		v1.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					w.Header().Set("Allow", http.MethodGet)
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				next.ServeHTTP(w, r)
			})
		})
		v1.Use(internalauth.RequireAPIKey(a.APIKeyManager))
		v1.Use(a.requireActiveUser)
		v1.Use(a.requirePrincipal)
		v1.Get("/me", a.handleV1Me)
		v1.Get("/organizations/{organizationID}/projects", a.handleListProjects)
		v1.Get("/projects/{projectID}", a.handleGetProject)
		v1.Get("/projects/{projectID}/crawls", a.handleListCrawls)
		v1.Get("/projects/{projectID}/bucket-trends", a.handleGetProjectBucketTrends)
		v1.Get("/projects/{projectID}/score-potential", a.handleGetProjectScorePotential)
		v1.Get("/crawls/{crawlID}", a.handleGetCrawl)
		v1.Get("/crawls/{crawlID}/score-breakdown", a.handleGetCrawlScoreBreakdown)
		v1.Get("/crawls/{crawlID}/page-health", a.handleGetCrawlPageHealth)
		v1.Get("/crawls/{crawlID}/pages", a.handleListCrawlPages)
		v1.Get("/crawls/{crawlID}/issues", a.handleListCrawlIssues)
		v1.Get("/crawls/{crawlID}/links", a.handleListCrawlLinks)
		v1.Get("/crawls/{crawlID}/site-graph", a.handleGetCrawlSiteGraph)
		v1.Get("/crawl-pages/{pageID}", a.handleGetCrawlPage)
		v1.Get("/crawl-issues/{issueID}", a.handleGetCrawlIssue)
		v1.Get("/crawl-links/{linkID}", a.handleGetCrawlLink)
	})

	return r
}
