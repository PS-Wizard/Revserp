package shared

import "strings"

const DefaultIssuePenalty = 6.0

// IssueBasePenalty returns the configured base penalty for one issue type.
func IssueBasePenalty(issueType string, issuePenaltyByType map[string]float64) float64 {
	if penalty, exists := issuePenaltyByType[issueType]; exists {
		return penalty
	}
	return DefaultIssuePenalty
}

// SeverityMultiplier returns the configured penalty multiplier for one severity level.
func SeverityMultiplier(severity string) float64 {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "high":
		return 1.0
	case "medium":
		return 0.6
	case "low":
		return 0.3
	default:
		return 0.6
	}
}

// SeverityMultiplierWithConfig returns the configured penalty multiplier for one severity level.
func SeverityMultiplierWithConfig(severity string, scoringConfig ScoringConfig) float64 {
	severity = strings.ToLower(strings.TrimSpace(severity))
	if multiplier, exists := scoringConfig.SeverityMultipliers[severity]; exists {
		return multiplier
	}
	return SeverityMultiplier(severity)
}
