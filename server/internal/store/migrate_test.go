package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// seedMigrateSource fills s with a representative data set through raw
// SQL so every column — including reviewed_at and clean_name, which
// the public API writes only via the moderation pass — carries a
// non-default value somewhere.
func seedMigrateSource(t *testing.T, s *Store) {
	t.Helper()
	blobA := compressFiles([]byte(`[{"path":"a.mkv","size":100}]`))
	blobB := []byte(`[{"path":"b.txt","size":5}]`) // uncompressed form also occurs
	rows := []struct {
		hash, name string
		size       int64
		files      []byte
		created    int64
		reviewed   int64
		clean      string
		alias      string
	}{
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "Alpha [raw] release", 100, blobA, 1000, 0, "", ""},
		{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "Beta", 200, blobB, 2000, 1700000000, "Beta (clean)", ""},
		{"cccccccccccccccccccccccccccccccccccccccc", "Gamma", 300, blobA, 3000, 0, "", "Gamma alias"},
	}
	for _, r := range rows {
		if _, err := s.exec(`INSERT INTO torrents
			(info_hash, name, total_size, file_count, files, created_at, reviewed_at, clean_name, alias)
			VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?)`,
			r.hash, r.name, r.size, r.files, r.created, r.reviewed, r.clean, r.alias); err != nil {
			t.Fatalf("seed torrent %s: %v", r.hash, err)
		}
	}
	if err := s.IncrStat("seen", 42); err != nil {
		t.Fatal(err)
	}
	if err := s.IncrStat("fetched", 7); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exec(`INSERT INTO blocked (info_hash, reason, name, created_at) VALUES (?, ?, ?, ?)`,
		"dddddddddddddddddddddddddddddddddddddddd", "spam", "Blocked One", 4000); err != nil {
		t.Fatalf("seed blocked: %v", err)
	}
}

// torrentRow reads one full torrents row for content comparison.
func torrentRow(t *testing.T, s *Store, hash string) (name string, size int64, files []byte, created, reviewed int64, clean, alias string) {
	t.Helper()
	err := s.queryRow(`SELECT name, total_size, files, created_at, reviewed_at, clean_name, alias
		FROM torrents WHERE info_hash = ?`, hash).
		Scan(&name, &size, &files, &created, &reviewed, &clean, &alias)
	if err != nil {
		t.Fatalf("read torrent %s: %v", hash, err)
	}
	return
}

func tableReport(t *testing.T, rep *MigrateReport, table string) TableReport {
	t.Helper()
	for _, tr := range rep.Tables {
		if tr.Table == table {
			return tr
		}
	}
	t.Fatalf("report has no %s entry: %+v", table, rep.Tables)
	return TableReport{}
}

