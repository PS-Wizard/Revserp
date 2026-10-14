package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ps-wizard/revserp/internal/config"
	internaldb "github.com/ps-wizard/revserp/internal/db"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func crawlLinkStatusScratchPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if v := strings.TrimSpace(os.Getenv("DATABASE_URL")); v != "" {
		t.Skip("refusing to run: DATABASE_URL is set; scratch fixture only")
	}
	dsn := strings.TrimSpace(os.Getenv("CRAWL_LINK_STATUS_SCRATCH_DATABASE_URL"))
	if dsn == "" {
		t.Skip("CRAWL_LINK_STATUS_SCRATCH_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "postgres" || parsed.Hostname() != "127.0.0.1" || parsed.Port() != "18547" || strings.TrimPrefix(parsed.Path, "/") != "revserp_layer_c_link_status_20261014" {
		t.Skip("scratch DSN failed safety verification")
	}
	ctx := context.Background()
	pool, err := internaldb.Connect(ctx, dsn, config.DefaultDBStatementTimeout, config.DefaultDBLockTimeout)
	if err != nil {
		t.Skipf("scratch database is not available: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type linkStatusFixture struct {
	queries *sqlc.Queries
	ownerID pgtype.UUID
	otherID pgtype.UUID
	crawlA  pgtype.UUID
	crawlB  pgtype.UUID
	crawlP  pgtype.UUID
	links   map[string]pgtype.UUID
}

func insertLinkStatusUser(t *testing.T, ctx context.Context, tx pgx.Tx) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('link-status-test', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

func insertLinkStatusLink(t *testing.T, ctx context.Context, tx pgx.Tx, crawlID pgtype.UUID, source, target string, internal any, status any) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO crawl_links (crawl_id, source_url, target_url, is_internal, target_status) VALUES ($1,$2,$3,$4,$5) RETURNING id`, crawlID, source, target, internal, status).Scan(&id); err != nil {
		t.Fatalf("create link %s: %v", target, err)
	}
	return id
}

func newLinkStatusFixture(t *testing.T) (context.Context, *linkStatusFixture) {
	t.Helper()
	pool := crawlLinkStatusScratchPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin test transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	queries := sqlc.New(pool).WithTx(tx)

	var orgID pgtype.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('link-status-test') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatalf("create org: %v", err)
	}
	ownerID := insertLinkStatusUser(t, ctx, tx)
	otherID := insertLinkStatusUser(t, ctx, tx)
	if _, err := tx.Exec(ctx, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, ownerID); err != nil {
		t.Fatalf("create membership: %v", err)
	}
	var projectID pgtype.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'link-status-test','https://example.com') RETURNING id`, orgID).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	newCrawl := func() pgtype.UUID {
		var id pgtype.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO crawls (project_id, status) VALUES ($1,'completed') RETURNING id`, projectID).Scan(&id); err != nil {
			t.Fatalf("create crawl: %v", err)
		}
		return id
	}
	crawlA, crawlB, crawlP := newCrawl(), newCrawl(), newCrawl()

	addPage := func(crawl pgtype.UUID, pageURL string, status any) {
		if _, err := tx.Exec(ctx, `INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1,$2,$3)`, crawl, pageURL, status); err != nil {
			t.Fatalf("create page %s: %v", pageURL, err)
		}
	}
	addPage(crawlA, "https://example.com/a", 200)
	addPage(crawlA, "https://example.com/a/", 404)
	addPage(crawlA, "https://example.com/stored", 404)
	addPage(crawlA, "https://example.com/nostatus", nil)
	addPage(crawlA, "https://external.example/x", 500)
	addPage(crawlA, "https://example.com/leak", 500)
	addPage(crawlA, "https://example.com/only-b", 200)
	addPage(crawlB, "https://example.com/a", 404)
	addPage(crawlB, "https://example.com/leak", 200)

	links := map[string]pgtype.UUID{}
	links["stored"] = insertLinkStatusLink(t, ctx, tx, crawlA, "https://example.com/s-stored", "https://example.com/stored", true, 201)
	links["derived"] = insertLinkStatusLink(t, ctx, tx, crawlA, "https://example.com/s-derived", "https://example.com/a", true, nil)
	links["unknown"] = insertLinkStatusLink(t, ctx, tx, crawlA, "https://example.com/s-unknown", "https://example.com/missing", true, nil)
	links["nullpage"] = insertLinkStatusLink(t, ctx, tx, crawlA, "https://example.com/s-nullpage", "https://example.com/nostatus", true, nil)
	links["external"] = insertLinkStatusLink(t, ctx, tx, crawlA, "https://example.com/s-external", "https://external.example/x", false, nil)
	links["nullflag"] = insertLinkStatusLink(t, ctx, tx, crawlA, "https://example.com/s-nullflag", "https://example.com/a", nil, nil)
	links["leak"] = insertLinkStatusLink(t, ctx, tx, crawlA, "https://example.com/s-leak", "https://example.com/leak", true, nil)
	links["border"] = insertLinkStatusLink(t, ctx, tx, crawlB, "https://example.com/s-border", "https://example.com/only-b", true, nil)

	base := time.Now()
	for i := range 5 {
		target := "https://example.com/p" + string(rune('0'+i))
		addPage(crawlP, target, 200+i)
		var id pgtype.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO crawl_links (crawl_id, source_url, target_url, is_internal, target_status, created_at) VALUES ($1,$2,$3,TRUE,NULL,$4) RETURNING id`, crawlP, "https://example.com/src", target, base.Add(time.Duration(i)*time.Second)).Scan(&id); err != nil {
			t.Fatalf("create page link %d: %v", i, err)
		}
		links[target] = id
	}

	return ctx, &linkStatusFixture{queries: queries, ownerID: ownerID, otherID: otherID, crawlA: crawlA, crawlB: crawlB, crawlP: crawlP, links: links}
}

