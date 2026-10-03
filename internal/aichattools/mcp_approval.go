// MCP approval preparation: the bounded, exact description of one remote call
// shown in an approval card, plus the WordPress pre-write snapshot the approved
// call is re-checked against on resume.
//
// This helper carries no policy. Whether a call needs a decision comes from the
// caller's saved user policy; an explicit Ask applies to reads, drafts and
// dry runs alike, and nothing here can exempt one. Preparation only reads: it
// never dispatches the requested mutation.
//
// The displayed text is plain text, bounded, and never invented: it shows the
// exact arguments, and a before view only where the adapter could really read
// the state the call would replace. Snapshot digests hash the full state, never
// the clipped text shown in the card.
package aichattools

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
	// maxMCPApprovalTextBytes bounds the plain-text target/before/after
	// preview. These strings are untrusted remote content shown in a card.
	maxMCPApprovalTextBytes = 2000
	// maxMCPApprovalFieldBytes bounds one field inside that preview.
	maxMCPApprovalFieldBytes = 400
)

// ErrMCPPreflightPermission reports that a read the safety preparation needs
// was refused by saved user policy. The session the caller passes to
// PrepareMCPApproval must be its policy-guarded session: preflight reads
// (WordPress get_content, get_page_structure) answer to the saved permission of
// that exact read tool, and a refusal is returned wrapped in this error, so
// preparation never bypasses Ask or Deny and never quietly shows no before
// view.
var ErrMCPPreflightPermission = errors.New("mcp preflight read not permitted by saved permission")

// MCPApprovalProposal is what the user is asked to approve for one exact remote
// call. It carries no Required flag on purpose: approval comes from saved user
// policy, so this helper can neither demand a decision nor waive one.
type MCPApprovalProposal struct {
	// Service is the connection service the call belongs to: "wordpress" or
	// "custom". It selects the local adapter, never a transport profile.
	Service string `json:"service"`
	// Tool is the exact remote tool name the connection advertised.
	Tool string `json:"tool"`
	// Target names the call in one bounded line, without remote text.
	Target string `json:"target"`
	// Before is the bounded state the call would replace, when the adapter could
	// read one. Empty means the card shows no before view at all.
	Before string `json:"before"`
	// After is the exact bounded arguments, rendered one field per line. It is
	// never a diff the helper invented.
	After string `json:"after"`
	// Snapshot is the compact digest of the full pre-write state, present only
	// when Before is real. Re-running this helper with the approved arguments
	// must reproduce it byte for byte; any difference blocks the call.
	Snapshot json.RawMessage `json:"snapshot,omitempty"`
}

// PrepareMCPApproval builds the bounded proposal for one remote call and never
// dispatches it. remoteName is the exact name the connection advertised, not a
// model alias. A non-nil error blocks the call: malformed arguments, a
// platform refusal and an unreadable pre-write state all fail closed.
//
// The proposal is built for every service. WordPress adds a real before view and
// snapshot for the calls that change one existing post; everything else, custom
// connections included, discloses the exact arguments and nothing more.
//
// session must be the caller's policy-guarded session, and the requested
// mutation is never dispatched here.
func PrepareMCPApproval(ctx context.Context, session MCPSession, service, remoteName string, args json.RawMessage) (MCPApprovalProposal, error) {
	remote := strings.TrimSpace(remoteName)
	if remote == "" {
		return MCPApprovalProposal{}, errors.New("aichattools: mcp approval: empty remote tool name")
	}
	fields, err := decodeMCPArgs(args)
	if err != nil {
		return MCPApprovalProposal{}, fmt.Errorf("aichattools: mcp %s: %w", remote, err)
	}
	if refusal := mcpToolRefusal(service, remote, args); refusal != "" {
		return MCPApprovalProposal{}, fmt.Errorf("aichattools: mcp %s: %s", remote, refusal)
	}
	proposal := MCPApprovalProposal{
		Service: service,
		Tool:    remote,
		Target:  mcpApprovalTarget(remote, fields),
		After:   mcpApprovalArgs(fields),
	}
	state, err := mcpWordPressPreWriteState(ctx, session, service, remote, fields)
	if err != nil || state.snapshot == nil {
		return proposal, err
	}
	before := state.preview
	if wordPressBuilderTools[remote] {
		element, err := wordPressBuilderPreview(ctx, session, remote, fields)
		if err != nil {
			return MCPApprovalProposal{}, err
		}
		if element != "" {
			before += "\n" + element
		}
		proposal.After = wordPressBuilderChange(remote, fields)
	}
	proposal.Before = mcpApprovalText(before)
	proposal.Snapshot = state.snapshot
	return proposal, nil
}

