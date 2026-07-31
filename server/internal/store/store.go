// Package store persists indexed torrents and pipeline counters in SQLite
// using the pure-Go modernc.org/sqlite driver (no cgo).
package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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

// readConns is the size of the read-only connection pool. WAL's whole point is
// that readers never block on the writer, so serializing reads behind the
// single write connection only made the moderation sweep and the search API
// wait for each other.
const readConns = 4

// Store wraps the SQLite database handles: one connection for writes (SQLite
// admits a single writer) and a small pool for reads.
type Store struct {
	w   *sql.DB
	r   *sql.DB
	fts bool
}

// Open opens (and creates if needed) the database at path and ensures the
// schema exists.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer; keep a single connection to avoid lock churn.
	db.SetMaxOpenConns(1)
	const schema = `
CREATE TABLE IF NOT EXISTS torrents (
	info_hash   TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	total_size  INTEGER NOT NULL,
	file_count  INTEGER NOT NULL,
	-- JSON file list, zstd-compressed when that is smaller than the plain
	-- text. See compressFiles/decompressFiles.
	files       BLOB NOT NULL,
	created_at  INTEGER NOT NULL,
	reviewed_at INTEGER NOT NULL DEFAULT 0,
	-- Title with promotional junk stripped by the moderation pass. Empty means
	-- "not trimmed"; name always keeps the raw title so trimming is reversible
	-- and search still matches text that only appears in the original.
	clean_name  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS stats (
	key   TEXT PRIMARY KEY,
	value INTEGER NOT NULL
);
-- Infohashes removed by moderation. Kept so the crawler cannot re-add (and
-- re-pay to re-classify) a torrent that was already rejected.
CREATE TABLE IF NOT EXISTS blocked (
	info_hash  TEXT PRIMARY KEY,
	reason     TEXT NOT NULL,
	name       TEXT NOT NULL,
	created_at INTEGER NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// Migrate databases created before reviewed_at existed. SQLite has no
	// ADD COLUMN IF NOT EXISTS, so a duplicate-column error is the success
	// case on an already-migrated database. This must run before anything
	// that references reviewed_at — on a pre-migration database the column
	// does not exist yet and those statements would fail.
	for _, m := range []struct{ name, stmt string }{
		{"reviewed_at", `ALTER TABLE torrents ADD COLUMN reviewed_at INTEGER NOT NULL DEFAULT 0`},
		{"clean_name", `ALTER TABLE torrents ADD COLUMN clean_name TEXT NOT NULL DEFAULT ''`},
	} {
		if _, err := db.Exec(m.stmt); err != nil &&
			!strings.Contains(err.Error(), "duplicate column name") {
			db.Close()
			return nil, fmt.Errorf("migrate %s: %w", m.name, err)
		}
	}
	// Migrate the uncompressed files_json TEXT column (databases created
	// before compression) into the files BLOB column.
	if err := migrateFilesBlob(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate files_json to files: %w", err)
	}
	// Partial index: only unreviewed rows are indexed, so it stays tiny and the
	// moderation sweep never scans the full table. Created after the migration
	// above so the column it filters on is guaranteed to exist.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_torrents_unreviewed
		ON torrents(created_at) WHERE reviewed_at = 0`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create unreviewed index: %w", err)
	}
	// Newest-first is the sort order of every search. Without this index an
	// empty query — the landing page — sorts the whole table per request,
	// and a keyword query cannot stop early after filling its page.
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_torrents_created
		ON torrents(created_at DESC)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create created_at index: %w", err)
	}
	fts, err := setupFTS(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create fts index: %w", err)
	}
	s := &Store{w: db, r: db, fts: fts}
	// An in-memory database lives inside its connection: a second handle would
	// open a second, empty database. Tests are the only user, and they are
	// single-threaded, so share the one handle there.
	if !isMemory(path) {
		rdb, err := sql.Open("sqlite", dsn)
		if err != nil {
			db.Close()
			return nil, err
		}
		rdb.SetMaxOpenConns(readConns)
		rdb.SetMaxIdleConns(readConns)
		s.r = rdb
	}
	return s, nil
}

