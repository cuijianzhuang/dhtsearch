package store

import (
	"context"
	"fmt"
	"strings"
)

// This file implements whole-database migration between the two
// backends (the `dhtsearch-server migrate` subcommand and, through it,
// the installer's database switch). The schemas are isomorphic by
// design — info_hash stays hex TEXT, timestamps stay Unix seconds, the
// files column stays the same zstd bytes — so a migration is a plain
// row copy with no value conversion, in either direction.
//
// Copy semantics: a row whose primary key already exists in the target
// is skipped, never overwritten (ON CONFLICT DO NOTHING / INSERT OR
// IGNORE). That makes re-running a migration safe and lets a switch
// merge into a target that already holds data. After each table is
// copied, every source primary key is re-checked against the target;
// any missing key fails the migration.

// colKind selects how a column value is held while it crosses between
// the two databases.
type colKind int

const (
	colText colKind = iota
	colInt
	colBlob
)

// migrateTable describes one table's copy: columns in schema order,
// the primary key used for ordering, keyset pagination, the conflict
// target and the post-copy existence check.
type migrateTable struct {
	name string
	pk   string
	cols []string
	kind []colKind
}

var migrateTables = []migrateTable{
	{
		name: "torrents",
		pk:   "info_hash",
		cols: []string{"info_hash", "name", "total_size", "file_count", "files",
			"created_at", "reviewed_at", "clean_name", "alias"},
		kind: []colKind{colText, colText, colInt, colInt, colBlob,
			colInt, colInt, colText, colText},
	},
	{
		name: "stats",
		pk:   "key",
		cols: []string{"key", "value"},
		kind: []colKind{colText, colInt},
	},
	{
		name: "blocked",
		pk:   "info_hash",
		cols: []string{"info_hash", "reason", "name", "created_at"},
		kind: []colKind{colText, colText, colText, colInt},
	},
}

// migrateBatchSize bounds one read batch (and one target transaction).
// Rows carry the files blob, so the batch is kept modest.
const migrateBatchSize = 200

// TableReport is the per-table outcome of a Migrate run.
type TableReport struct {
	Table    string
	Source   int64 // rows in the source when the copy started
	Inserted int64 // rows actually written to the target
	Skipped  int64 // source rows the target already had (left untouched)
	Dest     int64 // rows in the target after the copy
	Missing  int64 // source keys not found in the target afterwards; must be 0
}

// MigrateReport is the full outcome of Migrate.
type MigrateReport struct {
	From   string // source dialect: "sqlite" or "postgres"
	To     string // target dialect
	Tables []TableReport
}

// Migrate copies every table from src to dst (either direction) and
// verifies that every source row is present in dst afterwards. Both
// stores must already be open; opening them runs the usual schema
// creation/migrations, so a pre-files-blob source is normalized before
// its rows are read and a fresh target gets its tables first.
//
// The source is only read, never written to (beyond what opening it
// does). Callers migrating a live database must stop the writer first:
// rows written to the source mid-copy are not picked up.
func Migrate(ctx context.Context, src, dst *Store) (*MigrateReport, error) {
	rep := &MigrateReport{From: src.Dialect(), To: dst.Dialect()}
	for _, t := range migrateTables {
		tr, err := migrateOneTable(ctx, src, dst, t)
		if err != nil {
			return rep, err
		}
		rep.Tables = append(rep.Tables, tr)
		if tr.Missing > 0 {
			return rep, fmt.Errorf("migrate %s: %d source rows missing in target after copy", t.name, tr.Missing)
		}
	}
	return rep, nil
}

