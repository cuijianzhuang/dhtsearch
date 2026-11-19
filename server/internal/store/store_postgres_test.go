package store

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"dhtsearch/server/internal/filter"
)

// Dialect unit tests: these need no database server and pin down the
// translation rules the Postgres path relies on.

func TestDialectRebind(t *testing.T) {
	if got := sqliteDialect.rebind(`SELECT * FROM t WHERE a = ? AND b IN (?,?)`); got != `SELECT * FROM t WHERE a = ? AND b IN (?,?)` {
		t.Fatalf("sqlite rebind changed the query: %s", got)
	}
	got := postgresDialect.rebind(`UPDATE t SET a = ? WHERE b IN (?,?,?) AND c = ?`)
	want := `UPDATE t SET a = $1 WHERE b IN ($2,$3,$4) AND c = $5`
	if got != want {
		t.Fatalf("postgres rebind:\n got %s\nwant %s", got, want)
	}
}

func TestDialectSearchSemantics(t *testing.T) {
	// SQLite LIKE is ASCII case-insensitive; Postgres needs ILIKE to
	// keep the same behaviour.
	if sqliteDialect.likeOp() != "LIKE" || postgresDialect.likeOp() != "ILIKE" {
		t.Fatalf("like ops: sqlite=%s postgres=%s", sqliteDialect.likeOp(), postgresDialect.likeOp())
	}
	if !strings.Contains(postgresDialect.displayNameExpr(), "CASE WHEN") {
		t.Fatalf("postgres display name must use CASE WHEN: %s", postgresDialect.displayNameExpr())
	}
	if !strings.Contains(sqliteDialect.displayNameExpr(), "IIF(") {
		t.Fatalf("sqlite display name must use IIF: %s", sqliteDialect.displayNameExpr())
	}
	pg := postgresDialect.schemaStatements()
	joined := strings.Join(pg, "\n")
	for _, want := range []string{"BYTEA", "BIGINT"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("postgres schema missing %s:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "BLOB") || strings.Contains(joined, "AUTOINCREMENT") {
		t.Fatalf("postgres schema leaked SQLite types:\n%s", joined)
	}
	if !strings.Contains(postgresDialect.insertTorrent(), "ON CONFLICT (info_hash) DO NOTHING") {
		t.Fatalf("postgres upsert must use ON CONFLICT: %s", postgresDialect.insertTorrent())
	}
	if !strings.Contains(sqliteDialect.insertTorrent(), "INSERT OR IGNORE") {
		t.Fatalf("sqlite upsert must keep INSERT OR IGNORE: %s", sqliteDialect.insertTorrent())
	}
}

func TestOpenURLDispatch(t *testing.T) {
	for _, u := range []string{"postgres://u:p@h:5432/db", "postgresql://u:p@h/db"} {
		if !IsPostgresURL(u) {
			t.Fatalf("IsPostgresURL(%q) = false", u)
		}
	}
	for _, u := range []string{"./dhtsearch.db", ":memory:", "file:x.db"} {
		if IsPostgresURL(u) {
			t.Fatalf("IsPostgresURL(%q) = true", u)
		}
	}
	// A SQLite URL still opens as SQLite through the dispatcher.
	s, err := OpenURL(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Dialect() != "sqlite" {
		t.Fatalf("OpenURL(:memory:) dialect = %s, want sqlite", s.Dialect())
	}
}

// TestPostgresStore is the Postgres integration test. It runs only
// when DHTSEARCH_TEST_POSTGRES_URL points at a throwaway database
// (the test drops and recreates the store tables in it):
//
//	createdb dhtsearch_test
//	DHTSEARCH_TEST_POSTGRES_URL=postgres://localhost/dhtsearch_test?sslmode=disable \
//	  go test ./internal/store/ -run TestPostgresStore -v
func TestPostgresStore(t *testing.T) {
	url := os.Getenv("DHTSEARCH_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("DHTSEARCH_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	if !IsPostgresURL(url) {
		t.Fatalf("DHTSEARCH_TEST_POSTGRES_URL is not a postgres URL: %q", url)
	}
	admin, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS torrents`, `DROP TABLE IF EXISTS stats`, `DROP TABLE IF EXISTS blocked`,
	} {
		if _, err := admin.Exec(stmt); err != nil {
			t.Fatalf("reset test database: %v", err)
		}
	}
	admin.Close()

	s, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	if s.Dialect() != "postgres" {
		t.Fatalf("dialect = %s, want postgres", s.Dialect())
	}

	recs := []Torrent{
		{InfoHash: "aa", Name: "Big Buck Bunny 2008", TotalSize: 700 << 20, FileCount: 1, CreatedAt: 100,
			Files: []filter.File{{Path: "bbb.mkv", Size: 700 << 20}}},
		{InfoHash: "bb", Name: "Sintel 2010 Open Movie", TotalSize: 1 << 30, FileCount: 1, CreatedAt: 200},
		{InfoHash: "cc", Name: "Ubuntu 24.04 ISO", TotalSize: 5 << 30, FileCount: 1, CreatedAt: 300},
	}
	for _, r := range recs {
		if err := s.Upsert(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Upsert(recs[0]); err != nil { // duplicate: ignored
		t.Fatal(err)
	}
	if total, _ := s.Count(t.Context()); total != 3 {
		t.Fatalf("count=%d, want 3", total)
	}

	// Search is case-insensitive on Postgres too (ILIKE), matching the
	// SQLite LIKE behaviour callers rely on.
	items, total, err := s.Search(t.Context(), "big bunny", 1, 10)
	if err != nil || total != 1 || items[0].InfoHash != "aa" {
		t.Fatalf("keyword search: items=%v total=%d err=%v", items, total, err)
	}
	if items[0].Files[0].Path != "bbb.mkv" {
		t.Fatalf("files blob round-trip failed: %+v", items[0].Files)
	}
	if _, total, err = s.Search(t.Context(), "100%", 1, 10); err != nil || total != 0 {
		t.Fatalf("wildcard query: total=%d err=%v", total, err)
	}

	// Moderation flow: unreviewed -> block -> no resurrection.
	cands, err := s.Unreviewed(10)
	if err != nil || len(cands) != 3 {
		t.Fatalf("unreviewed: %d cands err=%v", len(cands), err)
	}
	if n, err := s.Block([]string{"bb"}, []string{"Sintel 2010 Open Movie"}, "adult", 500); err != nil || n != 1 {
		t.Fatalf("block: n=%d err=%v", n, err)
	}
	if err := s.Upsert(recs[1]); err != nil {
		t.Fatal(err)
	}
	if total, _ := s.Count(t.Context()); total != 2 {
		t.Fatalf("count=%d, want 2 (blocked hash was re-added)", total)
	}
	if n, err := s.MarkReviewed([]string{"aa"}, 500); err != nil || n != 1 {
		t.Fatalf("mark reviewed: n=%d err=%v", n, err)
	}
	if pending, _ := s.PendingReviewCount(t.Context()); pending != 1 {
		t.Fatalf("pending=%d, want 1", pending)
	}

	// Stats upsert (ON CONFLICT DO UPDATE) accumulates.
	if err := s.IncrStat("seen", 3); err != nil {
		t.Fatal(err)
	}
	if err := s.IncrStat("seen", 2); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Stats(t.Context()); err != nil || m["seen"] != 5 {
		t.Fatalf("stats: %v err=%v", m, err)
	}

	// Alias + clean-name paths.
	if _, err := s.SetCleanNames(map[string]string{"aa": "Big Buck Bunny"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SetAlias("cc", "Ubuntu 镜像"); err != nil || !ok {
		t.Fatalf("SetAlias: ok=%v err=%v", ok, err)
	}
	if _, total, _ := s.Search(t.Context(), "镜像", 1, 10); total != 1 {
		t.Fatalf("alias search: total=%d, want 1", total)
	}

	// Reopening runs the migrations again; duplicate-column errors must
	// be treated as success, exactly as on SQLite.
	s2, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	s2.Close()
}
