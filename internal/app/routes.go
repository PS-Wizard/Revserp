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
		AllowedOrigins: []string{
			"http://localhost:3000",
			"http://localhost:5173",
			"http://127.0.0.1:3000",
			"http://127.0.0.1:5173",
		},
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
	r.Get("/invites/{token}", a.handleGetInvite)

	r.Group(func(protected chi.Router) {
		protected.Use(internalauth.RequireSession(a.SessionManager))
		protected.Get("/me", a.handleMe)
		protected.Post("/me/active-organization", a.handleSetActiveOrganization)
		protected.Post("/organizations/{organizationID}/leave", a.handleLeaveOrganization)
		protected.Post("/organizations/{organizationID}/projects", a.handleCreateProject)
		protected.Get("/organizations/{organizationID}/projects", a.handleListProjects)
		protected.Post("/organizations/{organizationID}/invites", a.handleCreateOrganizationInvite)
		protected.Get("/organizations/{organizationID}/invites", a.handleListOrganizationInvites)
		protected.Post("/organizations/{organizationID}/invites/{inviteID}/revoke", a.handleRevokeOrganizationInvite)
		protected.Get("/projects/{projectID}", a.handleGetProject)
		protected.Delete("/projects/{projectID}", a.handleDeleteProject)
		protected.Post("/projects/{projectID}/crawls", a.handleCreateCrawl)
		protected.Get("/projects/{projectID}/crawls", a.handleListCrawls)
		protected.Get("/crawls/{crawlID}", a.handleGetCrawl)
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