// mcpApprovalTarget names what the user is approving: the exact remote tool name
// plus the identifier the arguments name, so the card never shows remote text
// where an identity belongs.
func mcpApprovalTarget(remote string, fields map[string]json.RawMessage) string {
	for _, key := range []string{"id", "post_id", "attachment_id", "revision_id", "comment_id", "object_id", "term_id", "page_id", "ids", "urls"} {
		if value := mcpStringField(fields, key); value != "" {
			return remote + " #" + capUTF8Bytes(value, 64)
		}
	}
	return remote
}

// decodeMCPArgs parses tool arguments as a field map. Empty or null arguments
// are an empty field set, not an error.
func decodeMCPArgs(args json.RawMessage) (map[string]json.RawMessage, error) {
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

// mcpStringField returns a scalar string argument, or "" when absent or not a
// scalar. Callers treat a missing value as "not specified", which keeps every
// default on the safe side.
func mcpStringField(fields map[string]json.RawMessage, key string) string {
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

// mcpApprovalText bounds one untrusted preview string and says when it clipped,
// so a partial field is never read as the whole thing.
func mcpApprovalText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if len(s) > maxMCPApprovalTextBytes {
		return capUTF8Bytes(s, maxMCPApprovalTextBytes) + "…[truncated]"
	}
	return s
}

// mcpPreviewField bounds one field of a preview and marks the clip.
func mcpPreviewField(value string) string {
	if len(value) > maxMCPApprovalFieldBytes {
		return capUTF8Bytes(value, maxMCPApprovalFieldBytes) + "…[truncated]"
	}
	return value
}

// mcpApprovalArgs renders the supplied arguments as bounded plain text, one
// field per line, so the card discloses exactly what was requested.
func mcpApprovalArgs(fields map[string]json.RawMessage) string {
	lines := mcpArgLines(fields, nil)
	if len(lines) == 0 {
		return "(no arguments)"
	}
	return mcpApprovalText(strings.Join(lines, "\n"))
}

// mcpArgLines renders each argument as one "key: value" line, sorted, skipping
// the keys a caller already shows in its own header.
func mcpArgLines(fields map[string]json.RawMessage, skip map[string]bool) []string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if !skip[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+": "+mcpFieldText(string(fields[key])))
	}
	return lines
}

// mcpFieldText renders one argument value: scalars verbatim, structures as
// compact JSON on one line, everything bounded.
func mcpFieldText(raw string) string {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return capUTF8Bytes(raw, maxMCPApprovalFieldBytes)
	}
	text, ok := value.(string)
	if !ok {
		encoded, err := json.Marshal(value)
		if err != nil {
			return capUTF8Bytes(raw, maxMCPApprovalFieldBytes)
		}
		text = string(encoded)
	}
	return mcpPreviewField(text)
}

// mcpAnyValue parses remote JSON so a snapshot stores structure, not a string
// that happened to contain JSON. Unparseable text is stored verbatim.
func mcpAnyValue(text string) any {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return text
	}
	return value
}

// mcpMapString reads one string field out of a parsed remote object.
func mcpMapString(record map[string]any, key string) string {
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

// mcpStableMeta drops only volatile meta from a snapshot. Builder data such as
// _elementor_data, templates and SEO meta are real content: dropping them would
// let a changed page compare equal to the one the user approved.
func mcpStableMeta(value any) any {
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

// mcpApprovalSnapshot builds the compact pre-write snapshot: what the call
// addressed (kind, id, status) plus a digest of the full relevant state. The
// digest hashes untruncated values, so a resume comparison stays exact while the
// stored snapshot stays small whatever the page weighs.
func mcpApprovalSnapshot(kind string, identity map[string]any, values map[string]any) json.RawMessage {
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

// mcpSnapshotValue normalizes one remote field for a snapshot: the text of a
// content wrapper ({"raw": ...}) becomes that text, so comparison and bounding
// work on the content itself rather than on a nested wrapper.
func mcpSnapshotValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range []string{"raw", "rendered"} {
			if text, ok := typed[key].(string); ok {
				return text
			}
		}
		flat := make(map[string]any, len(typed))
		for key, entry := range typed {
			flat[key] = mcpSnapshotValue(entry)
		}
		return flat
	case []any:
		out := make([]any, len(typed))
		for i, entry := range typed {
			out[i] = mcpSnapshotValue(entry)
		}
		return out
	}
	return value
}

// mcpSnapshotString reads one remote field as plain text, seeing through the
// {"raw": ...} and {"rendered": ...} wrappers WordPress returns.
func mcpSnapshotString(record map[string]any, key string) string {
	text, _ := mcpSnapshotValue(record[key]).(string)
	return text
}
