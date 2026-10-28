package mail

import (
	"errors"
	"strings"
	"testing"
)

func TestSendWithoutHostReturnsNotConfigured(t *testing.T) {
	m := New("", 587, "", "", "")
	if m.Configured() {
		t.Fatal("empty host should not be configured")
	}
	if err := m.Send("a@example.com", "s", "text", "<p>html</p>"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestBuildInviteRendersLink(t *testing.T) {
	c := BuildInvite("Ada <ada@example.com>", "Acme & Co", "https://beta.revserp.ai/invite/abc123")
	if !strings.Contains(c.Subject, "Acme & Co") {
		t.Fatalf("subject missing org: %q", c.Subject)
	}
	for _, want := range []string{"https://beta.revserp.ai/invite/abc123", "Accept invite", "Ada"} {
		if !strings.Contains(c.HTML, want) {
			t.Fatalf("html missing %q", want)
		}
	}
	if strings.Contains(c.HTML, "<ada@example.com>") {
		t.Fatal("html must escape the inviter address")
	}
	if !strings.Contains(c.Text, "https://beta.revserp.ai/invite/abc123") {
		t.Fatal("text part must carry the plain URL fallback")
	}
}

func TestBuildCrawlFinishedCopy(t *testing.T) {
	done := BuildCrawlFinished(true, "example.com", "https://beta.revserp.ai/app?project=1")
	if !strings.Contains(done.Subject, "completed") || !strings.Contains(done.HTML, "Open project") {
		t.Fatalf("bad completed copy: %+v", done)
	}
	failed := BuildCrawlFinished(false, "example.com", "https://beta.revserp.ai/app?project=1")
	if !strings.Contains(failed.Subject, "failed") || !strings.Contains(failed.HTML, "Crawl failed") {
		t.Fatalf("bad failed copy: %+v", failed)
	}
}
