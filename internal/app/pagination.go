package app

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultPaginationLimit = 50
	maxPaginationLimit     = 200
)

type paginationResponse struct {
	Limit  int32 `json:"limit"`
	Offset int32 `json:"offset"`
	Count  int32 `json:"count"`
	Total  int64 `json:"total"`
}

// parsePaginationParams parses list pagination params and applies safe defaults.
func parsePaginationParams(r *http.Request) (int32, int32, error) {
	limit, err := parsePaginationInt32(r.URL.Query().Get("limit"), defaultPaginationLimit)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid limit")
	}
	if limit <= 0 {
		return 0, 0, fmt.Errorf("invalid limit")
	}
	if limit > maxPaginationLimit {
		limit = maxPaginationLimit
	}

	offset, err := parsePaginationInt32(r.URL.Query().Get("offset"), 0)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid offset")
	}
	if offset < 0 {
		return 0, 0, fmt.Errorf("invalid offset")
	}

	return limit, offset, nil
}

// parsePaginationInt32 parses one optional query integer with a fallback default.
func parsePaginationInt32(value string, defaultValue int32) (int32, error) {
	trimmedValue := strings.TrimSpace(value)
	if trimmedValue == "" {
		return defaultValue, nil
	}

	parsedValue, err := strconv.ParseInt(trimmedValue, 10, 32)
	if err != nil {
		return 0, err
	}

	return int32(parsedValue), nil
}
