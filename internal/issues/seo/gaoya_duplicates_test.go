package seo

import (
	"context"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ps-wizard/revserp/internal/issues/shared"
)

func gaoyaStubPath(t *testing.T) string {
	t.Helper()
	absolute, err := filepath.Abs(filepath.Join("testdata", "gaoya_stub.sh"))
	if err != nil {
		t.Fatalf("stub path: %v", err)
	}
	if err := os.Chmod(absolute, 0o755); err != nil {
		t.Fatalf("chmod stub: %v", err)
	}
	t.Setenv("GAOYA_PATH", absolute)
	return absolute
}

func gaoyaBinaryAvailable() bool {
	if override := os.Getenv("GAOYA_PATH"); override != "" {
		stat, err := os.Stat(override)
		return err == nil && !stat.IsDir()
	}
	_, err := exec.LookPath("revserp-gaoya")
	return err == nil
}

func gaoyaStubTextPages(urls ...string) []shared.PageFact {
	pages := make([]shared.PageFact, 0, len(urls))
	for index, url := range urls {
		pages = append(pages, shared.PageFact{
			ID:          testDuplicateUUID(byte(index + 1)),
			URL:         url,
			VisibleText: "stub input page " + url + " carries enough distinct words to be eligible",
		})
	}
	return pages
}

func gaoyaRelationURLs(evidence DuplicateEvidence) [][2]string {
	relations := make([][2]string, 0, len(evidence.Relations))
	for _, relation := range evidence.Relations {
		relations = append(relations, [2]string{relation.LeftPage.URL, relation.RightPage.URL})
	}
	return relations
}

func TestGaoyaPairFlowsIntoRelationsWithEngineScore(t *testing.T) {
	gaoyaStubPath(t)
	t.Setenv("GAOYA_STUB_MODE", "pairs")
	t.Setenv("GAOYA_STUB_PAIR", "2:0:0.93")
	pages := gaoyaStubTextPages("https://example.com/a", "https://example.com/b", "https://example.com/c")

	issues, evidence, err := deriveNearDuplicateIssuesWithEvidence(context.Background(), pages)
	if err != nil {
		t.Fatalf("deriveNearDuplicateIssuesWithEvidence: %v", err)
	}
	if len(evidence.Relations) != 1 {
		t.Fatalf("got %d relations, want 1", len(evidence.Relations))
	}
	relation := evidence.Relations[0]
	if relation.LeftPage.URL != "https://example.com/a" || relation.RightPage.URL != "https://example.com/c" {
		t.Fatalf("pair not ordered deterministically: %+v", relation)
	}
	if relation.Similarity != 0.93 {
		t.Fatalf("similarity = %v, want engine-native 0.93", relation.Similarity)
	}
	if relation.IssueType != DuplicateIssueTypeNearDuplicateContent {
		t.Fatalf("issue type = %q", relation.IssueType)
	}
	issueURLs := map[string]bool{}
	for _, issue := range issues {
		if issue.IssueType != "near_duplicate_content" {
			t.Fatalf("unexpected issue type %q", issue.IssueType)
		}
		issueURLs[issue.URL] = true
	}
	if len(issueURLs) != 2 || !issueURLs["https://example.com/a"] || !issueURLs["https://example.com/c"] {
		t.Fatalf("near-duplicate issues cover %v, want a and c", issueURLs)
	}
}

func TestGaoyaStringIDsMapToEligiblePages(t *testing.T) {
	gaoyaStubPath(t)
	t.Setenv("GAOYA_STUB_MODE", "stringids")
	t.Setenv("GAOYA_STUB_PAIR", "1:0:0.97")
	pages := gaoyaStubTextPages("https://example.com/a", "https://example.com/b")

	_, evidence, err := deriveNearDuplicateIssuesWithEvidence(context.Background(), pages)
	if err != nil {
		t.Fatalf("deriveNearDuplicateIssuesWithEvidence: %v", err)
	}
	if len(evidence.Relations) != 1 {
		t.Fatalf("got %d relations, want 1", len(evidence.Relations))
	}
	relation := evidence.Relations[0]
	if relation.LeftPage.URL != "https://example.com/a" || relation.RightPage.URL != "https://example.com/b" {
		t.Fatalf("string ids mis-mapped: %+v", relation)
	}
	if relation.Similarity != 0.97 {
		t.Fatalf("similarity = %v, want 0.97", relation.Similarity)
	}
}

