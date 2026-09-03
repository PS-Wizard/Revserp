package app

import (
	"net/http"
)

type adminOrganizationResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// handleAdminListOrganizations returns all organizations.
func (a *App) handleAdminListOrganizations(w http.ResponseWriter, r *http.Request) {
	orgs, err := a.Queries.ListAllOrganizations(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}

	items := make([]adminOrganizationResponse, 0, len(orgs))
	for _, org := range orgs {
		items = append(items, adminOrganizationResponse{
			ID:        org.ID.String(),
			Name:      org.Name,
			CreatedAt: org.CreatedAt.Time.Format("2006-01-02T15:04:05Z"),
		})
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"organizations": items})
}
