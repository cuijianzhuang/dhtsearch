// Package store persists indexed torrents and pipeline counters.
//
// Two backends are supported behind one dialect abstraction (see
// dialect.go): SQLite via the pure-Go modernc.org/sqlite driver (no
// cgo, the default), and Postgres via jackc/pgx in stdlib mode,
// selected with a postgres:// DATABASE_URL.
package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"

	"dhtsearch/server/internal/filter"
)

// Torrent is one indexed torrent record.
type Torrent struct {
	InfoHash  string        `json:"info_hash"`
	Name      string        `json:"name"`
	TotalSize int64         `json:"total_size"`
	FileCount int           `json:"file_count"`
	Files     []filter.File `json:"files"`
	CreatedAt int64         `json:"created_at"`
	// Alias is the release's title as published on an RSS source (e.g. a
	// dmhy page title carrying the Chinese and Japanese names), where the
	// torrent's own name is often romaji only. Searched alongside the name.
	Alias string `json:"alias,omitempty"`
}

// Candidate is the slim projection of a torrent handed to the moderation
// pass. The file list is deliberately excluded: it is the bulk of the row and
// the classifier only needs the name, size and file count.
type Candidate struct {
	InfoHash  string
	Name      string
	TotalSize int64
	FileCount int
}

// Store wraps the database handle and its SQL dialect.
type Store struct {
	db *sql.DB
	d  dialect
}

// Dialect reports the backend in use: "sqlite" or "postgres".
func (s *Store) Dialect() string { return s.d.name }

// --- query helpers: every statement goes through rebind so call sites
// --- never spell placeholders per-backend.

func (s *Store) exec(query string, args ...interface{}) (sql.Result, error) {
	return s.db.Exec(s.d.rebind(query), args...)
}

func (s *Store) execContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.d.rebind(query), args...)
}

func (s *Store) query(query string, args ...interface{}) (*sql.Rows, error) {
	return s.db.Query(s.d.rebind(query), args...)
}

func (s *Store) queryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.d.rebind(query), args...)
}

func (s *Store) queryRow(query string, args ...interface{}) *sql.Row {
	return s.db.QueryRow(s.d.rebind(query), args...)
}

func (s *Store) queryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return s.db.QueryRowContext(ctx, s.d.rebind(query), args...)
}

// Open opens (and creates if needed) the SQLite database at path and
// ensures the schema exists. Kept as the historical entry point; new
// code that wants backend selection should use OpenURL.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open(sqliteDialect.driverName(), dsn)
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer; keep a single connection to avoid lock churn.
	db.SetMaxOpenConns(1)
	return openStore(db, sqliteDialect)
}

// OpenPostgres opens the Postgres database at dsn (a postgres:// URL or
// a pgx keyword/value string) and ensures the schema exists.
func OpenPostgres(dsn string) (*Store, error) {
	db, err := sql.Open(postgresDialect.driverName(), dsn)
	if err != nil {
		return nil, err
	}
	// Unlike SQLite there is no single-writer constraint; a small pool
	// lets searches overlap the writer instead of queueing behind it.
	db.SetMaxOpenConns(10)
	return openStore(db, postgresDialect)
}

// IsPostgresURL reports whether raw is a Postgres connection URL.
func IsPostgresURL(raw string) bool {
	return strings.HasPrefix(raw, "postgres://") || strings.HasPrefix(raw, "postgresql://")
}

// OpenURL opens whichever backend raw selects: a postgres:// or
// postgresql:// URL means Postgres; anything else is treated as a
// SQLite file path (including ":memory:"), matching Open.
func OpenURL(raw string) (*Store, error) {
	if IsPostgresURL(raw) {
		return OpenPostgres(raw)
	}
	return Open(raw)
}

