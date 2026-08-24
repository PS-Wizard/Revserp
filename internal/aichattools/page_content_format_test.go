package aichattools

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestFormatPageContentBlocks_Table(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		pageURL    string
		wantErr    bool
		wantLen    int
		check      func(t *testing.T, got []pageContentBlock)
		wantNoHTML bool
	}{
		{
			name:    "empty array",
			raw:     `[]`,
			wantLen: 0,
		},
		{
			name:    "null gives empty",
			raw:     `null`,
			wantLen: 0,
		},
		{
			name:    "empty bytes gives empty",
			raw:     ``,
			wantLen: 0,
		},
		{
			name:    "whitespace gives empty",
			raw:     `   `,
			wantLen: 0,
		},
		{
			name:    "malformed json errors",
			raw:     `not json`,
			wantErr: true,
		},
		{
			name:    "malformed top-level object errors",
			raw:     `{"tag":"h1","text":"hi"}`,
			wantErr: true,
		},
		{
			name:    "unknown tag skipped",
			raw:     `[{"tag":"div","text":"hello","html":"hello"}]`,
			wantLen: 0,
		},
		{
			name:    "heading strong conversion",
			raw:     `[{"tag":"h2","text":"Hi world","html":"<strong>Hi</strong> world"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Type != "heading" || got[0].Level != 2 || got[0].Markdown != "**Hi** world" {
					t.Fatalf("heading mismatch %+v", got[0])
				}
			},
		},
		{
			name:    "heading fallback when html missing",
			raw:     `[{"tag":"h1","text":"Hello *world*"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				// text "*world*" should be escaped
				if got[0].Type != "heading" || got[0].Level != 1 {
					t.Fatalf("want heading h1 got %+v", got[0])
				}
				if !strings.Contains(got[0].Markdown, `\*world\*`) {
					t.Fatalf("want escaped *, got %q", got[0].Markdown)
				}
			},
		},
		{
			name:    "heading fallback when html malformed empty",
			raw:     `[{"tag":"h1","text":"fallback text","html":""}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Markdown != "fallback text" {
					t.Fatalf("fallback markdown %q", got[0].Markdown)
				}
			},
		},
		{
			name:    "paragraph link relative resolved",
			raw:     `[{"tag":"p","text":"click","html":"<a href=\"/path\">click</a>"}]`,
			pageURL: "https://example.com/base/page",
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Type != "paragraph" {
					t.Fatalf("type %q", got[0].Type)
				}
				want := "[click](https://example.com/path)"
				if got[0].Markdown != want {
					t.Fatalf("markdown %q want %q", got[0].Markdown, want)
				}
			},
		},
		{
			name:    "paragraph link unsafe scheme stripped",
			raw:     `[{"tag":"p","text":"x","html":"<a href=\"javascript:alert(1)\">x</a>"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if strings.Contains(got[0].Markdown, "javascript") {
					t.Fatalf("unsafe scheme leaked %q", got[0].Markdown)
				}
				if got[0].Markdown != "x" {
					t.Fatalf("want plain x, got %q", got[0].Markdown)
				}
			},
		},
		{
			name:    "paragraph link mailto allowed",
			raw:     `[{"tag":"p","text":"mail","html":"<a href=\"mailto:foo@example.com\">mail</a>"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Markdown != "[mail](mailto:foo@example.com)" {
					t.Fatalf("mailto %q", got[0].Markdown)
				}
			},
		},
		{
			name:    "paragraph br and em",
			raw:     `[{"tag":"p","text":"a b","html":"line1<br>line2 <em>hi</em>"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Markdown != "line1\\\nline2 *hi*" {
					t.Fatalf("br/em %q", got[0].Markdown)
				}
			},
		},
		{
			name:    "paragraph inline code",
			raw:     `[{"tag":"p","text":"code","html":"<code>foo</code>"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Markdown != "`foo`" {
					t.Fatalf("code %q", got[0].Markdown)
				}
			},
		},
		{
			name:    "paragraph inline code with backtick",
			raw:     `[{"tag":"p","text":"x","html":"<code>a ` + "`" + ` b</code>"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				want := "``a ` b``"
				if got[0].Markdown != want {
					t.Fatalf("code backtick %q want %q", got[0].Markdown, want)
				}
			},
		},
		{
			name:    "paragraph inline image",
			raw:     `[{"tag":"p","text":"Check","html":"Check <img src=\"/img.jpg\" alt=\"pic\"> now"}]`,
			pageURL: "https://example.com/",
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				want := "Check ![pic](https://example.com/img.jpg) now"
				if got[0].Markdown != want {
					t.Fatalf("inline img %q want %q", got[0].Markdown, want)
				}
			},
		},
		{
			name:    "paragraph strong b em i",
			raw:     `[{"tag":"p","text":"t","html":"<strong>a</strong> <b>b</b> <em>c</em> <i>d</i>"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Markdown != "**a** **b** *c* *d*" {
					t.Fatalf("strong/b em/i %q", got[0].Markdown)
				}
			},
		},
		{
			name:    "paragraph url escaping parentheses",
			raw:     `[{"tag":"p","text":"x","html":"<a href=\"https://example.com/a(b)\">x</a>"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if !strings.Contains(got[0].Markdown, `\(b\)`) {
					t.Fatalf("url escaping %q", got[0].Markdown)
				}
			},
		},
		{
			name:    "unordered list simple",
			raw:     `[{"tag":"ul","text":"item1 item2","html":"<li>item1</li><li>item <strong>2</strong></li>"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Type != "unordered_list" {
					t.Fatalf("type %q", got[0].Type)
				}
				if len(got[0].Items) != 2 {
					t.Fatalf("items %v", got[0].Items)
				}
				if got[0].Items[0] != "item1" || got[0].Items[1] != "item **2**" {
					t.Fatalf("items %q", got[0].Items)
				}
			},
		},
		{
			name:    "ordered list",
			raw:     `[{"tag":"ol","text":"a b","html":"<li>a</li><li>b</li>"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Type != "ordered_list" || len(got[0].Items) != 2 {
					t.Fatalf("ol %+v", got[0])
				}
			},
		},
		{
			name:    "nested list indentation",
			raw:     `[{"tag":"ul","text":"top nested1","html":"<li>top<ul><li>nested1</li><li>nested2</li></ul></li><li>second</li>"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if len(got[0].Items) != 2 {
					t.Fatalf("nested len %d %v", len(got[0].Items), got[0].Items)
				}
				if !strings.Contains(got[0].Items[0], "top") || !strings.Contains(got[0].Items[0], "    - nested1") {
					t.Fatalf("nested item %q", got[0].Items[0])
				}
				if !strings.Contains(got[0].Items[0], "\n") {
					t.Fatalf("nested should have newline %q", got[0].Items[0])
				}
			},
		},
		{
			name:    "list fallback when html missing",
			raw:     `[{"tag":"ul","text":"only text"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if len(got[0].Items) != 1 || got[0].Items[0] != "only text" {
					t.Fatalf("fallback items %v", got[0].Items)
				}
			},
		},
		{
			name:    "blockquote",
			raw:     `[{"tag":"blockquote","text":"quote","html":"<strong>quote</strong> text"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Type != "blockquote" || got[0].Markdown != "**quote** text" {
					t.Fatalf("bq %+v", got[0])
				}
			},
		},
		{
			name:    "image block relative url resolved",
			raw:     `[{"tag":"img","text":"desc","html":"<img src=\"/a.jpg\" alt=\"desc\">"}]`,
			pageURL: "https://example.com/page",
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Type != "image" || got[0].Alt != "desc" || got[0].URL != "https://example.com/a.jpg" {
					t.Fatalf("img %+v", got[0])
				}
			},
		},
		{
			name:    "image block with data-src priority",
			raw:     `[{"tag":"img","text":"x","html":"<img data-src=\"/b.jpg\" src=\"/a.jpg\" alt=\"x\">"}]`,
			pageURL: "https://example.com/",
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].URL != "https://example.com/b.jpg" {
					t.Fatalf("data-src priority %q", got[0].URL)
				}
			},
		},
		{
			name:    "image block skip if empty",
			raw:     `[{"tag":"img","text":"","html":"<img src=\"\" alt=\"\">"}]`,
			wantLen: 0,
		},
		{
			name:    "image block fallback text as alt",
			raw:     `[{"tag":"img","text":"fallback alt"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Alt != "fallback alt" || got[0].URL != "" {
					t.Fatalf("fallback alt %+v", got[0])
				}
			},
		},
		{
			name:    "image block mailto not url",
			raw:     `[{"tag":"img","text":"x","html":"<img src=\"javascript:alert(1)\" alt=\"x\">"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].URL != "" {
					t.Fatalf("unsafe img url not filtered %q", got[0].URL)
				}
				if got[0].Alt != "x" {
					t.Fatalf("alt %q", got[0].Alt)
				}
			},
		},
		{
			name:    "pre code block",
			raw:     `[{"tag":"pre","text":"fallback","html":"code <b>line</b>"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Type != "code" || got[0].Text != "code line" {
					t.Fatalf("pre %+v", got[0])
				}
			},
		},
		{
			name:    "pre fallback when html absent",
			raw:     `[{"tag":"pre","text":"fallback code"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Text != "fallback code" {
					t.Fatalf("pre fallback %q", got[0].Text)
				}
			},
		},
		{
			name:    "preserve order",
			raw:     `[{"tag":"h1","text":"title","html":"title"},{"tag":"p","text":"para","html":"para"},{"tag":"ul","text":"a","html":"<li>a</li>"}]`,
			wantLen: 3,
			check: func(t *testing.T, got []pageContentBlock) {
				if got[0].Type != "heading" || got[1].Type != "paragraph" || got[2].Type != "unordered_list" {
					t.Fatalf("order %+v", got)
				}
			},
		},
		{
			name:    "escape text special chars",
			raw:     `[{"tag":"p","text":"a * b"}]`,
			wantLen: 1,
			check: func(t *testing.T, got []pageContentBlock) {
				if !strings.Contains(got[0].Markdown, `\*`) {
					t.Fatalf("escape %q", got[0].Markdown)
				}
			},
		},
		{
			name:    "empty heading skipped",
			raw:     `[{"tag":"h1","text":"","html":""}]`,
			wantLen: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := formatPageContentBlocks([]byte(tc.raw), tc.pageURL)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("len %d want %d got %+v", len(got), tc.wantLen, got)
			}
			if tc.check != nil {
				tc.check(t, got)
			}
			// assert marshaled JSON has no html
			if len(got) > 0 {
				b, err := json.Marshal(got)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				if strings.Contains(string(b), `"html"`) {
					t.Fatalf("marshaled JSON contains html: %s", string(b))
				}
				// also ensure no raw tag "html" appears as field
				var m []map[string]any
				if err := json.Unmarshal(b, &m); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				for _, mm := range m {
					if _, ok := mm["html"]; ok {
						t.Fatalf("marshaled block has html field: %v", mm)
					}
				}
			}
		})
	}
}

func TestFormatPageContentBlocks_MarshaledNoHTML(t *testing.T) {
	raw := `[{"tag":"p","text":"hi","html":"<strong>hi</strong>"},{"tag":"img","text":"a","html":"<img src=\"/a.jpg\" alt=\"a\">"}]`
	got, err := formatPageContentBlocks([]byte(raw), "https://example.com/")
	if err != nil {
		t.Fatalf("err %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal %v", err)
	}
	s := string(b)
	if strings.Contains(s, "html") {
		t.Fatalf("html leaked in json %s", s)
	}
	if strings.Contains(s, "<strong>") {
		t.Fatalf("raw html leaked %s", s)
	}
}

func TestFormatPageContentBlocks_RelativeURLResolution(t *testing.T) {
	raw := `[{"tag":"p","text":"x","html":"<a href=\"../other\">x</a>"}]`
	got, err := formatPageContentBlocks([]byte(raw), "https://example.com/a/b/page")
	if err != nil {
		t.Fatalf("err %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len %d", len(got))
	}
	if !strings.Contains(got[0].Markdown, "https://example.com/a/other") {
		t.Fatalf("relative not resolved %q", got[0].Markdown)
	}
}

func TestFormatPageContentBlocks_OnlyHttpHttpsMailtoTel(t *testing.T) {
	cases := []struct {
		href string
		keep bool
	}{
		{"https://example.com/", true},
		{"http://example.com/", true},
		{"mailto:test@example.com", true},
		{"tel:+123", true},
		{"ftp://example.com/", false},
		{"data:text/plain,hi", false},
		{"javascript:alert(1)", false},
	}
	for _, c := range cases {
		raw := mustJSON(t, []map[string]string{{"tag": "p", "text": "x", "html": `<a href="` + c.href + `">x</a>`}})
		got, err := formatPageContentBlocks(raw, "https://example.com/")
		if err != nil {
			t.Fatalf("err %v", err)
		}
		hasLink := strings.Contains(got[0].Markdown, "](")
		if hasLink != c.keep {
			t.Fatalf("href %q keep=%v got markdown %q", c.href, c.keep, got[0].Markdown)
		}
	}
}

func TestFormatPageContentBlocksPreservesEscapedEdgePunctuation(t *testing.T) {
	got, err := formatPageContentBlocks([]byte(`[{"tag":"p","text":"*literal*"}]`), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Markdown != `\*literal\*` {
		t.Fatalf("markdown = %q", got[0].Markdown)
	}
}