// isMemory reports whether path names an in-memory database rather than a file.
func isMemory(path string) bool {
	return path == ":memory:" || strings.Contains(path, "mode=memory")
}

// FTSEnabled reports whether the full-text index backs keyword search. It is
// informational: the LIKE path is a complete fallback, so a database without
// the index answers the same queries, only slower on the rare-term case.
func (s *Store) FTSEnabled() bool { return s.fts }

// ftsTriggers keep the external-content index in step with the table. The
// update trigger is scoped to the indexed columns so the moderation sweep's
// reviewed_at stamping — a hundred rows a batch, none of them retitled — does
// not churn the index.
var ftsTriggers = []string{
	`CREATE TRIGGER torrents_fts_ai AFTER INSERT ON torrents BEGIN
		INSERT INTO torrents_fts(rowid, name, clean_name)
			VALUES (new.rowid, new.name, new.clean_name);
	END`,
	`CREATE TRIGGER torrents_fts_ad AFTER DELETE ON torrents BEGIN
		INSERT INTO torrents_fts(torrents_fts, rowid, name, clean_name)
			VALUES ('delete', old.rowid, old.name, old.clean_name);
	END`,
	`CREATE TRIGGER torrents_fts_au AFTER UPDATE OF name, clean_name ON torrents BEGIN
		INSERT INTO torrents_fts(torrents_fts, rowid, name, clean_name)
			VALUES ('delete', old.rowid, old.name, old.clean_name);
		INSERT INTO torrents_fts(rowid, name, clean_name)
			VALUES (new.rowid, new.name, new.clean_name);
	END`,
}

