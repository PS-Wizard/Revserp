// Package aiskills loads the Git-managed AI skill folders into a read-only
// filesystem catalog shared by the admin skills listing and the chat worker
// skill tools. Listings expose metadata and file paths only, never bodies,
// and reads serve bounded chunks confined to one skill directory.
package aiskills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	// skillRootFile is the per-skill instruction file. Its parent directory,
	// relative to the catalog root, is the skill id.
	skillRootFile = "SKILL.md"

	MaxSkillCount = 1024
	MaxSkillFiles = 128
	// MaxSkillFileBytes bounds one disk read of a supplemental reference file.
	// References stay servable up to this size; larger files fail the read
	// with an explicit size error instead of disappearing from the manifest.
	MaxSkillFileBytes = 256 << 10
	// MaxSkillRootBytes bounds the SKILL.md root instructions. A root must
	// complete inside the per-turn skill byte budget (at least 96KiB) and the
	// 20 agent rounds, so roots stay far below the reference limit.
	MaxSkillRootBytes        = 64 << 10
	MaxSkillIDBytes          = 256
	MaxSkillNameBytes        = 128
	MaxSkillDescriptionBytes = 2048
	MaxSkillPathBytes        = 1024
	MaxReadContentBytes      = 12 << 10
	// DefaultMaxSerializedBytes bounds one serialized read result. Callers
	// pass the worker tool result cap minus a margin.
	DefaultMaxSerializedBytes = 30 << 10
)

// Skill is one skill's metadata plus its sorted readable file list, relative
// to the skill directory in POSIX slash format. It never carries file bodies.
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Files       []string `json:"files"`
}

// ReadResult is one bounded chunk of a skill file. NextOffset is the byte
// position for the next read, or null when the file is exhausted.
type ReadResult struct {
	Skill          string   `json:"skill"`
	Path           string   `json:"path"`
	Content        string   `json:"content"`
	Revision       string   `json:"revision"`
	NextOffset     *int     `json:"next_offset"`
	HasMore        bool     `json:"has_more"`
	AvailableFiles []string `json:"available_files,omitempty"`
}

// Catalog is a snapshot of the skill folders under one root directory,
// scanned once at construction. Reads serve bounded chunks from disk through
// nested os.Root confinement; the directory tree is never rescanned per read.
type Catalog struct {
	root   string
	skills []Skill
	err    error
}

// New scans root for skills. A missing root yields an empty catalog so the
// product keeps working with no skills checked out. A malformed committed
// skill fails the whole catalog with a diagnostic instead of silently
// omitting that skill. New never returns nil and never panics.
func New(root string) *Catalog {
	catalog := &Catalog{root: root, skills: []Skill{}}
	if strings.TrimSpace(root) == "" {
		return catalog
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return catalog
		}
		catalog.err = fmt.Errorf("list skills directory %q: %w", root, err)
		return catalog
	}
	if !info.IsDir() {
		catalog.err = fmt.Errorf("skills directory %q is not a directory", root)
		return catalog
	}
	skills, err := scanSkills(root)
	if err != nil {
		catalog.err = err
		return catalog
	}
	catalog.skills = skills
	return catalog
}

// Err reports the whole-catalog load failure, or nil when the catalog is
// usable. A missing skill root is not a failure: it yields an empty catalog.
func (c *Catalog) Err() error {
	if c == nil {
		return errors.New("skills catalog is not configured")
	}
	return c.err
}

// Skills returns the catalog metadata sorted by id, or an empty non-nil
// slice when the root is missing or the catalog failed to load.
func (c *Catalog) Skills() []Skill {
	if c == nil || c.err != nil {
		return []Skill{}
	}
	out := make([]Skill, len(c.skills))
	for i, skill := range c.skills {
		files := make([]string, len(skill.Files))
		copy(files, skill.Files)
		skill.Files = files
		out[i] = skill
	}
	return out
}

func (c *Catalog) Find(id string) (Skill, bool) {
	if c == nil || c.err != nil {
		return Skill{}, false
	}
	for _, skill := range c.skills {
		if skill.ID == id {
			files := make([]string, len(skill.Files))
			copy(files, skill.Files)
			skill.Files = files
			return skill, true
		}
	}
	return Skill{}, false
}

