package app

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/competitorcrawls"
	"github.com/ps-wizard/revserp/internal/competitorsetup"
	"github.com/ps-wizard/revserp/internal/crawler"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// handleGetCompetitorInitialization returns one competitor's initialization
// screen: crawl -> keyword_generation -> keyword_analysis. A competitor that
// never initialized reads as ready with every step pending. The GET is
// read-only; the AI worker sweep advances running setups, so progress never
// depends on UI polling.
func (a *App) handleGetCompetitorInitialization(w http.ResponseWriter, r *http.Request) {
	competitor, ok := a.loadInitializationCompetitor(w, r)
	if !ok {
		return
	}

	setup, err := a.Queries.GetCompetitorSetupByCompetitor(r.Context(), competitor.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		setNoStore(w)
		writeJSON(w, http.StatusOK, competitorsetup.MissingView())
		return
	}
	if err != nil {
		serverError(w, r, err)
		return
	}

	setNoStore(w)
	writeJSON(w, http.StatusOK, competitorsetup.RowView(setup))
}

// handleStartCompetitorInitialization starts or resumes one competitor's
// initialization. First start creates the setup and queues the competitor
// child crawl under the resolved parent crawl; later calls reconcile or resume
// at the failed step and never duplicate a crawl or job.
func (a *App) handleStartCompetitorInitialization(w http.ResponseWriter, r *http.Request) {
	competitor, userID, ok := a.loadInitializationCompetitorForStart(w, r)
	if !ok {
		return
	}

	parentCrawlID, err := resolveInitializationParentCrawlID(r, a.Queries, competitor.ProjectID, userID)
	if err != nil {
		var paramErr featureParamError
		if errors.As(err, &paramErr) {
			writeJSONError(w, http.StatusBadRequest, paramErr.Error())
			return
		}
		if err.Error() == "a completed project crawl is required" {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		serverError(w, r, err)
		return
	}

	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		serverError(w, r, err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	queries := a.Queries.WithTx(tx)

	outcome, err := competitorsetup.StartOrResume(r.Context(), queries,
		competitor.OrganizationID, competitor.ProjectID, competitor.ID, userID, parentCrawlID,
		func(ctx context.Context, parent pgtype.UUID) (pgtype.UUID, error) {
			return enqueueInitializationCrawl(ctx, queries, parent, competitor.ID, userID)
		})
	if err != nil {
		if errors.Is(err, competitorsetup.ErrInvalidFailedStep) {
			writeJSONError(w, http.StatusConflict, "competitor initialization cannot be resumed")
			return
		}
		serverError(w, r, err)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		serverError(w, r, err)
		return
	}

	status := http.StatusOK
	if outcome.Started {
		status = http.StatusCreated
	}
	setNoStore(w)
	writeJSON(w, status, competitorsetup.RowView(outcome.Setup))
}

func (a *App) handleRetryCompetitorInitialization(w http.ResponseWriter, r *http.Request) {
	a.handleStartCompetitorInitialization(w, r)
}

func enqueueInitializationCrawl(ctx context.Context, queries *sqlc.Queries, parentCrawlID, competitorID, userID pgtype.UUID) (pgtype.UUID, error) {
	crawlID, err := competitorcrawls.EnqueueOneCompetitorCrawl(ctx, queries, parentCrawlID, competitorID, userID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	if !crawlID.Valid {
		return pgtype.UUID{}, errors.New("a completed project crawl is required")
	}
	return crawlID, nil
}

type initializationCompetitor struct {
	ID             pgtype.UUID
	ProjectID      pgtype.UUID
	OrganizationID pgtype.UUID
}

func (a *App) loadInitializationCompetitor(w http.ResponseWriter, r *http.Request) (initializationCompetitor, bool) {
	projectID, err := parseUUIDParam(chi.URLParam(r, "projectID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid project id")
		return initializationCompetitor{}, false
	}
	competitorID, err := parseUUIDParam(chi.URLParam(r, "competitorID"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid competitor id")
		return initializationCompetitor{}, false
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return initializationCompetitor{}, false
	}

	project, err := a.Queries.GetProjectByIDForUser(r.Context(), sqlc.GetProjectByIDForUserParams{
		ID:     projectID,
		UserID: principal.User.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "project not found")
			return initializationCompetitor{}, false
		}
		serverError(w, r, err)
		return initializationCompetitor{}, false
	}

	if _, err := a.Queries.GetProjectCompetitorByIDForUser(r.Context(), sqlc.GetProjectCompetitorByIDForUserParams{
		CompetitorID: competitorID,
		UserID:       principal.User.ID,
		ProjectID:    projectID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSONError(w, http.StatusNotFound, "competitor not found")
			return initializationCompetitor{}, false
		}
		serverError(w, r, err)
		return initializationCompetitor{}, false
	}

	return initializationCompetitor{ID: competitorID, ProjectID: projectID, OrganizationID: project.OrganizationID}, true
}

func (a *App) loadInitializationCompetitorForStart(w http.ResponseWriter, r *http.Request) (initializationCompetitor, pgtype.UUID, bool) {
	competitor, ok := a.loadInitializationCompetitor(w, r)
	if !ok {
		return initializationCompetitor{}, pgtype.UUID{}, false
	}

	principal, ok := a.getPrincipal(w, r)
	if !ok {
		return initializationCompetitor{}, pgtype.UUID{}, false
	}

	if err := requireOrganizationManager(r.Context(), a.Queries, competitor.OrganizationID, principal.User.ID); err != nil {
		writeInvitePermissionError(w, err)
		return initializationCompetitor{}, pgtype.UUID{}, false
	}

	return competitor, principal.User.ID, true
}

func resolveInitializationParentCrawlID(r *http.Request, queries *sqlc.Queries, projectID, userID pgtype.UUID) (pgtype.UUID, error) {
	parentCrawlID, crawl, err := lookupCompetitorParentCrawl(r, queries, projectID, userID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	if parentCrawlID.Valid {
		if crawl.Status != "completed" || !crawler.IsManualLikeSource(crawl.Source) {
			return pgtype.UUID{}, featureParamError("invalid crawl id")
		}
		return parentCrawlID, nil
	}
	latest, err := queries.GetLatestCompletedHomeCrawlIDForProject(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, errors.New("a completed project crawl is required")
		}
		return pgtype.UUID{}, err
	}
	return latest, nil
}
