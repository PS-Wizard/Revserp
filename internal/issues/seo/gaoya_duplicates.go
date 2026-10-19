package seo

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

// Near-duplicate detection delegates to the revserp-gaoya binary (gaoya
// MinHash LSH) with one call per analysis over VISIBLE_TEXT ONLY. Fixed
// experiment params: threshold 0.9, 128 hashes, 5-word shingles, lowercase,
// seed 3. There is no fallback to the old in-Go trigram path: a missing,
// crashing, or timing-out binary returns an error and the caller fails the
// analysis.
const (
	gaoyaSimilarityThreshold = 0.9
	gaoyaNumHashes           = 128
	gaoyaShingleWords        = 5
	gaoyaLowercase           = true
	gaoyaSeed                = 3

	gaoyaDefaultTimeout     = 5 * time.Minute
	gaoyaRayonNumThreads    = "2"
	gaoyaStderrCaptureLimit = 8192
	gaoyaInputCtxCheckEvery = 256
	gaoyaPairsCtxCheckEvery = 1024
)

// gaoyaNearDuplicatePair is one engine-verified near-duplicate page pair.
// Similarity is the native MinHash signature estimate (6-decimal score), not
// an exact shingle Jaccard.
type gaoyaNearDuplicatePair struct {
	left       int
	right      int
	similarity float64
}

func gaoyaBinaryPath() string {
	if override := os.Getenv("GAOYA_PATH"); override != "" {
		return override
	}
	return "revserp-gaoya"
}

// gaoyaEligiblePageIndexes returns the page indexes eligible for
// near-duplicate input: extraction-failed pages are excluded even when their
// error text is non-blank, and blank-text pages carry no signal.
func gaoyaEligiblePageIndexes(pageFacts []shared.PageFact) []int {
	eligible := make([]int, 0, len(pageFacts))
	for pageIndex := range pageFacts {
		if pageFacts[pageIndex].ContentExtractionFailed() {
			continue
		}
		if strings.TrimSpace(pageFacts[pageIndex].VisibleText) == "" {
			continue
		}
		eligible = append(eligible, pageIndex)
	}
	return eligible
}

func runGaoyaNearDuplicateDetection(ctx context.Context, pageFacts []shared.PageFact) ([]gaoyaNearDuplicatePair, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	eligible := gaoyaEligiblePageIndexes(pageFacts)
	if len(eligible) < 2 {
		return nil, nil
	}
	workDir, err := os.MkdirTemp("", "revserp-gaoya-")
	if err != nil {
		return nil, fmt.Errorf("revserp-gaoya: create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	inputPath := filepath.Join(workDir, "input.jsonl")
	outDir := filepath.Join(workDir, "out")
	if err := writeGaoyaInput(ctx, inputPath, pageFacts, eligible); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := executeGaoyaBinary(ctx, inputPath, outDir); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return readGaoyaPairs(ctx, outDir, eligible)
}

// writeGaoyaInput writes one JSONL row per eligible page: {id, text} with the
// stable page index as a STRING id and visible text only. Every encode and
// write failure aborts the analysis; rows are never quietly dropped.
func writeGaoyaInput(ctx context.Context, inputPath string, pageFacts []shared.PageFact, eligible []int) error {
	inputFile, err := os.OpenFile(inputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("revserp-gaoya: create input: %w", err)
	}
	writeErr := func() error {
		for rowNumber, pageIndex := range eligible {
			if rowNumber%gaoyaInputCtxCheckEvery == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			// Text schema errors must fail, not quiet-drop: any marshal or
			// write error aborts before the binary ever runs.
			row, err := json.Marshal(struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			}{ID: strconv.Itoa(pageIndex), Text: pageFacts[pageIndex].VisibleText})
			if err != nil {
				return fmt.Errorf("revserp-gaoya: encode input row %d: %w", rowNumber, err)
			}
			row = append(row, '\n')
			if _, err := inputFile.Write(row); err != nil {
				return fmt.Errorf("revserp-gaoya: write input row %d: %w", rowNumber, err)
			}
		}
		return nil
	}()
	if closeErr := inputFile.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return writeErr
	}
	return nil
}

// executeGaoyaBinary runs the wrapper with bounded time (default 5 minutes or
// the remaining context deadline, whichever is earlier), pinned worker threads,
// and bounded stderr capture.
func executeGaoyaBinary(ctx context.Context, inputPath string, outDir string) error {
	timeout := gaoyaDefaultTimeout
	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(execCtx, gaoyaBinaryPath(),
		"--input", inputPath,
		"--out-dir", outDir,
		"--threshold", strconv.FormatFloat(gaoyaSimilarityThreshold, 'f', -1, 64),
		"--num-hashes", strconv.Itoa(gaoyaNumHashes),
		"--shingle", strconv.Itoa(gaoyaShingleWords),
		"--lowercase", strconv.FormatBool(gaoyaLowercase),
		"--seed", strconv.FormatUint(uint64(gaoyaSeed), 10),
	)
	cmd.Env = append(os.Environ(), "RAYON_NUM_THREADS="+gaoyaRayonNumThreads)
	stderr := &cappedBuffer{limit: gaoyaStderrCaptureLimit}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if execCtx.Err() != nil {
			return fmt.Errorf("revserp-gaoya: binary timed out or was cancelled: %w", execCtx.Err())
		}
		return fmt.Errorf("revserp-gaoya: binary failed: %v: %s", err, stderr.String())
	}
	return nil
}

type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (capped *cappedBuffer) Write(output []byte) (int, error) {
	consumed := len(output)
	if remaining := capped.limit - capped.buf.Len(); remaining > 0 {
		if len(output) > remaining {
			output = output[:remaining]
			capped.truncated = true
		}
		capped.buf.Write(output)
	} else if len(output) > 0 {
		capped.truncated = true
	}
	return consumed, nil
}

