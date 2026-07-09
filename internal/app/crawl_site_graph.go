package app

import (
	"errors"
	"net/http"
	"net/url"
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
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

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

		nodeIndexByURL[normalized] = len(nodes)
		nodes = append(nodes, siteGraphNodeResponse{
			URL:    page.Url,
			Title:  title,
			Status: status,
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
		if node.Status >= 400 {
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
