package competitorgaps

import (
	"net/url"
	"strings"
)

func normalizeGraphURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		trimmed := strings.SplitN(raw, "#", 2)[0]
		return strings.TrimSuffix(trimmed, "/")
	}

	parsed.Fragment = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")

	return parsed.String()
}

func pageLooksLikeHomepage(pageURL string) bool {
	trimmedPageURL := strings.TrimSpace(strings.ToLower(pageURL))
	return countURLPathSegments(trimmedPageURL) == 0
}

func countURLPathSegments(pageURL string) int {
	withoutProtocol := strings.TrimPrefix(strings.TrimPrefix(pageURL, "https://"), "http://")
	pathParts := strings.SplitN(withoutProtocol, "/", 2)
	if len(pathParts) < 2 {
		return 0
	}
	trimmedPath := strings.Trim(pathParts[1], "/")
	if trimmedPath == "" {
		return 0
	}
	return len(strings.Split(trimmedPath, "/"))
}

func homepageRootKey(pages []graphPage, seedURL string) (string, bool) {
	for _, page := range pages {
		if pageLooksLikeHomepage(page.URL) {
			return normalizeGraphURL(page.URL), true
		}
	}
	if strings.TrimSpace(seedURL) == "" {
		return "", false
	}
	want := normalizeGraphURL(seedURL)
	for _, page := range pages {
		if normalizeGraphURL(page.URL) == want {
			return want, true
		}
	}
	return "", false
}

// hopsFromHome is BFS distance from the crawl homepage over directed internal
// links among crawled page URLs. Pages with no path from home are omitted.
func hopsFromHome(pages []graphPage, links []graphLink, seedURL string) map[string]int {
	home, ok := homepageRootKey(pages, seedURL)
	if !ok {
		return map[string]int{}
	}

	nodes := make(map[string]struct{}, len(pages))
	for _, page := range pages {
		nodes[normalizeGraphURL(page.URL)] = struct{}{}
	}
	if _, exists := nodes[home]; !exists {
		return map[string]int{}
	}

	adjacency := make(map[string][]string, len(nodes))
	for _, link := range links {
		sourceKey := normalizeGraphURL(link.SourceURL)
		targetKey := normalizeGraphURL(link.TargetURL)
		if _, ok := nodes[sourceKey]; !ok {
			continue
		}
		if _, ok := nodes[targetKey]; !ok {
			continue
		}
		if sourceKey == targetKey {
			continue
		}
		adjacency[sourceKey] = append(adjacency[sourceKey], targetKey)
	}

	hops := map[string]int{home: 0}
	queue := []string{home}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range adjacency[current] {
			if _, seen := hops[next]; seen {
				continue
			}
			hops[next] = hops[current] + 1
			queue = append(queue, next)
		}
	}
	return hops
}

func maxHop(hops map[string]int) int {
	max := 0
	for _, hop := range hops {
		if hop > max {
			max = hop
		}
	}
	return max
}

func slicePages(pages []graphPage, hops map[string]int, radius int) []graphPage {
	sliced := make([]graphPage, 0)
	for _, page := range pages {
		hop, reachable := hops[normalizeGraphURL(page.URL)]
		if !reachable || hop > radius {
			continue
		}
		if !pageIsScoreable(page) {
			continue
		}
		sliced = append(sliced, page)
	}
	return sliced
}
