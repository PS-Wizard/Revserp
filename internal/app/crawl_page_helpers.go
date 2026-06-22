package app

import (
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"
)

// nullableText converts a string into pgtype.Text.
func nullableText(value string) pgtype.Text {
	return pgText(value)
}

// nullableInt4 converts an optional int into pgtype.Int4.
func nullableInt4(value *int32) pgtype.Int4 {
	if value == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *value, Valid: true}
}

// nullableBool converts an optional bool into pgtype.Bool.
func nullableBool(value *bool) pgtype.Bool {
	if value == nil {
		return pgtype.Bool{}
	}
	return pgtype.Bool{Bool: *value, Valid: true}
}

// nullableJSON converts optional raw JSON into []byte for jsonb columns.
func nullableJSON(value json.RawMessage) []byte {
	if len(value) == 0 {
		return nil
	}
	return []byte(value)
}