func TestGaoyaEligibilitySkipsFailedAndBlankPages(t *testing.T) {
	pages := []shared.PageFact{
		{URL: "https://example.com/failed", VisibleText: "error text must not count", ContentExtractionStatus: "failed"},
		{URL: "https://example.com/empty"},
		{URL: "https://example.com/spaces", VisibleText: "   \t\n "},
		{URL: "https://example.com/good", VisibleText: "real body text"},
	}
	eligible := gaoyaEligiblePageIndexes(pages)
	if len(eligible) != 1 || eligible[0] != 3 {
		t.Fatalf("eligible = %v, want [3]", eligible)
	}
}

func TestGaoyaFewerThanTwoEligibleSkipsBinary(t *testing.T) {
	t.Setenv("GAOYA_PATH", filepath.Join(t.TempDir(), "does-not-exist"))
	cases := map[string][]shared.PageFact{
		"none":   {},
		"single": {{URL: "https://example.com/a", VisibleText: "only page"}},
		"failed plus good": {
			{URL: "https://example.com/a", VisibleText: "base text", ContentExtractionStatus: "failed"},
			{URL: "https://example.com/b", VisibleText: "other text"},
		},
		"blank plus good": {
			{URL: "https://example.com/a"},
			{URL: "https://example.com/b", VisibleText: "other text"},
		},
	}
	for name, pages := range cases {
		pairs, err := runGaoyaNearDuplicateDetection(context.Background(), pages)
		if err != nil {
			t.Fatalf("%s: fast return must not need the binary: %v", name, err)
		}
		if len(pairs) != 0 {
			t.Fatalf("%s: got %d pairs, want none", name, len(pairs))
		}
	}
}

func TestGaoyaInvalidOutputIsErrorNotPartialSuccess(t *testing.T) {
	pages := gaoyaStubTextPages("https://example.com/a", "https://example.com/b")
	cases := map[string]string{
		"unknown left":       "pairs:9:1:0.95",
		"unknown right":      "pairs:0:7:0.95",
		"self pair":          "pairs:1:1:0.95",
		"score above":        "pairs:0:1:1.5",
		"score below":        "pairs:0:1:-0.2",
		"score under thresh": "pairs:0:1:0.5",
		"score text":         "pairs:0:1:abc",
		"malformed":          "malformed:",
	}
	for name, stub := range cases {
		gaoyaStubPath(t)
		mode, pair, _ := strings.Cut(stub, ":")
		t.Setenv("GAOYA_STUB_MODE", mode)
		if pair != "" {
			t.Setenv("GAOYA_STUB_PAIR", pair)
		}
		pairs, err := runGaoyaNearDuplicateDetection(context.Background(), pages)
		if err == nil {
			t.Fatalf("%s: expected error, got pairs %v", name, pairs)
		}
		if pairs != nil {
			t.Fatalf("%s: expected no partial pairs, got %v", name, pairs)
		}
	}
}

