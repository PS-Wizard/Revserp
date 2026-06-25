package crawler

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// StartWorkerPool starts crawl workers that process jobs until the jobs channel closes.
// requestDelay introduces a per-worker pause between consecutive requests when non-zero.
// requestJitter adds a random offset in [-requestJitter, +requestJitter] to the delay.
func StartWorkerPool(ctx context.Context, workerCount int, fetcher *Fetcher, parser *Parser, renderer htmlRenderer, jobs <-chan CrawlJob, requestDelay time.Duration, requestJitter time.Duration) <-chan CrawlResult {
	results := make(chan CrawlResult)

	var workerGroup sync.WaitGroup
	for range workerCount {
		workerGroup.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}

					result := ProcessJob(ctx, fetcher, parser, renderer, job)

					select {
					case <-ctx.Done():
						return
					case results <- result:
					}

					// Jitter randomizes inter-request spacing so traffic looks less robotic.
					// Sleep between requests to stay within site rate limits.
					// The select ensures cancellation is not blocked by the delay.
					effective := requestDelay
					if requestJitter > 0 {
						offset := rand.Int63n(2*int64(requestJitter)+1) - int64(requestJitter)
						effective = requestDelay + time.Duration(offset)
						if effective < 0 {
							effective = 0
						}
					}
					if effective > 0 {
						select {
						case <-ctx.Done():
							return
						case <-time.After(effective):
						}
					}
				}
			}
		})
	}

	go func() {
		workerGroup.Wait()
		close(results)
	}()

	return results
}
