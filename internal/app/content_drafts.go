package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// contentDraftResponse is the wire shape for a content draft. ConversationID
// is the draft's exactly-one attached Revbot conversation.
type contentDraftResponse struct {
	ID              string  `json:"id"`
	ProjectID       string  `json:"project_id"`
	ConversationID  *string `json:"conversation_id"`
	Title           string  `json:"title"`
	MetaTitle       string  `json:"meta_title"`
	MetaDescription string  `json:"meta_description"`
	TargetKeyword   string  `json:"target_keyword"`
	Body            string  `json:"body"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

type contentDraftListItem struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	TargetKeyword  string  `json:"target_keyword"`
	ConversationID *string `json:"conversation_id"`
	UpdatedAt      string  `json:"updated_at"`
}

type createContentDraftRequest struct {
	Title *string `json:"title"`
}

type updateContentDraftRequest struct {
	Title           *string `json:"title"`
	MetaTitle       *string `json:"meta_title"`
	MetaDescription *string `json:"meta_description"`
	TargetKeyword   *string `json:"target_keyword"`
	Body            *string `json:"body"`
}

// handleCreateContentDraft creates a draft and its attached conversation in
// one transaction; the conversation insert cascades the membership check, so
// a missing project or non-member yields 404 with nothing written.
func (a *App) handleCreateContentDraft(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	var body createContentDraftRequest
	if !readOptionalStrictJSONOrRespond(w, r, &body) {
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User
	title := strings.TrimSpace(optionalStringValue(body.Title))
	conversationTitle := title
	if conversationTitle == "" {
		conversationTitle = "New draft"
	}

	var draft sqlc.GetContentDraftForUserRow
	committed := a.withTx(w, r, func(queries *sqlc.Queries) error {
		created, err := queries.CreateContentDraftForUser(r.Context(), sqlc.CreateContentDraftForUserParams{
			UserID:    user.ID,
			ProjectID: projectID,
			Title:     optionalTextParam(body.Title),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "project not found")
				return err
			}
			serverError(w, r, err)
			return err
		}
		if _, err := queries.CreateAIConversationForContentDraft(r.Context(), sqlc.CreateAIConversationForContentDraftParams{
			UserID:         user.ID,
			ProjectID:      projectID,
			Title:          conversationTitle,
			ContentDraftID: created.ID,
		}); err != nil {
			serverError(w, r, err)
			return err
		}
		fetched, err := queries.GetContentDraftForUser(r.Context(), sqlc.GetContentDraftForUserParams{
			DraftID: created.ID,
			UserID:  user.ID,
		})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		draft = fetched
		return nil
	})
	if !committed {
		return
	}
	writeJSON(w, http.StatusCreated, newContentDraftResponse(draft))
}

// handleListContentDrafts lists a project's drafts for a member, newest first.
func (a *App) handleListContentDrafts(w http.ResponseWriter, r *http.Request) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	rows, err := a.Queries.ListContentDraftsForProjectForUser(r.Context(), sqlc.ListContentDraftsForProjectForUserParams{
		ProjectID: projectID,
		UserID:    principal.User.ID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}
	items := make([]contentDraftListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, contentDraftListItem{
			ID:             row.ID.String(),
			Title:          row.Title,
			TargetKeyword:  row.TargetKeyword,
			ConversationID: nullableUUIDString(row.ConversationID),
			UpdatedAt:      formatTimestamp(row.UpdatedAt),
		})
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"drafts": items})
}

// handleGetContentDraft returns one draft visible to the caller.
func (a *App) handleGetContentDraft(w http.ResponseWriter, r *http.Request) {
	draftID, err := parseUUIDParam(chi.URLParam(r, "draftID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid draft id")
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	draft, err := a.Queries.GetContentDraftForUser(r.Context(), sqlc.GetContentDraftForUserParams{
		DraftID: draftID,
		UserID:  principal.User.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSONError(w, http.StatusNotFound, "draft not found")
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, newContentDraftResponse(draft))
}

// handleUpdateContentDraft applies a partial draft update. Omitted fields
// keep their values; updated_at always advances.
func (a *App) handleUpdateContentDraft(w http.ResponseWriter, r *http.Request) {
	draftID, err := parseUUIDParam(chi.URLParam(r, "draftID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid draft id")
		return
	}
	var body updateContentDraftRequest
	if !readOptionalStrictJSONOrRespond(w, r, &body) {
		return
	}
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	user := principal.User

	var draft sqlc.GetContentDraftForUserRow
	committed := a.withTx(w, r, func(queries *sqlc.Queries) error {
		if _, err := queries.UpdateContentDraftForUser(r.Context(), sqlc.UpdateContentDraftForUserParams{
			DraftID:         draftID,
			UserID:          user.ID,
			Title:           optionalTextParam(body.Title),
			MetaTitle:       optionalTextParam(body.MetaTitle),
			MetaDescription: optionalTextParam(body.MetaDescription),
			TargetKeyword:   optionalTextParam(body.TargetKeyword),
			Body:            optionalTextParam(body.Body),
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "draft not found")
				return err
			}
			serverError(w, r, err)
			return err
		}
		fetched, err := queries.GetContentDraftForUser(r.Context(), sqlc.GetContentDraftForUserParams{
			DraftID: draftID,
			UserID:  user.ID,
		})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		draft = fetched
		return nil
	})
	if !committed {
		return
	}
	writeJSON(w, http.StatusOK, newContentDraftResponse(draft))
}

// handleDeleteContentDraft hard-deletes a draft; the attached conversation
// cascades via the content_draft_id foreign key.
func (a *App) handleDeleteContentDraft(w http.ResponseWriter, r *http.Request) {
	draftID, err := parseUUIDParam(chi.URLParam(r, "draftID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid draft id")
		return
	}
	if !a.withTx(w, r, func(queries *sqlc.Queries) error {
		principal, ok := a.getPrincipal(w, r)
		if !ok {
			return errors.New("missing principal")
		}
		deletedRows, err := queries.DeleteContentDraftForUser(r.Context(), sqlc.DeleteContentDraftForUserParams{
			DraftID: draftID,
			UserID:  principal.User.ID,
		})
		if err != nil {
			serverError(w, r, err)
			return err
		}
		if deletedRows == 0 {
			writeJSONError(w, http.StatusNotFound, "draft not found")
			return errors.New("draft not found")
		}
		return nil
	}) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func newContentDraftResponse(draft sqlc.GetContentDraftForUserRow) contentDraftResponse {
	return contentDraftResponse{
		ID:              draft.ID.String(),
		ProjectID:       draft.ProjectID.String(),
		ConversationID:  nullableUUIDString(draft.ConversationID),
		Title:           draft.Title,
		MetaTitle:       draft.MetaTitle,
		MetaDescription: draft.MetaDescription,
		TargetKeyword:   draft.TargetKeyword,
		Body:            draft.Body,
		CreatedAt:       formatTimestamp(draft.CreatedAt),
		UpdatedAt:       formatTimestamp(draft.UpdatedAt),
	}
}

// optionalTextParam maps an absent JSON field to NULL (keep the column) and
// a present one to its trimmed value, including an explicit empty string.
func optionalTextParam(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.TrimSpace(*value), Valid: true}
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
