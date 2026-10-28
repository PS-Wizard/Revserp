package worker

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/mail"
)

// maybeNotifyCrawlFinished sends the opt-in finish email for one crawl. It
// runs once per processed crawl from runCrawlGuarded, after the terminal
// status write (including panic recovery), so completed and failed both land
// here while user-cancelled crawls read back 'cancelled' and are skipped.
// Send failures are logged only; notified_at is still claimed exactly once so
// no retry path can ever double-send.
func (w *Worker) maybeNotifyCrawlFinished(ctx context.Context, crawlID pgtype.UUID) {
	mailer := w.cfg.Mailer
	if mailer == nil || !mailer.Configured() {
		return
	}
	// Detached: the crawl context may already be cancelled at this point, and
	// the notification write has to land regardless.
	ctx = context.WithoutCancel(ctx)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	state, err := w.queries.GetCrawlNotifyState(ctx, crawlID)
	if err != nil {
		log.Printf("crawl notify: read state failed: crawl_id=%s error=%v", crawlID.String(), err)
		return
	}
	if state.Status != "completed" && state.Status != "failed" {
		return
	}
	to := strings.TrimSpace(state.NotifyEmail.String)
	if !state.NotifyEmail.Valid || to == "" {
		return
	}
	if state.NotifiedAt.Valid {
		return
	}
	origin := strings.TrimSuffix(strings.TrimSpace(state.NotifyOrigin.String), "/")
	if origin == "" {
		origin = strings.TrimSuffix(strings.TrimSpace(w.cfg.FrontendURL), "/")
	}
	projectName := strings.TrimSpace(state.ProjectName)
	if projectName == "" {
		projectName = strings.TrimSpace(state.ProjectBaseUrl)
	}
	projectURL := strings.TrimSuffix(origin, "/") + "/app?project=" + state.ProjectID.String()
	content := mail.BuildCrawlFinished(state.Status == "completed", projectName, projectURL)
	rows, err := w.queries.MarkCrawlNotified(ctx, crawlID)
	if err != nil {
		log.Printf("crawl notify: mark notified failed: crawl_id=%s error=%v", crawlID.String(), err)
		return
	}
	if rows == 0 {
		return
	}
	if err := mailer.Send(to, content.Subject, content.Text, content.HTML); err != nil {
		log.Printf("crawl notify: send failed: crawl_id=%s error=%v", crawlID.String(), err)
	}
}
