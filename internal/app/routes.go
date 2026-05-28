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
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		MaxAge:         300,
	}))

	r.Get("/health", a.handleHealth)

	r.Group(func(protected chi.Router) {
		protected.Use(internalauth.RequireAuth(a.AuthVerifier))
		protected.Get("/me", a.handleMe)
		protected.Post("/organizations/{organizationID}/projects", a.handleCreateProject)
		protected.Get("/organizations/{organizationID}/projects", a.handleListProjects)
		protected.Get("/projects/{projectID}", a.handleGetProject)
		protected.Post("/projects/{projectID}/crawls", a.handleCreateCrawl)
		protected.Get("/projects/{projectID}/crawls", a.handleListCrawls)
		protected.Get("/crawls/{crawlID}", a.handleGetCrawl)
	})

	return r
}
