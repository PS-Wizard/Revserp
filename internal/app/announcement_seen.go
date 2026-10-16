package app

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)


type announcementSeenReceipt struct {
	AnnouncementID string `json:"announcement_id"`
	SeenAt         string `json:"seen_at"`
}

func validAnnouncementID(id string) bool {
	if len(id) == 0 || len(id) > 100 {
		return false
	}
	if !isAnnouncementIDChar(id[0], true) {
		return false
	}
	for i := 1; i < len(id); i++ {
		if !isAnnouncementIDChar(id[i], false) {
			return false
		}
	}
	return true
}

func isAnnouncementIDChar(c byte, first bool) bool {
	if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
		return true
	}
	return !first && (c == '.' || c == '_' || c == '-')
}

func (a *App) handleListAnnouncementSeen(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	rows, err := a.Queries.ListUserAnnouncementSeen(r.Context(), principal.User.ID)
	if err != nil {
		serverError(w, r, err)
		return
	}
	receipts := make([]announcementSeenReceipt, 0, len(rows))
	for _, row := range rows {
		receipts = append(receipts, announcementSeenReceipt{
			AnnouncementID: row.AnnouncementID,
			SeenAt:         formatTimestamptz(row.SeenAt),
		})
	}
	setNoStore(w)
	writeJSON(w, http.StatusOK, map[string]any{"seen_announcements": receipts, "user_created_at": formatTimestamptz(principal.User.CreatedAt)})
}

func (a *App) handleMarkAnnouncementSeen(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return
	}
	announcementID := chi.URLParam(r, "announcementID")
	if !validAnnouncementID(announcementID) {
		writeJSONError(w, http.StatusBadRequest, "invalid announcement id")
		return
	}
	if err := a.Queries.MarkUserAnnouncementSeen(r.Context(), sqlc.MarkUserAnnouncementSeenParams{
		UserID:         principal.User.ID,
		AnnouncementID: announcementID,
	}); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
