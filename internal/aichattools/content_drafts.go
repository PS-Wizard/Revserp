package aichattools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

// Content draft tools. They only serve draft-scoped conversations: the draft
// id always comes from Scope.ContentDraftID (server-derived), never from
// model arguments, plus Scope.ProjectID. Normal conversations never see
// these tools because the worker only adds them to the draft registry.
const (
	contentDraftToolRead    = "read_draft"
	contentDraftToolOutline = "set_outline"
	contentDraftToolWrite   = "write_section"
	contentDraftToolEdit    = "edit_draft"

	contentDraftMaxTitle           = 200
	contentDraftMaxMetaTitle       = 200
	contentDraftMaxMetaDescription = 500
	contentDraftMaxTargetKeyword   = 200
	contentDraftMaxHeading         = 200
	contentDraftMaxSections        = 30
	contentDraftMaxEdits           = 20
	contentDraftMaxEditText        = 20000
	contentDraftMaxSectionContent  = 200000
	contentDraftMaxBodyBytes       = 500000
	contentDraftReadBodyBytes      = 200000
	contentDraftWriteArgsBytes     = 256 << 10
)

const readDraftSchema = `{
  "type": "object",
  "properties": {},
  "additionalProperties": false
}`

const readDraftDescription = "Read the conversation's content draft: title, meta title, meta description, target keyword, and the current markdown body. Call this before editing existing text, because the user may have edited the draft directly beside the chat. No arguments."

const setOutlineSchema = `{
  "type": "object",
  "required": ["title", "sections"],
  "properties": {
    "title": {"type": "string", "minLength": 1, "maxLength": 200},
    "meta_title": {"type": "string", "maxLength": 200},
    "meta_description": {"type": "string", "maxLength": 500},
    "target_keyword": {"type": "string", "maxLength": 200},
    "sections": {
      "type": "array", "minItems": 1, "maxItems": 30,
      "items": {
        "type": "object",
        "properties": {
          "heading": {"type": "string", "minLength": 1, "maxLength": 200},
          "level": {"type": "integer", "enum": [2, 3]}
        },
        "required": ["heading", "level"],
        "additionalProperties": false
      }
    }
  },
  "additionalProperties": false
}`

const setOutlineDescription = "Replace the draft body with a heading skeleton (## for level 2, ### for level 3, sections still empty) and set its title and optional meta fields. Use for a new piece after the angle is settled. Sections need at least 1 and at most 30 entries."

const writeSectionSchema = `{
  "type": "object",
  "required": ["heading", "content"],
  "properties": {
    "heading": {"type": "string", "minLength": 1, "maxLength": 200, "description": "Exact section heading text (matches case-insensitively)."},
    "content": {"type": "string", "minLength": 1, "maxLength": 200000, "description": "Markdown for the heading's own body. Do NOT include the heading line itself and do NOT include other headings; subsections need their own write_section calls."}
  },
  "additionalProperties": false
}`

const writeSectionDescription = "Write one draft heading's own body: replaces only the text between the named heading line and the next heading of any level (or end of body); subsections below it are left intact. Match the heading exactly (case-insensitive); the call fails listing available headings when it is missing or ambiguous. Content is section markdown only, never the heading line, and must not contain headings of its own. Call write_section once per heading (including ### subsections), in order, or use set_outline to change structure."

const editDraftSchema = `{
  "type": "object",
  "properties": {
    "edits": {
      "type": "array", "minItems": 0, "maxItems": 20,
      "items": {
        "type": "object",
        "properties": {
          "old": {"type": "string", "minLength": 1, "maxLength": 20000},
          "new": {"type": "string", "maxLength": 20000}
        },
        "required": ["old", "new"],
        "additionalProperties": false
      }
    },
    "title": {"type": "string", "minLength": 1, "maxLength": 200},
    "meta_title": {"type": "string", "maxLength": 200},
    "meta_description": {"type": "string", "maxLength": 500},
    "target_keyword": {"type": "string", "maxLength": 200}
  },
  "additionalProperties": false
}`

