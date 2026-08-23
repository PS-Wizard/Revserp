package app

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type siteGraphNodeResponse struct {
	URL    string `json:"url"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	In     int    `json:"in"`
	Out    int    `json:"out"`
	// Broken is the single flag the graph renders on. A page is broken on a 4xx/5xx
	// status, on being a soft 404 (which answers 200, so status alone misses it),
	// or on having failed to fetch at all (which has no status at all).
	Broken bool   `json:"broken"`
	Reason string `json:"reason,omitempty"`
}

type siteGraphStatsResponse struct {
	Pages  int `json:"pages"`
	Links  int `json:"links"`
	Broken int `json:"broken"`
}

// handleGetCrawlSiteGraph returns the internal-link graph of a crawl the user can access,
// in a compact node/edge form suitable for a force-graph visualization.
func (a *App) handleGetCrawlSiteGraph(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	queries := a.Queries.WithTx(tx)
	principal, ok := a.getPrincipal(w, r)

	if !ok {

		return

	}
	user := principal.User
	crawl, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	pages, err := queries.ListCrawlPagesForCrawl(r.Context(), crawlID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	links, err := queries.ListInternalCrawlLinksForCrawl(r.Context(), crawlID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	nodes := make([]siteGraphNodeResponse, 0, len(pages))
	nodeIndexByURL := make(map[string]int, len(pages))
	for _, page := range pages {
		normalized := normalizeGraphURL(page.Url)
		if _, exists := nodeIndexByURL[normalized]; exists {
			continue
		}

		status := 0
		if page.StatusCode.Valid {
			status = int(page.StatusCode.Int32)
		}
		title := ""
		if page.Title.Valid {
			title = page.Title.String
		}

		fetchError := ""
		if page.FetchError.Valid {
			fetchError = page.FetchError.String
		}
		broken, reason := siteGraphBrokenReason(status, page.Soft404, fetchError)

		nodeIndexByURL[normalized] = len(nodes)
		nodes = append(nodes, siteGraphNodeResponse{
			URL:    page.Url,
			Title:  title,
			Status: status,
			Broken: broken,
			Reason: reason,
		})
	}

	edges := make([][2]int, 0, len(links))
	seenEdges := make(map[[2]int]struct{}, len(links))
	for _, link := range links {
		sourceIndex, sourceOK := nodeIndexByURL[normalizeGraphURL(link.SourceUrl)]
		targetIndex, targetOK := nodeIndexByURL[normalizeGraphURL(link.TargetUrl)]
		if !sourceOK || !targetOK || sourceIndex == targetIndex {
			continue
		}

		edge := [2]int{sourceIndex, targetIndex}
		if _, exists := seenEdges[edge]; exists {
			continue
		}
		seenEdges[edge] = struct{}{}
		edges = append(edges, edge)

		nodes[sourceIndex].Out++
		nodes[targetIndex].In++
	}

	broken := 0
	for _, node := range nodes {
		if node.Broken {
			broken++
		}
	}

	if crawl.Status == string(CrawlStatusCompleted) {
		setImmutableCache(w)
	} else {
		setNoStore(w)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"nodes": nodes,
		"edges": edges,
		"stats": siteGraphStatsResponse{
			Pages:  len(nodes),
			Links:  len(edges),
			Broken: broken,
		},
	})
}

// siteGraphBrokenReason classifies one page's health for the graph, returning a
// short human-readable reason for the broken ones.
func siteGraphBrokenReason(status int, soft404 bool, fetchError string) (bool, string) {
	switch {
	case fetchError != "":
		return true, "Could not be fetched"
	case soft404:
		return true, "Not found (returned " + strconv.Itoa(status) + ")"
	case status >= 500:
		return true, "Server error " + strconv.Itoa(status)
	case status >= 400:
		return true, "Client error " + strconv.Itoa(status)
	}
	return false, ""
}

// normalizeGraphURL normalizes a URL for internal-link graph matching: it
// strips the fragment, lowercases the scheme and host, and trims a trailing
// slash from the path (except for a bare root path). It falls back to a
// best-effort string trim if the URL fails to parse.
func normalizeGraphURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		trimmed := strings.SplitN(raw, "#", 2)[0]
		return strings.TrimSuffix(trimmed, "/")
	}

	parsed.Fragment = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")

	return parsed.String()
}
