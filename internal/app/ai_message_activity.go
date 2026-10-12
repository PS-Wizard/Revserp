package app

import "encoding/json"

type aiMessageActivity struct {
	Round  int    `json:"round"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

func activityFromContentBlocks(role string, blocks []byte) []aiMessageActivity {
	if role != "assistant" || len(blocks) == 0 {
		return nil
	}
	var stored []struct {
		Type string `json:"type"`
		aiMessageActivity
	}
	if json.Unmarshal(blocks, &stored) != nil {
		return nil
	}
	var activity []aiMessageActivity
	for _, block := range stored {
		if block.Type != "activity" || block.Round < 0 || (block.Status != "running" && block.Status != "complete") {
			continue
		}
		activity = append(activity, block.aiMessageActivity)
	}
	return activity
}
