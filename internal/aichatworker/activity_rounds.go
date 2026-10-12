package aichatworker

// Public assistant activity rounds in ai_messages.content_blocks. They never
// carry reasoning; interrupted rounds stay "running".

import "encoding/json"

const (
	assistantActivityStatusRunning  = "running"
	assistantActivityStatusComplete = "complete"
)

type assistantActivityBlock struct {
	Type   string `json:"type"`
	Round  int    `json:"round"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

// assistantRoundKind classifies a finished provider round.
func assistantRoundKind(hasToolCalls bool) string {
	if hasToolCalls {
		return "activity"
	}
	return "answer"
}

func parseAssistantActivityBlocks(blocks []byte) []assistantActivityBlock {
	if len(blocks) == 0 {
		return nil
	}
	var stored []assistantActivityBlock
	if json.Unmarshal(blocks, &stored) != nil {
		return nil
	}
	activity := make([]assistantActivityBlock, 0, len(stored))
	for _, block := range stored {
		if block.Type != "activity" || block.Round < 0 {
			continue
		}
		if block.Status != assistantActivityStatusRunning && block.Status != assistantActivityStatusComplete {
			continue
		}
		activity = append(activity, block)
	}
	return activity
}

// activityBlockRaw applies update to every activity entry, preserving unknown
// entries byte for byte, and appends insert when nothing matched.
func activityBlockRaw(blocks []byte, update func(*assistantActivityBlock) bool, insert *assistantActivityBlock) []byte {
	var raw []json.RawMessage
	if len(blocks) != 0 {
		if json.Unmarshal(blocks, &raw) != nil {
			raw = nil
		}
	}
	matched := false
	for i, entry := range raw {
		var block assistantActivityBlock
		if json.Unmarshal(entry, &block) != nil || block.Type != "activity" {
			continue
		}
		if update(&block) {
			matched = true
			encoded, err := json.Marshal(block)
			if err == nil {
				raw[i] = encoded
			}
		}
	}
	if !matched && insert != nil {
		encoded, err := json.Marshal(insert)
		if err == nil {
			raw = append(raw, encoded)
		}
	}
	if len(raw) == 0 {
		return []byte("[]")
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return []byte("[]")
	}
	return encoded
}

func upsertAssistantActivityText(blocks []byte, round int, delta string) []byte {
	var current string
	var raw []json.RawMessage
	if len(blocks) != 0 {
		_ = json.Unmarshal(blocks, &raw)
	}
	for _, entry := range raw {
		var block assistantActivityBlock
		if json.Unmarshal(entry, &block) != nil || block.Type != "activity" || block.Round != round {
			continue
		}
		current = block.Text
		break
	}
	text := current + delta
	updated := false
	out := activityBlockRaw(blocks, func(block *assistantActivityBlock) bool {
		if block.Round != round {
			return false
		}
		updated = true
		block.Text = text
		block.Status = assistantActivityStatusRunning
		return true
	}, nil)
	if !updated {
		out = activityBlockRaw(blocks, func(*assistantActivityBlock) bool { return false },
			&assistantActivityBlock{Type: "activity", Round: round, Text: text, Status: assistantActivityStatusRunning})
	}
	return out
}

func completeAssistantActivityRoundRaw(blocks []byte, round int, fullText string) []byte {
	updated := false
	out := activityBlockRaw(blocks, func(block *assistantActivityBlock) bool {
		if block.Round != round {
			return false
		}
		updated = true
		block.Text = fullText
		block.Status = assistantActivityStatusComplete
		return true
	}, nil)
	if !updated {
		out = activityBlockRaw(blocks, func(*assistantActivityBlock) bool { return false },
			&assistantActivityBlock{Type: "activity", Round: round, Text: fullText, Status: assistantActivityStatusComplete})
	}
	return out
}

func removeAssistantActivityRoundRaw(blocks []byte, round int) []byte {
	var raw []json.RawMessage
	if len(blocks) != 0 {
		if json.Unmarshal(blocks, &raw) != nil {
			return blocks
		}
	}
	kept := raw[:0]
	for _, entry := range raw {
		var block assistantActivityBlock
		if json.Unmarshal(entry, &block) == nil && block.Type == "activity" && block.Round == round {
			continue
		}
		kept = append(kept, entry)
	}
	if len(kept) == 0 {
		return []byte("[]")
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return blocks
	}
	return encoded
}

// maxAssistantActivityRoundRaw reports the highest persisted public round, or
// -1 when nothing is visible yet.
func maxAssistantActivityRoundRaw(blocks []byte) int {
	maxRound := -1
	for _, block := range parseAssistantActivityBlocks(blocks) {
		if block.Round > maxRound {
			maxRound = block.Round
		}
	}
	return maxRound
}
