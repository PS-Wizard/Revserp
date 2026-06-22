package app

import (
	"encoding/json"

	"github.com/ps-wizard/revserp/internal/crawler"
)

// normalizeCreateCrawlConfigSnapshot validates and resolves crawl config defaults before storing them.
func normalizeCreateCrawlConfigSnapshot(rawConfigSnapshot json.RawMessage) ([]byte, error) {
	_, normalizedConfigSnapshot, err := crawler.NormalizeConfigSnapshot(rawConfigSnapshot)
	if err != nil {
		return nil, err
	}

	return normalizedConfigSnapshot, nil
}