const editDraftDescription = "Polish the draft with small exact-text replacements, applied in order. Each old string must occur exactly once in the current body; the whole call fails with no changes when any edit misses or matches more than once. At most 20 edits. Optional title/meta fields update the draft metadata in the same call. Use for the final polish pass, not for writing sections."

// ContentDraftDefs lists the draft tools in call order. They stay out of
// CatalogDefs on purpose: the admin drawer and denylist validation describe
// normal chats, and draft availability is scope-based, not catalog-based.
func ContentDraftDefs() []Def {
	return []Def{readDraftTool().Def, setOutlineTool().Def, writeSectionTool().Def, editDraftTool().Def}
}

// NewContentDraftRegistry returns the normal served tools plus the four
// draft tools. The turn denylist still applies to both halves.
func NewContentDraftRegistry(blocked []string) *Registry {
	registry := NewFilteredRegistry(blocked)
	deny := make(map[string]bool, len(blocked))
	for _, name := range blocked {
		deny[name] = true
	}
	for _, tool := range []Tool{readDraftTool(), setOutlineTool(), writeSectionTool(), editDraftTool()} {
		if deny[tool.Def.Name] {
			continue
		}
		_ = registry.Add(tool)
	}
	return registry
}

func readDraftTool() Tool {
	return Tool{
		Def:     Def{Name: contentDraftToolRead, Label: "Read draft", Description: readDraftDescription, Schema: json.RawMessage(readDraftSchema)},
		Execute: executeReadDraft,
	}
}

func setOutlineTool() Tool {
	return Tool{
		Def:     Def{Name: contentDraftToolOutline, Label: "Set outline", Description: setOutlineDescription, Schema: json.RawMessage(setOutlineSchema)},
		Execute: executeSetOutline,
	}
}

func writeSectionTool() Tool {
	return Tool{
		Def:     Def{Name: contentDraftToolWrite, Label: "Write section", Description: writeSectionDescription, Schema: json.RawMessage(writeSectionSchema)},
		Execute: executeWriteSection,
	}
}

func editDraftTool() Tool {
	return Tool{
		Def:     Def{Name: contentDraftToolEdit, Label: "Edit draft", Description: editDraftDescription, Schema: json.RawMessage(editDraftSchema)},
		Execute: executeEditDraft,
	}
}

// outlineSection is one skeleton heading: level 2 renders ##, level 3 ###.
type outlineSection struct {
	Heading string
	Level   int
}

// draftEdit is one exact-text replacement.
type draftEdit struct {
	Old string
	New string
}

// renderOutline renders the heading skeleton: one markdown heading per
// line, blank line between sections, trailing newline, no body text.
func renderOutline(sections []outlineSection) string {
	parts := make([]string, 0, len(sections))
	for _, section := range sections {
		parts = append(parts, strings.Repeat("#", section.Level)+" "+section.Heading)
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// draftHeading is one parsed markdown heading line.
type draftHeading struct {
	Index int
	Level int
	Text  string
}

// parseDraftHeadings lists every ATX heading line in order, ignoring lines
// inside fenced code blocks (``` or ~~~) where # starts no heading.
func parseDraftHeadings(body string) []draftHeading {
	var headings []draftHeading
	inFence := false
	for i, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			continue
		}
		level := 0
		for level < len(trimmed) && trimmed[level] == '#' {
			level++
		}
		if level == 0 || level > 6 || level == len(trimmed) || (trimmed[level] != ' ' && trimmed[level] != '\t') {
			continue
		}
		text := strings.TrimSpace(trimmed[level:])
		if text == "" {
			continue
		}
		headings = append(headings, draftHeading{Index: i, Level: level, Text: text})
	}
	return headings
}

// availableDraftHeadings renders the heading list for not-found errors.
func availableDraftHeadings(headings []draftHeading) string {
	names := make([]string, 0, len(headings))
	for _, heading := range headings {
		names = append(names, fmt.Sprintf("%q", heading.Text))
	}
	if len(names) == 0 {
		return "the draft has no headings yet"
	}
	return "available headings: " + strings.Join(names, ", ")
}

