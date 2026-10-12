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

// historyExchange is one prior turn kept whole, with compact write records.
type historyExchange struct {
	messages []ai.Message
	writes   []ledgerWrite
}

// historyStatuses indexes historical tool outcomes for honest terminal stubs.
func historyStatuses(history []historyExchange) map[string]string {
	statuses := map[string]string{}
	for _, exchange := range history {
		for _, write := range exchange.writes {
			if _, ok := statuses[write.CallID]; !ok {
				statuses[write.CallID] = ledgerOutcome(write.Status, write.Summary)
			}
		}
	}
	return statuses
}

// fitHistoryExchanges keeps the newest whole exchanges that fit the input
// guard. An over-guard newest exchange is still kept whole for an explicit
// terminal failure instead of silent truncation.
func fitHistoryExchanges(system, current ai.Message, newestFirst []historyExchange, model, effort string, tools []ai.ToolDef) ([]ai.Message, int, bool) {
	remaining := chatInputBudgetTokens(model) - ai.EstimateChatInputTokens(ai.Request{
		Model: model, Effort: effort, Messages: []ai.Message{system, current}, Tools: tools,
	})
	// The tool schema is charged once in the base request above; each exchange
	// pays only its own delta so large schemas never crowd out history.
	toolSchemaTokens := ai.EstimateChatInputTokens(ai.Request{Tools: tools})
	exchangeTokens := func(exchange historyExchange) int {
		return ai.EstimateChatInputTokens(ai.Request{
			Model: model, Effort: effort, Messages: exchange.messages, Tools: tools,
		}) - toolSchemaTokens
	}
	newestOver := len(newestFirst) > 0 && exchangeTokens(newestFirst[0]) > remaining
	kept := make([]historyExchange, 0, len(newestFirst))
	for _, exchange := range newestFirst {
		tokens := exchangeTokens(exchange)
		if tokens > remaining {
			break
		}
		remaining -= tokens
		kept = append(kept, exchange)
	}
	if len(kept) == 0 && len(newestFirst) > 0 {
		kept = append(kept, newestFirst[0])
	}
	messages := make([]ai.Message, 0, 2+len(kept)*4)
	messages = append(messages, system)
	for i := len(kept) - 1; i >= 0; i-- {
		messages = append(messages, kept[i].messages...)
	}
	return append(messages, current), len(newestFirst) - len(kept), newestOver
}

// countAssistantToolRounds counts provider tool rounds for the work cap,
// ignoring public round identities.
func countAssistantToolRounds(transcript []ai.Message) int {
	n := 0
	for _, message := range transcript {
		if message.Role == ai.RoleAssistant && len(message.ToolCalls) > 0 {
			n++
		}
	}
	return n
}

// fitHistoryWithLedger fits exchanges whole, ledgers dropped writes into the
// system prompt under the same guard, and flags an over-guard newest exchange
// for explicit failure.
func fitHistoryWithLedger(system, current ai.Message, oldestFirst []historyExchange, model, effort string, tools []ai.ToolDef) ([]ai.Message, string, bool) {
	budget := chatInputBudgetTokens(model)
	requestTokens := func(sys ai.Message, kept []ai.Message) int {
		messages := make([]ai.Message, 0, len(kept)+2)
		messages = append(messages, sys)
		messages = append(messages, kept...)
		messages = append(messages, current)
		return ai.EstimateChatInputTokens(ai.Request{Model: model, Effort: effort, Messages: messages, Tools: tools})
	}
	newestFirst := make([]historyExchange, 0, len(oldestFirst))
	for i := len(oldestFirst) - 1; i >= 0; i-- {
		newestFirst = append(newestFirst, oldestFirst[i])
	}
	fitted, dropped, newestOver := fitHistoryExchanges(system, current, newestFirst, model, effort, tools)
	if newestOver {
		return fitted, "Context notice: the newest history exchange alone exceeds the model's context window, so this turn fails instead of answering from a stubbed pseudo-fit. Start a new conversation or shorten the newest exchange.", true
	}
	if dropped == 0 {
		return fitted, "", false
	}
	keptCount := len(newestFirst) - dropped
	keptNewest := append([]historyExchange(nil), newestFirst[:keptCount]...)
	droppedChron := append([]historyExchange(nil), newestFirst[keptCount:]...)
	for i, j := 0, len(droppedChron)-1; i < j; i, j = i+1, j-1 {
		droppedChron[i], droppedChron[j] = droppedChron[j], droppedChron[i]
	}
	for {
		ledger := renderWriteLedger(droppedChron)
		sys := system
		sys.Content += ledger
		kept := make([]ai.Message, 0, keptCount*4)
		for i := len(keptNewest) - 1; i >= 0; i-- {
			kept = append(kept, keptNewest[i].messages...)
		}
		if requestTokens(sys, kept) <= budget {
			notice := contextTrimNotice(dropped, len(keptNewest))
			if ledger != "" {
				notice += " Executed-write records from the omitted turns are listed in the system prompt."
			}
			messages := make([]ai.Message, 0, len(kept)+2)
			messages = append(messages, sys)
			messages = append(messages, kept...)
			return append(messages, current), notice, false
		}
		if len(keptNewest) == 1 {
			return fitted, "Context notice: the newest history exchange plus its omitted-write ledger exceeds the model's context window, so this turn fails instead of silently dropping executed-write evidence.", true
		}
		evicted := keptNewest[len(keptNewest)-1]
		keptNewest = keptNewest[:len(keptNewest)-1]
		droppedChron = append(droppedChron, evicted)
		dropped = len(newestFirst) - len(keptNewest)
	}
}
