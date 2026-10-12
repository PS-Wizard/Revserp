package aichattools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// AskUserQuestionName is the wizard tool: one call carries 1..5 questions.
	AskUserQuestionName = "ask_user_question"

	askUserQuestionMaxArgsBytes       = 32 << 10
	askUserQuestionMaxQuestions       = 5
	askUserQuestionMaxIDBytes         = 64
	askUserQuestionMaxHeaderRunes     = 12
	askUserQuestionMaxPromptBytes     = 500
	askUserQuestionMaxDescBytes       = 1000
	askUserQuestionMaxOptionDescBytes = 500
	askUserQuestionMaxOptions         = 8
	askUserQuestionMinOptions         = 2
	askUserQuestionMaxLabelBytes      = 160
	askUserQuestionMaxTextBytes       = 4000
	askUserQuestionMaxReplyBytes      = 4000
	askUserQuestionMaxURLBytes        = 2048
	askUserQuestionToolResultBytes    = 32 << 10
)

const askUserQuestionSchema = `{
  "type": "object",
  "properties": {
    "questions": {
      "type": "array", "minItems": 1, "maxItems": 5,
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string", "maxLength": 64},
          "header": {"type": "string", "maxLength": 12},
          "prompt": {"type": "string", "maxLength": 500},
          "description": {"type": "string", "maxLength": 1000},
          "mode": {"type": "string", "enum": ["single", "multiple"]},
          "allow_custom": {"type": "boolean"},
          "options": {
            "type": "array", "minItems": 2, "maxItems": 8,
            "items": {
              "type": "object",
              "properties": {
                "id": {"type": "string", "maxLength": 64},
                "label": {"type": "string", "maxLength": 160},
                "description": {"type": "string", "maxLength": 500},
                "url": {"type": "string", "maxLength": 2048},
                "recommended": {"type": "boolean"}
              },
              "required": ["id", "label"],
              "additionalProperties": false
            }
          }
        },
        "required": ["id", "header", "prompt", "mode", "allow_custom", "options"],
        "additionalProperties": false
      }
    }
  },
  "required": ["questions"],
  "additionalProperties": false
}`

const askUserQuestionDescription = "Ask the user a short wizard of preference questions when their choice genuinely changes what you do next. Bundle every independent question into this ONE call; never split independent steps across calls. A later question set is only for questions that depend on an earlier answer or on new research the answers unlocked. Ask only for a meaningful preference, scope, or trade-off you cannot resolve yourself. Never ask for facts the available tools can answer; research and verify citation options with the data and web tools first, then offer the verified options here. Mark at most one option per question as recommended to express your recommendation; never preselect it. Honour decisions the user already stated instead of re-asking them. The user's answers guide what you prepare next; they are never authorization to write, change, or publish anything. A skipped question is an explicit skip, never consent and never permission to choose defaults."

// AskUserQuestionOption is one selectable row in a user question.
type AskUserQuestionOption struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Description string  `json:"description,omitempty"`
	URL         *string `json:"url,omitempty"`
	Recommended bool    `json:"recommended,omitempty"`
}

// AskUserQuestion is one step of the wizard carried by a single tool call.
type AskUserQuestion struct {
	ID          string                  `json:"id"`
	Header      string                  `json:"header"`
	Prompt      string                  `json:"prompt"`
	Description string                  `json:"description,omitempty"`
	Mode        string                  `json:"mode"`
	AllowCustom bool                    `json:"allow_custom"`
	Options     []AskUserQuestionOption `json:"options"`
}

// UserQuestionAnswer is one resolved answer: selections, custom text, a
// direct reply, or an explicit skip.
type UserQuestionAnswer struct {
	QuestionID        string   `json:"question_id"`
	SelectedOptionIDs []string `json:"selected_option_ids"`
	CustomText        string   `json:"custom_text,omitempty"`
	Skipped           bool     `json:"skipped,omitempty"`
}

func askUserQuestionTool() Tool {
	return Tool{
		Def: Def{
			Name:        AskUserQuestionName,
			Label:       "Ask user question",
			Description: askUserQuestionDescription,
			Schema:      json.RawMessage(askUserQuestionSchema),
			Feature:     "ai_chat",
		},
		Execute: executeAskUserQuestion,
	}
}

