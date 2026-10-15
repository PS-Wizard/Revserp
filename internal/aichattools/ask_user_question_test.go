package aichattools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validQuestionArgs(header, id, mode string, allowCustom bool) string {
	custom := "false"
	if allowCustom {
		custom = "true"
	}
	return `{"questions": [{"id": "` + id + `", "header": "` + header + `", "prompt": "Pick one", "mode": "` + mode + `", "allow_custom": ` + custom + `, "options": [{"id": "a", "label": "Alpha"}, {"id": "b", "label": "Beta"}]}]}`
}

func TestValidateAskUserQuestionArgsTable(t *testing.T) {
	valid := validQuestionArgs("Scope", "q1", "single", false)
	if _, err := ValidateAskUserQuestionArgs(json.RawMessage(valid)); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	cases := []struct {
		name string
		args string
	}{
		{"empty", ``},
		{"no questions", `{"questions": []}`},
		{"six questions", `{"questions": [` + strings.Repeat(`{"id":"q","header":"H","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]},`, 5) + `{"id":"q6","header":"H","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]}`},
		{"unknown top field", `{"questions": [], "extra": 1}`},
		{"unknown question field", `{"questions": [{"id":"q","header":"H","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}],"extra":1}]}`},
		{"missing header", `{"questions": [{"id":"q","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]}`},
		{"blank header", validQuestionArgs("  ", "q1", "single", false)},
		{"header thirteen runes", validQuestionArgs("1234567890123", "q1", "single", false)},
		{"header line break", validQuestionArgs("Scope\nNow", "q1", "single", false)},
		{"duplicate question ids", `{"questions": [` + strings.TrimPrefix(strings.TrimSuffix(validQuestionArgs("Scope", "q1", "single", false), `]}`), `{"questions": [`) + `,` + strings.TrimPrefix(strings.TrimSuffix(validQuestionArgs("Scope", "q1", "single", false), `]}`), `{"questions": [`) + `]}`},
		{"bad mode", validQuestionArgs("Scope", "q1", "rank", false)},
		{"allow_custom absent", `{"questions": [{"id":"q","header":"H","prompt":"P","mode":"single","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]}`},
		{"one option", `{"questions": [{"id":"q","header":"H","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A"}]}]}`},
		{"duplicate option ids", `{"questions": [{"id":"q","header":"H","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A"},{"id":"a","label":"B"}]}]}`},
		{"two recommended", `{"questions": [{"id":"q","header":"H","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A","recommended":true},{"id":"b","label":"B","recommended":true}]}]}`},
		{"option description multiline", `{"questions": [{"id":"q","header":"H","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A","description":"one\ntwo"},{"id":"b","label":"B"}]}]}`},
		{"option description 501 bytes", `{"questions": [{"id":"q","header":"H","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A","description":"` + strings.Repeat("x", 501) + `"},{"id":"b","label":"B"}]}]}`},
		{"option url credentials", `{"questions": [{"id":"q","header":"H","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A","url":"https://user:pass@example.com/x"},{"id":"b","label":"B"}]}]}`},
		{"option url scheme", `{"questions": [{"id":"q","header":"H","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A","url":"ftp://example.com/x"},{"id":"b","label":"B"}]}]}`},
		{"option url internal", `{"questions": [{"id":"q","header":"H","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A","url":"http://169.254.169.254/x"},{"id":"b","label":"B"}]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ValidateAskUserQuestionArgs(json.RawMessage(tc.args)); err == nil {
				t.Fatalf("args accepted, want rejection: %s", tc.args)
			}
		})
	}
}

func TestValidateAskUserQuestionHeaderRunesNotBytes(t *testing.T) {
	// Rune count, not byte count.
	header := strings.Repeat("é", 12)
	questions, err := ValidateAskUserQuestionArgs(json.RawMessage(validQuestionArgs(header, "q1", "single", false)))
	if err != nil {
		t.Fatalf("twelve-rune header rejected: %v", err)
	}
	if questions[0].Header != header {
		t.Fatalf("header = %q, want %q", questions[0].Header, header)
	}
	if _, err := ValidateAskUserQuestionArgs(json.RawMessage(validQuestionArgs(strings.Repeat("é", 13), "q1", "single", false))); err == nil {
		t.Fatal("thirteen-rune header accepted")
	}
}

func TestValidateAskUserQuestionTrimsHeader(t *testing.T) {
	questions, err := ValidateAskUserQuestionArgs(json.RawMessage(validQuestionArgs("  Scope  ", "q1", "single", false)))
	if err != nil {
		t.Fatalf("padded header rejected: %v", err)
	}
	if questions[0].Header != "Scope" {
		t.Fatalf("header = %q, want trimmed Scope", questions[0].Header)
	}
}

func storedQuestions(t *testing.T, args string) []byte {
	t.Helper()
	questions, err := ValidateAskUserQuestionArgs(json.RawMessage(args))
	if err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	encoded, err := json.Marshal(questions)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestValidateUserQuestionDecisionTable(t *testing.T) {
	single := storedQuestions(t, validQuestionArgs("Scope", "q1", "single", false))
	multi := storedQuestions(t, validQuestionArgs("Scope", "q1", "multiple", true))

	t.Run("single selection", func(t *testing.T) {
		status, canonical, reply, err := ValidateUserQuestionDecision(single, "answer",
			[]UserQuestionAnswerInput{{QuestionID: "q1", SelectedOptionIDs: []string{"b"}}}, "")
		if err != nil || status != "answered" || reply != "" {
			t.Fatalf("status=%q reply=%q err=%v", status, reply, err)
		}
		var answers []UserQuestionAnswer
		if err := json.Unmarshal(canonical, &answers); err != nil {
			t.Fatal(err)
		}
		if len(answers) != 1 || len(answers[0].SelectedOptionIDs) != 1 || answers[0].SelectedOptionIDs[0] != "b" {
			t.Fatalf("canonical answers = %s", canonical)
		}
	})

	t.Run("single selection plus custom rejected", func(t *testing.T) {
		custom := storedQuestions(t, validQuestionArgs("Scope", "q1", "single", true))
		if _, _, _, err := ValidateUserQuestionDecision(custom, "answer",
			[]UserQuestionAnswerInput{{QuestionID: "q1", SelectedOptionIDs: []string{"a"}, CustomText: "other"}}, ""); err == nil {
			t.Fatal("single selection plus custom accepted")
		}
	})

	t.Run("multi selection plus custom", func(t *testing.T) {
		status, canonical, _, err := ValidateUserQuestionDecision(multi, "answer",
			[]UserQuestionAnswerInput{{QuestionID: "q1", SelectedOptionIDs: []string{"b", "a"}, CustomText: " plus this "}}, "")
		if err != nil || status != "answered" {
			t.Fatalf("err=%v status=%q", err, status)
		}
		var answers []UserQuestionAnswer
		if err := json.Unmarshal(canonical, &answers); err != nil {
			t.Fatal(err)
		}
		if len(answers[0].SelectedOptionIDs) != 2 || answers[0].SelectedOptionIDs[0] != "a" || answers[0].SelectedOptionIDs[1] != "b" || answers[0].CustomText != "plus this" {
			t.Fatalf("canonical answers = %s", canonical)
		}
	})

	t.Run("custom rejected when allow_custom false", func(t *testing.T) {
		if _, _, _, err := ValidateUserQuestionDecision(single, "answer",
			[]UserQuestionAnswerInput{{QuestionID: "q1", CustomText: "x"}}, ""); err == nil {
			t.Fatal("custom text accepted with allow_custom=false")
		}
	})

	t.Run("skipped answer inside answer decision", func(t *testing.T) {
		status, canonical, _, err := ValidateUserQuestionDecision(single, "answer",
			[]UserQuestionAnswerInput{{QuestionID: "q1", Skipped: true}}, "")
		if err != nil || status != "answered" {
			t.Fatalf("err=%v status=%q", err, status)
		}
		var answers []UserQuestionAnswer
		if err := json.Unmarshal(canonical, &answers); err != nil {
			t.Fatal(err)
		}
		if !answers[0].Skipped || len(answers[0].SelectedOptionIDs) != 0 {
			t.Fatalf("canonical answers = %s", canonical)
		}
	})

	t.Run("skipped with selections rejected", func(t *testing.T) {
		if _, _, _, err := ValidateUserQuestionDecision(single, "answer",
			[]UserQuestionAnswerInput{{QuestionID: "q1", Skipped: true, SelectedOptionIDs: []string{"a"}}}, ""); err == nil {
			t.Fatal("skipped answer with selections accepted")
		}
	})

	t.Run("unknown question rejected", func(t *testing.T) {
		if _, _, _, err := ValidateUserQuestionDecision(single, "answer",
			[]UserQuestionAnswerInput{{QuestionID: "nope", SelectedOptionIDs: []string{"a"}}}, ""); err == nil {
			t.Fatal("unknown question accepted")
		}
	})

	t.Run("unknown option rejected", func(t *testing.T) {
		if _, _, _, err := ValidateUserQuestionDecision(single, "answer",
			[]UserQuestionAnswerInput{{QuestionID: "q1", SelectedOptionIDs: []string{"zzz"}}}, ""); err == nil {
			t.Fatal("unknown option accepted")
		}
	})

	t.Run("direct reply", func(t *testing.T) {
		status, canonical, reply, err := ValidateUserQuestionDecision(single, "answer", nil, "  do it this way  ")
		if err != nil || status != "answered" || reply != "do it this way" || canonical != nil {
			t.Fatalf("status=%q reply=%q canonical=%s err=%v", status, reply, canonical, err)
		}
	})

	t.Run("answers plus reply rejected", func(t *testing.T) {
		if _, _, _, err := ValidateUserQuestionDecision(single, "answer",
			[]UserQuestionAnswerInput{{QuestionID: "q1", SelectedOptionIDs: []string{"a"}}}, "reply"); err == nil {
			t.Fatal("answers plus reply accepted")
		}
	})

	t.Run("empty answer rejected", func(t *testing.T) {
		if _, _, _, err := ValidateUserQuestionDecision(single, "answer", nil, ""); err == nil {
			t.Fatal("answer with neither answers nor reply accepted")
		}
	})

	t.Run("skip", func(t *testing.T) {
		status, canonical, reply, err := ValidateUserQuestionDecision(single, "skip", nil, "")
		if err != nil || status != "skipped" || canonical != nil || reply != "" {
			t.Fatalf("status=%q canonical=%s reply=%q err=%v", status, canonical, reply, err)
		}
	})

	t.Run("skip with body rejected", func(t *testing.T) {
		if _, _, _, err := ValidateUserQuestionDecision(single, "skip",
			[]UserQuestionAnswerInput{{QuestionID: "q1", Skipped: true}}, ""); err == nil {
			t.Fatal("skip with answers accepted")
		}
		if _, _, _, err := ValidateUserQuestionDecision(single, "skip", nil, "x"); err == nil {
			t.Fatal("skip with reply accepted")
		}
	})

	t.Run("bad decision rejected", func(t *testing.T) {
		if _, _, _, err := ValidateUserQuestionDecision(single, "maybe", nil, ""); err == nil {
			t.Fatal("bad decision accepted")
		}
	})
}

func TestValidateUserQuestionDecisionOrdersCanonically(t *testing.T) {
	args := `{"questions": [
		{"id": "q1", "header": "Scope", "prompt": "P1", "mode": "single", "allow_custom": false, "options": [{"id": "a", "label": "A"}, {"id": "b", "label": "B"}]},
		{"id": "q2", "header": "Pages", "prompt": "P2", "mode": "multiple", "allow_custom": true, "options": [{"id": "x", "label": "X"}, {"id": "y", "label": "Y"}]}
	]}`
	stored := storedQuestions(t, args)
	_, canonical, _, err := ValidateUserQuestionDecision(stored, "answer", []UserQuestionAnswerInput{
		{QuestionID: "q2", SelectedOptionIDs: []string{"y", "x"}},
		{QuestionID: "q1", SelectedOptionIDs: []string{"b"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	var answers []UserQuestionAnswer
	if err := json.Unmarshal(canonical, &answers); err != nil {
		t.Fatal(err)
	}
	if len(answers) != 2 || answers[0].QuestionID != "q1" || answers[1].QuestionID != "q2" {
		t.Fatalf("answers out of question order: %s", canonical)
	}
	if len(answers[1].SelectedOptionIDs) != 2 || answers[1].SelectedOptionIDs[0] != "x" || answers[1].SelectedOptionIDs[1] != "y" {
		t.Fatalf("selections out of option order: %s", canonical)
	}
}

func TestFormatUserQuestionToolResult(t *testing.T) {
	args := `{"questions": [
		{"id": "q1", "header": "Scope", "prompt": "P1", "mode": "single", "allow_custom": false, "options": [{"id": "a", "label": "Alpha"}, {"id": "b", "label": "Beta"}]},
		{"id": "q2", "header": "Pages", "prompt": "P2", "mode": "multiple", "allow_custom": true, "options": [{"id": "x", "label": "Ex"}, {"id": "y", "label": "Why"}]}
	]}`
	var questions []AskUserQuestion
	if err := json.Unmarshal(storedQuestions(t, args), &questions); err != nil {
		t.Fatal(err)
	}

	content, summary := FormatUserQuestionToolResult(questions, "answered",
		[]UserQuestionAnswer{
			{QuestionID: "q1", SelectedOptionIDs: []string{"b"}},
			{QuestionID: "q2", SelectedOptionIDs: []string{"x"}, CustomText: "plus this", Skipped: false},
		}, "")
	for _, want := range []string{"Scope (q1)", "Beta (b)", "Pages (q2)", "Ex (x)", "plus this"} {
		if !strings.Contains(content, want) {
			t.Errorf("result missing %q: %q", want, content)
		}
	}
	if summary == "" {
		t.Error("empty summary")
	}

	replyContent, replySummary := FormatUserQuestionToolResult(questions, "answered", nil, "do it this way")
	if !strings.Contains(replyContent, "do it this way") || replySummary == "" {
		t.Errorf("reply result = %q / %q", replyContent, replySummary)
	}

	skipContent, skipSummary := FormatUserQuestionToolResult(questions, "skipped", nil, "")
	for _, want := range []string{"skipped", "not consent", "not permission"} {
		if !strings.Contains(skipContent, want) {
			t.Errorf("skip result missing %q: %q", want, skipContent)
		}
	}
	if skipSummary == "" {
		t.Error("empty skip summary")
	}
}

func TestMarshalUserQuestionRequestShape(t *testing.T) {
	questions := json.RawMessage(`[{"id":"q1","header":"Scope","prompt":"P","mode":"single","allow_custom":false,"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]`)
	created := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	encoded, err := MarshalUserQuestionRequest("req", "turn", "call-1", "pending", questions, nil, "", created, nil)
	if err != nil {
		t.Fatal(err)
	}
	var entity map[string]any
	if err := json.Unmarshal(encoded, &entity); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "turn_id", "tool_call_id", "status", "questions", "created_at"} {
		if _, ok := entity[key]; !ok {
			t.Errorf("entity missing %q: %s", key, encoded)
		}
	}
	for _, key := range []string{"answers", "reply", "answered_at"} {
		if _, ok := entity[key]; ok {
			t.Errorf("pending entity must omit %q: %s", key, encoded)
		}
	}

	answeredAt := created.Add(time.Minute)
	answers := json.RawMessage(`[{"question_id":"q1","selected_option_ids":["a"]}]`)
	encoded, err = MarshalUserQuestionRequest("req", "turn", "call-1", "answered", questions, answers, "", created, &answeredAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &entity); err != nil {
		t.Fatal(err)
	}
	if _, ok := entity["answers"]; !ok {
		t.Errorf("answered entity missing answers: %s", encoded)
	}
	if _, ok := entity["answered_at"]; !ok {
		t.Errorf("answered entity missing answered_at: %s", encoded)
	}
}

func TestAskUserQuestionDescriptionContract(t *testing.T) {
	wants := []string{
		"only when a missing material preference",
		"cannot be resolved safely without them",
		"sensible reversible or read-only choices",
		"infer the selected scope from the provided context",
		"a meaningful preference, scope, or trade-off",
		"never authorization to write, change, or publish anything",
		"never consent and never permission to choose defaults",
		"independent safe analysis",
	}
	for _, want := range wants {
		if !strings.Contains(askUserQuestionDescription, want) {
			t.Errorf("ask_user_question description missing %q", want)
		}
	}
	for _, def := range CatalogDefs() {
		if def.Name != AskUserQuestionName {
			continue
		}
		if def.Description != askUserQuestionDescription {
			t.Error("ask_user_question Def.Description drifted from askUserQuestionDescription")
		}
	}
}
