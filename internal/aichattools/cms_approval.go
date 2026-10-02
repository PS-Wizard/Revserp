package aichattools

// CMS write approval policy: PrepareCMSApproval decides whether one CMS tool
// call must be approved by the user before it may run, and builds the
// plain-text proposal an approval card shows.
//
// The rules exist in code, not in the model's system prompt, because a
// published-page write, a media library write and a menu write are not equally
// sensitive:
//
//   - Rune reads never need approval; Rune create/update always do, and an
//     update carries a snapshot of the record it is about to replace.
//   - WordPress reads never need approval, and neither does a dry run of a
//     reviewed tool whose schema really has that flag. Creating content in
//     draft (or with no status at all) needs none; creating it with any
//     other status does. Editing one existing post needs none only while a
//     fresh read proves that post is still a draft.
//   - Everything with a shared or site-wide effect (publishing, delete, undo,
//     restore, media, terms, comments, redirects, menus, widgets, site
//     identity, performance, raw meta, bulk and site-wide search/replace)
//     always needs approval, whatever the arguments say.
//
// A tool with no catalogue entry has no policy and needs no approval. That is
// the accepted risk of a live server adding tools: the known-sensitive policy
// below still applies in full, and exposure (not approval) is what the
// transport restricts.
//
// A helper error blocks the write; it never degrades into "run it anyway".
// A write to an existing post is snapshotted from the state it replaces, so
// approving it and resuming re-reads that state instead of trusting the
// approval card.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	// maxCMSApprovalTextBytes bounds the plain-text target/before/after
	// preview. These strings are untrusted remote content shown in a card.
	maxCMSApprovalTextBytes = 2000
	// maxCMSApprovalFieldBytes bounds one field inside that preview.
	maxCMSApprovalFieldBytes = 400
)

// CMSApprovalProposal is the approval decision plus what the user is asked to
// approve. Target, Before and After are plain text for display, never HTML,
// and never contain secrets. Snapshot is the bounded pre-write state to
// compare against when the approved call finally runs.
type CMSApprovalProposal struct {
	Required bool            `json:"required"`
	Target   string          `json:"target"`
	Before   string          `json:"before"`
	After    string          `json:"after"`
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
}

// PrepareCMSApproval decides whether toolName needs user approval before the
// handler may run it, for provider "rune" or "wordpress". toolName may carry
// the provider namespace (cms__ or wp__) or the original server name. A
// non-nil error blocks the call: an unknown provider, a name still carrying
// the other provider's namespace, malformed arguments and an unusable
// pre-write read all fail closed. A valid but uncatalogued name returns
// Required false: it has no approval policy.
//
// The returned proposal is meaningful only when Required is true; otherwise
// it is empty and the caller runs the tool directly. Snapshot is present when
// a pre-write read was available and worth comparing on resume: re-running
// this helper with the approved arguments must produce the same digest, and a
// changed digest or a newly required approval means the approved call must not
// run.
func PrepareCMSApproval(ctx context.Context, session RuneSession, provider string, toolName string, args json.RawMessage) (CMSApprovalProposal, error) {
	original, providerKey, err := cmsToolIdentity(provider, toolName)
	if err != nil {
		return CMSApprovalProposal{}, err
	}
	fields, err := decodeCMSArgs(args)
	if err != nil {
		return CMSApprovalProposal{}, fmt.Errorf("aichattools: %s %s: %w", providerKey, original, err)
	}
	if providerKey == CMSProviderWordPress {
		return prepareWordPressApproval(ctx, session, original, fields)
	}
	return prepareRuneApproval(ctx, session, original, fields)
}

// CMS provider keys. One active CMS per project; each has exactly one tool set.
const (
	CMSProviderRune      = "rune"
	CMSProviderWordPress = "wordpress"
)

