package locationkeywords

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// execCaptureDB records every Exec statement for source-scoping assertions.
type execCaptureDB struct {
	sqls []string
	args [][]any
	err  error
}

func (f *execCaptureDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, pgx.ErrNoRows
}

func (f *execCaptureDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func (f *execCaptureDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.sqls = append(f.sqls, sql)
	f.args = append(f.args, args)
	return pgconn.CommandTag{}, f.err
}

func TestReplaceRevserpKeywordsTouchesRevserpOnly(t *testing.T) {
	db := &execCaptureDB{}
	locationID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	if err := ReplaceRevserpKeywords(context.Background(), db, locationID, []string{"Acme"}, []string{"Leak Repair"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(db.sqls) != 3 {
		t.Fatalf("replace must delete plus insert both kinds, got %d statements: %v", len(db.sqls), db.sqls)
	}
	if !strings.Contains(db.sqls[0], "source = 'revserp'") || !strings.Contains(db.sqls[0], "DELETE") {
		t.Errorf("first statement must delete the revserp source only:\n%s", db.sqls[0])
	}
	for i, stmt := range db.sqls[1:] {
		if !strings.HasPrefix(stmt, "INSERT INTO location_keywords") {
			t.Errorf("statement %d must insert stored rows:\n%s", i+1, stmt)
		}
		// The source travels as an Exec arg; every insert must carry revserp.
		if got := db.args[i+1][4]; got != SourceRevserp {
			t.Errorf("insert %d source arg = %v, want revserp", i+1, got)
		}
	}
	joined := strings.Join(db.sqls, "\n")
	for _, forbidden := range []string{"'user'", "'selected'"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("revserp replace must never mention %s:\n%s", forbidden, joined)
		}
	}
}

func TestCombineSavedSuggestedKeywords(t *testing.T) {
	derived := KeywordGroup{Branded: []string{}, NonBranded: []string{"Plumber", "Plumber in Springfield"}}
	saved := KeywordGroup{Branded: []string{"Acme"}, NonBranded: []string{"plumber in springfield", "Emergency Plumber"}}
	combined := CombineSavedSuggestedKeywords(derived, saved)
	if len(combined.Branded) != 1 || combined.Branded[0] != "Acme" {
		t.Fatalf("branded = %#v, want the saved brand phrase", combined.Branded)
	}
	// Saved text wins the exact-key collision; the saved-only phrase joins.
	want := map[string]bool{"Emergency Plumber": true, "plumber in springfield": true, "Plumber": true}
	if len(combined.NonBranded) != len(want) {
		t.Fatalf("non_branded = %#v, want %d phrases", combined.NonBranded, len(want))
	}
	for _, phrase := range combined.NonBranded {
		if !want[phrase] {
			t.Fatalf("non_branded = %#v, unexpected %q", combined.NonBranded, phrase)
		}
	}
	empty := CombineSavedSuggestedKeywords(KeywordGroup{}, KeywordGroup{})
	if len(empty.Branded) != 0 || len(empty.NonBranded) != 0 {
		t.Fatalf("empty inputs must stay empty: %#v", empty)
	}
}