func linkStatusByTarget(t *testing.T, ctx context.Context, fx *linkStatusFixture, target string) sqlc.ListCrawlLinksForCrawlByUserRow {
	t.Helper()
	rows, err := fx.queries.ListCrawlLinksForCrawlByUser(ctx, sqlc.ListCrawlLinksForCrawlByUserParams{CrawlID: fx.crawlA, UserID: fx.ownerID, Limit: 200, Offset: 0})
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	for _, row := range rows {
		if row.TargetUrl == target {
			return row
		}
	}
	t.Fatalf("link %s not listed", target)
	return sqlc.ListCrawlLinksForCrawlByUserRow{}
}

func TestLinkStoredStatusPreserved(t *testing.T) {
	ctx, fx := newLinkStatusFixture(t)
	got, err := fx.queries.GetCrawlLinkByIDForUser(ctx, sqlc.GetCrawlLinkByIDForUserParams{ID: fx.links["stored"], UserID: fx.ownerID})
	if err != nil {
		t.Fatalf("get stored link: %v", err)
	}
	if !got.TargetStatus.Valid || got.TargetStatus.Int32 != 201 {
		t.Fatalf("stored status = %+v, want 201 over page 404", got.TargetStatus)
	}
	listed := linkStatusByTarget(t, ctx, fx, "https://example.com/stored")
	if !listed.TargetStatus.Valid || listed.TargetStatus.Int32 != 201 {
		t.Fatalf("listed stored status = %+v, want 201", listed.TargetStatus)
	}
}

func TestLinkDerivedLowestStatus(t *testing.T) {
	ctx, fx := newLinkStatusFixture(t)
	got, err := fx.queries.GetCrawlLinkByIDForUser(ctx, sqlc.GetCrawlLinkByIDForUserParams{ID: fx.links["derived"], UserID: fx.ownerID})
	if err != nil {
		t.Fatalf("get derived link: %v", err)
	}
	if !got.TargetStatus.Valid || got.TargetStatus.Int32 != 200 {
		t.Fatalf("derived status = %+v, want lowest 200 of {200,404}", got.TargetStatus)
	}
}