// stripLeadingHeadingLine drops a leading heading line that repeats the
// target heading, so model-included heading lines never duplicate.
func stripLeadingHeadingLine(content, heading string) string {
	lines := strings.Split(content, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) {
		return content
	}
	text := strings.TrimSpace(lines[start])
	for len(text) > 0 && text[0] == '#' {
		text = strings.TrimSpace(text[1:])
	}
	if text != "" && strings.EqualFold(text, strings.TrimSpace(heading)) {
		return strings.Join(lines[start+1:], "\n")
	}
	return content
}

// replaceSection swaps only the named heading's own direct body: the text
// between the heading line and the next heading of any level (or end of
// body). Subsection headings below it are left intact; each heading is
// written with its own call. Matching is case-insensitive after trimming;
// missing or ambiguous headings error. Content carrying its own headings is
// rejected (after stripping a leading repeat of the target heading) so one
// call can never swallow sibling sections.
func replaceSection(body, heading, content string) (string, error) {
	want := strings.TrimSpace(heading)
	lines := strings.Split(body, "\n")
	headings := parseDraftHeadings(body)
	var matches []draftHeading
	for _, heading := range headings {
		if strings.EqualFold(heading.Text, want) {
			matches = append(matches, heading)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("heading %q not found; %s", want, availableDraftHeadings(headings))
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("heading %q matches %d sections; make it unique, %s", want, len(matches), availableDraftHeadings(headings))
	}
	target := matches[0]
	end := len(lines)
	for _, heading := range headings {
		if heading.Index > target.Index {
			end = heading.Index
			break
		}
	}
	stripped := strings.TrimSpace(stripLeadingHeadingLine(content, want))
	if inner := parseDraftHeadings(stripped); len(inner) > 0 {
		return "", fmt.Errorf("content must not contain headings; call write_section per heading, or use set_outline to change structure")
	}
	trimmed := stripped
	var builder strings.Builder
	for _, line := range lines[:target.Index+1] {
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	if trimmed != "" {
		builder.WriteString("\n")
		builder.WriteString(trimmed)
		builder.WriteString("\n")
	}
	for _, line := range lines[end:] {
		builder.WriteString("\n")
		builder.WriteString(line)
	}
	return normalizeDraftBlankLines(builder.String()), nil
}

// normalizeDraftBlankLines collapses 3+ consecutive newlines to two.
func normalizeDraftBlankLines(body string) string {
	for strings.Contains(body, "\n\n\n") {
		body = strings.ReplaceAll(body, "\n\n\n", "\n\n")
	}
	return body
}

// applyDraftEdits applies exact-text replacements sequentially and
// all-or-nothing: the first miss or ambiguous match aborts with no changes.
func applyDraftEdits(body string, edits []draftEdit) (string, error) {
	updated := body
	for i, edit := range edits {
		if edit.Old == "" {
			return "", fmt.Errorf("edit %d failed: old text must not be empty", i+1)
		}
		count := strings.Count(updated, edit.Old)
		if count == 0 {
			return "", fmt.Errorf("edit %d failed: old text not found", i+1)
		}
		if count > 1 {
			return "", fmt.Errorf("edit %d failed: old text matches %d locations; make it unique", i+1, count)
		}
		updated = strings.Replace(updated, edit.Old, edit.New, 1)
	}
	return updated, nil
}

func countDraftWords(text string) int {
	return len(strings.Fields(text))
}

func requireDraftScope(s Scope) error {
	if s.Queries == nil || s.DB == nil {
		return errors.New("draft scope has no database access")
	}
	if !s.ContentDraftID.Valid {
		return errors.New("this conversation has no content draft attached")
	}
	if !s.ProjectID.Valid {
		return errors.New("draft scope has no project")
	}
	return nil
}

func draftToolError(name string, err error) (Result, error) {
	return Result{Content: name + " error: " + err.Error()}, nil
}

// withLockedDraft runs fn inside a transaction holding the draft row lock,
// so a concurrent chat write and a direct user edit serialize instead of
// clobbering each other.
func withLockedDraft(ctx context.Context, s Scope, fn func(ctx context.Context, q *sqlc.Queries) (Result, error)) (Result, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("begin draft transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var locked pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM content_drafts WHERE id = $1 AND project_id = $2 FOR UPDATE`, s.ContentDraftID, s.ProjectID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, &modelError{msg: "content draft not found"}
		}
		return Result{}, fmt.Errorf("lock content draft: %w", err)
	}
	result, err := fn(ctx, s.Queries.WithTx(tx))
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("commit draft transaction: %w", err)
	}
	return result, nil
}

