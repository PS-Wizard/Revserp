package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
	issueshared "github.com/ps-wizard/revserp/internal/issues/shared"
)

// aiConversationTurnContext holds all data loaded within a single transaction
// for handling one AI conversation message turn.
type aiConversationTurnContext struct {
	crawlID            pgtype.UUID
	conversation       sqlc.AiConversation
	pillar             issueshared.PillarScoreBreakdown
	buckets            []issueshared.BucketScoreBreakdown
	selectedIssues     []issueshared.IssueTypeScoreBreakdown
	issueRows          []aiFixIssueRow
	businessProfile    sqlc.GetProjectBusinessProfileByProjectIDRow
	hasBusinessProfile bool
	previousMessages   []sqlc.AiMessage
}

// loadConversationTurnContext fetches all turn context within the active transaction,
// locking the conversation row with FOR UPDATE to serialize concurrent messages.
// On a client error (400/404) it returns a non-zero httpStatus with the message in err.
// On a server error it returns httpStatus == 0 with err populated.
// On success it returns the context with httpStatus == 0 and err == nil.
func (a *App) loadConversationTurnContext(r *http.Request, queries *sqlc.Queries, tx pgx.Tx, conversationID pgtype.UUID, userID pgtype.UUID, req *createAIConversationMessageRequest) (*aiConversationTurnContext, int, error) {
	conversation, err := queries.GetAIConversationForUserForUpdate(r.Context(), sqlc.GetAIConversationForUserForUpdateParams{ID: conversationID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, http.StatusNotFound, errors.New("conversation not found")
		}
		return nil, 0, err
	}

	crawlID, err := resolveAIMessageCrawlID(req.CrawlID, conversation.CrawlID)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	crawl, err := queries.GetCrawlByIDForUser(r.Context(), sqlc.GetCrawlByIDForUserParams{ID: crawlID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, http.StatusNotFound, errors.New("crawl not found")
		}
		return nil, 0, err
	}
	if !sameUUID(crawl.ProjectID, conversation.ProjectID) {
		return nil, http.StatusBadRequest, errors.New("crawl does not belong to conversation project")
	}

	breakdownRow, err := queries.GetCrawlScoreBreakdownByCrawlForUser(r.Context(), sqlc.GetCrawlScoreBreakdownByCrawlForUserParams{CrawlID: crawlID, UserID: userID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, http.StatusNotFound, errors.New("crawl score breakdown not found")
		}
		return nil, 0, err
	}
	var snapshot issueshared.ScoreBreakdownSnapshot
	if err := json.Unmarshal(breakdownRow.BreakdownJson, &snapshot); err != nil {
		return nil, 0, err
	}

	fixRequest := aiFixRequest{
		PillarID:     req.PillarID,
		BucketIDs:    req.BucketIDs,
		IssueTypeIDs: req.IssueTypeIDs,
	}
	pillar, buckets, selectedIssues, err := resolveAIFixScope(snapshot, fixRequest)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	issueRows, err := loadAIFixIssueRows(r, tx, crawlID, userID, req.PillarID, req.BucketIDs, req.IssueTypeIDs, req.IssueURLs)
	if err != nil {
		return nil, 0, err
	}
	if len(req.IssueURLs) > 0 && len(issueRows) == 0 {
		return nil, http.StatusBadRequest, errors.New("issue_urls did not match the selected issue scope")
	}

	businessProfile, hasBusinessProfile, err := getProjectBusinessProfileByProjectID(r.Context(), queries, crawl.ProjectID)
	if err != nil {
		return nil, 0, err
	}

	previousMessages, err := queries.ListAIMessagesForConversationForUser(r.Context(), sqlc.ListAIMessagesForConversationForUserParams{ConversationID: conversationID, UserID: userID})
	if err != nil {
		return nil, 0, err
	}

	return &aiConversationTurnContext{
		crawlID:            crawlID,
		conversation:       conversation,
		pillar:             pillar,
		buckets:            buckets,
		selectedIssues:     selectedIssues,
		issueRows:          issueRows,
		businessProfile:    businessProfile,
		hasBusinessProfile: hasBusinessProfile,
		previousMessages:   previousMessages,
	}, 0, nil
}

// buildTurnPrompt assembles the complete AI prompt from conversation history and the new user message.
func buildTurnPrompt(systemPrompt string, ctx *aiConversationTurnContext, content string) string {
	promptMessages := append(aiFixMessagesFromRows(ctx.previousMessages), aiFixMessage{Role: "user", Content: content})
	return buildAIFixPrompt(systemPrompt, ctx.pillar, ctx.buckets, ctx.selectedIssues, ctx.issueRows, ctx.businessProfile, ctx.hasBusinessProfile, normalizeAIFixMessages(promptMessages))
}

// marshalScopePayload encodes the conversation scope to JSON for persistence.
func marshalScopePayload(pillar issueshared.PillarScoreBreakdown, buckets []issueshared.BucketScoreBreakdown, selectedIssues []issueshared.IssueTypeScoreBreakdown, issueURLs []string) ([]byte, error) {
	return json.Marshal(newAIMessageScope(pillar, buckets, selectedIssues, issueURLs))
}

// persistTurnMessages creates the user/assistant messages and updates the conversation timestamp.
func persistTurnMessages(ctx context.Context, queries *sqlc.Queries, conversationID pgtype.UUID, crawlID pgtype.UUID, content string, scopePayload []byte, aiContent string, model string) (sqlc.AiMessage, sqlc.AiMessage, sqlc.AiConversation, error) {
	userMessage, err := queries.CreateAIMessage(ctx, sqlc.CreateAIMessageParams{
		ConversationID: conversationID,
		Role:           "user",
		Content:        content,
		CrawlID:        crawlID,
		Scope:          scopePayload,
		Model:          pgtype.Text{},
	})
	if err != nil {
		return sqlc.AiMessage{}, sqlc.AiMessage{}, sqlc.AiConversation{}, err
	}

	assistantMessage, err := queries.CreateAIMessage(ctx, sqlc.CreateAIMessageParams{
		ConversationID: conversationID,
		Role:           "assistant",
		Content:        aiContent,
		CrawlID:        crawlID,
		Scope:          scopePayload,
		Model:          aiNullableText(model),
	})
	if err != nil {
		return sqlc.AiMessage{}, sqlc.AiMessage{}, sqlc.AiConversation{}, err
	}

	updatedConversation, err := queries.UpdateAIConversationTouched(ctx, conversationID)
	if err != nil {
		return sqlc.AiMessage{}, sqlc.AiMessage{}, sqlc.AiConversation{}, err
	}

	return userMessage, assistantMessage, updatedConversation, nil
}