// Read returns one bounded chunk of a skill file. An empty relPath reads
// SKILL.md, the skill's root instructions. Offset is a byte position from an
// earlier result and must land on a UTF-8 boundary; a non-empty revision
// must match the file's current content hash or the read fails so a changed
// file is re-read from the start. maxContent caps the chunk (<=0 selects
// MaxReadContentBytes) and maxSerialized caps the serialized result (<=0
// selects DefaultMaxSerializedBytes); the chunk shrinks until the serialized
// result fits, keeping offsets exact, and the read fails when even the
// envelope alone cannot fit.
func (c *Catalog) Read(skillID, relPath string, offset int, revision string, maxContent, maxSerialized int) (ReadResult, error) {
	if c == nil {
		return ReadResult{}, errors.New("skills catalog is not configured")
	}
	if c.err != nil {
		return ReadResult{}, fmt.Errorf("skills catalog unavailable: %w", c.err)
	}
	skill, ok := c.Find(skillID)
	if !ok {
		return ReadResult{}, fmt.Errorf("unknown skill %q", skillID)
	}
	if relPath == "" {
		relPath = skillRootFile
	}
	if err := validSkillPath(relPath); err != nil {
		return ReadResult{}, err
	}
	known := false
	for _, file := range skill.Files {
		if file == relPath {
			known = true
			break
		}
	}
	if !known {
		return ReadResult{}, fmt.Errorf("unknown file %q for skill %q", relPath, skillID)
	}
	if offset < 0 {
		return ReadResult{}, fmt.Errorf("offset %d must be >= 0", offset)
	}
	if maxContent <= 0 {
		maxContent = MaxReadContentBytes
	}
	if maxSerialized <= 0 {
		maxSerialized = DefaultMaxSerializedBytes
	}
	data, err := c.readFile(skillID, relPath)
	if err != nil {
		return ReadResult{}, err
	}
	if !utf8.Valid(data) {
		return ReadResult{}, fmt.Errorf("skill file %q of skill %q is not valid UTF-8", relPath, skillID)
	}
	digest := sha256.Sum256(data)
	hash := hex.EncodeToString(digest[:])
	if revision != "" && revision != hash {
		return ReadResult{}, fmt.Errorf("skill file %q of skill %q changed since the earlier read; re-read from offset 0 without a revision", relPath, skillID)
	}
	if offset > len(data) {
		return ReadResult{}, fmt.Errorf("offset %d is beyond the end of skill file %q (%d bytes)", offset, relPath, len(data))
	}
	if offset < len(data) && !utf8.RuneStart(data[offset]) {
		return ReadResult{}, fmt.Errorf("offset %d is not a UTF-8 boundary in skill file %q", offset, relPath)
	}
	content := chunkUTF8(data, offset, maxContent)
	result := ReadResult{Skill: skillID, Path: relPath, Content: content, Revision: hash}
	result.NextOffset, result.HasMore = nextOffset(offset+len(content), len(data))
	if relPath == skillRootFile && offset == 0 {
		result.AvailableFiles = append([]string(nil), skill.Files...)
	}
	return fitSerialized(result, data, offset, maxSerialized)
}

// readFile reads one skill file confined to its skill directory: a nested
// root per skill, so a symlink can never reach a sibling skill or an
// arbitrary backend file.
func (c *Catalog) readFile(skillID, relPath string) ([]byte, error) {
	catalogRoot, err := os.OpenRoot(c.root)
	if err != nil {
		return nil, fmt.Errorf("open skills directory %q: %w", c.root, err)
	}
	defer func() { _ = catalogRoot.Close() }()
	if err := validSkillID(skillID); err != nil {
		return nil, err
	}
	skillRoot, err := catalogRoot.OpenRoot(filepath.FromSlash(skillID))
	if err != nil {
		return nil, fmt.Errorf("open skill %q: %w", skillID, err)
	}
	defer func() { _ = skillRoot.Close() }()
	name := filepath.FromSlash(relPath)
	info, err := skillRoot.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("stat skill file %q of skill %q: %w", relPath, skillID, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("skill file %q of skill %q is not a regular file", relPath, skillID)
	}
	file, err := skillRoot.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open skill file %q of skill %q: %w", relPath, skillID, err)
	}
	defer func() { _ = file.Close() }()
	limit := MaxSkillFileBytes
	if relPath == skillRootFile {
		limit = MaxSkillRootBytes
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read skill file %q of skill %q: %w", relPath, skillID, err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("skill file %q of skill %q exceeds the %d byte skill file limit", relPath, skillID, limit)
	}
	return data, nil
}