func (capped *cappedBuffer) String() string {
	if capped.truncated {
		return capped.buf.String() + "...(truncated)"
	}
	return capped.buf.String()
}

// readGaoyaPairs streams pairs.jsonl ({left_id, right_id, score,
// score_kind:"minhash"}) with a bounded line buffer and maps the
// string-or-integer ids back to eligible page indexes. Unknown ids, self
// pairs, a wrong score_kind, scores below the configured threshold or
// outside [0,1], and malformed rows are errors, never partial success.
func readGaoyaPairs(ctx context.Context, outDir string, eligible []int) ([]gaoyaNearDuplicatePair, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pairsFile, err := os.Open(filepath.Join(outDir, "pairs.jsonl"))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("revserp-gaoya: read pairs output: %w", err)
	}
	defer func() { _ = pairsFile.Close() }()
	eligibleByID := make(map[string]int, len(eligible))
	for _, pageIndex := range eligible {
		eligibleByID[strconv.Itoa(pageIndex)] = pageIndex
	}
	scanner := bufio.NewScanner(pairsFile)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	seen := make(map[[2]int]struct{})
	var pairs []gaoyaNearDuplicatePair
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		trimmed := bytes.TrimSpace(scanner.Bytes())
		if len(trimmed) == 0 {
			continue
		}
		// Copy before the next Scan reuses the buffer.
		line := append([]byte(nil), trimmed...)
		leftID, rightID, score, err := parseGaoyaPairLine(line)
		if err != nil {
			return nil, fmt.Errorf("revserp-gaoya: pairs.jsonl line %d: %w", lineNumber, err)
		}
		left, ok := eligibleByID[leftID]
		if !ok {
			return nil, fmt.Errorf("revserp-gaoya: pairs.jsonl line %d references unknown left_id %q", lineNumber, leftID)
		}
		right, ok := eligibleByID[rightID]
		if !ok {
			return nil, fmt.Errorf("revserp-gaoya: pairs.jsonl line %d references unknown right_id %q", lineNumber, rightID)
		}
		if left == right {
			return nil, fmt.Errorf("revserp-gaoya: pairs.jsonl line %d references the same page %q twice", lineNumber, leftID)
		}
		ordered := [2]int{left, right}
		if ordered[0] > ordered[1] {
			ordered[0], ordered[1] = ordered[1], ordered[0]
		}
		if _, dup := seen[ordered]; dup {
			continue
		}
		seen[ordered] = struct{}{}
		pairs = append(pairs, gaoyaNearDuplicatePair{left: ordered[0], right: ordered[1], similarity: score})
		if len(pairs)%gaoyaPairsCtxCheckEvery == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("revserp-gaoya: read pairs output: %w", err)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].left != pairs[j].left {
			return pairs[i].left < pairs[j].left
		}
		return pairs[i].right < pairs[j].right
	})
	return pairs, nil
}

func parseGaoyaPairLine(line []byte) (string, string, float64, error) {
	var record struct {
		LeftID    json.RawMessage `json:"left_id"`
		RightID   json.RawMessage `json:"right_id"`
		Score     json.RawMessage `json:"score"`
		ScoreKind string          `json:"score_kind"`
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&record); err != nil {
		return "", "", 0, fmt.Errorf("revserp-gaoya: malformed pair line: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", "", 0, fmt.Errorf("revserp-gaoya: malformed pair line: trailing data")
	}
	if record.ScoreKind != "minhash" {
		return "", "", 0, fmt.Errorf("revserp-gaoya: pair has unexpected score_kind %q, want \"minhash\"", record.ScoreKind)
	}
	leftID, err := gaoyaPairIDString(record.LeftID, "left_id")
	if err != nil {
		return "", "", 0, err
	}
	rightID, err := gaoyaPairIDString(record.RightID, "right_id")
	if err != nil {
		return "", "", 0, err
	}
	score, err := gaoyaPairScore(record.Score)
	if err != nil {
		return "", "", 0, err
	}
	return leftID, rightID, score, nil
}

func gaoyaPairIDString(raw json.RawMessage, field string) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("revserp-gaoya: pair is missing %s", field)
	}
	if raw[0] == '"' {
		var id string
		if err := json.Unmarshal(raw, &id); err != nil {
			return "", fmt.Errorf("revserp-gaoya: pair has invalid %s: %w", field, err)
		}
		return id, nil
	}
	var id json.Number
	if err := json.Unmarshal(raw, &id); err != nil {
		return "", fmt.Errorf("revserp-gaoya: pair has invalid %s: %w", field, err)
	}
	if _, err := id.Int64(); err != nil {
		return "", fmt.Errorf("revserp-gaoya: pair has non-integer %s %q", field, id.String())
	}
	return id.String(), nil
}

// gaoyaPairScore decodes the native MinHash estimate: a JSON number only,
// within [threshold, 1]. Anything below the configured threshold means the
// engine broke its own contract.
func gaoyaPairScore(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("revserp-gaoya: pair is missing score")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var score any
	if err := decoder.Decode(&score); err != nil {
		return 0, fmt.Errorf("revserp-gaoya: pair has invalid score: %w", err)
	}
	number, ok := score.(json.Number)
	if !ok {
		return 0, fmt.Errorf("revserp-gaoya: pair score must be a number")
	}
	value, err := number.Float64()
	if err != nil {
		return 0, fmt.Errorf("revserp-gaoya: pair has invalid score %q", number.String())
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < gaoyaSimilarityThreshold || value > 1 {
		return 0, fmt.Errorf("revserp-gaoya: pair score %v outside [%.2f,1]", value, gaoyaSimilarityThreshold)
	}
	return value, nil
}
