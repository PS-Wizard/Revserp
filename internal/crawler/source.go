package crawler

// IsManualLikeSource reports whether a crawl source is a user-requested home
// crawl. Manual, auto, and MCP crawls share the same baseline/competitor-gap
// semantics; competitor crawls do not.
func IsManualLikeSource(source string) bool {
	switch source {
	case "manual", "auto", "mcp":
		return true
	default:
		return false
	}
}
