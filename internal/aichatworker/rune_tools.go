package aichatworker

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/aichattools"
)

// RuneConnector dials one Rune session for a turn. It matches the pending
// internal/runecms Connect contract. The worker leaves RuneDial nil unless a
// connector is wired; tests inject a fake. There is no production network
// bypass: a nil connector simply means CMS tools stay disconnected.
type RuneConnector func(ctx context.Context, endpoint, token string) (aichattools.RuneSession, error)

// runeConnection is one loaded project CMS connection. The endpoint and
// token never leave this file: they are used only to dial, never logged.
type runeConnection struct {
	provider  string
	endpoint  string
	encrypted string
	revision  string
}

// loadRuneConnection loads the saved Rune connection for a project only when
// the user is still an organization member and the integrations feature is
// enabled. A missing row or any query error means no CMS for this turn;
// native chat still works.
func (w *Worker) loadRuneConnection(ctx context.Context, userID, projectID pgtype.UUID) *runeConnection {
	conn := w.loadCMSConnection(ctx, userID, projectID)
	if conn == nil || conn.provider != string(CMSProviderRune) {
		return nil
	}
	return conn
}

// runeGuard rechecks before every CMS call that the user is still a member,
// the integrations feature is still on, and the saved rune revision is
// unchanged, so a disconnect or replacement rejects stale calls.
func (w *Worker) runeGuard(userID, projectID pgtype.UUID, revision string) func(ctx context.Context) error {
	return w.cmsGuard(userID, projectID, string(CMSProviderRune), revision)
}

// setupRune dials one Rune session for the turn and registers its tools on
// the per-turn registry. It returns a brief system status line (never tokens,
// URLs, or remote text) and a cleanup closing the session at the end of the
// turn. Any failure leaves native chat working.
func (w *Worker) setupRune(ctx context.Context, scope turnScope, disabled []string, registry *aichattools.Registry) (string, func()) {
	if w.RuneDial == nil {
		return "CMS content is not connected for this project.", nil
	}
	conn := w.loadRuneConnection(ctx, scope.UserID, scope.ProjectID)
	if conn == nil {
		return "CMS content is not connected for this project.", nil
	}
	if w.GSC == nil {
		log.Printf("ai chat cms unavailable: worker_id=%s reason=no decryptor", w.cfg.ID)
		return "CMS content is unavailable for this turn; answer without it.", nil
	}
	token, err := w.GSC.DecryptSecret(conn.encrypted)
	if err != nil || token == "" {
		log.Printf("ai chat cms unavailable: worker_id=%s reason=decrypt failed", w.cfg.ID)
		return "CMS content is unavailable for this turn; answer without it.", nil
	}
	status, _, closeCMS := w.setupRuneConn(ctx, scope, conn, token, disabled, registry)
	return status, closeCMS
}

// filterRuneSpecs drops session tools covered by the turn's denylist
// snapshot, matching both original and namespaced spellings.
func filterRuneSpecs(specs []aichattools.RuneToolDef, disabled []string) []aichattools.RuneToolDef {
	blocked := make(map[string]bool, len(disabled))
	for _, name := range disabled {
		blocked[name] = true
	}
	out := make([]aichattools.RuneToolDef, 0, len(specs))
	for _, spec := range specs {
		if blocked[spec.Name] || blocked[aichattools.NamespaceRuneName(spec.Name)] ||
			blocked[aichattools.NamespaceRuneName(trimRunePrefix(spec.Name))] {
			continue
		}
		out = append(out, spec)
	}
	return out
}

// trimRunePrefix strips a namespace clients may already include.
func trimRunePrefix(name string) string {
	if len(name) > len(aichattools.RuneToolPrefix) && name[:len(aichattools.RuneToolPrefix)] == aichattools.RuneToolPrefix {
		return name[len(aichattools.RuneToolPrefix):]
	}
	return name
}