// NormalizeCMSProvider maps the stored provider value and its short spellings
// to one canonical key, so callers never branch on raw database text.
func NormalizeCMSProvider(provider string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case CMSProviderRune, "cms":
		return CMSProviderRune, true
	case CMSProviderWordPress, "wp":
		return CMSProviderWordPress, true
	default:
		return "", false
	}
}

// cmsToolIdentity trims the provider's own namespace prefix and returns the
// original server tool name. A name namespaced by the other provider keeps its
// prefix and is rejected, so a WordPress call can never resolve to a Rune tool
// and vice versa.
func cmsToolIdentity(provider, toolName string) (original, providerKey string, err error) {
	providerKey, ok := NormalizeCMSProvider(provider)
	if !ok {
		return "", "", fmt.Errorf("aichattools: unknown CMS provider %q", provider)
	}
	prefix := RuneToolPrefix
	if providerKey == CMSProviderWordPress {
		prefix = WordPressToolPrefix
	}
	original = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(toolName), prefix))
	if original == "" {
		return "", "", fmt.Errorf("aichattools: empty %s CMS tool name", providerKey)
	}
	other := WordPressToolPrefix
	if providerKey == CMSProviderWordPress {
		other = RuneToolPrefix
	}
	if strings.HasPrefix(original, other) {
		return "", "", fmt.Errorf("aichattools: %s CMS tool %q carries a foreign namespace", providerKey, original)
	}
	return original, providerKey, nil
}

// decodeCMSArgs parses tool arguments as a field map. Empty or null arguments
// are an empty field set, not an error.
func decodeCMSArgs(args json.RawMessage) (map[string]json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(args))
	if trimmed == "" || trimmed == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil {
		return nil, errors.New("arguments are not a JSON object")
	}
	if fields == nil {
		return map[string]json.RawMessage{}, nil
	}
	return fields, nil
}

// cmsStringField returns a scalar string argument, or "" when absent or not a
// scalar. Callers treat a missing value as "not specified", which keeps the
// policy defaulting to the safe side.
func cmsStringField(fields map[string]json.RawMessage, key string) string {
	raw, ok := fields[key]
	if !ok {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}
	return ""
}

// cmsBoolField reports whether a boolean argument is explicitly true.
func cmsBoolField(fields map[string]json.RawMessage, key string) bool {
	raw, ok := fields[key]
	if !ok {
		return false
	}
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}

// ---------------------------------------------------------------------------
// Rune
// ---------------------------------------------------------------------------

// prepareRuneApproval keeps the Rune policy from before the approval feature:
// reads run, creates and updates need approval, and an update is previewed
// against the record it would replace.
func prepareRuneApproval(ctx context.Context, session RuneSession, original string, fields map[string]json.RawMessage) (CMSApprovalProposal, error) {
	switch original {
	case "list_collections", "get_collection_schema", "list_records", "read_record":
		return CMSApprovalProposal{}, nil
	case "create_record":
		return CMSApprovalProposal{
			Required: true,
			Target:   cmsApprovalText("New " + cmsRuneCollection(fields) + " record"),
			After:    cmsApprovalArgs(fields),
		}, nil
	case "update_record":
		snapshot, before, err := runeRecordSnapshot(ctx, session, original, fields)
		if err != nil {
			return CMSApprovalProposal{}, err
		}
		return CMSApprovalProposal{
			Required: true,
			Target:   cmsApprovalText("Update " + cmsRuneCollection(fields) + " record #" + cmsStringField(fields, "id")),
			Before:   before,
			After:    cmsApprovalArgs(fields),
			Snapshot: snapshot,
		}, nil
	default:
		// Unclassified Rune tool: no approval policy exists for it, so none is
		// required. The user accepts that a newly added write runs unapproved
		// until a rule is written here.
		return CMSApprovalProposal{}, nil
	}
}