func chunkUTF8(data []byte, offset, max int) string {
	end := offset + max
	if end > len(data) {
		end = len(data)
	}
	for end < len(data) && end > offset && !utf8.RuneStart(data[end]) {
		end--
	}
	if end == offset && offset < len(data) {
		_, size := utf8.DecodeRune(data[offset:])
		end = offset + size
	}
	return string(data[offset:end])
}

func nextOffset(end, total int) (*int, bool) {
	if end >= total {
		return nil, false
	}
	next := end
	return &next, true
}

// fitSerialized shrinks the chunk until the serialized result fits
// maxSerialized bytes, keeping offsets exact against the full file. It fails
// when the envelope alone cannot fit, so shrinking always makes progress.
func fitSerialized(result ReadResult, data []byte, offset, maxSerialized int) (ReadResult, error) {
	for {
		encoded, err := json.Marshal(result)
		if err != nil {
			return ReadResult{}, err
		}
		if len(encoded) <= maxSerialized {
			return result, nil
		}
		if result.Content == "" {
			return ReadResult{}, fmt.Errorf("skill read of %q exceeds the %d byte result limit", result.Path, maxSerialized)
		}
		raw := []byte(result.Content)
		half := len(raw) / 2
		for half > 0 && !utf8.RuneStart(raw[half]) {
			half--
		}
		if half == 0 {
			_, size := utf8.DecodeRune(raw)
			half = size
		}
		if half >= len(raw) {
			return ReadResult{}, fmt.Errorf("skill read of %q exceeds the %d byte result limit", result.Path, maxSerialized)
		}
		result.Content = string(raw[:half])
		result.NextOffset, result.HasMore = nextOffset(offset+len(result.Content), len(data))
	}
}

// scanSkills collects every SKILL.md definition, then loads each one with the
// full set of skill directories so a parent skill never manifests a nested
// skill root's files as its own. Any invalid definition fails the whole scan
// with a diagnostic naming the file.
func scanSkills(root string) ([]Skill, error) {
	type pendingSkill struct {
		id        string
		dir       string
		skillPath string
	}
	var pending []pendingSkill
	err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if filePath != root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != skillRootFile {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(filePath))
		if err != nil {
			return err
		}
		id := filepath.ToSlash(rel)
		if id == "." || id == "" {
			return fmt.Errorf("skill file %q sits at the catalog root and has no skill id", filePath)
		}
		if err := validSkillID(id); err != nil {
			return fmt.Errorf("skill file %q: %w", filePath, err)
		}
		pending = append(pending, pendingSkill{id: id, dir: filepath.Dir(filePath), skillPath: filePath})
		if len(pending) > MaxSkillCount {
			return fmt.Errorf("skills directory %q holds more than %d skills", root, MaxSkillCount)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	nested := make(map[string]bool, len(pending))
	for _, item := range pending {
		nested[filepath.Clean(item.dir)] = true
	}
	skills := make([]Skill, 0, len(pending))
	for _, item := range pending {
		skill, err := loadSkill(item.skillPath, item.id, nested)
		if err != nil {
			return nil, err
		}
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].ID < skills[j].ID })
	return skills, nil
}

// loadSkill reads one SKILL.md definition plus its sorted readable file list.
// The definition must be a regular file; symlinks fail the catalog.
func loadSkill(skillPath, id string, nested map[string]bool) (Skill, error) {
	info, err := os.Lstat(skillPath)
	if err != nil {
		return Skill{}, err
	}
	if !info.Mode().IsRegular() {
		return Skill{}, fmt.Errorf("skill file %q is not a regular file", skillPath)
	}
	data, err := boundedReadFile(skillPath, MaxSkillRootBytes)
	if err != nil {
		return Skill{}, err
	}
	name, description, err := parseSkillFrontmatter(skillPath, string(data))
	if err != nil {
		return Skill{}, err
	}
	files, err := listSkillFiles(filepath.Dir(skillPath), nested)
	if err != nil {
		return Skill{}, err
	}
	return Skill{ID: id, Name: name, Description: description, Files: files}, nil
}

func boundedReadFile(filePath string, limit int) ([]byte, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read skill file %q: %w", filePath, err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("skill file %q exceeds the %d byte skill file limit", filePath, limit)
	}
	return data, nil
}