func executeAskUserQuestion(_ context.Context, _ json.RawMessage, _ Scope) (Result, error) {
	return Result{}, errors.New("ask_user_question error: this tool is resolved by the turn worker, not by direct execution")
}

type askUserQuestionArgs struct {
	Questions []askUserQuestionInput `json:"questions"`
}

type askUserQuestionInput struct {
	ID          string                       `json:"id"`
	Header      string                       `json:"header"`
	Prompt      string                       `json:"prompt"`
	Description string                       `json:"description"`
	Mode        string                       `json:"mode"`
	AllowCustom *bool                        `json:"allow_custom"`
	Options     []askUserQuestionOptionInput `json:"options"`
}

type askUserQuestionOptionInput struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Description string  `json:"description"`
	URL         *string `json:"url"`
	Recommended *bool   `json:"recommended"`
}

// ValidateAskUserQuestionArgs checks one wizard tool call and returns the
// canonical questions. Unknown fields, byte overruns, bad URLs, and
// duplicate ids all fail; the worker parks only valid calls.
func ValidateAskUserQuestionArgs(raw json.RawMessage) ([]AskUserQuestion, error) {
	if len(raw) == 0 || len(raw) > askUserQuestionMaxArgsBytes {
		return nil, fmt.Errorf("ask_user_question args must be 1..%d bytes", askUserQuestionMaxArgsBytes)
	}
	var args askUserQuestionArgs
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("ask_user_question args are not valid: %v", err)
	}
	if len(args.Questions) == 0 || len(args.Questions) > askUserQuestionMaxQuestions {
		return nil, fmt.Errorf("ask_user_question needs 1..%d questions, got %d", askUserQuestionMaxQuestions, len(args.Questions))
	}
	seen := make(map[string]bool, len(args.Questions))
	questions := make([]AskUserQuestion, 0, len(args.Questions))
	for _, input := range args.Questions {
		question, err := validateAskUserQuestion(input)
		if err != nil {
			return nil, err
		}
		if seen[question.ID] {
			return nil, fmt.Errorf("ask_user_question question id %q is duplicated", question.ID)
		}
		seen[question.ID] = true
		questions = append(questions, question)
	}
	return questions, nil
}

func validateAskUserQuestion(input askUserQuestionInput) (AskUserQuestion, error) {
	if strings.TrimSpace(input.ID) == "" || len(input.ID) > askUserQuestionMaxIDBytes {
		return AskUserQuestion{}, fmt.Errorf("ask_user_question question id must be nonblank and at most %d bytes", askUserQuestionMaxIDBytes)
	}
	if header := strings.TrimSpace(input.Header); header == "" || utf8.RuneCountInString(header) > askUserQuestionMaxHeaderRunes || strings.ContainsAny(header, "\r\n") {
		return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q header must be a nonblank one-line label of at most %d characters", input.ID, askUserQuestionMaxHeaderRunes)
	}
	if strings.TrimSpace(input.Prompt) == "" || len(input.Prompt) > askUserQuestionMaxPromptBytes {
		return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q prompt must be nonblank and at most %d bytes", input.ID, askUserQuestionMaxPromptBytes)
	}
	if len(input.Description) > askUserQuestionMaxDescBytes {
		return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q description must be at most %d bytes", input.ID, askUserQuestionMaxDescBytes)
	}
	if input.Mode != "single" && input.Mode != "multiple" {
		return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q mode must be single or multiple", input.ID)
	}
	if input.AllowCustom == nil {
		return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q must set allow_custom explicitly", input.ID)
	}
	if len(input.Options) < askUserQuestionMinOptions || len(input.Options) > askUserQuestionMaxOptions {
		return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q needs %d..%d options, got %d", input.ID, askUserQuestionMinOptions, askUserQuestionMaxOptions, len(input.Options))
	}
	seen := make(map[string]bool, len(input.Options))
	recommended := 0
	options := make([]AskUserQuestionOption, 0, len(input.Options))
	for _, raw := range input.Options {
		if strings.TrimSpace(raw.ID) == "" || len(raw.ID) > askUserQuestionMaxIDBytes {
			return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q option id must be nonblank and at most %d bytes", input.ID, askUserQuestionMaxIDBytes)
		}
		if seen[raw.ID] {
			return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q option id %q is duplicated", input.ID, raw.ID)
		}
		seen[raw.ID] = true
		if strings.TrimSpace(raw.Label) == "" || len(raw.Label) > askUserQuestionMaxLabelBytes {
			return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q option %q label must be nonblank and at most %d bytes", input.ID, raw.ID, askUserQuestionMaxLabelBytes)
		}
		if len(raw.Description) > askUserQuestionMaxOptionDescBytes || strings.ContainsAny(raw.Description, "\r\n") {
			return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q option %q description must be one line of at most %d bytes", input.ID, raw.ID, askUserQuestionMaxOptionDescBytes)
		}
		var optionURL *string
		if raw.URL != nil && strings.TrimSpace(*raw.URL) != "" {
			trimmed := strings.TrimSpace(*raw.URL)
			if len(trimmed) > askUserQuestionMaxURLBytes {
				return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q option %q url must be at most %d bytes", input.ID, raw.ID, askUserQuestionMaxURLBytes)
			}
			if err := validateQuestionOptionURL(trimmed); err != nil {
				return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q option %q url is not allowed: %v", input.ID, raw.ID, err)
			}
			optionURL = &trimmed
		}
		option := AskUserQuestionOption{ID: raw.ID, Label: raw.Label, Description: raw.Description, URL: optionURL}
		if raw.Recommended != nil && *raw.Recommended {
			recommended++
			option.Recommended = true
		}
		options = append(options, option)
	}
	if recommended > 1 {
		return AskUserQuestion{}, fmt.Errorf("ask_user_question question %q allows at most one recommended option", input.ID)
	}
	return AskUserQuestion{
		ID: input.ID, Header: strings.TrimSpace(input.Header), Prompt: input.Prompt, Description: input.Description,
		Mode: input.Mode, AllowCustom: *input.AllowCustom, Options: options,
	}, nil
}

func validateQuestionOptionURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("not a valid URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return errors.New("must use http or https")
	}
	if parsed.User != nil {
		return errors.New("must not include credentials")
	}
	host := parsed.Hostname()
	if host == "" {
		return errors.New("must include a host")
	}
	lowered := strings.ToLower(strings.TrimSuffix(host, "."))
	if lowered == "localhost" || strings.HasSuffix(lowered, ".localhost") ||
		strings.HasSuffix(lowered, ".local") || strings.HasSuffix(lowered, ".internal") ||
		strings.HasSuffix(lowered, ".invalid") {
		return errors.New("host is not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return errors.New("host is not allowed")
		}
	}
	return nil
}

// ValidateUserQuestionDecision checks a resolve body and returns the stored
// form: status, canonical answers JSON (nil for reply/skip), reply. Answers
// sort into question order with selections in option order.
func ValidateUserQuestionDecision(questionsRaw []byte, decision string, answers []UserQuestionAnswerInput, reply string) (status string, canonicalAnswers []byte, canonicalReply string, err error) {
	var questions []AskUserQuestion
	if err := json.Unmarshal(questionsRaw, &questions); err != nil || len(questions) == 0 {
		return "", nil, "", errors.New("the stored questions are not valid")
	}
	byID := make(map[string]AskUserQuestion, len(questions))
	for _, question := range questions {
		byID[question.ID] = question
	}
	switch decision {
	case "answer":
		hasAnswers := len(answers) > 0
		hasReply := strings.TrimSpace(reply) != ""
		if hasAnswers == hasReply {
			return "", nil, "", errors.New("answer needs exactly one of the complete answers array or a direct reply")
		}
		if hasReply {
			if len(reply) > askUserQuestionMaxReplyBytes {
				return "", nil, "", fmt.Errorf("reply must be at most %d bytes", askUserQuestionMaxReplyBytes)
			}
			return "answered", nil, strings.TrimSpace(reply), nil
		}
		canonical, err := canonicalizeUserQuestionAnswers(questions, byID, answers)
		if err != nil {
			return "", nil, "", err
		}
		encoded, err := json.Marshal(canonical)
		if err != nil {
			return "", nil, "", errors.New("the answers could not be stored")
		}
		return "answered", encoded, "", nil
	case "skip":
		if len(answers) > 0 || strings.TrimSpace(reply) != "" {
			return "", nil, "", errors.New("skip carries neither answers nor a reply")
		}
		return "skipped", nil, "", nil
	default:
		return "", nil, "", errors.New("decision must be answer or skip")
	}
}