func migrateOneTable(ctx context.Context, src, dst *Store, t migrateTable) (TableReport, error) {
	var rep TableReport
	rep.Table = t.name
	if err := src.queryRowContext(ctx, `SELECT COUNT(*) FROM `+t.name).Scan(&rep.Source); err != nil {
		return rep, fmt.Errorf("migrate %s: count source: %w", t.name, err)
	}

	ins := dst.d.insertOrIgnore(t.name, t.cols, t.pk)
	lastPK := ""
	for {
		batch, err := readBatch(ctx, src, t, lastPK)
		if err != nil {
			return rep, fmt.Errorf("migrate %s: read source: %w", t.name, err)
		}
		if len(batch) == 0 {
			break
		}
		n, err := insertBatch(ctx, dst, ins, batch)
		if err != nil {
			return rep, fmt.Errorf("migrate %s: write target: %w", t.name, err)
		}
		rep.Inserted += n
		rep.Skipped += int64(len(batch)) - n
		lastPK = batch[len(batch)-1].pk
	}

	if err := dst.queryRowContext(ctx, `SELECT COUNT(*) FROM `+t.name).Scan(&rep.Dest); err != nil {
		return rep, fmt.Errorf("migrate %s: count target: %w", t.name, err)
	}
	missing, err := countMissingKeys(ctx, src, dst, t)
	if err != nil {
		return rep, fmt.Errorf("migrate %s: verify: %w", t.name, err)
	}
	rep.Missing = missing
	return rep, nil
}

// migrateRow is one source row: typed values in column order plus the
// primary key lifted out for keyset pagination.
type migrateRow struct {
	pk   string
	vals []interface{}
}

// readBatch reads up to migrateBatchSize rows with pk > lastPK,
// ordered by the primary key.
func readBatch(ctx context.Context, s *Store, t migrateTable, lastPK string) ([]migrateRow, error) {
	q := `SELECT ` + strings.Join(t.cols, ", ") + ` FROM ` + t.name +
		` WHERE ` + t.pk + ` > ? ORDER BY ` + t.pk + ` LIMIT ?`
	rows, err := s.queryContext(ctx, q, lastPK, migrateBatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []migrateRow
	for rows.Next() {
		holders := make([]interface{}, len(t.cols))
		for i, k := range t.kind {
			switch k {
			case colText:
				holders[i] = new(string)
			case colInt:
				holders[i] = new(int64)
			case colBlob:
				holders[i] = new([]byte)
			}
		}
		if err := rows.Scan(holders...); err != nil {
			return nil, err
		}
		r := migrateRow{vals: make([]interface{}, len(t.cols))}
		for i, h := range holders {
			switch v := h.(type) {
			case *string:
				r.vals[i] = *v
			case *int64:
				r.vals[i] = *v
			case *[]byte:
				r.vals[i] = *v
			}
		}
		r.pk = r.vals[pkIndex(t)].(string)
		out = append(out, r)
	}
	return out, rows.Err()
}

func pkIndex(t migrateTable) int {
	for i, c := range t.cols {
		if c == t.pk {
			return i
		}
	}
	return 0
}

// insertBatch writes one batch in a single transaction and returns how
// many rows were actually inserted (the rest conflicted and were
// skipped).
func insertBatch(ctx context.Context, dst *Store, ins string, batch []migrateRow) (int64, error) {
	tx, err := dst.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(dst.d.rebind(ins))
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	var n int64
	for _, r := range batch {
		res, err := stmt.ExecContext(ctx, r.vals...)
		if err != nil {
			return 0, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		n += affected
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// countMissingKeys re-reads every source primary key in batches and
// counts how many do not exist in the target.
func countMissingKeys(ctx context.Context, src, dst *Store, t migrateTable) (int64, error) {
	var missing int64
	lastPK := ""
	for {
		keys, err := readKeys(ctx, src, t, lastPK)
		if err != nil {
			return 0, err
		}
		if len(keys) == 0 {
			return missing, nil
		}
		args := make([]interface{}, len(keys))
		for i, k := range keys {
			args[i] = k
		}
		var found int64
		q := `SELECT COUNT(*) FROM ` + t.name + ` WHERE ` + t.pk + ` IN (` + placeholders(len(keys)) + `)`
		if err := dst.queryRowContext(ctx, q, args...).Scan(&found); err != nil {
			return 0, err
		}
		missing += int64(len(keys)) - found
		lastPK = keys[len(keys)-1]
	}
}

func readKeys(ctx context.Context, s *Store, t migrateTable, lastPK string) ([]string, error) {
	rows, err := s.queryContext(ctx,
		`SELECT `+t.pk+` FROM `+t.name+` WHERE `+t.pk+` > ? ORDER BY `+t.pk+` LIMIT ?`,
		lastPK, migrateBatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}
