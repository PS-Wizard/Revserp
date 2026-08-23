package app

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type createCrawlLinkRequest struct {
	SourceURL    string `json:"source_url"`
	TargetURL    string `json:"target_url"`
	AnchorText   string `json:"anchor_text"`
	IsInternal   *bool  `json:"is_internal"`
	TargetStatus *int32 `json:"target_status"`
	Nofollow     *bool  `json:"nofollow"`
}

type crawlLinkResponse struct {
	ID           string `json:"id"`
	CrawlID      string `json:"crawl_id"`
	SourceURL    string `json:"source_url"`
	TargetURL    string `json:"target_url"`
	AnchorText   string `json:"anchor_text,omitempty"`
	IsInternal   *bool  `json:"is_internal,omitempty"`
	TargetStatus *int32 `json:"target_status,omitempty"`
	Nofollow     *bool  `json:"nofollow,omitempty"`
	CreatedAt    string `json:"created_at"`
}

// handleCreateCrawlLink creates a link row for a crawl the user can access.
func (a *App) handleCreateCrawlLink(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	var requestBody createCrawlLinkRequest
	if err := readJSON(r, &requestBody); err != nil && !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	sourceURL := strings.TrimSpace(requestBody.SourceURL)
	targetURL := strings.TrimSpace(requestBody.TargetURL)
	if sourceURL == "" || targetURL == "" {
		writeJSONError(w, http.StatusBadRequest, "source_url and target_url are required")
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
	if _, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	link, err := queries.CreateCrawlLink(r.Context(), sqlc.CreateCrawlLinkParams{
		CrawlID:      crawlID,
		SourceUrl:    sourceURL,
		TargetUrl:    targetURL,
		AnchorText:   nullableText(requestBody.AnchorText),
		IsInternal:   nullableBool(requestBody.IsInternal),
		TargetStatus: nullableInt4(requestBody.TargetStatus),
		Nofollow:     nullableBool(requestBody.Nofollow),
	})
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			writeJSONError(w, http.StatusConflict, "crawl link already exists")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, newCrawlLinkResponse(link))
}

// handleListCrawlLinks lists link rows for a crawl the user can access.
func (a *App) handleListCrawlLinks(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}
	limit, offset, err := parsePaginationParams(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
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
	if _, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	total, err := queries.CountCrawlLinksForCrawlByUser(r.Context(), sqlc.CountCrawlLinksForCrawlByUserParams{CrawlID: crawlID, UserID: user.ID})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	links, err := queries.ListCrawlLinksForCrawlByUser(r.Context(), sqlc.ListCrawlLinksForCrawlByUserParams{
		CrawlID: crawlID,
		UserID:  user.ID,
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	responses := make([]crawlLinkResponse, 0, len(links))
	for _, link := range links {
		responses = append(responses, newCrawlLinkResponse(link))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"links": responses,
		"pagination": paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(responses)),
			Total:  total,
		},
	})
}

// handleGetCrawlLink returns a link row only if the current user belongs to the owning organization.
func (a *App) handleGetCrawlLink(w http.ResponseWriter, r *http.Request) {
	linkID, err := parseUUIDParam(chi.URLParam(r, "linkID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid link id")
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
	link, err := queries.GetCrawlLinkByIDForUser(r.Context(), sqlc.GetCrawlLinkByIDForUserParams{ID: linkID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl link not found")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, newCrawlLinkResponse(link))
}

// newCrawlLinkResponse converts a crawl link row into an API response.
func newCrawlLinkResponse(link sqlc.CrawlLink) crawlLinkResponse {
	response := crawlLinkResponse{
		ID:        link.ID.String(),
		CrawlID:   link.CrawlID.String(),
		SourceURL: link.SourceUrl,
		TargetURL: link.TargetUrl,
		CreatedAt: formatTimestamp(link.CreatedAt),
	}

	if link.AnchorText.Valid {
		response.AnchorText = link.AnchorText.String
	}
	if link.IsInternal.Valid {
		response.IsInternal = &link.IsInternal.Bool
	}
	if link.TargetStatus.Valid {
		response.TargetStatus = &link.TargetStatus.Int32
	}
	if link.Nofollow.Valid {
		response.Nofollow = &link.Nofollow.Bool
	}

	return response
}
