package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"dhtsearch/server/internal/filter"
)

// Fixtures carrying the byte patterns real DHT metadata produces:
// lone invalid bytes (0xf8, 0xfe) and truncated multi-byte sequences.
// Postgres TEXT columns reject all of these (SQLSTATE 22021); SQLite
// stores them verbatim, which is how they ended up in live databases
// and then broke SQLite -> Postgres migration.
const (
	badName   = "Ubuntu \xf8\xfe Remix 24.04"
	goodName  = "Ubuntu � Remix 24.04"
	badPath   = "dir/\xe2\x82file.mkv"
	goodPath  = "dir/�file.mkv"
	badAlias  = "别名 \xf8 title"
	goodAlias = "别名 � title"
)

func TestSanitizeUTF8(t *testing.T) {
	cases := map[string]string{
		"":                      "",
		"plain ascii":           "plain ascii",
		"中文标题 123":              "中文标题 123",
		badName:                 goodName,
		badPath:                 goodPath,
		"Caf\xc3":               "Caf�", // truncated 2-byte sequence
		"\xf8":                  "�",
		"a\x80b":                "a�b", // stray continuation byte
		"already � replacement": "already � replacement",
	}
	for in, want := range cases {
		if got := sanitizeUTF8(in); got != want {
			t.Errorf("sanitizeUTF8(%q) = %q, want %q", in, got, want)
		}
		if got := sanitizeUTF8(in); !utf8.ValidString(got) {
			t.Errorf("sanitizeUTF8(%q) = %q, still invalid UTF-8", in, got)
		}
	}
}

