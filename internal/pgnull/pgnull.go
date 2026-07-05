// Package pgnull holds shared helpers for converting Go primitives into
// nullable pgtype values, so callers agree on a single definition of "empty".
package pgnull

import (
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// Text converts a string into pgtype.Text, treating whitespace-only strings
// as NULL (e.g. a title of "   " is "missing", not a literal blank string).
func Text(value string) pgtype.Text {
	if strings.TrimSpace(value) == "" {
		return pgtype.Text{}
	}

	return pgtype.Text{String: value, Valid: true}
}
