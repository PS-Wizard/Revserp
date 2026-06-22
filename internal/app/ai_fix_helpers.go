package app

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

// normalizeAIFixMessages trims and caps client-owned in-memory chat history.
func normalizeAIFixMessages(messages []aiFixMessage) []aiFixMessage {
	if len(messages) > maxAIFixMessages {
		messages = messages[len(messages)-maxAIFixMessages:]
	}

	normalizedMessages := make([]aiFixMessage, 0, len(messages))
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		content := truncateAIFixText(strings.TrimSpace(message.Content), maxAIFixMessageLength)
		if content == "" || (role != "user" && role != "assistant") {
			continue
		}
		normalizedMessages = append(normalizedMessages, aiFixMessage{Role: role, Content: content})
	}

	return normalizedMessages
}

// normalizeStringIDs trims repeated empty IDs from a string slice.
func normalizeStringIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	normalizedValues := make([]string, 0, len(values))
	for _, value := range values {
		trimmedValue := strings.TrimSpace(value)
		if trimmedValue == "" {
			continue
		}
		if _, exists := seen[trimmedValue]; exists {
			continue
		}
		seen[trimmedValue] = struct{}{}
		normalizedValues = append(normalizedValues, trimmedValue)
	}
	return normalizedValues
}

// truncateAIFixText caps long prompt fields without splitting short values.
func truncateAIFixText(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	if maxLength <= 1 {
		return value[:maxLength]
	}
	return strings.TrimSpace(value[:maxLength-1]) + "\u2026"
}

func aiFixBucketLabel(buckets []issueshared.BucketScoreBreakdown) string {
	if len(buckets) == 1 {
		return buckets[0].Label
	}
	return fmt.Sprintf("%d buckets", len(buckets))
}

func aiFixBucketURLCount(buckets []issueshared.BucketScoreBreakdown) int32 {
	var total int32
	for _, bucket := range buckets {
		total += bucket.AffectedURLCount
	}
	return total
}

func aiFixIssueBucketID(buckets []issueshared.BucketScoreBreakdown, issueID string) string {
	for _, bucket := range buckets {
		for _, issue := range bucket.Issues {
			if issue.ID == issueID {
				return bucket.ID
			}
		}
	}
	return ""
}

// textValue extracts a nullable Postgres text field.
func aiFixTextValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return strings.TrimSpace(value.String)
}

// emptyFallback makes missing fields explicit in model context.
func emptyFallback(value string) string {
	if strings.TrimSpace(value) == "" {
		return "Missing"
	}
	return value
}
