package app

import (
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/pgnull"
)

// pgText converts a string into pgtype.Text.
func pgText(value string) pgtype.Text {
	return pgnull.Text(value)
}