// runeRecordSnapshot reads the record an update would replace so the user can
// compare before and after, and so the approved call can be re-checked on
// resume. A read that cannot produce a snapshot blocks the write instead of
// running it unreviewed.
func runeRecordSnapshot(ctx context.Context, session RuneSession, original string, fields map[string]json.RawMessage) (json.RawMessage, string, error) {
	collection, id := cmsRuneCollection(fields), cmsStringField(fields, "id")
	if session == nil || collection == "" || id == "" {
		return nil, "", fmt.Errorf("aichattools: cannot read the %s record this update would replace; the write was not performed", original)
	}
	callArgs, err := json.Marshal(map[string]string{"collection": collection, "id": id})
	if err != nil {
		return nil, "", fmt.Errorf("aichattools: cannot read the record this update would replace; the write was not performed")
	}
	res, err := session.Call(ctx, "read_record", callArgs)
	if err != nil || res.IsError {
		return nil, "", fmt.Errorf("aichattools: the record this update would replace could not be read; the write was not performed")
	}
	return cmsApprovalSnapshot("rune_record",
		map[string]any{"collection": collection, "id": id},
		map[string]any{"record": cmsAnyValue(res.Content)},
	), cmsApprovalText(res.Content), nil
}

func cmsRuneCollection(fields map[string]json.RawMessage) string {
	if collection := cmsStringField(fields, "collection"); collection != "" {
		return collection
	}
	return "CMS"
}

// ---------------------------------------------------------------------------
// WordPress
// ---------------------------------------------------------------------------

// prepareWordPressApproval applies the WordPress policy: a dry run of a tool
// whose schema really has the flag is free, a draft-only edit of one post is
// free while a fresh read still finds that post a draft, and every shared or
// site-wide effect needs approval.
func prepareWordPressApproval(ctx context.Context, session RuneSession, original string, fields map[string]json.RawMessage) (CMSApprovalProposal, error) {
	tool, known := lookupWordPressTool(original)
	if !known {
		// Unclassified tool: no approval policy exists for it, so none is
		// required. Known policies keep their draft and dry-run exceptions.
		return CMSApprovalProposal{}, nil
	}
	// dry_run previews change nothing only where the reviewed tool really has
	// the flag. Every other tool ignores an unknown argument and writes anyway,
	// so a stray dry_run must never be an exemption.
	if cmsBoolField(fields, "dry_run") && wpDryRunTools[original] {
		return CMSApprovalProposal{}, nil
	}
	required, snapshotWorthy := wordPressApprovalNeeded(tool, fields)
	if !required {
		return CMSApprovalProposal{}, nil
	}
	proposal := CMSApprovalProposal{
		Required: true,
		Target:   cmsApprovalText(wordPressApprovalTarget(tool, fields)),
		After:    cmsApprovalArgs(fields),
	}
	if !snapshotWorthy {
		// No safe pre-write state to compare: the card discloses the exact
		// arguments instead, and the caller re-checks the connection guard.
		return proposal, nil
	}
	// The call changes one existing post, so it is previewed against the state
	// it replaces. A read that cannot produce that state blocks the write
	// instead of approving it with nothing to compare on resume.
	state, err := wordPressPostSnapshot(ctx, session, tool, fields)
	if err != nil {
		return CMSApprovalProposal{}, err
	}
	if state.snapshot == nil {
		// The arguments name no existing post, so there is no state to read
		// and the card discloses the exact arguments instead.
		return proposal, nil
	}
	before := wordPressPostPreview(state.record, state.status)
	if builderWriteTools[tool.Name] {
		if element := wordPressBuilderPreview(ctx, session, fields); element != "" {
			before += "\n" + element
		}
		proposal.After = wordPressBuilderChange(tool, fields)
	}
	proposal.Before = cmsApprovalText(before)
	proposal.Snapshot = state.snapshot
	// Only an edit of one post and nothing else is waived, and only while a
	// fresh read still finds that post a draft with nothing in the arguments
	// that publishes it. A shared effect stays gated even on a draft.
	draftOnly := tool.Approve == wpWriteDraftPost && state.draft && !wordPressPublishes(fields)
	proposal.Required = !draftOnly
	return proposal, nil
}

