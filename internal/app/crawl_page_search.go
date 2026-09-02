package app

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type crawlPageSearchItem struct {
	ID    string  `json:"id"`
	URL   string  `json:"url"`
	Title *string `json:"title,omitempty"`
}

type crawlPageSearchResponse struct {
	CrawlID    string                `json:"crawl_id"`
	Query      string                `json:"query"`
	Pages      []crawlPageSearchItem `json:"pages"`
	Pagination paginationResponse    `json:"pagination"`
}

func (a *App) handleSearchCrawlPages(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) > 512 {
		writeJSONError(w, http.StatusBadRequest, "query is too long")
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
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
	total, err := a.Queries.CountCrawlPagesSearchForUser(r.Context(), sqlc.CountCrawlPagesSearchForUserParams{
		CrawlID: crawlID,
		UserID:  user.ID,
		Query:   q,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	rows, err := a.Queries.SearchCrawlPagesForUser(r.Context(), sqlc.SearchCrawlPagesForUserParams{
		CrawlID: crawlID,
		UserID:  user.ID,
		Query:   q,
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	pages := make([]crawlPageSearchItem, 0, len(rows))
	for _, row := range rows {
		pages = append(pages, buildCrawlPageSearchItem(row.ID, row.Url, row.Title))
	}
	if isCrawlStatusTerminal(crawl.Status) {
		setImmutableCache(w)
	} else {
		setNoStore(w)
	}
	writeJSON(w, http.StatusOK, crawlPageSearchResponse{
		CrawlID: crawlID.String(),
		Query:   q,
		Pages:   pages,
		Pagination: paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(pages)),
			Total:  total,
		},
	})
}

func buildCrawlPageSearchItem(id pgtype.UUID, url string, title pgtype.Text) crawlPageSearchItem {
	item := crawlPageSearchItem{ID: id.String(), URL: url}
	if title.Valid {
		t := title.String
		item.Title = &t
	}
	return item
}
