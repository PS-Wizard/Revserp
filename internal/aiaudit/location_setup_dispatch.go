package aiaudit

import (
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// locationSetupScope extracts the location scope of a setup-owned job.
// Location prompt, profile, and map-query jobs always carry it; visibility
// and local runs resolve scope through their audit or run row instead.
func locationSetupScope(job sqlc.ClaimNextPendingAIWorkerJobRow) (pgtype.UUID, bool) {
	if !job.LocationID.Valid {
		return pgtype.UUID{}, false
	}
	return pgtype.UUID{Bytes: job.LocationID.Bytes, Valid: true}, true
}

// locationSetupAuditID extracts the audit scope of a visibility job.
func locationSetupAuditID(job sqlc.ClaimNextPendingAIWorkerJobRow) (pgtype.UUID, bool) {
	if !job.AuditID.Valid {
		return pgtype.UUID{}, false
	}
	return pgtype.UUID{Bytes: job.AuditID.Bytes, Valid: true}, true
}

// locationSetupRunID extracts the run scope of a local visibility job.
func locationSetupRunID(job sqlc.ClaimNextPendingAIWorkerJobRow) (pgtype.UUID, bool) {
	if !job.LocalRunID.Valid {
		return pgtype.UUID{}, false
	}
	return pgtype.UUID{Bytes: job.LocalRunID.Bytes, Valid: true}, true
}