// wpDryRunTools are the reviewed tools whose live schema has a dry_run flag,
// so previewing them changes nothing. Any other tool silently ignores the
// argument and would still write, so it keeps its normal policy.
var wpDryRunTools = map[string]bool{
	"bulk_update_content":    true,
	"bulk_set_seo":           true,
	"search_replace_content": true,
	"edit_page_element":      true,
	"delete_page_element":    true,
	"optimize_image":         true,
	"optimize_site":          true,
	"bulk_set_image_alt":     true,
	"database_cleanup":       true,
}

// builderWriteTools are the reviewed builder writes whose card must show the
// element text the call replaces instead of only its arguments.
var builderWriteTools = map[string]bool{
	"edit_page_element":   true,
	"insert_page_section": true,
	"delete_page_element": true,
}

// wordPressApprovalNeeded reports whether one call needs approval and whether
// it changes one existing post, whose state must then be read and
// snapshotted. It is the single place the WordPress sensitivity rules live.
func wordPressApprovalNeeded(tool wordpressTool, fields map[string]json.RawMessage) (required, snapshotWorthy bool) {
	switch tool.Approve {
	case wpRead:
		return false, false
	case wpReadWrite:
		// generate_schema and the settings tools read unless asked to apply.
		if cmsBoolField(fields, "apply") {
			return true, true
		}
		switch strings.ToLower(cmsStringField(fields, "action")) {
		case "", "get", "list":
			return false, false
		}
		return true, false
	case wpWriteCreateDraft:
		// Draft-first: an omitted status creates a draft, but a future date
		// makes the server schedule the post as "future", which publishes on
		// its own, so it is not the safe path either.
		return wordPressPublishes(fields), false
	case wpWriteDraftPost:
		// A term is shared taxonomy, not one post.
		if strings.EqualFold(cmsStringField(fields, "object_type"), "term") {
			return true, false
		}
		// One existing post, including one that would publish it: the user
		// approves against the state the write replaces.
		return true, true
	default:
		// Everything else is shared or site-wide and always needs approval.
		// Deleting content is the one such call that still removes readable
		// state, so it is snapshotted whenever it names the post.
		return true, tool.Name == "delete_content" && cmsWordPressPostID(fields) != ""
	}
}

// wordPressPublishes reports whether the arguments move content out of a
// private draft: any non-draft status, or a date that makes the server
// schedule the post for later. A read of a draft cannot excuse either.
func wordPressPublishes(fields map[string]json.RawMessage) bool {
	status := strings.ToLower(cmsStringField(fields, "status"))
	return status != "" && status != "draft" || cmsStringField(fields, "date") != ""
}

// cmsWordPressPostID names the existing post a call would change, accepting
// both spellings the reviewed tools use.
func cmsWordPressPostID(fields map[string]json.RawMessage) string {
	for _, key := range []string{"id", "post_id"} {
		if value := cmsStringField(fields, key); value != "" {
			return value
		}
	}
	return ""
}

// wordPressPostState is the pre-write state of the one post a call would
// change: what the card shows and what the snapshot compares.
type wordPressPostState struct {
	snapshot json.RawMessage
	record   map[string]any
	status   string
	draft    bool
}

