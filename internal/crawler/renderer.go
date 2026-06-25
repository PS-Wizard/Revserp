package crawler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const defaultRendererConcurrency = 1
const defaultRendererTimeout = 5 * time.Second
const defaultRendererKillTimeout = 7 * time.Second

// htmlRenderer renders one URL to HTML for JavaScript fallback.
type htmlRenderer interface {
	RenderHTML(ctx context.Context, targetURL string) (FetchResult, error)
}

// Renderer wraps Obscura subprocess rendering behind a shared concurrency limiter.
type Renderer struct {
	binaryPath  string
	timeout     time.Duration
	killTimeout time.Duration
	slots       chan struct{}
}

// NewRenderer builds a shared Obscura renderer with bounded concurrency.
func NewRenderer(binaryPath string, concurrency int, timeout time.Duration, killTimeout time.Duration) *Renderer {
	if strings.TrimSpace(binaryPath) == "" {
		return nil
	}
	if concurrency <= 0 {
		concurrency = defaultRendererConcurrency
	}
	if timeout <= 0 {
		timeout = defaultRendererTimeout
	}
	if killTimeout <= 0 {
		killTimeout = defaultRendererKillTimeout
	}
	// Obscura's own --timeout must fire a hair before the hard SIGKILL deadline so
	// the browser exits cleanly with a timeout error instead of being force-killed
	// mid-render (which leaves no usable output and looks like a deadline error).
	if killTimeout <= timeout {
		killTimeout = timeout + time.Second
	}

	return &Renderer{
		binaryPath:  binaryPath,
		timeout:     timeout,
		killTimeout: killTimeout,
		slots:       make(chan struct{}, concurrency),
	}
}

// RenderHTML renders one page with Obscura and returns the resulting HTML body.
func (renderer *Renderer) RenderHTML(ctx context.Context, targetURL string) (FetchResult, error) {
	if renderer == nil {
		return FetchResult{}, fmt.Errorf("renderer is not configured")
	}

	select {
	case <-ctx.Done():
		return FetchResult{}, ctx.Err()
	case renderer.slots <- struct{}{}:
	}
	defer func() {
		<-renderer.slots
	}()

	renderContext, cancelRender := context.WithTimeout(ctx, renderer.killTimeout)
	defer cancelRender()

	startedAt := time.Now()
	command := exec.CommandContext(
		renderContext,
		renderer.binaryPath,
		"fetch",
		targetURL,
		"--dump", "html",
		// domcontentloaded returns as soon as the DOM is parsed instead of waiting
		// for the network to fully settle. networkidle0 routinely blew past the kill
		// timeout on JS-heavy sites that keep background connections open (analytics,
		// chat widgets, long-poll), producing spurious "context deadline exceeded"
		// failures even though the page was already usable.
		"--wait-until", "domcontentloaded",
		// Stealth masks the headless fingerprint so bot-aware sites serve the real
		// page rather than a challenge/blank shell that yields empty rendered HTML.
		"--stealth",
		"--timeout", strconv.Itoa(int(renderer.timeout.Seconds())),
		"--quiet",
	)

	output, err := command.CombinedOutput()
	if err != nil {
		if renderContext.Err() != nil {
			return FetchResult{}, fmt.Errorf("render url: %w", renderContext.Err())
		}
		return FetchResult{}, fmt.Errorf("render url: %w: %s", err, strings.TrimSpace(string(output)))
	}

	renderedBody := extractRenderedHTML(output)
	if len(renderedBody) == 0 {
		return FetchResult{}, fmt.Errorf("render url: empty html output")
	}

	return FetchResult{
		FinalURL:     targetURL,
		StatusCode:   http.StatusOK,
		ContentType:  "text/html; charset=utf-8",
		Body:         renderedBody,
		ResponseTime: time.Since(startedAt),
		ResponseSize: len(renderedBody),
	}, nil
}

// extractRenderedHTML trims command noise before the returned HTML document.
func extractRenderedHTML(output []byte) []byte {
	doctypeIndex := bytes.Index(bytes.ToLower(output), []byte("<!doctype html"))
	if doctypeIndex >= 0 {
		return bytes.TrimSpace(output[doctypeIndex:])
	}

	htmlIndex := bytes.Index(bytes.ToLower(output), []byte("<html"))
	if htmlIndex >= 0 {
		return bytes.TrimSpace(output[htmlIndex:])
	}

	return bytes.TrimSpace(output)
}
