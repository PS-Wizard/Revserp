package app

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// pageHealthBuckets is the fixed histogram width: 0..19 issues plus a 20+ tail.
// Wide enough that real sites spread across it instead of piling on the cap.
const pageHealthBuckets = 21

type crawlPageHealthResponse struct {
	CrawlID string `json:"crawl_id"`
	// Buckets is always 21 entries: pages carrying 0..19 issues, then 20-or-more.
	Buckets    []int64 `json:"buckets"`
	TotalPages int64   `json:"total_pages"`
}

// handleGetCrawlPageHealth returns how many scoreable pages carry how many
// issues. It powers the compare view's distribution chart; the score breakdown
// only stores per-issue-type totals, not per-page counts.
func (a *App) handleGetCrawlPageHealth(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	user, _, err := a.ensureCurrentUser(r, a.Queries)
	if err != nil {
		serverError(w, r, err)
		return
	}

	crawl, err := a.Queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl not found")
			return
		}
		serverError(w, r, err)
		return
	}

	rows, err := a.Queries.GetCrawlPageIssueHistogramForUser(r.Context(), sqlc.GetCrawlPageIssueHistogramForUserParams{
		CrawlID: crawlID,
		UserID:  user.ID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	buckets := make([]int64, pageHealthBuckets)
	var total int64
	for _, row := range rows {
		index := int(row.IssueCount)
		if index < 0 || index >= pageHealthBuckets {
			continue
		}
		buckets[index] += row.PageCount
		total += row.PageCount
	}

	if isCrawlStatusTerminal(crawl.Status) {
		setImmutableCache(w)
	} else {
		setNoStore(w)
	}
	writeJSON(w, http.StatusOK, crawlPageHealthResponse{
		CrawlID:    crawlID.String(),
		Buckets:    buckets,
		TotalPages: total,
	})
}
