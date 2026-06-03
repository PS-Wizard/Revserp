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
	CreatedAt               string          `json:"created_at"`
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
	defer tx.Rollback(r.Context())

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
	defer tx.Rollback(r.Context())

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
	defer tx.Rollback(r.Context())

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

// newCrawlPageResponseFromCreateRow converts a created crawl page row into an API response.
func newCrawlPageResponseFromCreateRow(page sqlc.CreateCrawlPageRow) crawlPageResponse {
	return buildCrawlPageResponse(
		page.ID, page.CrawlID, page.Url, page.StatusCode, page.ContentType, page.SizeBytes, page.IsInternal,
		page.Depth, page.Title, page.MetaDescription, page.H1, page.H1Count, page.H2Count, page.H3Count,
		page.WordCount, page.VisibleText, page.Author, page.CanonicalUrl, page.Lang, page.Viewport, page.Robots, page.ImageCount,
		page.ImagesWithoutAltCount, page.ImagesWithoutDimensions, page.ExternalLinks, page.InternalLinks,
		page.ResponseTimeMs, page.JavascriptRendered, page.H2Headings, page.H3Headings, page.HeadingOutline,
		page.OgTags, page.JsonLd, page.CreatedAt,
	)
}

// newCrawlPageResponseFromListRow converts a listed crawl page row into an API response.
func newCrawlPageResponseFromListRow(page sqlc.ListCrawlPagesForCrawlByUserRow) crawlPageResponse {
	return buildCrawlPageResponse(
		page.ID, page.CrawlID, page.Url, page.StatusCode, page.ContentType, page.SizeBytes, page.IsInternal,
		page.Depth, page.Title, page.MetaDescription, page.H1, page.H1Count, page.H2Count, page.H3Count,
		page.WordCount, page.VisibleText, page.Author, page.CanonicalUrl, page.Lang, page.Viewport, page.Robots, page.ImageCount,
		page.ImagesWithoutAltCount, page.ImagesWithoutDimensions, page.ExternalLinks, page.InternalLinks,
		page.ResponseTimeMs, page.JavascriptRendered, page.H2Headings, page.H3Headings, page.HeadingOutline,
		page.OgTags, page.JsonLd, page.CreatedAt,
	)
}

// newCrawlPageResponseFromGetRow converts a fetched crawl page row into an API response.
func newCrawlPageResponseFromGetRow(page sqlc.GetCrawlPageByIDForUserRow) crawlPageResponse {
	return buildCrawlPageResponse(
		page.ID, page.CrawlID, page.Url, page.StatusCode, page.ContentType, page.SizeBytes, page.IsInternal,
		page.Depth, page.Title, page.MetaDescription, page.H1, page.H1Count, page.H2Count, page.H3Count,
		page.WordCount, page.VisibleText, page.Author, page.CanonicalUrl, page.Lang, page.Viewport, page.Robots, page.ImageCount,
		page.ImagesWithoutAltCount, page.ImagesWithoutDimensions, page.ExternalLinks, page.InternalLinks,
		page.ResponseTimeMs, page.JavascriptRendered, page.H2Headings, page.H3Headings, page.HeadingOutline,
		page.OgTags, page.JsonLd, page.CreatedAt,
	)
}

// buildCrawlPageResponse converts crawl page fields into an API response.
func buildCrawlPageResponse(
	id pgtype.UUID,
	crawlID pgtype.UUID,
	url string,
	statusCode pgtype.Int4,
	contentType pgtype.Text,
	sizeBytes pgtype.Int4,
	isInternal pgtype.Bool,
	depth pgtype.Int4,
	title pgtype.Text,
	metaDescription pgtype.Text,
	h1 pgtype.Text,
	h1Count pgtype.Int4,
	h2Count pgtype.Int4,
	h3Count pgtype.Int4,
	wordCount pgtype.Int4,
	visibleText pgtype.Text,
	author pgtype.Text,
	canonicalURL pgtype.Text,
	lang pgtype.Text,
	viewport pgtype.Text,
	robots pgtype.Text,
	imageCount pgtype.Int4,
	imagesWithoutAltCount pgtype.Int4,
	imagesWithoutDimensions pgtype.Int4,
	externalLinks pgtype.Int4,
	internalLinks pgtype.Int4,
	responseTimeMs pgtype.Int4,
	javascriptRendered pgtype.Bool,
	h2Headings []byte,
	h3Headings []byte,
	headingOutline []byte,
	ogTags []byte,
	jsonLD []byte,
	createdAt pgtype.Timestamptz,
) crawlPageResponse {
	response := crawlPageResponse{
		ID:        id.String(),
		CrawlID:   crawlID.String(),
		URL:       url,
		CreatedAt: formatTimestamp(createdAt),
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

	response.StatusCode = setInt4Pointer(statusCode)
	response.ContentType = setText(contentType)
	response.SizeBytes = setInt4Pointer(sizeBytes)
	response.IsInternal = setBoolPointer(isInternal)
	response.Depth = setInt4Pointer(depth)
	response.Title = setText(title)
	response.MetaDescription = setText(metaDescription)
	response.H1 = setText(h1)
	response.H1Count = setInt4Pointer(h1Count)
	response.H2Count = setInt4Pointer(h2Count)
	response.H3Count = setInt4Pointer(h3Count)
	response.WordCount = setInt4Pointer(wordCount)
	response.VisibleText = setText(visibleText)
	response.Author = setText(author)
	response.CanonicalURL = setText(canonicalURL)
	response.Lang = setText(lang)
	response.Viewport = setText(viewport)
	response.Robots = setText(robots)
	response.ImageCount = setInt4Pointer(imageCount)
	response.ImagesWithoutAltCount = setInt4Pointer(imagesWithoutAltCount)
	response.ImagesWithoutDimensions = setInt4Pointer(imagesWithoutDimensions)
	response.ExternalLinks = setInt4Pointer(externalLinks)
	response.InternalLinks = setInt4Pointer(internalLinks)
	response.ResponseTimeMs = setInt4Pointer(responseTimeMs)
	response.JavaScriptRendered = setBoolPointer(javascriptRendered)
	if len(h2Headings) > 0 {
		response.H2Headings = json.RawMessage(h2Headings)
	}
	if len(h3Headings) > 0 {
		response.H3Headings = json.RawMessage(h3Headings)
	}
	if len(headingOutline) > 0 {
		response.HeadingOutline = json.RawMessage(headingOutline)
	}
	if len(ogTags) > 0 {
		response.OGTags = json.RawMessage(ogTags)
	}
	if len(jsonLD) > 0 {
		response.JSONLD = json.RawMessage(jsonLD)
	}

	return response
}

// nullableText converts a string into pgtype.Text.
func nullableText(value string) pgtype.Text {
	return pgText(value)
}

// nullableInt4 converts an optional int into pgtype.Int4.
func nullableInt4(value *int32) pgtype.Int4 {
	if value == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *value, Valid: true}
}

// nullableBool converts an optional bool into pgtype.Bool.
func nullableBool(value *bool) pgtype.Bool {
	if value == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *value, Valid: true}
}

// nullableJSON converts optional raw JSON into []byte for jsonb columns.
func nullableJSON(value json.RawMessage) []byte {
	if len(value) == 0 {
		return nil
	}
	return []byte(value)
}
