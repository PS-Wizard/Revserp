package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type createCrawlPageRequest struct {
	URL                     string          `json:"url"`
	StatusCode              *int32          `json:"status_code"`
	ContentType             string          `json:"content_type"`
	SizeBytes               *int32          `json:"size_bytes"`
	IsInternal              *bool           `json:"is_internal"`
	Depth                   *int32          `json:"depth"`
	Title                   string          `json:"title"`
	MetaDescription         string          `json:"meta_description"`
	H1                      string          `json:"h1"`
	H1Count                 *int32          `json:"h1_count"`
	H2Count                 *int32          `json:"h2_count"`
	H3Count                 *int32          `json:"h3_count"`
	WordCount               *int32          `json:"word_count"`
	VisibleText             string          `json:"visible_text"`
	Author                  string          `json:"author"`
	CanonicalURL            string          `json:"canonical_url"`
	Lang                    string          `json:"lang"`
	Viewport                string          `json:"viewport"`
	Robots                  string          `json:"robots"`
	ImageCount              *int32          `json:"image_count"`
	ImagesWithoutAltCount   *int32          `json:"images_without_alt_count"`
	ImagesWithoutDimensions *int32          `json:"images_without_dimensions"`
	ExternalLinks           *int32          `json:"external_links"`
	InternalLinks           *int32          `json:"internal_links"`
	ResponseTimeMs          *int32          `json:"response_time_ms"`
	JavaScriptRendered      *bool           `json:"javascript_rendered"`
	H2Headings              json.RawMessage `json:"h2_headings"`
	H3Headings              json.RawMessage `json:"h3_headings"`
	HeadingOutline          json.RawMessage `json:"heading_outline"`
	OGTags                  json.RawMessage `json:"og_tags"`
	JSONLD                  json.RawMessage `json:"json_ld"`
}

type crawlPageResponse struct {
	ID                      string          `json:"id"`
	CrawlID                 string          `json:"crawl_id"`
	URL                     string          `json:"url"`
	StatusCode              *int32          `json:"status_code,omitempty"`
	ContentType             string          `json:"content_type,omitempty"`
	SizeBytes               *int32          `json:"size_bytes,omitempty"`
	IsInternal              *bool           `json:"is_internal,omitempty"`
	Depth                   *int32          `json:"depth,omitempty"`
	Title                   string          `json:"title,omitempty"`
	MetaDescription         string          `json:"meta_description,omitempty"`
	H1                      string          `json:"h1,omitempty"`
	H1Count                 *int32          `json:"h1_count,omitempty"`
	H2Count                 *int32          `json:"h2_count,omitempty"`
	H3Count                 *int32          `json:"h3_count,omitempty"`
	WordCount               *int32          `json:"word_count,omitempty"`
	VisibleText             string          `json:"visible_text,omitempty"`
	Author                  string          `json:"author,omitempty"`
	CanonicalURL            string          `json:"canonical_url,omitempty"`
	Lang                    string          `json:"lang,omitempty"`
	Viewport                string          `json:"viewport,omitempty"`
	Robots                  string          `json:"robots,omitempty"`
	ImageCount              *int32          `json:"image_count,omitempty"`
	ImagesWithoutAltCount   *int32          `json:"images_without_alt_count,omitempty"`
	ImagesWithoutDimensions *int32          `json:"images_without_dimensions,omitempty"`
	ExternalLinks           *int32          `json:"external_links,omitempty"`
	InternalLinks           *int32          `json:"internal_links,omitempty"`
	ResponseTimeMs          *int32          `json:"response_time_ms,omitempty"`
	JavaScriptRendered      *bool           `json:"javascript_rendered,omitempty"`
	H2Headings              json.RawMessage `json:"h2_headings,omitempty"`
	H3Headings              json.RawMessage `json:"h3_headings,omitempty"`
	HeadingOutline          json.RawMessage `json:"heading_outline,omitempty"`
	OGTags                  json.RawMessage `json:"og_tags,omitempty"`
	JSONLD                  json.RawMessage `json:"json_ld,omitempty"`
	ContentBlocks           json.RawMessage `json:"content_blocks,omitempty"`
	CreatedAt               string          `json:"created_at"`
}