// listSkillFiles enumerates the readable markdown and text files under one
// skill directory as sorted POSIX paths relative to that directory. Nested
// skill roots are skipped, so a parent skill never serves a nested skill's
// files. Symlinks, non-regular files, and paths outside the skill path
// grammar are not readable and stay unlisted. Oversized references stay
// listed: the read fails with an explicit size error.
func listSkillFiles(dir string, nested map[string]bool) ([]string, error) {
	files := []string{}
	err := filepath.WalkDir(dir, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if filePath != dir {
				if strings.HasPrefix(entry.Name(), ".") || nested[filepath.Clean(filePath)] {
					return filepath.SkipDir
				}
			}
			return nil
		}
		inner, err := filepath.Rel(dir, filePath)
		if err != nil {
			return err
		}
		canonical := filepath.ToSlash(inner)
		if validSkillPath(canonical) != nil || !readableSkillFile(canonical) {
			return nil
		}
		info, err := os.Lstat(filePath)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		files = append(files, canonical)
		if len(files) > MaxSkillFiles {
			return fmt.Errorf("skill directory %q holds more than %d readable files", dir, MaxSkillFiles)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func readableSkillFile(canonical string) bool {
	switch strings.ToLower(path.Ext(canonical)) {
	case ".md", ".markdown", ".txt":
		return true
	}
	return false
}

// validSkillID rejects empty segments, dot segments, backslashes, and
// absolute ids so an id can never address outside its skill directory.
func validSkillID(id string) error {
	if id == "" {
		return errors.New("skill id must not be empty")
	}
	if len(id) > MaxSkillIDBytes {
		return fmt.Errorf("skill id %q exceeds the %d byte limit", id, MaxSkillIDBytes)
	}
	if strings.Contains(id, "\\") || path.IsAbs(id) {
		return fmt.Errorf("skill id %q must be a relative slash-separated path", id)
	}
	for _, segment := range strings.Split(id, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("skill id %q must be a relative slash-separated path", id)
		}
	}
	return nil
}

// validSkillPath rejects escapes, absolute paths, and non markdown/text
// files so a read can never address outside its skill directory.
func validSkillPath(relPath string) error {
	if relPath == "" {
		return errors.New("skill path must not be empty")
	}
	if len(relPath) > MaxSkillPathBytes {
		return fmt.Errorf("skill path %q exceeds the %d byte limit", relPath, MaxSkillPathBytes)
	}
	if strings.Contains(relPath, "\\") || path.IsAbs(relPath) {
		return fmt.Errorf("skill path %q must be a relative slash-separated path", relPath)
	}
	for _, segment := range strings.Split(relPath, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("skill path %q must be a relative slash-separated path", relPath)
		}
	}
	if !readableSkillFile(relPath) {
		return fmt.Errorf("skill path %q is not a readable markdown or text file", relPath)
	}
	return nil
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// parseSkillFrontmatter extracts the YAML frontmatter name and description
// from one SKILL.md body, bounding both to keep the catalog small.
func parseSkillFrontmatter(skillPath, body string) (string, string, error) {
	raw, _, err := splitFrontmatter(body)
	if err != nil {
		return "", "", fmt.Errorf("skill file %q: %w", skillPath, err)
	}
	var meta skillFrontmatter
	if err := yaml.Unmarshal([]byte(raw), &meta); err != nil {
		return "", "", fmt.Errorf("skill file %q has invalid frontmatter: %w", skillPath, err)
	}
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		return "", "", fmt.Errorf("skill file %q frontmatter is missing a name", skillPath)
	}
	if strings.ContainsAny(name, "\r\n") {
		return "", "", fmt.Errorf("skill file %q frontmatter name must be a single line", skillPath)
	}
	if len(name) > MaxSkillNameBytes {
		return "", "", fmt.Errorf("skill file %q frontmatter name exceeds the %d byte limit", skillPath, MaxSkillNameBytes)
	}
	description := strings.TrimSpace(meta.Description)
	if description == "" {
		return "", "", fmt.Errorf("skill file %q frontmatter is missing a description", skillPath)
	}
	if len(description) > MaxSkillDescriptionBytes {
		return "", "", fmt.Errorf("skill file %q frontmatter description exceeds the %d byte limit", skillPath, MaxSkillDescriptionBytes)
	}
	return name, description, nil
}

func splitFrontmatter(body string) (string, string, error) {
	rest := strings.TrimPrefix(body, "\xef\xbb\xbf")
	lines := strings.SplitAfter(rest, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", errors.New("is missing YAML frontmatter fenced by --- lines")
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], ""), strings.Join(lines[i+1:], ""), nil
		}
	}
	return "", "", errors.New("has unterminated YAML frontmatter")
}
