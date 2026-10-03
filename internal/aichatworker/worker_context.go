package aichatworker

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ps-wizard/revserp/internal/ai"
)

const terminalProgressPrompt = "You have reached the end of tool use for this turn (tool-call limit or context capacity). Do not request any more tools. Write a final progress report using only what you have already gathered: list completed work, failed work, unknown outcomes, and unfinished work separately. Do not claim overall success if anything failed, is unknown, or is unfinished. Cite the specific issues you found where relevant."

func chatInputBudgetTokens(model string) int {
	return ai.ChatContextCapacity(model) - ai.ChatMaxOutputTokens
}

type chatContextTracker struct {
	lastUsage ai.Usage
	haveLast  bool
	hasTools  bool
	effort    string
	liveLen   int
}

func (t *chatContextTracker) noteSent(tools bool, effort string, liveLen int) {
	t.haveLast = false
	t.hasTools = tools
	t.effort = effort
	t.liveLen = liveLen
}

func (t *chatContextTracker) noteUsage(usage ai.Usage) {
	t.lastUsage = usage
	t.haveLast = usage.Prompt > 0 && usage.Completion >= 0
}

func (t *chatContextTracker) inputTokens(req ai.Request, live []ai.Message) int {
	if t.haveLast && (len(req.Tools) > 0) == t.hasTools && req.Effort == t.effort && len(live) >= t.liveLen {
		appended := append([]ai.Message(nil), live[t.liveLen:]...)
		// Provider completion usage already includes assistant output, but not its next-request envelopes.
		for i := range appended {
			if appended[i].Role != ai.RoleAssistant {
				continue
			}
			appended[i].Content = ""
			appended[i].ReasoningContent = ""
			appended[i].ToolCalls = append([]ai.ToolCall(nil), appended[i].ToolCalls...)
			for j := range appended[i].ToolCalls {
				appended[i].ToolCalls[j].Args = ""
			}
		}
		return t.lastUsage.Prompt + t.lastUsage.Completion + ai.EstimateChatInputTokens(ai.Request{Messages: appended})
	}
	return ai.EstimateChatInputTokens(req)
}

func recordToolRundown(rundown map[string]string, callID, status, summary string) {
	if rundown == nil || callID == "" {
		return
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = status
	}
	cut := len(summary)
	if cut > 200 {
		cut = 200
		for cut > 0 && !utf8.RuneStart(summary[cut]) {
			cut--
		}
		summary = summary[:cut] + "…[truncated]"
	}
	rundown[callID] = status + ": " + summary
}

func terminalStubToolContent(name, rundown string) string {
	if rundown == "" {
		rundown = "status unknown"
	}
	return fmt.Sprintf("earlier %q result omitted to fit context (%s); full result retained in turn history", name, rundown)
}

// DeepSeek omits reasoning without tools; preserve the original live reasoning for active calls.
func terminalNoToolsMessages(live []ai.Message, rundown map[string]string, model string) ([]ai.Message, int, bool) {
	clone := make([]ai.Message, 0, len(live)+1)
	clone = append(clone, live...)
	clone = append(clone, ai.Message{Role: ai.RoleUser, Content: terminalProgressPrompt})
	budget := chatInputBudgetTokens(model)
	tokens := ai.EstimateChatInputTokens(ai.Request{Messages: clone})
	if tokens <= budget {
		return clone, tokens, true
	}
	for i := range clone {
		if clone[i].Role != ai.RoleTool {
			continue
		}
		clone[i].Content = terminalStubToolContent(clone[i].Name, rundown[clone[i].ToolCallID])
		tokens = ai.EstimateChatInputTokens(ai.Request{Messages: clone})
		if tokens <= budget {
			return clone, tokens, true
		}
	}
	return clone, tokens, false
}

type historyPair struct {
	user      string
	assistant string
	images    []ai.Image
}

func selectHistoryPairs(system, current ai.Message, newestFirst []historyPair, model, effort string, tools []ai.ToolDef) []ai.Message {
	remaining := chatInputBudgetTokens(model) - ai.EstimateChatInputTokens(ai.Request{
		Model: model, Effort: effort, Messages: []ai.Message{system, current}, Tools: tools,
	})
	kept := make([]historyPair, 0, len(newestFirst))
	for _, pair := range newestFirst {
		pairTokens := ai.EstimateChatInputTokens(ai.Request{Messages: []ai.Message{
			{Role: ai.RoleUser, Content: pair.user, Images: pair.images},
			{Role: ai.RoleAssistant, Content: pair.assistant},
		}})
		if pairTokens > remaining {
			break
		}
		remaining -= pairTokens
		kept = append(kept, pair)
	}
	messages := make([]ai.Message, 0, 2+2*len(kept))
	messages = append(messages, system)
	for i := len(kept) - 1; i >= 0; i-- {
		messages = append(messages,
			ai.Message{Role: ai.RoleUser, Content: kept[i].user, Images: kept[i].images},
			ai.Message{Role: ai.RoleAssistant, Content: kept[i].assistant})
	}
	return append(messages, current)
}

func fitHistoryToTools(messages []ai.Message, model, effort string, tools []ai.ToolDef) []ai.Message {
	if len(messages) < 2 {
		return messages
	}
	pairs := make([]historyPair, 0, (len(messages)-2)/2)
	for i := len(messages) - 3; i >= 1; i -= 2 {
		pairs = append(pairs, historyPair{
			user: messages[i].Content, images: messages[i].Images, assistant: messages[i+1].Content,
		})
	}
	return selectHistoryPairs(messages[0], messages[len(messages)-1], pairs, model, effort, tools)
}
