package competitorgaps

import (
	"bytes"
	"encoding/json"
	"strings"
)

type storedPSIResult struct {
	Mobile storedPSIDeviceResult `json:"mobile"`
}

type storedPSIDeviceResult struct {
	Success          bool             `json:"success"`
	PerformanceScore *int             `json:"performance_score"`
	Metrics          storedPSIMetrics `json:"metrics"`
}

type storedPSIMetrics struct {
	FirstContentfulPaint   *float64 `json:"first_contentful_paint"`
	LargestContentfulPaint *float64 `json:"largest_contentful_paint"`
	CumulativeLayoutShift  *float64 `json:"cumulative_layout_shift"`
	FirstInputDelay        *float64 `json:"first_input_delay"`
	SpeedIndex             *float64 `json:"speed_index"`
	TimeToInteractive      *float64 `json:"time_to_interactive"`
}

func psiMetricsFromResults(raw []byte) *PSIMetrics {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var results []storedPSIResult
	if err := json.Unmarshal(trimmed, &results); err != nil || len(results) == 0 {
		return nil
	}
	mobile := results[0].Mobile
	if !mobile.Success {
		return nil
	}
	return &PSIMetrics{
		Performance: mobile.PerformanceScore,
		LCP:         mobile.Metrics.LargestContentfulPaint,
		FCP:         mobile.Metrics.FirstContentfulPaint,
		CLS:         mobile.Metrics.CumulativeLayoutShift,
		FID:         mobile.Metrics.FirstInputDelay,
		SpeedIndex:  mobile.Metrics.SpeedIndex,
		TTI:         mobile.Metrics.TimeToInteractive,
	}
}

func homepageSignals(pages []graphPage) HomepageSignals {
	for _, page := range pages {
		if !pageLooksLikeHomepage(page.URL) {
			continue
		}
		return HomepageSignals{
			URL:       page.URL,
			Title:     strings.TrimSpace(page.Title) != "",
			Meta:      strings.TrimSpace(page.MetaDescription) != "",
			H1:        strings.TrimSpace(page.H1) != "",
			Canonical: strings.TrimSpace(page.Canonical) != "",
			OG:        hasMeaningfulOGTags(page.OGTags),
			JSONLD:    hasMeaningfulJSONLD(page.JSONLD),
			WordCount: page.WordCount,
		}
	}
	return HomepageSignals{URL: ""}
}

func hasMeaningfulOGTags(ogTags []byte) bool {
	trimmedOGTags := bytes.TrimSpace(ogTags)
	if len(trimmedOGTags) == 0 || bytes.Equal(trimmedOGTags, []byte("null")) || bytes.Equal(trimmedOGTags, []byte("{}")) {
		return false
	}
	return true
}

func hasMeaningfulJSONLD(jsonLD []byte) bool {
	trimmedJSONLD := bytes.TrimSpace(jsonLD)
	if len(trimmedJSONLD) == 0 || bytes.Equal(trimmedJSONLD, []byte("null")) || bytes.Equal(trimmedJSONLD, []byte("[]")) {
		return false
	}
	return true
}
