package app

import (
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

// pgText converts a string into pgtype.Text.
func pgText(value string) pgtype.Text {
	if strings.TrimSpace(value) == "" {
		return pgtype.Text{}
	}

	return pgtype.Text{String: value, Valid: true}
}