func TestLinkUnknownStaysNull(t *testing.T) {
	ctx, fx := newLinkStatusFixture(t)
	for _, key := range []string{"unknown", "nullpage", "external", "nullflag"} {
		row, err := fx.queries.GetCrawlLinkByIDForUser(ctx, sqlc.GetCrawlLinkByIDForUserParams{ID: fx.links[key], UserID: fx.ownerID})
		if err != nil {
			t.Fatalf("%s: get link: %v", key, err)
		}
		if row.TargetStatus.Valid {
			t.Errorf("%s: status = %+v, want NULL", key, row.TargetStatus)
		}
	}
}

func TestLinkCrossCrawlIsolated(t *testing.T) {
	ctx, fx := newLinkStatusFixture(t)
	leak, err := fx.queries.GetCrawlLinkByIDForUser(ctx, sqlc.GetCrawlLinkByIDForUserParams{ID: fx.links["leak"], UserID: fx.ownerID})
	if err != nil {
		t.Fatalf("get leak link: %v", err)
	}
	if !leak.TargetStatus.Valid || leak.TargetStatus.Int32 != 500 {
		t.Fatalf("leak status = %+v, want same-crawl 500 not other-crawl 200", leak.TargetStatus)
	}
	border, err := fx.queries.GetCrawlLinkByIDForUser(ctx, sqlc.GetCrawlLinkByIDForUserParams{ID: fx.links["border"], UserID: fx.ownerID})
	if err != nil {
		t.Fatalf("get border link: %v", err)
	}
	if border.TargetStatus.Valid {
		t.Fatalf("border status = %+v, want NULL: matching page lives in another crawl", border.TargetStatus)
	}
}

