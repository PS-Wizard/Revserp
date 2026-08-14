package ai

import (
	"net/http"
	"time"
)

// defaultClient is a shared HTTP client used for one-shot (non-streaming)
// requests when none is provided. An overall Timeout is safe here because the
// whole call is a single bounded request/response.
var defaultClient = &http.Client{Timeout: 60 * time.Second}
