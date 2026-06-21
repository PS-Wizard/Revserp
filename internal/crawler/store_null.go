package crawler

import (
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// nullableFetchStatusCode builds a valid pgtype.Int4 for an HTTP status code when available.
func nullableFetchStatusCode(value int) pgtype.Int4 {
	if value <= 0 {
		return pgtype.Int4{}
	}

	return nullableInt4(value)
}

// nullableFetchResponseSize builds a valid pgtype.Int4 for a response size when available.
func nullableFetchResponseSize(value int) pgtype.Int4 {
	if value <= 0 {
		return pgtype.Int4{}
	}

	return nullableInt4(value)
}

// nullableText builds a valid pgtype.Text from a non-empty string.
func nullableText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}

	return pgtype.Text{String: value, Valid: true}
}

// nullableInt4 builds a valid pgtype.Int4 from an int value.
func nullableInt4(value int) pgtype.Int4 {
	return pgtype.Int4{Int32: int32(value), Valid: true}
}

// nullableBool builds a valid pgtype.Bool from a bool value.
func nullableBool(value bool) pgtype.Bool {
	return pgtype.Bool{Bool: value, Valid: true}
}

// mustMarshalJSON encodes a value into JSON bytes for jsonb columns.
func mustMarshalJSON(value any) []byte {
	encodedValue, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("marshal crawler json value: %v", err))
	}

	return encodedValue
}