// wordPressPostSnapshot reads the post one call would change. It fails
// closed: without a usable read there is nothing to compare the approved
// call against on resume, so the write is blocked rather than approved
// against an empty snapshot.
func wordPressPostSnapshot(ctx context.Context, session RuneSession, tool wordpressTool, fields map[string]json.RawMessage) (wordPressPostState, error) {
	id := cmsWordPressPostID(fields)
	if id == "" {
		// The arguments name no post, so there is no existing state to read.
		return wordPressPostState{}, nil
	}
	blocked := func(reason string) error {
		return fmt.Errorf("aichattools: the WordPress post %s would change %s; the write was not performed", tool.Name, reason)
	}
	if session == nil {
		return wordPressPostState{}, blocked("could not be read")
	}
	callArgs, err := json.Marshal(map[string]string{"id": id})
	if err != nil {
		return wordPressPostState{}, blocked("could not be read")
	}
	res, err := session.Call(ctx, "get_content", callArgs)
	if err != nil || res.IsError {
		return wordPressPostState{}, blocked("could not be read")
	}
	record := map[string]any{}
	if err := json.Unmarshal([]byte(res.Content), &record); err != nil || len(record) == 0 {
		return wordPressPostState{}, blocked("could not be read")
	}
	values := wordPressSnapshotValues(record)
	if len(values) == 0 {
		return wordPressPostState{}, blocked("returned no comparable state")
	}
	status := strings.ToLower(cmsMapString(record, "status"))
	return wordPressPostState{
		snapshot: cmsApprovalSnapshot("wordpress_post", map[string]any{"id": id, "status": status}, values),
		record:   record,
		status:   status,
		draft:    status == "draft",
	}, nil
}

// wordPressSnapshotValues collects the post state a compare must cover: the
// content fields an edit would change, plus the builder, template and SEO data
// that decides what the page really is. Volatile meta is left out.
func wordPressSnapshotValues(record map[string]any) map[string]any {
	values := map[string]any{}
	for _, key := range []string{
		"id", "type", "post_type", "status", "title", "content", "excerpt", "slug",
		"parent", "template", "permalink", "featured_media", "terms", "seo", "meta",
	} {
		value, ok := record[key]
		if !ok {
			continue
		}
		if key == "meta" {
			value = cmsStableMeta(value)
		}
		values[key] = cmsSnapshotValue(value)
	}
	return values
}

// wordPressPostPreview renders the post state as bounded plain text: what
// the write would replace, clipped and marked rather than silently cut.
func wordPressPostPreview(record map[string]any, status string) string {
	lines := []string{"status: " + status}
	for _, key := range []string{"title", "content", "excerpt"} {
		if text := cmsSnapshotString(record, key); text != "" {
			lines = append(lines, key+": "+cmsPreviewField(text))
		}
	}
	return strings.Join(lines, "\n")
}

// wordPressBuilderPreview reads the page's builder tree and returns the text
// of the element the call names, so a builder edit is shown as the copy it
// replaces. A failed read only costs the preview: the post snapshot still
// guards the write.
func wordPressBuilderPreview(ctx context.Context, session RuneSession, fields map[string]json.RawMessage) string {
	elementID := cmsStringField(fields, "element_id")
	if session == nil || elementID == "" {
		return ""
	}
	callArgs, err := json.Marshal(map[string]string{"id": cmsWordPressPostID(fields)})
	if err != nil {
		return ""
	}
	res, err := session.Call(ctx, "get_page_structure", callArgs)
	if err != nil || res.IsError {
		return ""
	}
	node, ok := cmsFindBuilderNode(cmsAnyValue(res.Content), elementID)
	if !ok {
		return ""
	}
	text := cmsBuilderNodeText(node)
	if text == "" {
		return ""
	}
	return "element " + elementID + " currently: " + cmsPreviewField(text)
}

// wordPressBuilderChange describes a builder write as the element it touches
// and the fields it rewrites, in plain text, without inventing an undo.
func wordPressBuilderChange(tool wordpressTool, fields map[string]json.RawMessage) string {
	header := tool.Label + " on page #" + cmsWordPressPostID(fields)
	if elementID := cmsStringField(fields, "element_id"); elementID != "" {
		header = tool.Label + " element " + elementID + " on page #" + cmsWordPressPostID(fields)
	}
	if tool.Name == "delete_page_element" {
		return header + "\nthis element is removed from the page"
	}
	lines := cmsArgLines(fields, builderSelectorKeys)
	if len(lines) == 0 {
		return header + "\n(no element changes supplied)"
	}
	return header + "\n" + strings.Join(lines, "\n")
}