func TestLinkPaginationResolvesPageOnly(t *testing.T) {
	ctx, fx := newLinkStatusFixture(t)
	rows, err := fx.queries.ListCrawlLinksForCrawlByUser(ctx, sqlc.ListCrawlLinksForCrawlByUserParams{CrawlID: fx.crawlP, UserID: fx.ownerID, Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("list page: %v", err)
	}
	if len(rows) != 2 || rows[0].TargetUrl != "https://example.com/p1" || rows[1].TargetUrl != "https://example.com/p2" {
		t.Fatalf("page rows = %v, want p1,p2 in created order", rows)
	}
	for i, row := range rows {
		if !row.TargetStatus.Valid || row.TargetStatus.Int32 != int32(201+i) {
			t.Fatalf("row %d status = %+v, want derived %d", i, row.TargetStatus, 201+i)
		}
	}
}

func TestLinkListingAuthorization(t *testing.T) {
	ctx, fx := newLinkStatusFixture(t)
	rows, err := fx.queries.ListCrawlLinksForCrawlByUser(ctx, sqlc.ListCrawlLinksForCrawlByUserParams{CrawlID: fx.crawlA, UserID: fx.otherID, Limit: 200, Offset: 0})
	if err != nil {
		t.Fatalf("outsider list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("outsider listed %d rows, want 0", len(rows))
	}
	if _, err := fx.queries.GetCrawlLinkByIDForUser(ctx, sqlc.GetCrawlLinkByIDForUserParams{ID: fx.links["stored"], UserID: fx.otherID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("outsider get err = %v, want no rows", err)
	}
}

func TestLinkFactsStayRaw(t *testing.T) {
	ctx, fx := newLinkStatusFixture(t)
	rows, err := fx.queries.ListInternalLinkFactsForCrawl(ctx, fx.crawlA)
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("facts rows = %d, want only the 5 internal links", len(rows))
	}
	for _, row := range rows {
		if !row.TargetUrlKey.Valid || row.TargetUrlKey.String == "" {
			t.Errorf("fact %s missing key", row.TargetUrl)
		}
		if row.TargetUrl == "https://example.com/a" && row.TargetStatus.Valid {
			t.Errorf("facts must stay raw: derived would be 200, stored is NULL")
		}
		if row.TargetUrl == "https://example.com/stored" && (!row.TargetStatus.Valid || row.TargetStatus.Int32 != 201) {
			t.Errorf("facts stored status = %+v, want raw 201", row.TargetStatus)
		}
	}
}

func TestLinkListHandlerReturnsDerivedStatus(t *testing.T) {
	pool := crawlLinkStatusScratchPool(t)
	ctx := context.Background()
	var orgID, userID, projectID, crawlID pgtype.UUID
	mustLinkStatusScan(t, ctx, pool, &orgID, `INSERT INTO organizations (name) VALUES ('link-status-handler') RETURNING id`)
	mustLinkStatusScan(t, ctx, pool, &userID, `INSERT INTO users (auth_provider, auth_subject, email) VALUES ('link-status-handler', gen_random_uuid()::text, gen_random_uuid()::text || '@example.com') RETURNING id`)
	mustLinkStatusExec(t, ctx, pool, `INSERT INTO organization_members (org_id, user_id, role) VALUES ($1,$2,'owner')`, orgID, userID)
	mustLinkStatusScan(t, ctx, pool, &projectID, `INSERT INTO projects (organization_id, name, base_url) VALUES ($1,'link-status-handler','https://example.com') RETURNING id`, orgID)
	mustLinkStatusScan(t, ctx, pool, &crawlID, `INSERT INTO crawls (project_id, status) VALUES ($1,'completed') RETURNING id`, projectID)
	mustLinkStatusExec(t, ctx, pool, `INSERT INTO crawl_pages (crawl_id, url, status_code) VALUES ($1,'https://example.com/known',200)`, crawlID)
	mustLinkStatusExec(t, ctx, pool, `INSERT INTO crawl_links (crawl_id, source_url, target_url, is_internal) VALUES ($1,'https://example.com/s-known','https://example.com/known',TRUE),($1,'https://example.com/s-unknown','https://example.com/unknown',TRUE)`, crawlID)
	t.Cleanup(func() {
		clean := context.Background()
		_, _ = pool.Exec(clean, `DELETE FROM crawl_links WHERE crawl_id = $1`, crawlID)
		_, _ = pool.Exec(clean, `DELETE FROM crawl_pages WHERE crawl_id = $1`, crawlID)
		_, _ = pool.Exec(clean, `DELETE FROM crawls WHERE id = $1`, crawlID)
		_, _ = pool.Exec(clean, `DELETE FROM projects WHERE id = $1`, projectID)
		_, _ = pool.Exec(clean, `DELETE FROM organization_members WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(clean, `DELETE FROM organizations WHERE id = $1`, orgID)
		_, _ = pool.Exec(clean, `DELETE FROM users WHERE id = $1`, userID)
	})

	app := &App{DB: pool, Queries: sqlc.New(pool)}
	req := httptest.NewRequest(http.MethodGet, "/?limit=10&offset=0", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("crawlID", crawlID.String())
	req = req.WithContext(withPrincipal(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx), Principal{User: sqlc.User{ID: userID}}))
	rr := httptest.NewRecorder()
	app.handleListCrawlLinks(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Links []crawlLinkResponse `json:"links"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode links: %v", err)
	}
	if len(body.Links) != 2 {
		t.Fatalf("links = %d, want 2", len(body.Links))
	}
	byTarget := map[string]crawlLinkResponse{}
	for _, link := range body.Links {
		byTarget[link.TargetURL] = link
	}
	if got := byTarget["https://example.com/known"].TargetStatus; got == nil || *got != 200 {
		t.Errorf("known target_status = %v, want 200 derived", got)
	}
	if got := byTarget["https://example.com/unknown"].TargetStatus; got != nil {
		t.Errorf("unknown target_status = %v, want omitted", *got)
	}
}

func mustLinkStatusScan(t *testing.T, ctx context.Context, pool *pgxpool.Pool, dest *pgtype.UUID, stmt string, args ...any) {
	t.Helper()
	if err := pool.QueryRow(ctx, stmt, args...).Scan(dest); err != nil {
		t.Fatalf("fixture scan: %v", err)
	}
}

func mustLinkStatusExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, stmt string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, stmt, args...); err != nil {
		t.Fatalf("fixture exec: %v", err)
	}
}
