package app

import (
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

func newAIConversationResponsesFromProjectRows(rows []sqlc.ListAIConversationsForProjectForUserRow) []aiConversationResponse {
	responses := make([]aiConversationResponse, 0, len(rows))
	for _, row := range rows {
		responses = append(responses, aiConversationResponse{
			ID:              row.ID.String(),
			ProjectID:       row.ProjectID.String(),
			CrawlID:         optionalUUIDString(row.CrawlID),
			CreatedByUserID: row.CreatedByUserID.String(),
			Title:           nullableTextString(row.Title),
			MessageCount:    row.MessageCount,
			CreatedAt:       formatTimestamp(row.CreatedAt),
			UpdatedAt:       formatTimestamp(row.UpdatedAt),
		})
	}
	return responses
}

func newAIConversationResponsesFromCrawlRows(rows []sqlc.ListAIConversationsForCrawlForUserRow) []aiConversationResponse {
	responses := make([]aiConversationResponse, 0, len(rows))
	for _, row := range rows {
		responses = append(responses, aiConversationResponse{
			ID:              row.ID.String(),
			ProjectID:       row.ProjectID.String(),
			CrawlID:         optionalUUIDString(row.CrawlID),
			CreatedByUserID: row.CreatedByUserID.String(),
			Title:           nullableTextString(row.Title),
			MessageCount:    row.MessageCount,
			CreatedAt:       formatTimestamp(row.CreatedAt),
			UpdatedAt:       formatTimestamp(row.UpdatedAt),
		})
	}
	return responses
}

func newAIConversationResponse(conversation sqlc.AiConversation, messageCount int32) aiConversationResponse {
	return aiConversationResponse{
		ID:              conversation.ID.String(),
		ProjectID:       conversation.ProjectID.String(),
		CrawlID:         optionalUUIDString(conversation.CrawlID),
		CreatedByUserID: conversation.CreatedByUserID.String(),
		Title:           textValue(conversation.Title),
		MessageCount:    messageCount,
		CreatedAt:       formatTimestamp(conversation.CreatedAt),
		UpdatedAt:       formatTimestamp(conversation.UpdatedAt),
	}
}

func newAIMessageResponses(messages []sqlc.AiMessage) []aiMessageResponse {
	responses := make([]aiMessageResponse, 0, len(messages))
	for _, message := range messages {
		responses = append(responses, newAIMessageResponse(message))
	}
	return responses
}

func newAIMessageResponse(message sqlc.AiMessage) aiMessageResponse {
	return aiMessageResponse{
		ID:             message.ID.String(),
		ConversationID: message.ConversationID.String(),
		Role:           message.Role,
		Content:        message.Content,
		CrawlID:        optionalUUIDString(message.CrawlID),
		Scope:          json.RawMessage(message.Scope),
		Model:          textValue(message.Model),
		CreatedAt:      formatTimestamp(message.CreatedAt),
	}
}

func aiFixMessagesFromRows(messages []sqlc.AiMessage) []aiFixMessage {
	responses := make([]aiFixMessage, 0, len(messages))
	for _, message := range messages {
		responses = append(responses, aiFixMessage{Role: message.Role, Content: message.Content})
	}
	return responses
}

func newAIMessageScope(pillar issueshared.PillarScoreBreakdown, buckets []issueshared.BucketScoreBreakdown, selectedIssues []issueshared.IssueTypeScoreBreakdown, issueURLs []string) aiMessageScope {
	scope := aiMessageScope{
		PillarID:        pillar.ID,
		PillarLabel:     pillar.Label,
		BucketIDs:       make([]string, 0, len(buckets)),
		BucketLabels:    make([]string, 0, len(buckets)),
		IssueTypeIDs:    make([]string, 0, len(selectedIssues)),
		IssueTypeLabels: make([]string, 0, len(selectedIssues)),
		IssueURLs:       issueURLs,
	}
	for _, bucket := range buckets {
		scope.BucketIDs = append(scope.BucketIDs, bucket.ID)
		scope.BucketLabels = append(scope.BucketLabels, bucket.Label)
	}
	for _, issue := range selectedIssues {
		scope.IssueTypeIDs = append(scope.IssueTypeIDs, issue.ID)
		scope.IssueTypeLabels = append(scope.IssueTypeLabels, issue.Label)
	}
	return scope
}

func aiNullableText(value string) pgtype.Text {
	trimmedValue := strings.TrimSpace(value)
	if trimmedValue == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: trimmedValue, Valid: true}
}

func nullableTextString(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func optionalUUIDString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return value.String()
}

func sameUUID(left pgtype.UUID, right pgtype.UUID) bool {
	return left.Valid && right.Valid && left.String() == right.String()
}

// normalizeMessageRequest trims, deduplicates, and validates AI message request fields.
// Returns an error message, or empty string on success.
func normalizeMessageRequest(req *createAIConversationMessageRequest) string {
	req.PillarID = strings.TrimSpace(req.PillarID)
	req.BucketID = strings.TrimSpace(req.BucketID)
	req.BucketIDs = normalizeStringIDs(req.BucketIDs)
	if len(req.BucketIDs) == 0 && req.BucketID != "" {
		req.BucketIDs = []string{req.BucketID}
	}
	req.IssueTypeIDs = normalizeStringIDs(req.IssueTypeIDs)
	req.IssueURLs = normalizeStringIDs(req.IssueURLs)
	if len(req.IssueURLs) > maxAIFixScopedURLs {
		req.IssueURLs = req.IssueURLs[:maxAIFixScopedURLs]
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.PillarID == "" || len(req.BucketIDs) == 0 || req.Content == "" {
		return "pillar_id, bucket_ids, and content are required"
	}
	return ""
}

// newCreateMessageResponse builds the response payload for a newly created conversation message.
func newCreateMessageResponse(updatedConversation sqlc.AiConversation, userMessage, assistantMessage sqlc.AiMessage, previousMessages []sqlc.AiMessage, pillar issueshared.PillarScoreBreakdown, buckets []issueshared.BucketScoreBreakdown, selectedIssues []issueshared.IssueTypeScoreBreakdown) createAIConversationMessageResponse {
	return createAIConversationMessageResponse{
		Conversation:     newAIConversationResponse(updatedConversation, int32(len(previousMessages)+2)),
		UserMessage:      newAIMessageResponse(userMessage),
		AssistantMessage: newAIMessageResponse(assistantMessage),
		Scope: aiFixScopeInfo{
			PillarLabel: pillar.Label,
			BucketLabel: aiFixBucketLabel(buckets),
			IssueCount:  len(selectedIssues),
			URLCount:    aiFixBucketURLCount(buckets),
		},
	}
}