// builderSelectorKeys are the arguments a builder card already names in its
// header, so its change lines carry only the new content.
var builderSelectorKeys = map[string]bool{"id": true, "post_id": true, "page_id": true, "element_id": true, "dry_run": true}

// cmsFindBuilderNode returns the first node in a builder tree whose element id
// matches, so a builder write is previewed as real copy and not arguments.
func cmsFindBuilderNode(node any, elementID string) (map[string]any, bool) {
	switch typed := node.(type) {
	case map[string]any:
		for _, key := range []string{"id", "element_id"} {
			if text, ok := typed[key].(string); ok && text == elementID {
				return typed, true
			}
		}
		for _, entry := range typed {
			if found, ok := cmsFindBuilderNode(entry, elementID); ok {
				return found, true
			}
		}
	case []any:
		for _, entry := range typed {
			if found, ok := cmsFindBuilderNode(entry, elementID); ok {
				return found, true
			}
		}
	}
	return nil, false
}

// cmsBuilderNodeText returns the first real text inside a builder element,
// looking at its settings so a card shows copy instead of a JSON blob.
func cmsBuilderNodeText(node any) string {
	switch typed := node.(type) {
	case map[string]any:
		for _, key := range []string{"text", "html", "content", "raw", "rendered", "title"} {
			if text, ok := typed[key].(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
		for _, key := range []string{"settings", "props", "attributes", "data"} {
			if text := cmsBuilderNodeText(typed[key]); text != "" {
				return text
			}
		}
	case []any:
		for _, entry := range typed {
			if text := cmsBuilderNodeText(entry); text != "" {
				return text
			}
		}
	}
	return ""
}

// cmsSnapshotString reads one remote field as plain text, seeing through the
// {"raw": ...} and {"rendered": ...} wrappers WordPress returns.
func cmsSnapshotString(record map[string]any, key string) string {
	text, _ := cmsSnapshotValue(record[key]).(string)
	return text
}

// wordPressApprovalTarget names what the user is approving: the tool label
// plus the identifier the arguments name, never remote text.
func wordPressApprovalTarget(tool wordpressTool, fields map[string]json.RawMessage) string {
	if tool.Name == "publish_content" {
		if postType := cmsStringField(fields, "post_type"); postType != "" {
			return "New WordPress " + postType
		}
		return "New WordPress content"
	}
	label := tool.Label
	for _, key := range []string{"id", "post_id", "attachment_id", "revision_id", "comment_id", "object_id", "term_id", "ids", "urls"} {
		if value := cmsStringField(fields, key); value != "" {
			return label + " #" + capUTF8Bytes(value, 64)
		}
	}
	return label
}

// ---------------------------------------------------------------------------
// Proposal text and snapshots
// ---------------------------------------------------------------------------

// cmsApprovalText bounds one untrusted preview string and says when it
// clipped, so a partial field is never read as the whole thing.
func cmsApprovalText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if len(s) > maxCMSApprovalTextBytes {
		return capUTF8Bytes(s, maxCMSApprovalTextBytes) + "…[truncated]"
	}
	return s
}

// cmsPreviewField bounds one field of a preview and marks the clip.
func cmsPreviewField(value string) string {
	if len(value) > maxCMSApprovalFieldBytes {
		return capUTF8Bytes(value, maxCMSApprovalFieldBytes) + "…[truncated]"
	}
	return value
}

// cmsApprovalArgs renders the supplied arguments as bounded plain text, one
// field per line, so the card discloses exactly what was requested.
func cmsApprovalArgs(fields map[string]json.RawMessage) string {
	lines := cmsArgLines(fields, nil)
	if len(lines) == 0 {
		return "(no arguments)"
	}
	return cmsApprovalText(strings.Join(lines, "\n"))
}

// cmsArgLines renders each argument as one "key: value" line, sorted, skipping
// the keys a caller already shows in its own header.
func cmsArgLines(fields map[string]json.RawMessage, skip map[string]bool) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if !skip[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+": "+cmsFieldText(string(fields[key])))
	}
	return lines
}

