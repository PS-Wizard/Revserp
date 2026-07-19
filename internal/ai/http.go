package ai

import (
	"net"
	"net/http"
	"time"
)

// defaultClient is a shared HTTP client used for one-shot (non-streaming)
// requests when none is provided. An overall Timeout is safe here because the
// whole call is a single bounded request/response.
var defaultClient = &http.Client{Timeout: 60 * time.Second}

// defaultStreamClient is a shared HTTP client used for streaming requests
// (agentic chat turns) when none is provided. It deliberately has no overall
// http.Client.Timeout: that field bounds the entire request including the
// full body read, which is wrong for a long-lived SSE stream and causes
// legitimate slow-but-alive streams to be killed mid-turn. Instead, the
// per-phase timeouts on the transport bound dialing, TLS handshake, and
// first-byte latency, while overall turn duration is left to the caller's
// context.
var defaultStreamClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 120 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   10,
	},
}
