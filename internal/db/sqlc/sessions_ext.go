package sqlc

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

const updateSessionLastUsedAt = `UPDATE sessions SET last_used_at = now(), updated_at = now() WHERE id = $1`

func (q *Queries) UpdateSessionLastUsedAt(ctx context.Context, id pgtype.UUID) error {
	_, err := q.db.Exec(ctx, updateSessionLastUsedAt, id)
	return err
}