// setupFTS builds the full-text index over the two title columns, backfilling
// it from rows that predate it.
//
// The tokenizer is trigram because it is the only one that preserves what LIKE
// did: it matches a substring anywhere in the title, including inside a CJK run
// that a word tokenizer would swallow whole. It indexes 3-rune sequences, so
// shorter terms are invisible to it — Search keeps the LIKE conditions on top
// for exactly that reason (see searchKeywords).
//
// Creation, backfill and triggers share one transaction: if the process dies
// partway, the whole index rolls back and the next Open rebuilds it, so a
// half-populated index is never left behind to answer queries with.
func setupFTS(db *sql.DB) (bool, error) {
	var exists int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'torrents_fts'`).Scan(&exists); err != nil {
		return false, err
	}
	if exists > 0 {
		return true, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE VIRTUAL TABLE torrents_fts USING fts5(
		name, clean_name, content='torrents', content_rowid='rowid', tokenize='trigram')`); err != nil {
		// A build without FTS5 is not a failure: fall back to LIKE-only search.
		return false, nil
	}
	if _, err := tx.Exec(`INSERT INTO torrents_fts(rowid, name, clean_name)
		SELECT rowid, name, clean_name FROM torrents`); err != nil {
		return false, err
	}
	for _, stmt := range ftsTriggers {
		if _, err := tx.Exec(stmt); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// Shared zstd coders. EncodeAll/DecodeAll on a nil-stream coder are safe for
// concurrent use. Options are static and valid, so construction cannot fail.
var (
	// Default level, not SpeedBetterCompression: these are a few hundred bytes
	// of JSON per row and the extra ratio is marginal, but the encode runs on
	// the insert path of a machine whose CPU the fetch pool already wants.
	zenc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
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
// drops files_json, and vacuums to hand the freed pages back to the OS. It is
// crash-safe: every batch commits separately, and until the DROP COLUMN at
// the end both columns coexist, so an interrupted run resumes where it left
// off on the next Open. No-op unless files_json exists.
func migrateFilesBlob(db *sql.DB) error {
	var hasOld int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('torrents') WHERE name = 'files_json'`).Scan(&hasOld); err != nil {
		return err
	}
	if hasOld == 0 {
		return nil
	}
	// The files column cannot be in the CREATE TABLE path here (the table
	// already existed), so add it. x'' marks "not yet backfilled": a real
	// blob is never empty because even an empty file list serializes to "[]".
	if _, err := db.Exec(`ALTER TABLE torrents ADD COLUMN files BLOB NOT NULL DEFAULT x''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column name") {
		return err
	}
	// Backfill in bounded batches so memory stays flat on large databases.
	for {
		rows, err := db.Query(
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
		up, err := tx.Prepare(`UPDATE torrents SET files = ? WHERE info_hash = ?`)
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
	// Return the freed pages to the filesystem. Best-effort: VACUUM needs
	// scratch space on disk, and a database that keeps its old size is still
	// fully functional, so a failure here must not block startup.
	db.Exec(`VACUUM`)
	return nil
}

// Upsert inserts a torrent, ignoring duplicates (same info hash) and
// infohashes previously rejected by moderation.
func (s *Store) Upsert(t Torrent) error {
	fj, err := json.Marshal(t.Files)
	if err != nil {
		return err
	}
	_, err = s.w.Exec(
		`INSERT OR IGNORE INTO torrents (info_hash, name, total_size, file_count, files, created_at)
		 SELECT ?, ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM blocked WHERE info_hash = ?)`,
		t.InfoHash, t.Name, t.TotalSize, t.FileCount, compressFiles(fj), t.CreatedAt, t.InfoHash)
	return err
}

// maxKeywords bounds how many query keywords turn into LIKE conditions.
// Each keyword adds two LIKE evaluations per scanned row, so an unbounded
// list lets one request multiply its own scan cost ~100x. Past a handful of
// keywords extra terms only narrow an already tiny result set; the tail is
// ignored.
const maxKeywords = 8

// minTrigramRunes is the shortest keyword the full-text index can find. The
// trigram tokenizer indexes 3-rune sequences, so a 1- or 2-rune term matches
// nothing there — and does so silently, returning no rows rather than an
// error. Two-rune CJK titles ("三体", "沙丘") are ordinary queries here, so
// short terms must never be handed to MATCH alone.
const minTrigramRunes = 3

// countCap bounds how far a result count will walk. maxOffset in the API stops
// pagination at 10k rows, so a caller can never reach past this many results;
// counting further only makes every search pay for a number nobody reads.
const countCap = 10_100

// selectFields is the projection shared by Search and Latest, decoded by
// scanTorrents. The table is always aliased t so the same list works whether
// or not the full-text index is joined in.
const selectFields = `SELECT t.info_hash, IIF(t.clean_name <> '', t.clean_name, t.name),
	t.total_size, t.file_count, t.files, t.created_at FROM `

// Page is one page of search results.
type Page struct {
	Items []Torrent
	// Total is the number of matches, counted no further than countCap.
	Total int
	// Capped reports that counting stopped early, making Total a lower bound.
	Capped bool
}

// Search finds torrents whose name contains every space-separated keyword
// of query (AND semantics), newest first. An empty query returns the latest
// additions. page is 1-based; pageSize must be > 0. ctx bounds the queries:
// an unindexed keyword match is a full-table scan, so callers must be able to
// cut it off when the client hangs up or a deadline passes.
func (s *Store) Search(ctx context.Context, query string, page, pageSize int) (Page, error) {
	if page < 1 {
		page = 1
	}
	keywords := strings.Fields(query)
	if len(keywords) > maxKeywords {
		keywords = keywords[:maxKeywords]
	}
	if len(keywords) == 0 {
		items, err := s.Latest(ctx, page, pageSize)
		if err != nil {
			return Page{}, err
		}
		total, err := s.Count(ctx)
		if err != nil {
			return Page{}, err
		}
		return Page{Items: items, Total: total}, nil
	}
	return s.searchKeywords(ctx, keywords, page, pageSize)
}

// searchKeywords runs a keyword query, through the full-text index when one is
// available.
//
// The LIKE conditions are applied either way, on top of any MATCH: the index
// narrows the candidate set, the LIKE decides. That keeps results byte-identical
// to the unindexed path — including for the short keywords the trigram index
// cannot see at all — so the index is an accelerator and never the definition
// of a match.
func (s *Store) searchKeywords(ctx context.Context, keywords []string, page, pageSize int) (Page, error) {
	conds, args := likeConds(keywords)
	from, order := "torrents t", "t.created_at DESC"
	if s.fts {
		if match := ftsMatch(keywords); match != "" {
			from = "torrents_fts f JOIN torrents t ON t.rowid = f.rowid"
			// rowid is assigned in insertion order and created_at is stamped at
			// insert, so walking the match list by rowid descending is already
			// newest-first — and lets the scan stop as soon as the page is
			// full, instead of materializing every match to sort it.
			order = "f.rowid DESC"
			conds = append([]string{"torrents_fts MATCH ?"}, conds...)
			args = append([]any{match}, args...)
		}
	}
	where := " WHERE " + strings.Join(conds, " AND ")

	var p Page
	countQ := `SELECT COUNT(*) FROM (SELECT 1 FROM ` + from + where + ` LIMIT ?)`
	if err := s.r.QueryRowContext(ctx, countQ, append(append([]any{}, args...), countCap+1)...).
		Scan(&p.Total); err != nil {
		return Page{}, err
	}
	if p.Total > countCap {
		p.Total, p.Capped = countCap, true
	}

	q := selectFields + from + where + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	rows, err := s.r.QueryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return Page{}, err
	}
	p.Items, err = scanTorrents(rows)
	if err != nil {
		return Page{}, err
	}
	if order != "t.created_at DESC" {
		// rowid order tracks created_at only for rows this instance inserted
		// itself; the delta sync merges older rows in after newer ones, which
		// leaves the two orders slightly apart. Sorting the fetched page puts
		// it right locally — the page boundaries stay approximate, which is
		// all created_at ordering ever claimed to be.
		sortByCreatedDesc(p.Items)
	}
	return p, nil
}

// likeConds builds the match conditions and their arguments, one per keyword.
func likeConds(keywords []string) ([]string, []any) {
	conds := make([]string, 0, len(keywords))
	args := make([]any, 0, 2*len(keywords))
	for _, kw := range keywords {
		// Match either title: the raw one so trimming can never hide a
		// result, and the cleaned one so a phrase that only reads
		// contiguously once the ad text is gone still matches.
		conds = append(conds, "(t.name LIKE ? ESCAPE '\\' OR t.clean_name LIKE ? ESCAPE '\\')")
		args = append(args, "%"+escapeLike(kw)+"%", "%"+escapeLike(kw)+"%")
	}
	return conds, args
}

// ftsMatch builds an FTS5 MATCH expression ANDing the keywords the trigram
// index can actually resolve. Returns "" when none qualify, which sends the
// query down the plain LIKE path.
func ftsMatch(keywords []string) string {
	var terms []string
	for _, kw := range keywords {
		if len([]rune(kw)) < minTrigramRunes {
			continue
		}
		// A double-quoted FTS5 string is a literal phrase; the quote itself is
		// escaped by doubling. Everything else, including the query syntax's
		// own operators, is inert inside it.
		terms = append(terms, `"`+strings.ReplaceAll(kw, `"`, `""`)+`"`)
	}
	return strings.Join(terms, " AND ")
}

// sortByCreatedDesc orders a page newest-first, ties broken by info hash so
// the order is stable across requests.
func sortByCreatedDesc(items []Torrent) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt != items[j].CreatedAt {
			return items[i].CreatedAt > items[j].CreatedAt
		}
		return items[i].InfoHash < items[j].InfoHash
	})
}