// cmsFieldText renders one argument value: scalars verbatim, structures as
// compact JSON on one line, everything bounded.
func cmsFieldText(raw string) string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return capUTF8Bytes(raw, maxCMSApprovalFieldBytes)
	}
	text, ok := value.(string)
	if !ok {
		encoded, err := json.Marshal(value)
		if err != nil {
			return capUTF8Bytes(raw, maxCMSApprovalFieldBytes)
		}
		text = string(encoded)
	}
	return cmsPreviewField(text)
}

// cmsAnyValue parses remote JSON so a snapshot stores structure, not a string
// that happened to contain JSON. Unparseable text is stored verbatim.
func cmsAnyValue(text string) any {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return text
	}
	return value
}

// cmsMapString reads one string field out of a parsed remote object.
func cmsMapString(record map[string]any, key string) string {
	switch value := record[key].(type) {
	case string:
		return value
	case map[string]any:
		for _, inner := range []string{"raw", "rendered", "value"} {
			if text, ok := value[inner].(string); ok {
				return text
			}
		}
	}
	return ""
}

// volatileMetaKeys are the meta fields that change on their own: edit locks,
// save timestamps and WordPress caches. Everything else a plugin stores,
// builder data included, is content the user chose and stays comparable.
var volatileMetaKeys = map[string]bool{
	"_edit_lock": true, "_edit_last": true, "_modified": true, "_modified_gmt": true,
	"modified": true, "modified_gmt": true, "date": true, "date_gmt": true,
}

// volatileMetaPrefixes are the cached and session meta that changes without
// anyone editing the post.
var volatileMetaPrefixes = []string{"_transient_", "_site_transient_", "_wp_session_"}

// cmsStableMeta drops only volatile meta from a snapshot. Builder data such as
// _elementor_data, templates and SEO meta are real content: dropping them
// would let a changed page compare equal to the one the user approved.
func cmsStableMeta(value any) any {
	meta, ok := value.(map[string]any)
	if !ok {
		return value
	}
	stable := make(map[string]any, len(meta))
	for key, entry := range meta {
		lower := strings.ToLower(key)
		if volatileMetaKeys[lower] {
			continue
		}
		volatile := false
		for _, prefix := range volatileMetaPrefixes {
			if strings.HasPrefix(lower, prefix) {
				volatile = true
				break
			}
		}
		if volatile {
			continue
		}
		stable[key] = entry
	}
	return stable
}

// cmsApprovalSnapshot builds the compact pre-write snapshot: what the call
// addressed (kind, id, status) plus a digest of the full relevant state. The
// digest hashes untruncated values, so a resume comparison stays exact while
// the stored snapshot stays small whatever the page weighs.
func cmsApprovalSnapshot(kind string, identity map[string]any, values map[string]any) json.RawMessage {
	full, err := json.Marshal(values)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(full)
	snapshot := map[string]any{"kind": kind, "digest": "sha256:" + hex.EncodeToString(sum[:])}
	for key, value := range identity {
		snapshot[key] = value
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil
	}
	return raw
}

// cmsSnapshotValue normalizes one remote field for a snapshot: the text of a
// content wrapper ({"raw": ...}) becomes that text, so comparison and bounding
// work on the content itself rather than on a nested wrapper.
func cmsSnapshotValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"raw", "rendered"} {
			if text, ok := typed[key].(string); ok {
				return text
			}
		}
		flat := make(map[string]any, len(typed))
		for key, entry := range typed {
			flat[key] = cmsSnapshotValue(entry)
		}
		return flat
	case []any:
		out := make([]any, len(typed))
		for i, entry := range typed {
			out[i] = cmsSnapshotValue(entry)
		}
		return out
	}
	return value
}
