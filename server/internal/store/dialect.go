package store

import (
	"strconv"
	"strings"
)

// dialect captures every SQL difference between the two supported
// backends. All store queries are written once in the SQLite-flavoured
// form (with ? placeholders) and passed through the dialect before they
// reach database/sql, so dialect knowledge never leaks into call sites:
// there is exactly one place that knows Postgres spells things
// differently.
//
// The two backends:
//
//   - SQLite (default): modernc.org/sqlite, no cgo, one connection,
//     WAL. Zero-ops for a single-node deployment.
//   - Postgres (opt-in via DATABASE_URL): jackc/pgx in stdlib mode.
//     For deployments that outgrow a single writer or want the index
//     and the API on different machines.
type dialect struct {
	name string // "sqlite" or "postgres"
}

var (
	sqliteDialect   = dialect{name: "sqlite"}
	postgresDialect = dialect{name: "postgres"}
)

func (d dialect) isPostgres() bool { return d.name == "postgres" }

// driverName is the database/sql driver registered for this dialect.
func (d dialect) driverName() string {
	if d.isPostgres() {
		return "pgx"
	}
	return "sqlite"
}

// rebind rewrites ? placeholders to Postgres $1, $2, ... SQLite keeps ?
// as-is. No query in this package carries a literal '?' inside a string
// literal, so a plain scan is safe.
func (d dialect) rebind(query string) string {
	if !d.isPostgres() {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// likeOp preserves the search semantics across backends: SQLite LIKE is
// case-insensitive for ASCII, Postgres LIKE is not, so Postgres uses
// ILIKE. (On Postgres the pg_trgm GIN indexes created in Open can serve
// ILIKE; see createIndexes.)
func (d dialect) likeOp() string {
	if d.isPostgres() {
		return "ILIKE"
	}
	return "LIKE"
}

// displayNameExpr picks the cleaned title when one exists, else the raw
// name. SQLite spells that IIF(...), Postgres CASE WHEN.
func (d dialect) displayNameExpr() string {
	if d.isPostgres() {
		return "CASE WHEN clean_name <> '' THEN clean_name ELSE name END"
	}
	return "IIF(clean_name <> '', clean_name, name)"
}

// isDuplicateColumn reports whether err is the "column already exists"
// failure of an ADD COLUMN migration. SQLite and Postgres phrase (and
// code) it differently.
func (d dialect) isDuplicateColumn(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if d.isPostgres() {
		// pgconn error: ERROR: column "x" of relation "torrents" already exists (SQLSTATE 42701)
		return strings.Contains(msg, "already exists") || strings.Contains(msg, "42701")
	}
	return strings.Contains(msg, "duplicate column name")
}

// schemaStatements are the CREATE TABLE statements for a fresh
// database, one per entry: Postgres (via pgx) cannot run several
// commands in a single Exec, so callers execute them in order.
//
// torrents.files holds the JSON file list, zstd-compressed when that is
// smaller than the plain text (see compressFiles/decompressFiles).
// clean_name is the title with promotional junk stripped by the
// moderation pass; empty means "not trimmed", and name always keeps
// the raw title so trimming is reversible and search still matches
// text that only appears in the original. alias is the publisher-side
// title from an RSS source (see Torrent.Alias); empty when the torrent
// was only seen on the DHT. blocked holds infohashes removed by
// moderation, so the crawler cannot re-add (and re-pay to re-classify)
// a torrent that was already rejected.
func (d dialect) schemaStatements() []string {
	intType, blobType := "INTEGER", "BLOB"
	idType := "INTEGER PRIMARY KEY AUTOINCREMENT"
	if d.isPostgres() {
		intType, blobType = "BIGINT", "BYTEA"
		idType = "BIGSERIAL PRIMARY KEY"
	}
	return []string{
		`CREATE TABLE IF NOT EXISTS torrents (
			info_hash   TEXT PRIMARY KEY,
			name        TEXT NOT NULL,
			total_size  ` + intType + ` NOT NULL,
			file_count  INTEGER NOT NULL,
			files       ` + blobType + ` NOT NULL,
			created_at  ` + intType + ` NOT NULL,
			reviewed_at ` + intType + ` NOT NULL DEFAULT 0,
			clean_name  TEXT NOT NULL DEFAULT '',
			alias       TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS stats (
			key   TEXT PRIMARY KEY,
			value ` + intType + ` NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS blocked (
			info_hash  TEXT PRIMARY KEY,
			reason     TEXT NOT NULL,
			name       TEXT NOT NULL,
			created_at ` + intType + ` NOT NULL
		)`,
		// Operator overrides for runtime configuration, written by the
		// admin settings API (see internal/settings). Only keys the
		// settings registry marks editable ever land here; the effective
		// value of a setting is this table's row when present, else the
		// environment, else the built-in default.
		`CREATE TABLE IF NOT EXISTS settings (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL,
			updated_at ` + intType + ` NOT NULL
		)`,
		// The trending service's last good state (one JSON blob under
		// a single key), so a restart serves the previous Douban
		// charts immediately instead of an empty homepage section
		// until the first successful refetch. See internal/trending.
		`CREATE TABLE IF NOT EXISTS trending_cache (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		// Public API tokens (see internal/api's token gate). Only the
		// SHA-256 hash of a token is stored — the plaintext is shown to
		// the operator exactly once, at creation. prefix is the
		// display-safe head of the token ("dht_" + first 8 secret
		// chars), enough to tell tokens apart in the console and in
		// logs without revealing anything usable.
		`CREATE TABLE IF NOT EXISTS api_tokens (
			id           ` + idType + `,
			name         TEXT NOT NULL,
			prefix       TEXT NOT NULL,
			token_hash   TEXT NOT NULL UNIQUE,
			enabled      INTEGER NOT NULL DEFAULT 1,
			rate_per_min ` + intType + ` NOT NULL DEFAULT 0,
			call_count   ` + intType + ` NOT NULL DEFAULT 0,
			last_used_at ` + intType + ` NOT NULL DEFAULT 0,
			created_at   ` + intType + ` NOT NULL
		)`,
	}
}

// upsertSetting writes one settings row. The right-hand side references
// only excluded.*, which both backends accept (see incrStat for the
// Postgres ambiguity this avoids).
func (d dialect) upsertSetting() string {
	return `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`
}

// insertTorrent is Upsert's statement: insert unless the hash is already
// indexed or was rejected by moderation.
func (d dialect) insertTorrent() string {
	if d.isPostgres() {
		return `INSERT INTO torrents (info_hash, name, total_size, file_count, files, created_at, alias)
		 SELECT ?, ?, ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM blocked WHERE info_hash = ?)
		 ON CONFLICT (info_hash) DO NOTHING`
	}
	return `INSERT OR IGNORE INTO torrents (info_hash, name, total_size, file_count, files, created_at, alias)
		 SELECT ?, ?, ?, ?, ?, ?, ?
		 WHERE NOT EXISTS (SELECT 1 FROM blocked WHERE info_hash = ?)`
}

// insertBlocked is Block's statement for recording a rejected infohash.
func (d dialect) insertBlocked() string {
	if d.isPostgres() {
		return `INSERT INTO blocked (info_hash, reason, name, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (info_hash) DO NOTHING`
	}
	return `INSERT OR IGNORE INTO blocked (info_hash, reason, name, created_at) VALUES (?, ?, ?, ?)`
}

// incrStat adds delta to a stats counter. The ON CONFLICT ... DO
// UPDATE form exists in both dialects, but Postgres rejects the
// unqualified "value = value + ..." (column reference "value" is
// ambiguous, SQLSTATE 42702), so it qualifies the target table.
func (d dialect) incrStat() string {
	if d.isPostgres() {
		return `INSERT INTO stats (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = stats.value + excluded.value`
	}
	return `INSERT INTO stats (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = value + excluded.value`
}

// insertOrIgnore builds a bulk-copy insert for Migrate: rows whose
// primary key already exists in the target are skipped, never
// overwritten. Table and column names come from a fixed list in
// migrate.go, never from user input.
func (d dialect) insertOrIgnore(table string, cols []string, conflictCol string) string {
	ph := make([]string, len(cols))
	for i := range ph {
		ph[i] = "?"
	}
	joined := strings.Join(cols, ", ")
	if d.isPostgres() {
		return `INSERT INTO ` + table + ` (` + joined + `) VALUES (` + strings.Join(ph, ", ") + `)
		 ON CONFLICT (` + conflictCol + `) DO NOTHING`
	}
	return `INSERT OR IGNORE INTO ` + table + ` (` + joined + `) VALUES (` + strings.Join(ph, ", ") + `)`
}

// hasColumnQuery counts occurrences of a column; args are (table, column)
// for Postgres and (table, column) via pragma for SQLite — the SQLite
// form takes the table name as a string literal argument to the pragma
// table-valued function.
func (d dialect) hasColumnQuery() string {
	if d.isPostgres() {
		return `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = ? AND column_name = ?`
	}
	return `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`
}

// addFilesColumn adds the files blob column to a pre-compression table.
func (d dialect) addFilesColumn() string {
	if d.isPostgres() {
		// ''::bytea is the empty bytea, the "not yet backfilled" marker.
		return `ALTER TABLE torrents ADD COLUMN files BYTEA NOT NULL DEFAULT ''::bytea`
	}
	// x'' marks "not yet backfilled": a real blob is never empty because
	// even an empty file list serializes to "[]".
	return `ALTER TABLE torrents ADD COLUMN files BLOB NOT NULL DEFAULT x''`
}
