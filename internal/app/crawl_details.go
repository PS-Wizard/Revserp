package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type crawlDetailsStatusCode struct {
	Code  *int32 `json:"code"`
	Count int64  `json:"count"`
}

type crawlDetailsFailedPage struct {
	URL        string  `json:"url"`
	StatusCode *int32  `json:"status_code"`
	Reason     *string `json:"reason"`
}

type crawlDetailsFailedPages struct {
	Total int64                    `json:"total"`
	Items []crawlDetailsFailedPage `json:"items"`
}

type crawlDetailsLinks struct {
	Internal              int64 `json:"internal"`
	External              int64 `json:"external"`
	Nofollow              int64 `json:"nofollow"`
	BrokenInternal        int64 `json:"broken_internal"`
	UniqueExternalDomains int64 `json:"unique_external_domains"`
}

type crawlDetailsResponse struct {
	CrawlID      string                   `json:"crawl_id"`
	StatusCodes  []crawlDetailsStatusCode `json:"status_codes"`
	Soft404Count int64                    `json:"soft_404_count"`
	FailedPages  crawlDetailsFailedPages  `json:"failed_pages"`
	Links        crawlDetailsLinks        `json:"links"`
}

// handleGetCrawlDetails returns the status-code breakdown, failed pages, and
// link totals backing the "Crawl details" dialog. Read-only; access is gated
// on GetCrawlByIDForUser exactly like handleGetCrawl.
func (a *App) handleGetCrawlDetails(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}

	crawl, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: principal.User.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}
		serverError(w, r, err)
		return
	}

	ctx := r.Context()

	statusRows, err := a.Queries.GetCrawlDetailsStatusCodes(ctx, crawlID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	soft404Count, err := a.Queries.CountCrawlDetailsSoft404(ctx, crawlID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	failedTotal, err := a.Queries.CountCrawlDetailsFailedPages(ctx, crawlID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	failedRows, err := a.Queries.ListCrawlDetailsFailedPages(ctx, crawlID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	linkStats, err := a.Queries.GetCrawlDetailsLinkStats(ctx, crawlID)
	if err != nil {
		serverError(w, r, err)
		return
	}

	statusCodes := make([]crawlDetailsStatusCode, 0, len(statusRows))
	for _, row := range statusRows {
		entry := crawlDetailsStatusCode{Count: row.Count}
		if row.StatusCode.Valid {
			code := row.StatusCode.Int32
			entry.Code = &code
		}
		statusCodes = append(statusCodes, entry)
	}

	failedItems := make([]crawlDetailsFailedPage, 0, len(failedRows))
	for _, row := range failedRows {
		item := crawlDetailsFailedPage{URL: row.Url}
		if row.StatusCode.Valid {
			code := row.StatusCode.Int32
			item.StatusCode = &code
		}
		if row.FetchError.Valid {
			reason := row.FetchError.String
			item.Reason = &reason
		}
		failedItems = append(failedItems, item)
	}

	if isCrawlStatusTerminal(crawl.Status) {
		setImmutableCache(w)
	} else {
		setNoStore(w)
	}
	writeJSON(w, http.StatusOK, crawlDetailsResponse{
		CrawlID:      crawlID.String(),
		StatusCodes:  statusCodes,
		Soft404Count: soft404Count,
		FailedPages: crawlDetailsFailedPages{
			Total: failedTotal,
			Items: failedItems,
		},
		Links: crawlDetailsLinks{
			Internal:              linkStats.InternalCount,
			External:              linkStats.ExternalCount,
			Nofollow:              linkStats.NofollowCount,
			BrokenInternal:        linkStats.BrokenInternalCount,
			UniqueExternalDomains: linkStats.UniqueExternalDomains,
		},
	})
}