// UserQuestionAnswerInput is one submitted answer before canonicalization.
type UserQuestionAnswerInput struct {
	QuestionID        string   `json:"question_id"`
	SelectedOptionIDs []string `json:"selected_option_ids"`
	CustomText        string   `json:"custom_text"`
	Skipped           bool     `json:"skipped"`
}

func canonicalizeUserQuestionAnswers(questions []AskUserQuestion, byID map[string]AskUserQuestion, answers []UserQuestionAnswerInput) ([]UserQuestionAnswer, error) {
	if len(answers) != len(questions) {
		return nil, fmt.Errorf("answer must cover all %d questions exactly once, got %d", len(questions), len(answers))
	}
	seen := make(map[string]bool, len(answers))
	byQuestionID := make(map[string]UserQuestionAnswerInput, len(answers))
	for _, answer := range answers {
		question, ok := byID[answer.QuestionID]
		if !ok || question.ID == "" {
			return nil, fmt.Errorf("answer references unknown question %q", answer.QuestionID)
		}
		if seen[answer.QuestionID] {
			return nil, fmt.Errorf("answer covers question %q more than once", answer.QuestionID)
		}
		seen[answer.QuestionID] = true
		byQuestionID[answer.QuestionID] = answer
	}
	canonical := make([]UserQuestionAnswer, 0, len(questions))
	for _, question := range questions {
		answer := byQuestionID[question.ID]
		resolved, err := canonicalizeOneUserQuestionAnswer(question, answer)
		if err != nil {
			return nil, err
		}
		canonical = append(canonical, resolved)
	}
	return canonical, nil
}

func canonicalizeOneUserQuestionAnswer(question AskUserQuestion, answer UserQuestionAnswerInput) (UserQuestionAnswer, error) {
	if answer.Skipped {
		if len(answer.SelectedOptionIDs) > 0 || strings.TrimSpace(answer.CustomText) != "" {
			return UserQuestionAnswer{}, fmt.Errorf("skipped answer for question %q must carry no selections or text", question.ID)
		}
		return UserQuestionAnswer{QuestionID: question.ID, SelectedOptionIDs: []string{}, Skipped: true}, nil
	}
	custom := strings.TrimSpace(answer.CustomText)
	if custom != "" && !question.AllowCustom {
		return UserQuestionAnswer{}, fmt.Errorf("question %q does not accept custom text", question.ID)
	}
	if len(answer.CustomText) > askUserQuestionMaxTextBytes {
		return UserQuestionAnswer{}, fmt.Errorf("custom text for question %q must be at most %d bytes", question.ID, askUserQuestionMaxTextBytes)
	}
	known := make(map[string]int, len(question.Options))
	for i, option := range question.Options {
		known[option.ID] = i
	}
	picked := make(map[string]bool, len(answer.SelectedOptionIDs))
	for _, id := range answer.SelectedOptionIDs {
		order, ok := known[id]
		if !ok || order < 0 {
			return UserQuestionAnswer{}, fmt.Errorf("answer for question %q selects unknown option %q", question.ID, id)
		}
		if picked[id] {
			return UserQuestionAnswer{}, fmt.Errorf("answer for question %q selects option %q more than once", question.ID, id)
		}
		picked[id] = true
	}
	ordered := make([]string, 0, len(picked))
	for _, option := range question.Options {
		if picked[option.ID] {
			ordered = append(ordered, option.ID)
		}
	}
	switch question.Mode {
	case "single":
		if len(ordered) > 0 && custom != "" {
			return UserQuestionAnswer{}, fmt.Errorf("single-choice question %q takes one selected option or custom text, not both", question.ID)
		}
		if len(ordered) > 1 {
			return UserQuestionAnswer{}, fmt.Errorf("single-choice question %q takes at most one selected option", question.ID)
		}
	default:
		if len(ordered) > len(question.Options) {
			return UserQuestionAnswer{}, fmt.Errorf("answer for question %q selects more options than exist", question.ID)
		}
	}
	if len(ordered) == 0 && custom == "" {
		return UserQuestionAnswer{}, fmt.Errorf("answer for question %q needs a selection or custom text", question.ID)
	}
	return UserQuestionAnswer{QuestionID: question.ID, SelectedOptionIDs: ordered, CustomText: custom}, nil
}