func draftModelResult(name string, res Result, err error) (Result, error) {
	if err != nil {
		if _, ok := err.(*modelError); ok {
			return draftToolError(name, err)
		}
		return Result{}, err
	}
	return res, nil
}

func executeReadDraft(ctx context.Context, raw json.RawMessage, s Scope) (Result, error) {
	if err := requireDraftScope(s); err != nil {
		return draftToolError(contentDraftToolRead, err)
	}
	if fields, err := strictJSONFields(raw); err != nil {
		return draftToolError(contentDraftToolRead, err)
	} else if len(fields) != 0 {
		return draftToolError(contentDraftToolRead, errors.New("read_draft takes no arguments"))
	}
	draft, err := s.Queries.GetContentDraftForProject(ctx, sqlc.GetContentDraftForProjectParams{
		ID:        s.ContentDraftID,
		ProjectID: s.ProjectID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return draftToolError(contentDraftToolRead, errors.New("content draft not found"))
		}
		return Result{}, fmt.Errorf("%s: read draft: %w", contentDraftToolRead, err)
	}
	body := draft.Body
	if len(body) > contentDraftReadBodyBytes {
		cut := contentDraftReadBodyBytes
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut] + "\n\n…[body truncated]"
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "Title: %s\n", draft.Title)
	fmt.Fprintf(&builder, "Meta title: %s\n", draft.MetaTitle)
	fmt.Fprintf(&builder, "Meta description: %s\n", draft.MetaDescription)
	fmt.Fprintf(&builder, "Target keyword: %s\n", draft.TargetKeyword)
	builder.WriteString("Body:\n")
	builder.WriteString(body)
	return Result{
		Content: builder.String(),
		Summary: fmt.Sprintf("Draft %q read (%d words)", draft.Title, countDraftWords(draft.Body)),
	}, nil
}

type setOutlineArgs struct {
	Title           string
	MetaTitle       *string
	MetaDescription *string
	TargetKeyword   *string
	Sections        []outlineSection
}

