package app

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func decodeOptionalJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return nil
	}
	if r.ContentLength == 0 {
		return nil
	}
	err := readJSON(r, target)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func timestamptzValue(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}
