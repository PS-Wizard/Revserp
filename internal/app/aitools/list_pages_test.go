package aitools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

type fakePageLister struct {
	gotListArg  sqlc.ListCrawlPagesFilteredForUserParams
	gotCountArg sqlc.CountCrawlPagesFilteredForUserParams
	rows        []sqlc.ListCrawlPagesFilteredForUserRow
	total       int64
}

func (f *fakePageLister) ListCrawlPagesFilteredForUser(_ context.Context, arg sqlc.ListCrawlPagesFilteredForUserParams) ([]sqlc.ListCrawlPagesFilteredForUserRow, error) {
	f.gotListArg = arg
	return f.rows, nil
}

func (f *fakePageLister) CountCrawlPagesFilteredForUser(_ context.Context, arg sqlc.CountCrawlPagesFilteredForUserParams) (int64, error) {
	f.gotCountArg = arg
	return f.total, nil
}

func TestExecListPages_UsesScopeIDsAndForwardsFilter(t *testing.T) {
	crawlID := testUUID(11)
	userID := testUUID(12)
	fake := &fakePageLister{
		rows:  []sqlc.ListCrawlPagesFilteredForUserRow{{Url: "https://example.com/pricing"}},
		total: 1,
	}

	result, err := execListPages(context.Background(), crawlID, userID, listPagesArgs{Filter: "pricing"}, fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.gotListArg.CrawlID != crawlID || fake.gotListArg.UserID != userID {
		t.Fatalf("expected list query to use scope IDs, got %+v", fake.gotListArg)
	}
	if fake.gotCountArg.CrawlID != crawlID || fake.gotCountArg.UserID != userID {
		t.Fatalf("expected count query to use scope IDs, got %+v", fake.gotCountArg)
	}
	if fake.gotListArg.Column3 != "pricing" {
		t.Errorf("expected filter forwarded, got %+v", fake.gotListArg)
	}

	var output listPagesOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		t.Fatalf("could not unmarshal result content: %v", err)
	}
	if output.TotalMatching != 1 || len(output.Pages) != 1 {
		t.Errorf("unexpected output: %+v", output)
	}
}

func TestExecListPages_ClampsLimit(t *testing.T) {
	fake := &fakePageLister{}
	if _, err := execListPages(context.Background(), testUUID(1), testUUID(2), listPagesArgs{Limit: 5000}, fake); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.gotListArg.Limit != maxListPagesLimit {
		t.Errorf("expected limit clamped to %d, got %d", maxListPagesLimit, fake.gotListArg.Limit)
	}
}