func TestMigrateSQLiteToSQLite(t *testing.T) {
	dir := t.TempDir()
	src, err := Open(filepath.Join(dir, "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	seedMigrateSource(t, src)

	dst, err := Open(filepath.Join(dir, "dst.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	rep, err := Migrate(context.Background(), src, dst)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if rep.From != "sqlite" || rep.To != "sqlite" {
		t.Fatalf("report dialects: %s -> %s", rep.From, rep.To)
	}
	for table, want := range map[string]int64{"torrents": 3, "stats": 2, "blocked": 1} {
		tr := tableReport(t, rep, table)
		if tr.Source != want || tr.Inserted != want || tr.Skipped != 0 || tr.Dest != want || tr.Missing != 0 {
			t.Fatalf("%s report = %+v, want source/inserted/dest=%d skipped/missing=0", table, tr, want)
		}
	}

	// Content must cross verbatim, including the files blob, the
	// moderation columns and the alias.
	name, size, files, created, reviewed, clean, alias := torrentRow(t, dst,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if name != "Beta" || size != 200 || created != 2000 || reviewed != 1700000000 ||
		clean != "Beta (clean)" || alias != "" {
		t.Fatalf("migrated row mismatch: name=%q size=%d created=%d reviewed=%d clean=%q alias=%q",
			name, size, created, reviewed, clean, alias)
	}
	if fj, err := decompressFiles(files); err != nil || string(fj) != `[{"path":"b.txt","size":5}]` {
		t.Fatalf("migrated files blob: %q, %v", fj, err)
	}
	if _, _, _, _, _, _, alias := torrentRow(t, dst,
		"cccccccccccccccccccccccccccccccccccccccc"); alias != "Gamma alias" {
		t.Fatalf("alias not migrated: %q", alias)
	}
	stats, err := dst.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats["seen"] != 42 || stats["fetched"] != 7 {
		t.Fatalf("migrated stats: %v", stats)
	}
	if ok, err := dst.IsBlocked("dddddddddddddddddddddddddddddddddddddddd"); err != nil || !ok {
		t.Fatalf("blocked row not migrated: %v %v", ok, err)
	}

	// Re-running skips everything and changes nothing.
	rep, err = Migrate(context.Background(), src, dst)
	if err != nil {
		t.Fatalf("re-run Migrate: %v", err)
	}
	for table, want := range map[string]int64{"torrents": 3, "stats": 2, "blocked": 1} {
		tr := tableReport(t, rep, table)
		if tr.Inserted != 0 || tr.Skipped != want || tr.Dest != want || tr.Missing != 0 {
			t.Fatalf("re-run %s report = %+v, want inserted=0 skipped=dest=%d", table, tr, want)
		}
	}

	// A conflicting row already in the target is never overwritten.
	if _, err := dst.exec(`UPDATE torrents SET name = 'Local Edit' WHERE info_hash = ?`,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	if name, _, _, _, _, _, _ := torrentRow(t, dst,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); name != "Local Edit" {
		t.Fatalf("conflicting target row was overwritten: %q", name)
	}
}

// TestMigratePostgres is the cross-backend integration test, gated on
// DHTSEARCH_TEST_POSTGRES_URL like TestPostgresStore (the database is
// dropped and recreated). It migrates SQLite -> Postgres, verifies,
// then migrates Postgres -> a fresh SQLite file and verifies again.
func TestMigratePostgres(t *testing.T) {
	url := os.Getenv("DHTSEARCH_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("DHTSEARCH_TEST_POSTGRES_URL not set; skipping Postgres migration test")
	}
	if !IsPostgresURL(url) {
		t.Fatalf("DHTSEARCH_TEST_POSTGRES_URL is not a postgres URL: %s", url)
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

	dir := t.TempDir()
	src, err := Open(filepath.Join(dir, "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	seedMigrateSource(t, src)

	pg, err := OpenPostgres(url)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	rep, err := Migrate(ctx, src, pg)
	if err != nil {
		t.Fatalf("sqlite -> postgres: %v", err)
	}
	if rep.From != "sqlite" || rep.To != "postgres" {
		t.Fatalf("report dialects: %s -> %s", rep.From, rep.To)
	}
	for table, want := range map[string]int64{"torrents": 3, "stats": 2, "blocked": 1} {
		tr := tableReport(t, rep, table)
		if tr.Source != want || tr.Inserted != want || tr.Skipped != 0 || tr.Dest != want || tr.Missing != 0 {
			t.Fatalf("sqlite->postgres %s report = %+v, want %d/%d", table, tr, want, want)
		}
	}
	name, _, files, _, reviewed, clean, _ := torrentRow(t, pg,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if name != "Beta" || reviewed != 1700000000 || clean != "Beta (clean)" {
		t.Fatalf("postgres row mismatch: name=%q reviewed=%d clean=%q", name, reviewed, clean)
	}
	if fj, err := decompressFiles(files); err != nil || string(fj) != `[{"path":"b.txt","size":5}]` {
		t.Fatalf("postgres files blob: %q, %v", fj, err)
	}
	if n, err := pg.Count(ctx); err != nil || n != 3 {
		t.Fatalf("postgres Count = %d, %v", n, err)
	}

	// Skip semantics on the Postgres side too.
	rep, err = Migrate(ctx, src, pg)
	if err != nil {
		t.Fatalf("re-run sqlite -> postgres: %v", err)
	}
	if tr := tableReport(t, rep, "torrents"); tr.Inserted != 0 || tr.Skipped != 3 {
		t.Fatalf("re-run torrents report = %+v, want inserted=0 skipped=3", tr)
	}

	// And back: Postgres -> a fresh SQLite file must reproduce the data.
	back, err := Open(filepath.Join(dir, "back.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer back.Close()
	rep, err = Migrate(ctx, pg, back)
	if err != nil {
		t.Fatalf("postgres -> sqlite: %v", err)
	}
	if rep.From != "postgres" || rep.To != "sqlite" {
		t.Fatalf("report dialects: %s -> %s", rep.From, rep.To)
	}
	for table, want := range map[string]int64{"torrents": 3, "stats": 2, "blocked": 1} {
		tr := tableReport(t, rep, table)
		if tr.Inserted != want || tr.Dest != want || tr.Missing != 0 {
			t.Fatalf("postgres->sqlite %s report = %+v, want inserted=dest=%d", table, tr, want)
		}
	}
	name, size, files, created, reviewed, clean, alias := torrentRow(t, back,
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if name != "Beta" || size != 200 || created != 2000 || reviewed != 1700000000 || clean != "Beta (clean)" || alias != "" {
		t.Fatalf("round-tripped row mismatch: %q %d %d %d %q %q", name, size, created, reviewed, clean, alias)
	}
	if string(files) != string([]byte(`[{"path":"b.txt","size":5}]`)) {
		t.Fatalf("round-tripped files blob differs: %q", files)
	}
	if _, _, _, _, _, _, alias := torrentRow(t, back,
		"cccccccccccccccccccccccccccccccccccccccc"); alias != "Gamma alias" {
		t.Fatalf("round-tripped alias: %q", alias)
	}
	stats, err := back.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats["seen"] != 42 || stats["fetched"] != 7 {
		t.Fatalf("round-tripped stats: %v", stats)
	}
}