// crawlPageRowData collects crawl page row fields for response building.
type crawlPageRowData struct {
	ID                      pgtype.UUID
	CrawlID                 pgtype.UUID
	Url                     string
	StatusCode              pgtype.Int4
	ContentType             pgtype.Text
	SizeBytes               pgtype.Int4
	IsInternal              pgtype.Bool
	Depth                   pgtype.Int4
	Title                   pgtype.Text
	MetaDescription         pgtype.Text
	H1                      pgtype.Text
	H1Count                 pgtype.Int4
	H2Count                 pgtype.Int4
	H3Count                 pgtype.Int4
	WordCount               pgtype.Int4
	VisibleText             pgtype.Text
	Author                  pgtype.Text
	CanonicalUrl            pgtype.Text
	Lang                    pgtype.Text
	Viewport                pgtype.Text
	Robots                  pgtype.Text
	ImageCount              pgtype.Int4
	ImagesWithoutAltCount   pgtype.Int4
	ImagesWithoutDimensions pgtype.Int4
	ExternalLinks           pgtype.Int4
	InternalLinks           pgtype.Int4
	ResponseTimeMs          pgtype.Int4
	JavascriptRendered      pgtype.Bool
	H2Headings              []byte
	H3Headings              []byte
	HeadingOutline          []byte
	OgTags                  []byte
	JsonLd                  []byte
	ContentBlocks           []byte
	CreatedAt               pgtype.Timestamptz
}

// handleCreateCrawlPage creates a page row for a crawl the user can access.
func (a *App) handleCreateCrawlPage(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	var requestBody createCrawlPageRequest
	if err := readJSON(r, &requestBody); err != nil && !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, "invalid json")
		return
	}

	url := strings.TrimSpace(requestBody.URL)
	if url == "" {
		writeJSONError(w, http.StatusBadRequest, "url is required")
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

	if _, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	page, err := queries.CreateCrawlPage(r.Context(), sqlc.CreateCrawlPageParams{
		CrawlID:                 crawlID,
		Url:                     url,
		StatusCode:              nullableInt4(requestBody.StatusCode),
		ContentType:             nullableText(requestBody.ContentType),
		SizeBytes:               nullableInt4(requestBody.SizeBytes),
		IsInternal:              nullableBool(requestBody.IsInternal),
		Depth:                   nullableInt4(requestBody.Depth),
		Title:                   nullableText(requestBody.Title),
		MetaDescription:         nullableText(requestBody.MetaDescription),
		H1:                      nullableText(requestBody.H1),
		H1Count:                 nullableInt4(requestBody.H1Count),
		H2Count:                 nullableInt4(requestBody.H2Count),
		H3Count:                 nullableInt4(requestBody.H3Count),
		WordCount:               nullableInt4(requestBody.WordCount),
		VisibleText:             nullableText(requestBody.VisibleText),
		Author:                  nullableText(requestBody.Author),
		CanonicalUrl:            nullableText(requestBody.CanonicalURL),
		Lang:                    nullableText(requestBody.Lang),
		Viewport:                nullableText(requestBody.Viewport),
		Robots:                  nullableText(requestBody.Robots),
		ImageCount:              nullableInt4(requestBody.ImageCount),
		ImagesWithoutAltCount:   nullableInt4(requestBody.ImagesWithoutAltCount),
		ImagesWithoutDimensions: nullableInt4(requestBody.ImagesWithoutDimensions),
		ExternalLinks:           nullableInt4(requestBody.ExternalLinks),
		InternalLinks:           nullableInt4(requestBody.InternalLinks),
		ResponseTimeMs:          nullableInt4(requestBody.ResponseTimeMs),
		JavascriptRendered:      nullableBool(requestBody.JavaScriptRendered),
		H2Headings:              nullableJSON(requestBody.H2Headings),
		H3Headings:              nullableJSON(requestBody.H3Headings),
		HeadingOutline:          nullableJSON(requestBody.HeadingOutline),
		OgTags:                  nullableJSON(requestBody.OGTags),
		JsonLd:                  nullableJSON(requestBody.JSONLD),
	})
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			writeJSONError(w, http.StatusConflict, "crawl page already exists")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, newCrawlPageResponseFromCreateRow(page))
}

