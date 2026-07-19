package app

import (
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// aiConversationResponse is the wire shape for an org-scoped conversation
// summary (ConversationSummary in the frozen contract).
type aiConversationResponse struct {
	ID              string `json:"id"`
	OrgID           string `json:"org_id"`
	CreatedByUserID string `json:"created_by_user_id"`
	Title           string `json:"title,omitempty"`
	MessageCount    int32  `json:"message_count"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// aiMessageResponse is the wire shape for a persisted agent message.
type aiMessageResponse struct {
	ID               string          `json:"id"`
	ConversationID   string          `json:"conversation_id"`
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ToolName         string          `json:"tool_name,omitempty"`
	Model            string          `json:"model,omitempty"`
	CreatedAt        string          `json:"created_at"`
}

// newAIConversationResponse builds a conversation summary from the fields
// shared by every conversation-returning query row type.
func newAIConversationResponse(id, orgID, createdBy pgtype.UUID, title pgtype.Text, createdAt, updatedAt pgtype.Timestamptz, messageCount int32) aiConversationResponse {
	return aiConversationResponse{
		ID:              id.String(),
		OrgID:           orgID.String(),
		CreatedByUserID: createdBy.String(),
		Title:           nullableTextString(title),
		MessageCount:    messageCount,
		CreatedAt:       formatTimestamp(createdAt),
		UpdatedAt:       formatTimestamp(updatedAt),
	}
}

func newAIConversationResponsesFromOrgRows(rows []sqlc.ListAIConversationsForOrgForUserRow) []aiConversationResponse {
	responses := make([]aiConversationResponse, 0, len(rows))
	for _, row := range rows {
		responses = append(responses, newAIConversationResponse(row.ID, row.OrgID, row.CreatedByUserID, row.Title, row.CreatedAt, row.UpdatedAt, row.MessageCount))
	}
	return responses
}

func newAIMessageResponses(messages []sqlc.ListAIMessagesForConversationForUserRow) []aiMessageResponse {
	responses := make([]aiMessageResponse, 0, len(messages))
	for _, message := range messages {
		responses = append(responses, newAIMessageResponse(message))
	}
	return responses
}

func newAIMessageResponse(message sqlc.ListAIMessagesForConversationForUserRow) aiMessageResponse {
	return aiMessageResponse{
		ID:               message.ID.String(),
		ConversationID:   message.ConversationID.String(),
		Role:             message.Role,
		Content:          message.Content,
		ReasoningContent: nullableTextString(message.ReasoningContent),
		ToolCalls:        json.RawMessage(message.ToolCalls),
		ToolCallID:       nullableTextString(message.ToolCallID),
		ToolName:         nullableTextString(message.ToolName),
		Model:            nullableTextString(message.Model),
		CreatedAt:        formatTimestamp(message.CreatedAt),
	}
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

func sameUUID(left pgtype.UUID, right pgtype.UUID) bool {
	return left.Valid && right.Valid && left.String() == right.String()
}