// Latest returns a page of the newest torrents without counting the table.
// It walks the created_at index, so its cost is O(pageSize + offset) — the
// cheap path the landing page should take instead of Search("").
func (s *Store) Latest(ctx context.Context, page, pageSize int) ([]Torrent, error) {
	if page < 1 {
		page = 1
	}
	rows, err := s.r.QueryContext(ctx,
		selectFields+`torrents t ORDER BY t.created_at DESC LIMIT ? OFFSET ?`,
		pageSize, (page-1)*pageSize)
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
		if err := rows.Scan(&t.InfoHash, &t.Name, &t.TotalSize, &t.FileCount, &fb, &t.CreatedAt); err != nil {
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
	rows, err := s.r.Query(
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
func (s *Store) MarkReviewed(hashes []string, ts int64) error {
	if len(hashes) == 0 {
		return nil
	}
	args := make([]interface{}, 0, len(hashes)+1)
	args = append(args, ts)
	for _, h := range hashes {
		args = append(args, h)
	}
	_, err := s.w.Exec(
		`UPDATE torrents SET reviewed_at = ? WHERE info_hash IN (`+placeholders(len(hashes))+`)`, args...)
	return err
}

// SetCleanNames records trimmed titles. clean is keyed by info hash and holds
// the title with promotional text removed; the raw name column is left alone.
// Returns the number of rows updated.
func (s *Store) SetCleanNames(clean map[string]string) (int64, error) {
	if len(clean) == 0 {
		return 0, nil
	}
	tx, err := s.w.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	up, err := tx.Prepare(`UPDATE torrents SET clean_name = ? WHERE info_hash = ?`)
	if err != nil {
		return 0, err
	}
	defer up.Close()

	var n int64
	for hash, name := range clean {
		res, err := up.Exec(name, hash)
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
	tx, err := s.w.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	ins, err := tx.Prepare(
		`INSERT OR IGNORE INTO blocked (info_hash, reason, name, created_at) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer ins.Close()
	for i, h := range hashes {
		if _, err := ins.Exec(h, reason, names[i], ts); err != nil {
			return 0, err
		}
	}

	args := make([]interface{}, len(hashes))
	for i, h := range hashes {
		args[i] = h
	}
	res, err := tx.Exec(`DELETE FROM torrents WHERE info_hash IN (`+placeholders(len(hashes))+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// Known reports whether an infohash is already indexed or has been rejected by
// moderation.
//
// Both answers mean the same thing to the pipeline: fetching this torrent's
// metadata would produce a row Upsert then discards. That fetch is the most
// expensive step there is — tens of seconds of a worker slot — and the crawler
// re-offers old hashes constantly, because its in-memory dedup ring holds only
// a few hours of discovery while the database holds everything. Checking here
// turns those wasted slots back into throughput. Both lookups are on a primary
// key.
func (s *Store) Known(ctx context.Context, hash string) (bool, error) {
	var n int
	err := s.r.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM torrents WHERE info_hash = ?)
		     OR EXISTS(SELECT 1 FROM blocked  WHERE info_hash = ?)`, hash, hash).Scan(&n)
	return n != 0, err
}

// BlockedCount returns the number of moderation-rejected infohashes.
func (s *Store) BlockedCount(ctx context.Context) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM blocked`).Scan(&n)
	return n, err
}

// PendingReviewCount returns how many stored torrents await moderation.
func (s *Store) PendingReviewCount(ctx context.Context) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM torrents WHERE reviewed_at = 0`).Scan(&n)
	return n, err
}

// placeholders returns "?, ?, ..." with n entries.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// IncrStat atomically adds delta to the named counter.
func (s *Store) IncrStat(key string, delta int64) error {
	_, err := s.w.Exec(
		`INSERT INTO stats (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = value + excluded.value`, key, delta)
	return err
}

// Stats returns all pipeline counters.
func (s *Store) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT key, value FROM stats`)
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
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM torrents`).Scan(&n)
	return n, err
}

// Close closes the database.
func (s *Store) Close() error {
	if s.r != s.w {
		s.r.Close()
	}
	return s.w.Close()
}

// escapeLike escapes LIKE wildcards and the escape char itself.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