// openStore runs schema creation and migrations on a fresh handle.
func openStore(db *sql.DB, d dialect) (*Store, error) {
	s := &Store{db: db, d: d}
	for _, stmt := range d.schemaStatements() {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	// Migrate databases created before reviewed_at existed. Neither
	// backend has ADD COLUMN IF NOT EXISTS, so a duplicate-column error
	// is the success case on an already-migrated database. This must run
	// before anything that references reviewed_at — on a pre-migration
	// database the column does not exist yet and those statements would
	// fail.
	intType := "INTEGER"
	if d.isPostgres() {
		intType = "BIGINT"
	}
	for _, m := range []struct{ name, stmt string }{
		{"reviewed_at", `ALTER TABLE torrents ADD COLUMN reviewed_at ` + intType + ` NOT NULL DEFAULT 0`},
		{"clean_name", `ALTER TABLE torrents ADD COLUMN clean_name TEXT NOT NULL DEFAULT ''`},
		{"alias", `ALTER TABLE torrents ADD COLUMN alias TEXT NOT NULL DEFAULT ''`},
	} {
		if _, err := db.Exec(m.stmt); err != nil && !d.isDuplicateColumn(err) {
			db.Close()
			return nil, fmt.Errorf("migrate %s: %w", m.name, err)
		}
	}
	// Migrate the uncompressed files_json TEXT column (databases created
	// before compression) into the files BLOB/BYTEA column.
	if err := s.migrateFilesBlob(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate files_json to files: %w", err)
	}
	if err := s.createIndexes(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// createIndexes builds the indexes every backend shares, plus the
// Postgres-only trigram indexes that keep substring search off a full
// sequential scan once the table grows.
func (s *Store) createIndexes() error {
	// Partial index: only unreviewed rows are indexed, so it stays tiny and the
	// moderation sweep never scans the full table. Created after the migration
	// above so the column it filters on is guaranteed to exist.
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_torrents_unreviewed
		ON torrents(created_at) WHERE reviewed_at = 0`); err != nil {
		return fmt.Errorf("create unreviewed index: %w", err)
	}
	// Newest-first is the sort order of every search. Without this index an
	// empty query — the landing page — sorts the whole table per request,
	// and a keyword query cannot stop early after filling its page.
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_torrents_created
		ON torrents(created_at DESC)`); err != nil {
		return fmt.Errorf("create created_at index: %w", err)
	}
	if s.d.isPostgres() {
		// Best-effort: pg_trgm needs the extension to be installable by
		// this role (superuser or rds_superuser on managed offerings).
		// Without it ILIKE still works, just as a sequential scan, so a
		// failure here must not block startup.
		if _, err := s.db.Exec(`CREATE EXTENSION IF NOT EXISTS pg_trgm`); err == nil {
			for _, col := range []string{"name", "clean_name", "alias"} {
				// Index names are built from a fixed column list, never
				// from user input.
				s.db.Exec(fmt.Sprintf(
					`CREATE INDEX IF NOT EXISTS idx_torrents_%s_trgm ON torrents USING GIN (%s gin_trgm_ops)`,
					col, col))
			}
		}
	}
	return nil
}

// Shared zstd coders. EncodeAll/DecodeAll on a nil-stream coder are safe for
// concurrent use. Options are static and valid, so construction cannot fail.
var (
	zenc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	zdec, _ = zstd.NewReader(nil)
)

// zstdMagic starts every zstd frame (RFC 8878). Raw JSON starts with '[', so
// the first bytes of a files blob say unambiguously whether it is compressed.
var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// compressFiles returns raw zstd-compressed, or raw itself when compression
// does not pay (small file lists, where the frame overhead wins).
func compressFiles(raw []byte) []byte {
	c := zenc.EncodeAll(raw, make([]byte, 0, len(raw)))
	if len(c) >= len(raw) {
		return raw
	}
	return c
}

// decompressFiles reverses compressFiles.
func decompressFiles(b []byte) ([]byte, error) {
	if !bytes.HasPrefix(b, zstdMagic) {
		return b, nil
	}
	return zdec.DecodeAll(b, nil)
}

// migrateFilesBlob rewrites databases created when the file list was stored
// as plain JSON in files_json: it backfills the compressed files column,
// drops files_json, and (on SQLite) vacuums to hand the freed pages back
// to the OS. It is crash-safe: every batch commits separately, and until
// the DROP COLUMN at the end both columns coexist, so an interrupted run
// resumes where it left off on the next Open. No-op unless files_json
// exists.
func (s *Store) migrateFilesBlob() error {
	db := s.db
	var hasOld int
	if err := s.queryRow(s.d.hasColumnQuery(), "torrents", "files_json").Scan(&hasOld); err != nil {
		return err
	}
	if hasOld == 0 {
		return nil
	}
	// The files column cannot be in the CREATE TABLE path here (the table
	// already existed), so add it. The empty blob marks "not yet
	// backfilled": a real blob is never empty because even an empty file
	// list serializes to "[]".
	if _, err := db.Exec(s.d.addFilesColumn()); err != nil && !s.d.isDuplicateColumn(err) {
		return err
	}
	// Backfill in bounded batches so memory stays flat on large databases.
	for {
		rows, err := s.query(
			`SELECT info_hash, files_json FROM torrents WHERE length(files) = 0 LIMIT 1000`)
		if err != nil {
			return err
		}
		type pending struct {
			hash string
			blob []byte
		}
		var batch []pending
		for rows.Next() {
			var hash, fj string
			if err := rows.Scan(&hash, &fj); err != nil {
				rows.Close()
				return err
			}
			if fj == "" {
				// Defensive: an empty string would neither unmarshal nor
				// leave the "not yet backfilled" state. Normalize.
				fj = "[]"
			}
			batch = append(batch, pending{hash, compressFiles([]byte(fj))})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(batch) == 0 {
			break
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		up, err := tx.Prepare(s.d.rebind(`UPDATE torrents SET files = ? WHERE info_hash = ?`))
		if err != nil {
			tx.Rollback()
			return err
		}
		for _, p := range batch {
			if _, err := up.Exec(p.blob, p.hash); err != nil {
				up.Close()
				tx.Rollback()
				return err
			}
		}
		up.Close()
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`ALTER TABLE torrents DROP COLUMN files_json`); err != nil {
		return err
	}
	if !s.d.isPostgres() {
		// Return the freed pages to the filesystem. Best-effort: VACUUM needs
		// scratch space on disk, and a database that keeps its old size is still
		// fully functional, so a failure here must not block startup.
		db.Exec(`VACUUM`)
	}
	return nil
}

// Upsert inserts a torrent, ignoring duplicates (same info hash) and
// infohashes previously rejected by moderation.
func (s *Store) Upsert(t Torrent) error {
	// Name, alias and file paths are raw DHT/feed bytes and may not be
	// valid UTF-8; sanitize before they reach a TEXT column (Postgres
	// rejects invalid UTF-8, SQLite does not). The file list itself is
	// stored as a compressed blob, but its paths are sanitized too so
	// both backends serve identical values.
	t.Name = sanitizeUTF8(t.Name)
	t.Alias = sanitizeUTF8(t.Alias)
	for i := range t.Files {
		t.Files[i].Path = sanitizeUTF8(t.Files[i].Path)
	}
	fj, err := json.Marshal(t.Files)
	if err != nil {
		return err
	}
	_, err = s.exec(s.d.insertTorrent(),
		t.InfoHash, t.Name, t.TotalSize, t.FileCount, compressFiles(fj), t.CreatedAt, t.Alias, t.InfoHash)
	return err
}

// SetAlias records an RSS-side title for an already indexed torrent. It
// reports whether the torrent exists; false means it still has to be
// fetched (or was blocked — see IsBlocked).
func (s *Store) SetAlias(infoHash, alias string) (bool, error) {
	res, err := s.exec(`UPDATE torrents SET alias = ? WHERE info_hash = ?`, sanitizeUTF8(alias), infoHash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// IsBlocked reports whether moderation rejected infoHash.
func (s *Store) IsBlocked(infoHash string) (bool, error) {
	var n int
	err := s.queryRow(`SELECT COUNT(*) FROM blocked WHERE info_hash = ?`, infoHash).Scan(&n)
	return n > 0, err
}

// maxKeywords bounds how many query keywords turn into LIKE conditions.
// Each keyword adds three LIKE evaluations per scanned row, so an unbounded
// list lets one request multiply its own scan cost ~100x. Past a handful of
// keywords extra terms only narrow an already tiny result set; the tail is
// ignored.
const maxKeywords = 8

// selectCols is the projection shared by Search and Latest, decoded by
// scanTorrents. The display-name expression is dialect-specific.
func (s *Store) selectCols() string {
	return `SELECT info_hash, ` + s.d.displayNameExpr() + `,
	total_size, file_count, files, created_at, alias FROM torrents`
}

// Search finds torrents whose name or alias contains every space-separated keyword
// of query (AND semantics), newest first. An empty query returns the latest
// additions. page is 1-based; pageSize must be > 0. ctx bounds the queries:
// keyword matching is a full-table scan, so callers must be able to cut it
// off when the client hangs up or a deadline passes.
func (s *Store) Search(ctx context.Context, query string, page, pageSize int) (items []Torrent, total int, err error) {
	if page < 1 {
		page = 1
	}
	where, args := "", []interface{}{}
	keywords := strings.Fields(query)
	if len(keywords) > maxKeywords {
		keywords = keywords[:maxKeywords]
	}
	if len(keywords) > 0 {
		var conds []string
		op := s.d.likeOp()
		for _, kw := range keywords {
			// Match either title: the raw one so trimming can never hide a
			// result, and the cleaned one so a phrase that only reads
			// contiguously once the ad text is gone still matches. The alias
			// carries the publisher's title, often the only place a Chinese
			// name appears for a romaji-named release.
			conds = append(conds, "(name "+op+" ? ESCAPE '\\' OR clean_name "+op+" ? ESCAPE '\\' OR alias "+op+" ? ESCAPE '\\')")
			like := "%" + escapeLike(kw) + "%"
			args = append(args, like, like, like)
		}
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	if err = s.queryRowContext(ctx, `SELECT COUNT(*) FROM torrents`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := s.selectCols() + where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := s.queryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	items, err = scanTorrents(rows)
	return items, total, err
}

// Latest returns a page of the newest torrents without counting the table.
// It walks the created_at index, so its cost is O(pageSize + offset) — the
// cheap path the landing page should take instead of Search("").
func (s *Store) Latest(ctx context.Context, page, pageSize int) ([]Torrent, error) {
	if page < 1 {
		page = 1
	}
	rows, err := s.queryContext(ctx,
		s.selectCols()+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}
	return scanTorrents(rows)
}

// scanTorrents drains rows from a selectCols query, decompressing each file
// list. It always closes rows.
func scanTorrents(rows *sql.Rows) (items []Torrent, err error) {
	defer rows.Close()
	for rows.Next() {
		var t Torrent
		var fb []byte
		if err := rows.Scan(&t.InfoHash, &t.Name, &t.TotalSize, &t.FileCount, &fb, &t.CreatedAt, &t.Alias); err != nil {
			return nil, err
		}
		fj, err := decompressFiles(fb)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(fj, &t.Files); err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}

// Unreviewed returns up to limit torrents the moderation pass has not seen
// yet, oldest first.
func (s *Store) Unreviewed(limit int) ([]Candidate, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.query(
		`SELECT info_hash, name, total_size, file_count FROM torrents
		 WHERE reviewed_at = 0 ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.InfoHash, &c.Name, &c.TotalSize, &c.FileCount); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkReviewed stamps hashes as classified so later sweeps skip them.
// Returns the number of rows actually stamped; hashes that are not in
// the table match nothing, so callers reporting an "affected" count
// stay truthful (same convention as Block).
func (s *Store) MarkReviewed(hashes []string, ts int64) (int64, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	args := make([]interface{}, 0, len(hashes)+1)
	args = append(args, ts)
	for _, h := range hashes {
		args = append(args, h)
	}
	res, err := s.exec(
		`UPDATE torrents SET reviewed_at = ? WHERE info_hash IN (`+placeholders(len(hashes))+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetCleanNames records trimmed titles. clean is keyed by info hash and holds
// the title with promotional text removed; the raw name column is left alone.
// Returns the number of rows updated.
func (s *Store) SetCleanNames(clean map[string]string) (int64, error) {
	if len(clean) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	up, err := tx.Prepare(s.d.rebind(`UPDATE torrents SET clean_name = ? WHERE info_hash = ?`))
	if err != nil {
		return 0, err
	}
	defer up.Close()

	var n int64
	for hash, name := range clean {
		res, err := up.Exec(sanitizeUTF8(name), hash)
		if err != nil {
			return 0, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		n += affected
	}
	return n, tx.Commit()
}

// Block deletes the named torrents and records them as rejected so the
// crawler cannot re-add them. names must be parallel to hashes; it is stored
// only for the audit trail. Returns the number of rows actually deleted.
func (s *Store) Block(hashes, names []string, reason string, ts int64) (int64, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	if len(names) != len(hashes) {
		return 0, fmt.Errorf("block: %d hashes but %d names", len(hashes), len(names))
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	ins, err := tx.Prepare(s.d.rebind(s.d.insertBlocked()))
	if err != nil {
		return 0, err
	}
	defer ins.Close()
	reason = sanitizeUTF8(reason)
	for i, h := range hashes {
		if _, err := ins.Exec(h, reason, sanitizeUTF8(names[i]), ts); err != nil {
			return 0, err
		}
	}

	args := make([]interface{}, len(hashes))
	for i, h := range hashes {
		args[i] = h
	}
	res, err := tx.Exec(s.d.rebind(`DELETE FROM torrents WHERE info_hash IN (`+placeholders(len(hashes))+`)`), args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// BlockedCount returns the number of moderation-rejected infohashes.
func (s *Store) BlockedCount(ctx context.Context) (int, error) {
	var n int
	err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM blocked`).Scan(&n)
	return n, err
}

// PendingReviewCount returns how many stored torrents await moderation.
func (s *Store) PendingReviewCount(ctx context.Context) (int, error) {
	var n int
	err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM torrents WHERE reviewed_at = 0`).Scan(&n)
	return n, err
}

// placeholders returns "?, ?, ..." with n entries.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// IncrStat atomically adds delta to the named counter.
func (s *Store) IncrStat(key string, delta int64) error {
	_, err := s.exec(s.d.incrStat(), key, delta)
	return err
}

// Stats returns all pipeline counters.
func (s *Store) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := s.queryContext(ctx, `SELECT key, value FROM stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int64{}
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, rows.Err()
}

// Count returns the number of stored torrents.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM torrents`).Scan(&n)
	return n, err
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// escapeLike escapes LIKE wildcards and the escape char itself.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