// handleListCrawlPages lists page rows for a crawl the user can access.
func (a *App) handleListCrawlPages(w http.ResponseWriter, r *http.Request) {
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
	user, _, err := a.ensureCurrentUser(r, queries)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if _, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: user.ID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	total, err := queries.CountCrawlPagesForCrawlByUser(r.Context(), sqlc.CountCrawlPagesForCrawlByUserParams{CrawlID: crawlID, UserID: user.ID})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	pages, err := queries.ListCrawlPagesForCrawlByUser(r.Context(), sqlc.ListCrawlPagesForCrawlByUserParams{
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

	responses := make([]crawlPageResponse, 0, len(pages))
	for _, page := range pages {
		responses = append(responses, newCrawlPageResponseFromListRow(page))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pages": responses,
		"pagination": paginationResponse{
			Limit:  limit,
			Offset: offset,
			Count:  int32(len(responses)),
			Total:  total,
		},
	})
}

// handleGetCrawlPage returns a page row only if the current user belongs to the owning organization.
func (a *App) handleGetCrawlPage(w http.ResponseWriter, r *http.Request) {
	pageID, err := parseUUIDParam(chi.URLParam(r, "pageID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid page id")
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

	page, err := queries.GetCrawlPageByIDForUser(r.Context(), sqlc.GetCrawlPageByIDForUserParams{ID: pageID, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl page not found")
			return
		}

		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, newCrawlPageResponseFromGetRow(page))
}

// handleGetCrawlPageByURL returns a page by crawl and URL for the editor.
func (a *App) handleGetCrawlPageByURL(w http.ResponseWriter, r *http.Request) {
	crawlID, err := parseUUIDParam(chi.URLParam(r, "crawlID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}
	pageURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if pageURL == "" {
		writeJSONError(w, http.StatusBadRequest, "url is required")
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
	page, err := queries.GetCrawlPageByURLForUser(r.Context(), sqlc.GetCrawlPageByURLForUserParams{CrawlID: crawlID, Url: pageURL, UserID: user.ID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "crawl page not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	writeJSON(w, http.StatusOK, buildCrawlPageResponse(crawlPageRowData{
		ID: page.ID, CrawlID: page.CrawlID, Url: page.Url, StatusCode: page.StatusCode, ContentType: page.ContentType, SizeBytes: page.SizeBytes, IsInternal: page.IsInternal, Depth: page.Depth, Title: page.Title, MetaDescription: page.MetaDescription, H1: page.H1, H1Count: page.H1Count, H2Count: page.H2Count, H3Count: page.H3Count, WordCount: page.WordCount, VisibleText: page.VisibleText, Author: page.Author, CanonicalUrl: page.CanonicalUrl, Lang: page.Lang, Viewport: page.Viewport, Robots: page.Robots, ImageCount: page.ImageCount, ImagesWithoutAltCount: page.ImagesWithoutAltCount, ImagesWithoutDimensions: page.ImagesWithoutDimensions, ExternalLinks: page.ExternalLinks, InternalLinks: page.InternalLinks, ResponseTimeMs: page.ResponseTimeMs, JavascriptRendered: page.JavascriptRendered, H2Headings: page.H2Headings, H3Headings: page.H3Headings, HeadingOutline: page.HeadingOutline, OgTags: page.OgTags, JsonLd: page.JsonLd, ContentBlocks: page.ContentBlocks, CreatedAt: page.CreatedAt,
	}))
}

// newCrawlPageResponseFromCreateRow converts a created crawl page row into an API response.
func newCrawlPageResponseFromCreateRow(page sqlc.CreateCrawlPageRow) crawlPageResponse {
	return buildCrawlPageResponse(crawlPageRowData{
		ID:                      page.ID,
		CrawlID:                 page.CrawlID,
		Url:                     page.Url,
		StatusCode:              page.StatusCode,
		ContentType:             page.ContentType,
		SizeBytes:               page.SizeBytes,
		IsInternal:              page.IsInternal,
		Depth:                   page.Depth,
		Title:                   page.Title,
		MetaDescription:         page.MetaDescription,
		H1:                      page.H1,
		H1Count:                 page.H1Count,
		H2Count:                 page.H2Count,
		H3Count:                 page.H3Count,
		WordCount:               page.WordCount,
		VisibleText:             page.VisibleText,
		Author:                  page.Author,
		CanonicalUrl:            page.CanonicalUrl,
		Lang:                    page.Lang,
		Viewport:                page.Viewport,
		Robots:                  page.Robots,
		ImageCount:              page.ImageCount,
		ImagesWithoutAltCount:   page.ImagesWithoutAltCount,
		ImagesWithoutDimensions: page.ImagesWithoutDimensions,
		ExternalLinks:           page.ExternalLinks,
		InternalLinks:           page.InternalLinks,
		ResponseTimeMs:          page.ResponseTimeMs,
		JavascriptRendered:      page.JavascriptRendered,
		H2Headings:              page.H2Headings,
		H3Headings:              page.H3Headings,
		HeadingOutline:          page.HeadingOutline,
		OgTags:                  page.OgTags,
		JsonLd:                  page.JsonLd,
		CreatedAt:               page.CreatedAt,
	})
}

// newCrawlPageResponseFromListRow converts a listed crawl page row into an API response.
func newCrawlPageResponseFromListRow(page sqlc.ListCrawlPagesForCrawlByUserRow) crawlPageResponse {
	return buildCrawlPageResponse(crawlPageRowData{
		ID:                      page.ID,
		CrawlID:                 page.CrawlID,
		Url:                     page.Url,
		StatusCode:              page.StatusCode,
		ContentType:             page.ContentType,
		SizeBytes:               page.SizeBytes,
		IsInternal:              page.IsInternal,
		Depth:                   page.Depth,
		Title:                   page.Title,
		MetaDescription:         page.MetaDescription,
		H1:                      page.H1,
		H1Count:                 page.H1Count,
		H2Count:                 page.H2Count,
		H3Count:                 page.H3Count,
		WordCount:               page.WordCount,
		VisibleText:             page.VisibleText,
		Author:                  page.Author,
		CanonicalUrl:            page.CanonicalUrl,
		Lang:                    page.Lang,
		Viewport:                page.Viewport,
		Robots:                  page.Robots,
		ImageCount:              page.ImageCount,
		ImagesWithoutAltCount:   page.ImagesWithoutAltCount,
		ImagesWithoutDimensions: page.ImagesWithoutDimensions,
		ExternalLinks:           page.ExternalLinks,
		InternalLinks:           page.InternalLinks,
		ResponseTimeMs:          page.ResponseTimeMs,
		JavascriptRendered:      page.JavascriptRendered,
		H2Headings:              page.H2Headings,
		H3Headings:              page.H3Headings,
		HeadingOutline:          page.HeadingOutline,
		OgTags:                  page.OgTags,
		JsonLd:                  page.JsonLd,
		ContentBlocks:           page.ContentBlocks,
		CreatedAt:               page.CreatedAt,
	})
}

// newCrawlPageResponseFromGetRow converts a fetched crawl page row into an API response.
func newCrawlPageResponseFromGetRow(page sqlc.GetCrawlPageByIDForUserRow) crawlPageResponse {
	return buildCrawlPageResponse(crawlPageRowData{
		ID:                      page.ID,
		CrawlID:                 page.CrawlID,
		Url:                     page.Url,
		StatusCode:              page.StatusCode,
		ContentType:             page.ContentType,
		SizeBytes:               page.SizeBytes,
		IsInternal:              page.IsInternal,
		Depth:                   page.Depth,
		Title:                   page.Title,
		MetaDescription:         page.MetaDescription,
		H1:                      page.H1,
		H1Count:                 page.H1Count,
		H2Count:                 page.H2Count,
		H3Count:                 page.H3Count,
		WordCount:               page.WordCount,
		VisibleText:             page.VisibleText,
		Author:                  page.Author,
		CanonicalUrl:            page.CanonicalUrl,
		Lang:                    page.Lang,
		Viewport:                page.Viewport,
		Robots:                  page.Robots,
		ImageCount:              page.ImageCount,
		ImagesWithoutAltCount:   page.ImagesWithoutAltCount,
		ImagesWithoutDimensions: page.ImagesWithoutDimensions,
		ExternalLinks:           page.ExternalLinks,
		InternalLinks:           page.InternalLinks,
		ResponseTimeMs:          page.ResponseTimeMs,
		JavascriptRendered:      page.JavascriptRendered,
		H2Headings:              page.H2Headings,
		H3Headings:              page.H3Headings,
		HeadingOutline:          page.HeadingOutline,
		OgTags:                  page.OgTags,
		JsonLd:                  page.JsonLd,
		ContentBlocks:           page.ContentBlocks,
		CreatedAt:               page.CreatedAt,
	})
}

// buildCrawlPageResponse converts crawl page fields into an API response.
func buildCrawlPageResponse(row crawlPageRowData) crawlPageResponse {
	response := crawlPageResponse{
		ID:        row.ID.String(),
		CrawlID:   row.CrawlID.String(),
		URL:       row.Url,
		CreatedAt: formatTimestamp(row.CreatedAt),
	}

	setInt4Pointer := func(value pgtype.Int4) *int32 {
		if !value.Valid {
			return nil
		}
		return &value.Int32
	}
	setBoolPointer := func(value pgtype.Bool) *bool {
		if !value.Valid {
			return nil
		}
		return &value.Bool
	}
	setText := func(value pgtype.Text) string {
		if !value.Valid {
			return ""
		}
		return value.String
	}

	response.StatusCode = setInt4Pointer(row.StatusCode)
	response.ContentType = setText(row.ContentType)
	response.SizeBytes = setInt4Pointer(row.SizeBytes)
	response.IsInternal = setBoolPointer(row.IsInternal)
	response.Depth = setInt4Pointer(row.Depth)
	response.Title = setText(row.Title)
	response.MetaDescription = setText(row.MetaDescription)
	response.H1 = setText(row.H1)
	response.H1Count = setInt4Pointer(row.H1Count)
	response.H2Count = setInt4Pointer(row.H2Count)
	response.H3Count = setInt4Pointer(row.H3Count)
	response.WordCount = setInt4Pointer(row.WordCount)
	response.VisibleText = setText(row.VisibleText)
	response.Author = setText(row.Author)
	response.CanonicalURL = setText(row.CanonicalUrl)
	response.Lang = setText(row.Lang)
	response.Viewport = setText(row.Viewport)
	response.Robots = setText(row.Robots)
	response.ImageCount = setInt4Pointer(row.ImageCount)
	response.ImagesWithoutAltCount = setInt4Pointer(row.ImagesWithoutAltCount)
	response.ImagesWithoutDimensions = setInt4Pointer(row.ImagesWithoutDimensions)
	response.ExternalLinks = setInt4Pointer(row.ExternalLinks)
	response.InternalLinks = setInt4Pointer(row.InternalLinks)
	response.ResponseTimeMs = setInt4Pointer(row.ResponseTimeMs)
	response.JavaScriptRendered = setBoolPointer(row.JavascriptRendered)
	if len(row.H2Headings) > 0 {
		response.H2Headings = json.RawMessage(row.H2Headings)
	}
	if len(row.H3Headings) > 0 {
		response.H3Headings = json.RawMessage(row.H3Headings)
	}
	if len(row.HeadingOutline) > 0 {
		response.HeadingOutline = json.RawMessage(row.HeadingOutline)
	}
	if len(row.OgTags) > 0 {
		response.OGTags = json.RawMessage(row.OgTags)
	}
	if len(row.JsonLd) > 0 {
		response.JSONLD = json.RawMessage(row.JsonLd)
	}
	if len(row.ContentBlocks) > 0 {
		response.ContentBlocks = json.RawMessage(row.ContentBlocks)
	}

	return response
}
