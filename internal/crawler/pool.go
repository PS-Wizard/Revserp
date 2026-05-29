package crawler

import (
	"context"
	"sync"
)

// StartWorkerPool starts crawl workers that process jobs until the jobs channel closes.
func StartWorkerPool(ctx context.Context, workerCount int, fetcher *Fetcher, parser *Parser, jobs <-chan CrawlJob) <-chan CrawlResult {
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

					result := ProcessJob(ctx, fetcher, parser, job)

					select {
					case <-ctx.Done():
						return
					case results <- result:
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
