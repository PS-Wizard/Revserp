package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type crawlPageHealthDetailResponse struct {
	CrawlID     string `json:"crawl_id"`
	PageID      string `json:"page_id"`
	URL         string `json:"url"`
	HealthScore int16  `json:"health_score"`
}

func (a *App) handleGetCrawlPageHealthDetail(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}
	pageID, err := parseUUIDParam(chi.URLParam(r, "pageID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid page id")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User
	crawl, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}
		serverError(w, r, err)
		return
	}
	row, err := a.Queries.GetCrawlPageHealthForUser(r.Context(), sqlc.GetCrawlPageHealthForUserParams{
		CrawlID: crawlID,
		PageID:  pageID,
		UserID:  user.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "page health score not found")
			return
		}
		serverError(w, r, err)
		return
	}
	if !row.HealthScore.Valid {
		writeJSONError(w, http.StatusNotFound, "page health score not found")
		return
	}
	if isCrawlStatusTerminal(crawl.Status) {
		setImmutableCache(w)
	} else {
		setNoStore(w)
	}
	writeJSON(w, http.StatusOK, crawlPageHealthDetailResponse{
		CrawlID:     crawlID.String(),
		PageID:      pageID.String(),
		URL:         row.Url,
		HealthScore: row.HealthScore.Int16,
	})
}
