package issues

import (
	"encoding/json"
	"testing"

	"github.com/ps-wizard/revserp/internal/issues/aeo"
)

// Older persisted configs predate missing_llms_txt: their aeo pillar's
// issue_penalty_by_type omits the key, so parsing must fill in the default 12
// instead of leaving it absent (scoring would otherwise fall back to the
// generic DefaultIssuePenalty of 6). An explicitly configured value must win.
func TestParseScoringConfigMissingLlmsTxtPenalty(t *testing.T) {
	t.Run("old config fills default 12", func(t *testing.T) {
		oldConfig := DefaultScoringConfig()
		pillar := oldConfig.Pillars[aeo.PillarID]
		delete(pillar.IssuePenaltyByType, "missing_llms_txt")
		oldConfig.Pillars[aeo.PillarID] = pillar
		raw, err := json.Marshal(oldConfig)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		config, err := ParseScoringConfig(raw)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got := config.Pillars[aeo.PillarID].IssuePenaltyByType["missing_llms_txt"]; got != 12 {
			t.Fatalf("missing_llms_txt penalty = %v, want 12", got)
		}
	})

	t.Run("explicit value wins", func(t *testing.T) {
		config := DefaultScoringConfig()
		pillar := config.Pillars[aeo.PillarID]
		pillar.IssuePenaltyByType["missing_llms_txt"] = 20
		config.Pillars[aeo.PillarID] = pillar
		raw, err := json.Marshal(config)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		parsed, err := ParseScoringConfig(raw)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got := parsed.Pillars[aeo.PillarID].IssuePenaltyByType["missing_llms_txt"]; got != 20 {
			t.Fatalf("missing_llms_txt penalty = %v, want explicit 20", got)
		}
	})
}
