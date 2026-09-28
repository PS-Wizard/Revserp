package app

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Bounds for the platform-admin crawl page worker override. PUT rejects
// anything outside this range with 400; values are never silently clamped.
const (
	minCrawlPageWorkerCount = 1
	maxCrawlPageWorkerCount = 100
)

// crawlConfigResponse is the agreed admin crawl-config contract. EnvWorkerCount
// is the boot-time CRAWL_PAGE_WORKER_COUNT; OverrideWorkerCount is the saved DB
// override (null when none); WorkerCount is the effective value new crawls use;
// Source is "admin" when an override is saved, "env" otherwise.
type crawlConfigResponse struct {
	WorkerCount         int    `json:"worker_count"`
	EnvWorkerCount      int    `json:"env_worker_count"`
	OverrideWorkerCount *int   `json:"override_worker_count"`
	Source              string `json:"source"`
}

// newCrawlConfigResponse resolves the effective count: the saved DB override
// wins when present, otherwise the boot-time env value applies.
func newCrawlConfigResponse(envWorkerCount int, overrideWorkerCount *int) crawlConfigResponse {
	effective := envWorkerCount
	source := "env"
	if overrideWorkerCount != nil {
		effective = *overrideWorkerCount
		source = "admin"
	}
	return crawlConfigResponse{
		WorkerCount:         effective,
		EnvWorkerCount:      envWorkerCount,
		OverrideWorkerCount: overrideWorkerCount,
		Source:              source,
	}
}

// readCrawlPageWorkerOverride returns the saved DB override, or nil when no
// override row exists. Other DB errors are returned to the caller.
func (a *App) readCrawlPageWorkerOverride(r *http.Request) (*int, error) {
	row, err := a.Queries.GetCrawlPageWorkerConfig(r.Context())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	override := int(row.WorkerCount)
	return &override, nil
}

// handleAdminGetCrawlConfig returns the effective page worker count. It never
// modifies the database.
func (a *App) handleAdminGetCrawlConfig(w http.ResponseWriter, r *http.Request) {
	override, err := a.readCrawlPageWorkerOverride(r)
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newCrawlConfigResponse(a.Config.CrawlPageWorkerCount, override))
}

type putCrawlConfigRequest struct {
	WorkerCount *int `json:"worker_count"`
}

// handleAdminPutCrawlConfig saves the page worker override. Only an integer in
// 1..100 is accepted; absent, float, or out-of-range values fail with 400.
func (a *App) handleAdminPutCrawlConfig(w http.ResponseWriter, r *http.Request) {
	var req putCrawlConfigRequest
	if !readJSONOrRespond(w, r, &req) {
		return
	}
	if req.WorkerCount == nil {
		writeJSONError(w, http.StatusBadRequest, "worker_count is required")
		return
	}
	if *req.WorkerCount < minCrawlPageWorkerCount || *req.WorkerCount > maxCrawlPageWorkerCount {
		writeJSONError(w, http.StatusBadRequest, "worker_count must be between 1 and 100")
		return
	}

	userID, err := a.currentUserID(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	row, err := a.Queries.UpsertCrawlPageWorkerConfig(r.Context(), sqlc.UpsertCrawlPageWorkerConfigParams{
		WorkerCount:     int32(*req.WorkerCount),
		UpdatedByUserID: userID,
	})
	if err != nil {
		serverError(w, r, err)
		return
	}

	override := int(row.WorkerCount)
	writeJSON(w, http.StatusOK, newCrawlConfigResponse(a.Config.CrawlPageWorkerCount, &override))
}

// handleAdminResetCrawlConfig deletes the saved override so the boot-time env
// value applies again. It accepts an empty or `{}` body.
func (a *App) handleAdminResetCrawlConfig(w http.ResponseWriter, r *http.Request) {
	var ignored struct{}
	if !readOptionalJSONOrRespond(w, r, &ignored) {
		return
	}
	if err := a.Queries.ResetCrawlPageWorkerConfig(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newCrawlConfigResponse(a.Config.CrawlPageWorkerCount, nil))
}