// FormatUserQuestionToolResult renders the full answer set as the ONE tool
// result the resumed turn feeds the model. A whole-set skip renders from
// status: it carries no answers, and an empty list would read as none.
func FormatUserQuestionToolResult(questions []AskUserQuestion, status string, answers []UserQuestionAnswer, reply string) (content, summary string) {
	labels := make(map[string]map[string]string, len(questions))
	questionHeaders := make(map[string]string, len(questions))
	for _, question := range questions {
		row := make(map[string]string, len(question.Options))
		for _, option := range question.Options {
			row[option.ID] = option.Label
		}
		labels[question.ID] = row
		questionHeaders[question.ID] = question.Header
	}
	var builder strings.Builder
	builder.WriteString("The user answered your questions. Use these answers to continue; do not re-ask them.")
	if status == "skipped" {
		builder.WriteString("\nThe user skipped the whole set. A skip is explicit: it is not consent and not permission to choose defaults. Proceed without the requested preferences or explain what you need next.")
		return capQuestionToolResult(builder.String()), "user skipped the questions"
	}
	if strings.TrimSpace(reply) != "" {
		builder.WriteString("\nDirect reply to the whole set: ")
		builder.WriteString(strings.TrimSpace(reply))
		capped := capQuestionToolResult(builder.String())
		return capped, "user replied directly to the questions"
	}
	answered, skipped := 0, 0
	for _, answer := range answers {
		builder.WriteString("\n- ")
		if header := questionHeaders[answer.QuestionID]; header != "" {
			builder.WriteString(header)
			builder.WriteString(" (")
			builder.WriteString(answer.QuestionID)
			builder.WriteString(")")
		} else {
			builder.WriteString(answer.QuestionID)
		}
		builder.WriteString(": ")
		if answer.Skipped {
			builder.WriteString("skipped by the user; this is not consent and not permission to choose defaults")
			skipped++
			continue
		}
		answered++
		parts := make([]string, 0, 2)
		if len(answer.SelectedOptionIDs) > 0 {
			names := make([]string, 0, len(answer.SelectedOptionIDs))
			for _, id := range answer.SelectedOptionIDs {
				if label, ok := labels[answer.QuestionID][id]; ok && label != "" {
					names = append(names, label+" ("+id+")")
				} else {
					names = append(names, id)
				}
			}
			parts = append(parts, "selected: "+strings.Join(names, ", "))
		}
		if strings.TrimSpace(answer.CustomText) != "" {
			parts = append(parts, "custom: "+strings.TrimSpace(answer.CustomText))
		}
		builder.WriteString(strings.Join(parts, "; "))
	}
	summary = fmt.Sprintf("user answered %d question(s), skipped %d", answered, skipped)
	return capQuestionToolResult(builder.String()), summary
}

func capQuestionToolResult(content string) string {
	if len(content) <= askUserQuestionToolResultBytes {
		return content
	}
	cut := askUserQuestionToolResultBytes
	for cut > 0 && cut < len(content) && (content[cut]&0xC0) == 0x80 {
		cut--
	}
	return content[:cut] + "\u2026[truncated]"
}

// MarshalUserQuestionRequest renders the shared entity shape (RFC3339
// times; empty reply/answers/answered_at absent) for REST and SSE alike.
func MarshalUserQuestionRequest(id, turnID, toolCallID, status string, questions, answers json.RawMessage, reply string, createdAt time.Time, answeredAt *time.Time) ([]byte, error) {
	entity := map[string]any{
		"id": id, "turn_id": turnID, "tool_call_id": toolCallID, "status": status,
		"questions":  json.RawMessage(append([]byte(nil), questions...)),
		"created_at": createdAt.UTC().Format(time.RFC3339),
	}
	if len(bytes.TrimSpace(answers)) > 0 && string(bytes.TrimSpace(answers)) != "null" {
		entity["answers"] = json.RawMessage(append([]byte(nil), answers...))
	}
	if strings.TrimSpace(reply) != "" {
		entity["reply"] = strings.TrimSpace(reply)
	}
	if answeredAt != nil && !answeredAt.IsZero() {
		entity["answered_at"] = answeredAt.UTC().Format(time.RFC3339)
	}
	return json.Marshal(entity)
}
