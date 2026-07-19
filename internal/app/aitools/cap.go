package aitools

import (
	"strconv"
	"strings"
)

// capText truncates text to maxLength bytes, appending a marker with the
// original length so the model knows content was cut.
func capText(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	return strings.TrimSpace(value[:maxLength]) + "\n[truncated, " + strconv.Itoa(len(value)) + " total chars]"
}

// clampLimit bounds a client-requested limit to (0, max], defaulting to def
// when the requested value is zero or negative.
func clampLimit(requested int, def int, max int) int32 {
	if requested <= 0 {
		requested = def
	}
	if requested > max {
		requested = max
	}
	return int32(requested)
}
