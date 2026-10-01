package projectkeywords

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ps-wizard/revserp/internal/db/sqlc"
)

func TestProjectKeywordMigrationRoundTripPreservesLegacyTerms(t *testing.T) {
	_, pool, ctx := newProjectKeywordsTestQueries(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	schema := pgx.Identifier{fmt.Sprintf("keyword_migration_%d", time.Now().UnixNano())}.Sanitize()
	for _, statement := range []string{
		"CREATE SCHEMA " + schema,
		"SET LOCAL search_path TO " + schema,
		"CREATE TABLE projects (id UUID PRIMARY KEY)",
		"CREATE TABLE project_business_profile (project_id UUID PRIMARY KEY, branded_keywords JSONB NOT NULL, non_branded_keywords JSONB NOT NULL, target_keywords JSONB NOT NULL)",
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	var projectID pgtype.UUID
	if err := tx.QueryRow(ctx, `INSERT INTO projects VALUES (gen_random_uuid()) RETURNING id`).Scan(&projectID); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("ü", 250)
	brand, _ := json.Marshal([]string{" ACME ", "Brand\u00a0Only", "Overlap Term"})
	nonBrand, _ := json.Marshal([]string{" overlap\u2003term ", "trail   boots", long})
	target, _ := json.Marshal([]string{"ACME", "target only", " "})
	if _, err := tx.Exec(ctx, `INSERT INTO project_business_profile VALUES ($1,$2::jsonb,$3::jsonb,$4::jsonb)`, projectID, string(brand), string(nonBrand), string(target)); err != nil {
		t.Fatal(err)
	}
	up, down, ok := strings.Cut(readProjectKeywordsMigration(t), "-- +goose Down")
	if !ok {
		t.Fatal("keyword migration has no Down section")
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("migration Up failed: %v", err)
	}
	lists, err := LoadProjectKeywordLists(ctx, sqlc.New(tx), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists.UserDefined) != 6 || len(lists.Combined) != 6 || len(lists.RevserpSuggested) != 0 {
		t.Fatalf("migration lost or duplicated legacy terms: %#v", lists)
	}
	byKey := map[string]string{}
	for _, keyword := range lists.UserDefined {
		byKey[NormalizeProjectKeywordKey(keyword.Keyword)] = keyword.Kind
	}
	for key, kind := range map[string]string{
		"acme": "brand", "brand only": "brand", "overlap term": "non_brand",
		"trail boots": "non_brand", long: "non_brand", "target only": "non_brand",
	} {
		if byKey[key] != kind {
			t.Fatalf("migrated %q has kind %q, want %q", key, byKey[key], kind)
		}
	}
	if _, err := tx.Exec(ctx, down); err != nil {
		t.Fatalf("migration Down failed: %v", err)
	}
	var restoredBrand, restoredNonBrand, restoredTarget []byte
	if err := tx.QueryRow(ctx, `SELECT branded_keywords, non_branded_keywords, target_keywords FROM project_business_profile WHERE project_id=$1`, projectID).Scan(&restoredBrand, &restoredNonBrand, &restoredTarget); err != nil {
		t.Fatal(err)
	}
	for index, raw := range [][]byte{restoredBrand, restoredNonBrand, restoredTarget} {
		var terms []string
		if err := json.Unmarshal(raw, &terms); err != nil {
			t.Fatal(err)
		}
		if len(terms) != []int{2, 4, 6}[index] {
			t.Fatalf("restored list %d lost terms: %v", index, terms)
		}
		for _, term := range terms {
			if _, exists := byKey[NormalizeProjectKeywordKey(term)]; !exists {
				t.Fatalf("Down introduced unknown term %q", term)
			}
		}
	}
	if _, err := tx.Exec(ctx, up); err != nil {
		t.Fatalf("migration retry after Down failed: %v", err)
	}
}