func TestGaoyaLargeStderrDoesNotFail(t *testing.T) {
	gaoyaStubPath(t)
	t.Setenv("GAOYA_STUB_MODE", "noisy")
	pages := gaoyaStubTextPages("https://example.com/a", "https://example.com/b")

	pairs, err := runGaoyaNearDuplicateDetection(context.Background(), pages)
	if err != nil {
		t.Fatalf("bounded stderr capture must not fail a good run: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("got %d pairs, want 1", len(pairs))
	}
}

func TestCappedBufferReturnsFullLength(t *testing.T) {
	t.Parallel()
	capped := &cappedBuffer{limit: 16}
	payload := make([]byte, 64)
	for index := range payload {
		payload[index] = byte('a' + index%26)
	}
	written, err := capped.Write(payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if written != len(payload) {
		t.Fatalf("Write returned %d, want full %d (io.Writer contract)", written, len(payload))
	}
	if capped.buf.Len() > 16 {
		t.Fatalf("buffer holds %d bytes, want at most 16", capped.buf.Len())
	}
	if !capped.truncated {
		t.Fatal("expected truncated flag after over-limit write")
	}
}

func TestGaoyaBinaryFailureIsErrorWithoutFallback(t *testing.T) {
	pages := gaoyaStubTextPages("https://example.com/a", "https://example.com/b")

	gaoyaStubPath(t)
	t.Setenv("GAOYA_STUB_MODE", "crash")
	if _, err := runGaoyaNearDuplicateDetection(context.Background(), pages); err == nil {
		t.Fatal("crashing binary must fail the analysis, not fall back")
	}

	t.Setenv("GAOYA_PATH", filepath.Join(t.TempDir(), "does-not-exist"))
	if _, err := runGaoyaNearDuplicateDetection(context.Background(), pages); err == nil {
		t.Fatal("missing binary must fail the analysis, not fall back")
	}
}

func TestGaoyaCancelledDuringRun(t *testing.T) {
	gaoyaStubPath(t)
	t.Setenv("GAOYA_STUB_MODE", "hang")
	pages := gaoyaStubTextPages("https://example.com/a", "https://example.com/b", "https://example.com/c")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	issues, evidence, err := deriveNearDuplicateIssuesWithEvidence(ctx, pages)
	if err == nil {
		t.Fatal("cancelled run must return an error")
	}
	if issues != nil || len(evidence.Relations) != 0 || len(evidence.Groups) != 0 {
		t.Fatalf("cancelled run returned partial results: %d issues", len(issues))
	}
}

func TestParseGaoyaPairLine(t *testing.T) {
	t.Parallel()
	left, right, score, err := parseGaoyaPairLine([]byte(`{"left_id":0,"right_id":"3","score":0.900000,"score_kind":"minhash"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if left != "0" || right != "3" || score != 0.9 {
		t.Fatalf("parsed (%q,%q,%v)", left, right, score)
	}
	for name, line := range map[string]string{
		"trailing data":      `{"left_id":0,"right_id":1,"score":0.9,"score_kind":"minhash"} {}`,
		"missing score":      `{"left_id":0,"right_id":1,"score_kind":"minhash"}`,
		"missing score kind": `{"left_id":0,"right_id":1,"score":0.9}`,
		"wrong score kind":   `{"left_id":0,"right_id":1,"score":0.9,"score_kind":"exact"}`,
		"score under thresh": `{"left_id":0,"right_id":1,"score":0.5,"score_kind":"minhash"}`,
		"float id":           `{"left_id":1.5,"right_id":1,"score":0.9,"score_kind":"minhash"}`,
		"string score":       `{"left_id":0,"right_id":1,"score":"0.9","score_kind":"minhash"}`,
		"not json":           `nope`,
	} {
		if _, _, _, err := parseGaoyaPairLine([]byte(line)); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

var gaoyaFixtureVocab = []string{
	"garden", "tomato", "compost", "prune", "harvest", "mulch", "seedling", "irrigation",
	"greenhouse", "perennial", "shrub", "orchard", "vineyard", "meadow", "thistle", "clover",
	"maple", "birch", "cedar", "willow", "sparrow", "robin", "finch", "wren", "thrush",
	"warbler", "brook", "pond", "stream", "spring", "autumn", "winter", "summer", "morning",
	"evening", "sunset", "dawn", "trail", "path", "fence", "gate", "barn", "shed",
	"wheelbarrow", "trowel", "spade", "rake", "hoe", "shears", "gloves", "boots", "apron",
	"basket", "crate", "jar", "cellar", "attic", "porch", "deck", "patio", "fountain",
	"statue", "bench", "arbor", "trellis", "gazebo", "bridge", "tower", "chapel", "market",
	"bakery", "school", "library", "museum", "harbor", "lighthouse", "ferry", "island",
	"canyon", "desert", "forest", "jungle", "savanna", "tundra", "glacier", "volcano",
	"cavern", "waterfall", "rainbow", "thunder", "lightning", "breeze", "frost", "dew",
	"mist", "cloud", "stone", "pebble", "boulder", "granite", "marble", "slate", "copper",
	"bronze", "silver", "linen", "cotton", "wool", "silk",
}

func buildGaoyaBodyText(seed int64, words int) string {
	rng := rand.New(rand.NewSource(seed))
	var body strings.Builder
	for index := 0; index < words; index++ {
		if index > 0 {
			body.WriteByte(' ')
		}
		body.WriteString(gaoyaFixtureVocab[rng.Intn(len(gaoyaFixtureVocab))])
	}
	return body.String()
}

func buildGaoyaNearCopy(body string, at int, replacement string) string {
	words := strings.Split(body, " ")
	words[at%len(words)] = replacement
	return strings.Join(words, " ")
}

func TestGaoyaRealBinarySmoke(t *testing.T) {
	if !gaoyaBinaryAvailable() {
		t.Skip("revserp-gaoya not on PATH (or GAOYA_PATH unset); run with the binary available for full coverage")
	}
	base := buildGaoyaBodyText(7, 500)
	pages := []shared.PageFact{
		{ID: testDuplicateUUID(1), URL: "https://example.com/base", Title: "Base guide", VisibleText: base},
		{ID: testDuplicateUUID(2), URL: "https://example.com/copy", Title: "Copy guide", VisibleText: buildGaoyaNearCopy(base, 100, "kiwi")},
		{ID: testDuplicateUUID(3), URL: "https://example.com/other", Title: "Other guide", VisibleText: buildGaoyaBodyText(99, 500)},
		{ID: testDuplicateUUID(4), URL: "https://example.com/failed", Title: "Failed guide", VisibleText: base, ContentExtractionStatus: "failed"},
	}
	if err := EnrichPageFactsWithContentFingerprintsWithContext(context.Background(), pages); err != nil {
		t.Fatalf("fingerprints: %v", err)
	}

	issues, evidence, err := deriveDuplicateContentIssuesWithEvidence(context.Background(), pages)
	if err != nil {
		t.Fatalf("deriveDuplicateContentIssuesWithEvidence: %v", err)
	}
	found := false
	for _, relation := range evidence.Relations {
		if relation.Similarity < 0.9 || relation.Similarity > 1 {
			t.Fatalf("relation similarity %v outside [0.9,1]: %+v", relation.Similarity, relation)
		}
		pair := [2]string{relation.LeftPage.URL, relation.RightPage.URL}
		if relation.LeftPage.URL == "https://example.com/failed" || relation.RightPage.URL == "https://example.com/failed" {
			t.Fatalf("failed page must not appear in relations: %+v", relation)
		}
		if pair == [2]string{"https://example.com/base", "https://example.com/copy"} {
			found = true
		}
		if pair == [2]string{"https://example.com/base", "https://example.com/other"} ||
			pair == [2]string{"https://example.com/copy", "https://example.com/other"} {
			t.Fatalf("unrelated page paired: %+v", relation)
		}
	}
	if !found {
		t.Fatalf("near-copy main bodies not paired; relations: %v", gaoyaRelationURLs(evidence))
	}
	nearIssueURLs := map[string]bool{}
	for _, issue := range issues {
		if issue.IssueType == "near_duplicate_content" {
			nearIssueURLs[issue.URL] = true
		}
	}
	if !nearIssueURLs["https://example.com/base"] || !nearIssueURLs["https://example.com/copy"] {
		t.Fatalf("near-duplicate issues cover %v", nearIssueURLs)
	}
	if nearIssueURLs["https://example.com/other"] || nearIssueURLs["https://example.com/failed"] {
		t.Fatalf("unexpected near-duplicate issues: %v", nearIssueURLs)
	}
}