// TestStoreSanitizesOnWriteSQLite drives every store write path that
// carries network-supplied text on the SQLite backend and checks the
// stored values come back sanitized (and still searchable).
func TestStoreSanitizesOnWriteSQLite(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()

	bad := Torrent{
		InfoHash:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Name:      badName,
		TotalSize: 100, FileCount: 1, CreatedAt: 100,
		Files: []filter.File{{Path: badPath, Size: 100}},
		Alias: badAlias,
	}
	if err := s.Upsert(bad); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	items, total, err := s.Search(ctx, "Remix", 1, 10)
	if err != nil || total != 1 {
		t.Fatalf("search sanitized name: total=%d err=%v", total, err)
	}
	got := items[0]
	if got.Name != goodName {
		t.Fatalf("stored name = %q, want %q", got.Name, goodName)
	}
	if got.Alias != goodAlias {
		t.Fatalf("stored alias = %q, want %q", got.Alias, goodAlias)
	}
	if len(got.Files) != 1 || got.Files[0].Path != goodPath {
		t.Fatalf("stored files = %+v, want path %q", got.Files, goodPath)
	}

	// SetAlias with invalid bytes.
	if ok, err := s.SetAlias(bad.InfoHash, badAlias); err != nil || !ok {
		t.Fatalf("SetAlias: ok=%v err=%v", ok, err)
	}
	if _, total, err := s.Search(ctx, "title", 1, 10); err != nil || total != 1 {
		t.Fatalf("search via sanitized alias: total=%d err=%v", total, err)
	}

	// SetCleanNames with invalid bytes.
	if _, err := s.SetCleanNames(map[string]string{bad.InfoHash: "Clean \xf8 Name"}); err != nil {
		t.Fatalf("SetCleanNames: %v", err)
	}
	var clean string
	if err := s.queryRow(`SELECT clean_name FROM torrents WHERE info_hash = ?`, bad.InfoHash).Scan(&clean); err != nil {
		t.Fatal(err)
	}
	if clean != "Clean � Name" {
		t.Fatalf("stored clean_name = %q, want %q", clean, "Clean � Name")
	}

	// Block copies the torrent name into blocked.name; both it and the
	// reason must be sanitized.
	other := Torrent{
		InfoHash:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Name:      "Bad \xf8 Torrent",
		TotalSize: 100, FileCount: 1, CreatedAt: 200,
	}
	if err := s.Upsert(other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Block([]string{other.InfoHash}, []string{"Bad \xf8 Torrent"}, "spam \xf8", 500); err != nil {
		t.Fatalf("Block: %v", err)
	}
	var bname, breason string
	if err := s.queryRow(`SELECT name, reason FROM blocked WHERE info_hash = ?`, other.InfoHash).Scan(&bname, &breason); err != nil {
		t.Fatal(err)
	}
	if bname != "Bad � Torrent" || breason != "spam �" {
		t.Fatalf("blocked row = %q / %q, want sanitized", bname, breason)
	}
}

// seedRawInvalid inserts a torrent and a blocked row with invalid
// UTF-8 through raw SQL, bypassing the write-path sanitization — the
// shape of a legacy SQLite database written before the fix.
func seedRawInvalid(t *testing.T, s *Store) {
	t.Helper()
	blob := compressFiles([]byte(`[{"path":"a.mkv","size":100}]`))
	if _, err := s.exec(`INSERT INTO torrents
		(info_hash, name, total_size, file_count, files, created_at, reviewed_at, clean_name, alias)
		VALUES (?, ?, ?, 1, ?, ?, 0, ?, ?)`,
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", badName, 100, blob, 1000,
		"Clean \xf8 Name", badAlias); err != nil {
		t.Fatalf("seed raw torrent: %v", err)
	}
	if _, err := s.exec(`INSERT INTO blocked (info_hash, reason, name, created_at) VALUES (?, ?, ?, ?)`,
		"ffffffffffffffffffffffffffffffffffffffff", "spam \xf8", "Bad \xf8 Torrent", 4000); err != nil {
		t.Fatalf("seed raw blocked: %v", err)
	}
}

// TestMigrateSanitizesSQLiteToSQLite: migration itself must sanitize,
// even when the target would have accepted the raw bytes.
func TestMigrateSanitizesSQLiteToSQLite(t *testing.T) {
	dir := t.TempDir()
	src, err := Open(filepath.Join(dir, "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	seedRawInvalid(t, src)

	dst, err := Open(filepath.Join(dir, "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	if _, err := Migrate(context.Background(), src, dst); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	name, _, _, _, _, clean, alias := torrentRow(t, dst,
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if name != goodName || clean != "Clean � Name" || alias != goodAlias {
		t.Fatalf("migrated row not sanitized: name=%q clean=%q alias=%q", name, clean, alias)
	}
	var bname string
	if err := dst.queryRow(`SELECT name FROM blocked WHERE info_hash = ?`,
		"ffffffffffffffffffffffffffffffffffffffff").Scan(&bname); err != nil {
		t.Fatal(err)
	}
	if bname != "Bad � Torrent" {
		t.Fatalf("migrated blocked name = %q, want sanitized", bname)
	}
}

// TestPostgresInvalidUTF8 is the integration regression test for the
// original failure — daily ingest and migration of DHT names that are
// not valid UTF-8 against real Postgres. Gated like TestPostgresStore.
func TestPostgresInvalidUTF8(t *testing.T) {
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
		`DROP TABLE IF EXISTS torrents`, `DROP TABLE IF EXISTS stats`,
		`DROP TABLE IF EXISTS blocked`, `DROP TABLE IF EXISTS settings`,
	} {
		if _, err := admin.Exec(stmt); err != nil {
			t.Fatalf("reset test database: %v", err)
		}
	}
	admin.Close()

	ctx := context.Background()
	pg, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer pg.Close()

	// Daily ingest path: this Upsert used to fail with SQLSTATE 22021.
	bad := Torrent{
		InfoHash:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Name:      badName,
		TotalSize: 100, FileCount: 1, CreatedAt: 100,
		Files: []filter.File{{Path: badPath, Size: 100}},
		Alias: badAlias,
	}
	if err := pg.Upsert(bad); err != nil {
		t.Fatalf("Upsert invalid UTF-8 on Postgres: %v", err)
	}
	items, total, err := pg.Search(ctx, "Remix", 1, 10)
	if err != nil || total != 1 {
		t.Fatalf("search sanitized name on Postgres: total=%d err=%v", total, err)
	}
	if items[0].Name != goodName || items[0].Alias != goodAlias ||
		len(items[0].Files) != 1 || items[0].Files[0].Path != goodPath {
		t.Fatalf("postgres row not sanitized: %+v", items[0])
	}
	if !utf8.ValidString(items[0].Name) {
		t.Fatalf("postgres name still invalid UTF-8: %q", items[0].Name)
	}
	if _, err := pg.SetCleanNames(map[string]string{bad.InfoHash: "Clean \xf8 Name"}); err != nil {
		t.Fatalf("SetCleanNames on Postgres: %v", err)
	}
	if ok, err := pg.SetAlias(bad.InfoHash, badAlias); err != nil || !ok {
		t.Fatalf("SetAlias on Postgres: ok=%v err=%v", ok, err)
	}

	// Migration path: a legacy SQLite database whose rows bypassed
	// sanitization must migrate into Postgres cleanly.
	dir := t.TempDir()
	src, err := Open(filepath.Join(dir, "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	seedRawInvalid(t, src)

	rep, err := Migrate(ctx, src, pg)
	if err != nil {
		t.Fatalf("migrate legacy SQLite -> Postgres: %v", err)
	}
	if tr := tableReport(t, rep, "torrents"); tr.Missing != 0 || tr.Inserted != 1 {
		t.Fatalf("torrents report = %+v, want inserted=1 missing=0", tr)
	}
	name, _, _, _, _, clean, alias := torrentRow(t, pg,
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if name != goodName || clean != "Clean � Name" || alias != goodAlias {
		t.Fatalf("migrated postgres row not sanitized: name=%q clean=%q alias=%q", name, clean, alias)
	}
	if _, total, err := pg.Search(ctx, "Remix", 1, 10); err != nil || total != 2 {
		t.Fatalf("search after migrate: total=%d err=%v, want 2", total, err)
	}

	// And back to SQLite: values stay sanitized, nothing regresses.
	back, err := Open(filepath.Join(dir, "back.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer back.Close()
	if _, err := Migrate(ctx, pg, back); err != nil {
		t.Fatalf("migrate Postgres -> SQLite: %v", err)
	}
	name, _, _, _, _, _, _ = torrentRow(t, back,
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	if name != goodName {
		t.Fatalf("round-tripped name = %q, want %q", name, goodName)
	}
}
