package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/businessprofile"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	"github.com/ps-wizard/revserp/internal/issues/shared"
	"github.com/ps-wizard/revserp/internal/keywords"
)

type projectKeywordsResponse struct {
	ProjectID string          `json:"project_id"`
	CrawlID   *string         `json:"crawl_id"`
	Seeds     []keywords.Seed `json:"seeds"`
}

// handleProjectKeywords returns the crawl × profile coverage matrix.
// Always recomputed. Never reads GSC. Missing profile or crawl is 200, not 404.
func (a *App) handleProjectKeywords(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	if _, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: principal.User.ID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return
		}
		serverError(w, r, err)
		return
	}

	targetKeywords := []string{}
	primaryLocation := ""
	profile, hasProfile, err := getProjectBusinessProfileByProjectID(r.Context(), a.Queries, projectID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	if hasProfile {
		decoded, decodeErr := businessprofile.DecodeTargetKeywords(profile.TargetKeywords)
		if decodeErr != nil {
			serverError(w, r, decodeErr)
			return
		}
		targetKeywords = decoded
		primaryLocation = textValue(profile.PrimaryLocation)
	}

	var crawlID *string
	pages := make([]keywords.Page, 0)
	latestCrawlID, err := a.Queries.GetLatestCompletedCrawlForProject(r.Context(), projectID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		serverError(w, r, err)
		return
	}
	if err == nil {
		id := latestCrawlID.String()
		crawlID = &id
		pageRows, listErr := a.Queries.ListKeywordCoveragePagesForCrawl(r.Context(), latestCrawlID)
		if listErr != nil {
			serverError(w, r, listErr)
			return
		}
		pages = keywordCoveragePages(pageRows)
	}

	seeds := keywords.Cover(pages, targetKeywords, primaryLocation)
	if seeds == nil {
		seeds = []keywords.Seed{}
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, projectKeywordsResponse{
		ProjectID: projectID.String(),
		CrawlID:   crawlID,
		Seeds:     seeds,
	})
}

func keywordCoveragePages(rows []sqlc.ListKeywordCoveragePagesForCrawlRow) []keywords.Page {
	pages := make([]keywords.Page, 0, len(rows))
	for _, row := range rows {
		if !shared.IsScoreablePage(shared.CrawlPageSignal{
			StatusCode:  row.StatusCode,
			ContentType: row.ContentType,
			Soft404:     row.Soft404,
			FetchError:  row.FetchError,
		}) {
			continue
		}
		pages = append(pages, keywords.Page{
			URL:   row.Url,
			Title: row.Title,
			H1:    row.H1,
		})
	}
	return pages
}
