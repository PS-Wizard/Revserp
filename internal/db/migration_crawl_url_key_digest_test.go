package db

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func readCrawlURLKeyDigestFile(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

func TestMigration088DigestIndexesReplaceRawKeyIndexes(t *testing.T) {
	migration := readCrawlURLKeyDigestFile(t, "../../migrations/000088_crawl_url_key_digest_indexes.sql")
	sections := strings.Split(migration, "-- +goose Down")
	if len(sections) != 2 {
		t.Fatal("migration 000088 must have one Down section")
	}
	up, down := sections[0], sections[1]
	compact := strings.Join(strings.Fields(up), " ")
	creates := []string{
		"CREATE INDEX CONCURRENTLY idx_crawl_links_crawl_id_target_url_key_digest ON crawl_links (crawl_id, digest(target_url_key, 'sha256'));",
		"CREATE INDEX CONCURRENTLY idx_crawl_pages_crawl_id_url_key_digest ON crawl_pages (crawl_id, digest(url_key, 'sha256'));",
	}
	if !strings.Contains(up, "+goose NO TRANSACTION") {
		t.Error("concurrent index builds require NO TRANSACTION")
	}
	for _, create := range creates {
		if !strings.Contains(compact, create) {
			t.Errorf("migration 000088 missing bounded index statement %q", create)
		}
	}
	firstCreate := strings.Index(up, "CREATE INDEX CONCURRENTLY idx_crawl_links_crawl_id_target_url_key_digest")
	secondCreate := strings.Index(up, "CREATE INDEX CONCURRENTLY idx_crawl_pages_crawl_id_url_key_digest")
	firstDrop := strings.Index(up, "DROP INDEX CONCURRENTLY IF EXISTS idx_crawl_links_crawl_id_target_url_key;")
	lastDrop := strings.Index(up, "DROP INDEX CONCURRENTLY IF EXISTS idx_crawl_pages_crawl_id_url_key;")
	if firstCreate < 0 || secondCreate < 0 || firstDrop < 0 || lastDrop < 0 ||
		!(firstCreate < secondCreate && secondCreate < firstDrop && firstDrop < lastDrop) {
		t.Error("migration 000088 must create both digest indexes before dropping old indexes")
	}
	restoreAt := strings.Index(down, "CREATE INDEX CONCURRENTLY idx_crawl_links_crawl_id_target_url_key")
	dropLinkDigestAt := strings.Index(down, "DROP INDEX CONCURRENTLY IF EXISTS idx_crawl_links_crawl_id_target_url_key_digest")
	dropPageDigestAt := strings.Index(down, "DROP INDEX CONCURRENTLY IF EXISTS idx_crawl_pages_crawl_id_url_key_digest")
	if restoreAt < 0 || dropLinkDigestAt < 0 || dropPageDigestAt < 0 || restoreAt > dropLinkDigestAt || restoreAt > dropPageDigestAt {
		t.Error("migration 000088 down must restore the link lookup index before dropping digest indexes")
	}
	if strings.Contains(down, "CREATE INDEX CONCURRENTLY idx_crawl_pages_crawl_id_url_key") {
		t.Error("migration 000088 down must not restore the page index removed by migration 000077")
	}
}

func TestResolveBatchJoinKeepsExactKeyEqualityBesideDigest(t *testing.T) {
	for _, file := range []string{"queries/crawl_links.sql", "sqlc/crawl_links.sql.go"} {
		query := readCrawlURLKeyDigestFile(t, file)
		for _, want := range []string{
			"digest(cp.url_key, 'sha256') = digest(cl.target_url_key, 'sha256')",
			"cp.url_key = cl.target_url_key",
		} {
			if !strings.Contains(query, want) {
				t.Errorf("%s resolver missing %q", file, want)
			}
		}
	}
}