func parseSetOutlineArgs(raw json.RawMessage) (setOutlineArgs, error) {
	var args setOutlineArgs
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for _, key := range []string{"title", "sections"} {
		if _, ok := fields[key]; !ok {
			return args, fmt.Errorf("missing required argument %q", key)
		}
	}
	for key, value := range fields {
		switch key {
		case "title":
			title, err := draftStringArg(key, value, 1, contentDraftMaxTitle)
			if err != nil {
				return args, err
			}
			args.Title = title
		case "meta_title":
			text, err := draftOptionalStringArg(key, value, contentDraftMaxMetaTitle)
			if err != nil {
				return args, err
			}
			args.MetaTitle = text
		case "meta_description":
			text, err := draftOptionalStringArg(key, value, contentDraftMaxMetaDescription)
			if err != nil {
				return args, err
			}
			args.MetaDescription = text
		case "target_keyword":
			text, err := draftOptionalStringArg(key, value, contentDraftMaxTargetKeyword)
			if err != nil {
				return args, err
			}
			args.TargetKeyword = text
		case "sections":
			sections, err := parseOutlineSections(value)
			if err != nil {
				return args, err
			}
			args.Sections = sections
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	return args, nil
}

func parseOutlineSections(value json.RawMessage) ([]outlineSection, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(value, &items); err != nil {
		return nil, errors.New(`argument "sections" must be an array`)
	}
	if len(items) < 1 || len(items) > contentDraftMaxSections {
		return nil, fmt.Errorf("argument \"sections\" must have between 1 and %d entries", contentDraftMaxSections)
	}
	sections := make([]outlineSection, len(items))
	for i := range items {
		fields, err := strictJSONFields(items[i])
		if err != nil {
			return nil, fmt.Errorf("sections[%d] must be a JSON object", i)
		}
		for _, key := range []string{"heading", "level"} {
			if _, ok := fields[key]; !ok {
				return nil, fmt.Errorf("sections[%d] missing required field %q", i, key)
			}
		}
		for key, value := range fields {
			switch key {
			case "heading":
				heading, err := draftStringArg(fmt.Sprintf("sections[%d].heading", i), value, 1, contentDraftMaxHeading)
				if err != nil {
					return nil, err
				}
				sections[i].Heading = heading
			case "level":
				var level int
				if err := json.Unmarshal(value, &level); err != nil {
					return nil, fmt.Errorf("sections[%d].level must be 2 or 3", i)
				}
				if level != 2 && level != 3 {
					return nil, fmt.Errorf("sections[%d].level must be 2 or 3", i)
				}
				sections[i].Level = level
			default:
				return nil, fmt.Errorf("unknown field %q in sections[%d]", key, i)
			}
		}
	}
	return sections, nil
}

func draftStringArg(name string, value json.RawMessage, minRunes, maxRunes int) (string, error) {
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return "", fmt.Errorf("argument %q must be a string", name)
	}
	text = strings.TrimSpace(text)
	n := utf8.RuneCountInString(text)
	if n < minRunes || n > maxRunes {
		if minRunes <= 1 {
			return "", fmt.Errorf("argument %q must be between 1 and %d characters after trimming", name, maxRunes)
		}
		return "", fmt.Errorf("argument %q must be between %d and %d characters after trimming", name, minRunes, maxRunes)
	}
	return text, nil
}

func draftOptionalStringArg(name string, value json.RawMessage, maxRunes int) (*string, error) {
	text, err := draftStringArg(name, value, 0, maxRunes)
	if err != nil {
		return nil, err
	}
	return &text, nil
}

func executeSetOutline(ctx context.Context, raw json.RawMessage, s Scope) (Result, error) {
	if len(raw) > contentDraftWriteArgsBytes {
		return draftToolError(contentDraftToolOutline, errors.New("arguments too large"))
	}
	if err := requireDraftScope(s); err != nil {
		return draftToolError(contentDraftToolOutline, err)
	}
	args, err := parseSetOutlineArgs(raw)
	if err != nil {
		return draftToolError(contentDraftToolOutline, err)
	}
	res, err := withLockedDraft(ctx, s, func(ctx context.Context, q *sqlc.Queries) (Result, error) {
		updated, err := q.UpdateContentDraftForProject(ctx, sqlc.UpdateContentDraftForProjectParams{
			ID:              s.ContentDraftID,
			ProjectID:       s.ProjectID,
			Title:           pgtype.Text{String: args.Title, Valid: true},
			MetaTitle:       draftNullText(args.MetaTitle),
			MetaDescription: draftNullText(args.MetaDescription),
			TargetKeyword:   draftNullText(args.TargetKeyword),
			Body:            pgtype.Text{String: renderOutline(args.Sections), Valid: true},
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Result{}, &modelError{msg: "content draft not found"}
			}
			return Result{}, fmt.Errorf("%s: update draft: %w", contentDraftToolOutline, err)
		}
		return Result{
			Content: fmt.Sprintf("Outline set: %d sections.", len(args.Sections)),
			Summary: fmt.Sprintf("Outline set: %d sections in %q", len(args.Sections), updated.Title),
		}, nil
	})
	return draftModelResult(contentDraftToolOutline, res, err)
}

func draftNullText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func executeWriteSection(ctx context.Context, raw json.RawMessage, s Scope) (Result, error) {
	if len(raw) > contentDraftWriteArgsBytes {
		return draftToolError(contentDraftToolWrite, errors.New("arguments too large"))
	}
	if err := requireDraftScope(s); err != nil {
		return draftToolError(contentDraftToolWrite, err)
	}
	fields, err := strictJSONFields(raw)
	if err != nil {
		return draftToolError(contentDraftToolWrite, err)
	}
	for _, key := range []string{"heading", "content"} {
		if _, ok := fields[key]; !ok {
			return draftToolError(contentDraftToolWrite, fmt.Errorf("missing required argument %q", key))
		}
	}
	var heading, content string
	for key, value := range fields {
		switch key {
		case "heading":
			heading, err = draftStringArg(key, value, 1, contentDraftMaxHeading)
			if err != nil {
				return draftToolError(contentDraftToolWrite, err)
			}
		case "content":
			content, err = draftStringArg(key, value, 1, contentDraftMaxSectionContent)
			if err != nil {
				return draftToolError(contentDraftToolWrite, err)
			}
		default:
			return draftToolError(contentDraftToolWrite, fmt.Errorf("unknown argument %q", key))
		}
	}
	res, err := withLockedDraft(ctx, s, func(ctx context.Context, q *sqlc.Queries) (Result, error) {
		draft, err := q.GetContentDraftForProject(ctx, sqlc.GetContentDraftForProjectParams{
			ID:        s.ContentDraftID,
			ProjectID: s.ProjectID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Result{}, &modelError{msg: "content draft not found"}
			}
			return Result{}, fmt.Errorf("%s: read draft: %w", contentDraftToolWrite, err)
		}
		updated, err := replaceSection(draft.Body, heading, content)
		if err != nil {
			return Result{}, &modelError{msg: err.Error()}
		}
		if len(updated) > contentDraftMaxBodyBytes {
			return Result{}, &modelError{msg: fmt.Sprintf("section too long: draft would exceed %d bytes", contentDraftMaxBodyBytes)}
		}
		if _, err := q.UpdateContentDraftForProject(ctx, sqlc.UpdateContentDraftForProjectParams{
			ID:        s.ContentDraftID,
			ProjectID: s.ProjectID,
			Body:      pgtype.Text{String: updated, Valid: true},
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Result{}, &modelError{msg: "content draft not found"}
			}
			return Result{}, fmt.Errorf("%s: update draft: %w", contentDraftToolWrite, err)
		}
		trimmed := strings.TrimSpace(heading)
		words := countDraftWords(strings.TrimSpace(stripLeadingHeadingLine(content, trimmed)))
		return Result{
			Content: fmt.Sprintf("Wrote section %q (%d words).", trimmed, words),
			Summary: fmt.Sprintf("Wrote %q (%d words)", trimmed, words),
		}, nil
	})
	return draftModelResult(contentDraftToolWrite, res, err)
}

type editDraftArgs struct {
	Edits           []draftEdit
	Title           *string
	MetaTitle       *string
	MetaDescription *string
	TargetKeyword   *string
}

func parseEditDraftArgs(raw json.RawMessage) (editDraftArgs, error) {
	var args editDraftArgs
	fields, err := strictJSONFields(raw)
	if err != nil {
		return args, err
	}
	for key, value := range fields {
		switch key {
		case "edits":
			var items []json.RawMessage
			if err := json.Unmarshal(value, &items); err != nil {
				return args, errors.New(`argument "edits" must be an array`)
			}
			if len(items) > contentDraftMaxEdits {
				return args, fmt.Errorf("argument \"edits\" must have at most %d entries", contentDraftMaxEdits)
			}
			args.Edits = make([]draftEdit, len(items))
			for i := range items {
				editFields, err := strictJSONFields(items[i])
				if err != nil {
					return args, fmt.Errorf("edits[%d] must be a JSON object", i)
				}
				for _, field := range []string{"old", "new"} {
					if _, ok := editFields[field]; !ok {
						return args, fmt.Errorf("edits[%d] missing required field %q", i, field)
					}
				}
				for field, fieldValue := range editFields {
					switch field {
					case "old":
						old, err := draftStringArg(fmt.Sprintf("edits[%d].old", i), fieldValue, 1, contentDraftMaxEditText)
						if err != nil {
							return args, err
						}
						args.Edits[i].Old = old
					case "new":
						var text string
						if err := json.Unmarshal(fieldValue, &text); err != nil {
							return args, fmt.Errorf("edits[%d].new must be a string", i)
						}
						if utf8.RuneCountInString(text) > contentDraftMaxEditText {
							return args, fmt.Errorf("edits[%d].new must be at most %d characters", i, contentDraftMaxEditText)
						}
						args.Edits[i].New = text
					default:
						return args, fmt.Errorf("unknown field %q in edits[%d]", field, i)
					}
				}
			}
		case "title":
			title, err := draftStringArg(key, value, 1, contentDraftMaxTitle)
			if err != nil {
				return args, err
			}
			args.Title = &title
		case "meta_title":
			text, err := draftOptionalStringArg(key, value, contentDraftMaxMetaTitle)
			if err != nil {
				return args, err
			}
			args.MetaTitle = text
		case "meta_description":
			text, err := draftOptionalStringArg(key, value, contentDraftMaxMetaDescription)
			if err != nil {
				return args, err
			}
			args.MetaDescription = text
		case "target_keyword":
			text, err := draftOptionalStringArg(key, value, contentDraftMaxTargetKeyword)
			if err != nil {
				return args, err
			}
			args.TargetKeyword = text
		default:
			return args, fmt.Errorf("unknown argument %q", key)
		}
	}
	return args, nil
}

func executeEditDraft(ctx context.Context, raw json.RawMessage, s Scope) (Result, error) {
	if len(raw) > contentDraftWriteArgsBytes {
		return draftToolError(contentDraftToolEdit, errors.New("arguments too large"))
	}
	if err := requireDraftScope(s); err != nil {
		return draftToolError(contentDraftToolEdit, err)
	}
	args, err := parseEditDraftArgs(raw)
	if err != nil {
		return draftToolError(contentDraftToolEdit, err)
	}
	if len(args.Edits) == 0 && args.Title == nil && args.MetaTitle == nil && args.MetaDescription == nil && args.TargetKeyword == nil {
		return draftToolError(contentDraftToolEdit, errors.New("no edits or metadata provided"))
	}
	res, err := withLockedDraft(ctx, s, func(ctx context.Context, q *sqlc.Queries) (Result, error) {
		draft, err := q.GetContentDraftForProject(ctx, sqlc.GetContentDraftForProjectParams{
			ID:        s.ContentDraftID,
			ProjectID: s.ProjectID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Result{}, &modelError{msg: "content draft not found"}
			}
			return Result{}, fmt.Errorf("%s: read draft: %w", contentDraftToolEdit, err)
		}
		updated, err := applyDraftEdits(draft.Body, args.Edits)
		if err != nil {
			return Result{}, &modelError{msg: err.Error()}
		}
		if len(updated) > contentDraftMaxBodyBytes {
			return Result{}, &modelError{msg: fmt.Sprintf("edits too long: draft would exceed %d bytes", contentDraftMaxBodyBytes)}
		}
		params := sqlc.UpdateContentDraftForProjectParams{
			ID:        s.ContentDraftID,
			ProjectID: s.ProjectID,
			Body:      pgtype.Text{String: updated, Valid: true},
		}
		var meta []string
		if args.Title != nil {
			params.Title = pgtype.Text{String: *args.Title, Valid: true}
			meta = append(meta, "title")
		}
		if args.MetaTitle != nil {
			params.MetaTitle = pgtype.Text{String: *args.MetaTitle, Valid: true}
			meta = append(meta, "meta title")
		}
		if args.MetaDescription != nil {
			params.MetaDescription = pgtype.Text{String: *args.MetaDescription, Valid: true}
			meta = append(meta, "meta description")
		}
		if args.TargetKeyword != nil {
			params.TargetKeyword = pgtype.Text{String: *args.TargetKeyword, Valid: true}
			meta = append(meta, "target keyword")
		}
		if _, err := q.UpdateContentDraftForProject(ctx, params); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Result{}, &modelError{msg: "content draft not found"}
			}
			return Result{}, fmt.Errorf("%s: update draft: %w", contentDraftToolEdit, err)
		}
		summary := fmt.Sprintf("Applied %d %s", len(args.Edits), pluralizeDraftEdits(len(args.Edits)))
		if len(meta) > 0 {
			summary += "; updated " + strings.Join(meta, ", ")
		}
		if len(args.Edits) == 0 {
			summary = "Updated " + strings.Join(meta, ", ")
		}
		return Result{Content: summary + ".", Summary: summary}, nil
	})
	return draftModelResult(contentDraftToolEdit, res, err)
}

func pluralizeDraftEdits(n int) string {
	if n == 1 {
		return "edit"
	}
	return "edits"
}
