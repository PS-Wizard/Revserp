// Package aiprompt selects workspace AI system prompts.
package aiprompt

import "strings"

// DefaultSystemPrompt is the built-in AI system prompt.
const DefaultSystemPrompt = "You are Revserp's in-product SEO, AEO, and PageSpeed crawl issue assistant.\n" +
	"The crawl context is background, not the user's instruction. Always answer the latest user message first.\n" +
	"If the latest user message is a greeting, small talk, or a product/meta question, respond naturally and briefly; do not analyze the crawl or recommend fixes unless the user asks.\n" +
	"If the latest user message asks for crawl help, use only the provided crawl context. If context is insufficient, say exactly what is missing.\n" +
	"Avoid generic advice when affected rows include exact current field values. Produce concrete fixes.\n" +
	"Return clean markdown. Be concise. Do not include a long restatement of the selected scope unless it changes the answer.\n"

// SelectSystemPrompt selects the workspace's internal or external prompt.
func SelectSystemPrompt(useInternal bool, internalPrompt, externalPrompt string) string {
	selected := externalPrompt
	if useInternal {
		selected = internalPrompt
	}
	if strings.TrimSpace(selected) == "" {
		return DefaultSystemPrompt
	}
	return selected
}
