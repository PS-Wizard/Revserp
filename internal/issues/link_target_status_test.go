package issues

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func linkStatusTestPage(key string, status int32) sqlc.ListCrawlPagesForCrawlRow {
	return sqlc.ListCrawlPagesForCrawlRow{
		UrlKey:     pgtype.Text{String: key, Valid: true},
		StatusCode: pgtype.Int4{Int32: status, Valid: true},
	}
}

func TestLinkDestinationStatuses(t *testing.T) {
	t.Parallel()
	pages := []sqlc.ListCrawlPagesForCrawlRow{
		linkStatusTestPage("https://example.com/a", 404),
		linkStatusTestPage("https://example.com/a", 200),
		linkStatusTestPage("https://example.com/ok", 200),
		{UrlKey: pgtype.Text{String: "https://example.com/nostatus", Valid: true}},
		{UrlKey: pgtype.Text{}, StatusCode: pgtype.Int4{Int32: 200, Valid: true}},
	}
	statuses, err := linkDestinationStatuses(context.Background(), pages)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if got := statuses["https://example.com/a"]; got != 200 {
		t.Fatalf("key collision status = %d, want smallest 200", got)
	}
	if _, ok := statuses["https://example.com/nostatus"]; ok {
		t.Fatal("null page status must not enter the lookup")
	}
	if _, ok := statuses[""]; ok {
		t.Fatal("invalid page key must not enter the lookup")
	}
	if len(statuses) != 2 {
		t.Fatalf("lookup has %d keys, want 2", len(statuses))
	}
}

func TestResolveLinkTargetStatus(t *testing.T) {
	t.Parallel()
	known := map[string]int32{
		"https://example.com/gone":  404,
		"https://example.com/moved": 301,
		"https://example.com/ok":    200,
		"":                          404,
	}
	tests := []struct {
		name   string
		stored pgtype.Int4
		key    pgtype.Text
		want   int32
	}{
		{"broken target resolves 404", pgtype.Int4{}, pgtype.Text{String: "https://example.com/gone", Valid: true}, 404},
		{"redirect target resolves 301", pgtype.Int4{}, pgtype.Text{String: "https://example.com/moved", Valid: true}, 301},
		{"healthy target resolves 200", pgtype.Int4{}, pgtype.Text{String: "https://example.com/ok", Valid: true}, 200},
		{"unfetched target stays unknown", pgtype.Int4{}, pgtype.Text{String: "https://example.com/missing", Valid: true}, 0},
		{"legacy stored status wins over lookup", pgtype.Int4{Int32: 200, Valid: true}, pgtype.Text{String: "https://example.com/gone", Valid: true}, 200},
		{"legacy stored 404 wins without lookup", pgtype.Int4{Int32: 404, Valid: true}, pgtype.Text{String: "https://example.com/missing", Valid: true}, 404},
		{"null key stays unknown", pgtype.Int4{}, pgtype.Text{}, 0},
		{"stored status wins with null key", pgtype.Int4{Int32: 404, Valid: true}, pgtype.Text{}, 404},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveLinkTargetStatus(test.stored, test.key, known); got != test.want {
				t.Fatalf("status = %d, want %d", got, test.want)
			}
		})
	}
}

func linkFactTestRow(source, target, key string, stored pgtype.Int4) sqlc.ListInternalLinkFactsForCrawlRow {
	return sqlc.ListInternalLinkFactsForCrawlRow{
		SourceUrl:    source,
		TargetUrl:    target,
		TargetUrlKey: pgtype.Text{String: key, Valid: true},
		TargetStatus: stored,
	}
}

func TestBuildLinkFacts(t *testing.T) {
	t.Parallel()
	rows := []sqlc.ListInternalLinkFactsForCrawlRow{
		linkFactTestRow("https://example.com/", "https://example.com/gone", "https://example.com/gone", pgtype.Int4{}),
		linkFactTestRow("https://example.com/", "https://example.com/legacy", "https://example.com/other", pgtype.Int4{Int32: 500, Valid: true}),
		linkFactTestRow("https://example.com/", "https://example.com/missing", "https://example.com/missing", pgtype.Int4{}),
	}
	known := map[string]int32{"https://example.com/gone": 404}
	facts, err := buildLinkFacts(context.Background(), rows, known)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if len(facts) != 3 {
		t.Fatalf("facts = %d, want 3", len(facts))
	}
	if facts[0].TargetStatus != 404 || facts[1].TargetStatus != 500 || facts[2].TargetStatus != 0 {
		t.Fatalf("statuses = %v, want [404 500 0]", facts)
	}
}

func TestBuildLinkFactsCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rows := []sqlc.ListInternalLinkFactsForCrawlRow{
		linkFactTestRow("https://example.com/", "https://example.com/gone", "https://example.com/gone", pgtype.Int4{}),
	}
	if _, err := buildLinkFacts(ctx, rows, map[string]int32{}); err == nil {
		t.Fatal("error = nil on cancelled context, want cancellation")
	}
}

func TestLinkDestinationStatusesCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pages := make([]sqlc.ListCrawlPagesForCrawlRow, deriveCtxCheckPages+1)
	if _, err := linkDestinationStatuses(ctx, pages); err == nil {
		t.Fatal("error = nil on cancelled context, want cancellation")
	}
}

func BenchmarkLinkDestinationStatuses6kPages(b *testing.B) {
	pages := make([]sqlc.ListCrawlPagesForCrawlRow, 6000)
	for i := range pages {
		pages[i] = linkStatusTestPage(fmt.Sprintf("https://example.com/page-%d", i%600), int32(200+i%5))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := linkDestinationStatuses(context.Background(), pages); err != nil {
			b.Fatalf("error = %v, want nil", err)
		}
	}
}

var resolveLinkTargetStatusSink int64

func BenchmarkResolveLinkTargetStatus1M(b *testing.B) {
	keys := []string{
		"https://example.com/gone",
		"https://example.com/moved",
		"https://example.com/ok",
		"https://example.com/missing",
		"https://example.com/gone",
		"https://example.com/ok",
		"https://example.com/moved",
		"https://example.com/absent",
	}
	known := map[string]int32{
		"https://example.com/gone":  404,
		"https://example.com/moved": 301,
		"https://example.com/ok":    200,
	}
	unstored := pgtype.Int4{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var sum int64
		for j := 0; j < 1000000; j++ {
			sum += int64(resolveLinkTargetStatus(unstored, pgtype.Text{String: keys[j%len(keys)], Valid: true}, known))
		}
		resolveLinkTargetStatusSink = sum
	}
}
