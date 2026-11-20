package store

import (
	"context"
	"database/sql"
	"time"
)

// This file backs the fetch admission negative cache (see
// internal/admission): the metadata queue's push path asks, per
// hash, "is this already indexed, and is it in failure backoff?"
// before spending a worker slot on it, and every fetch outcome is
// recorded back here so the backoff state survives restarts.
//
// FetchAttempt is one row of fetch_attempts: the consecutive-failure
// state of one infohash. A hash with no row has either never failed
// or has since been fetched successfully (success deletes the row).
type FetchAttempt struct {
	InfoHash    string
	Fails       int64  // consecutive failed fetch attempts
	NextRetryAt int64  // unix seconds; admission refuses the hash before this
	LastOutcome string // "timeout" | "failed" | "prescreen"
	UpdatedAt   int64  // unix seconds of the last recorded outcome
}

// fetchBackoffBase is the first backoff interval, after one failure.
const fetchBackoffBase = time.Hour

// FetchBackoff returns how long a hash with the given consecutive
// failure count (already including the failure being recorded) waits
// before it may be admitted again: the base interval doubling per
// failure — 1h, 2h, 4h, 8h, ... — capped at max. max <= 0 selects a
// 24h cap. The doubling loop stops at the cap, so large failure
// counts cannot overflow the duration.
func FetchBackoff(fails int64, max time.Duration) time.Duration {
	if max <= 0 {
		max = 24 * time.Hour
	}
	d := fetchBackoffBase
	for i := int64(1); i < fails && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// FetchAdmission answers the two admission questions for one hash in
// a single round trip: whether the hash is already in the torrents
// table, and — when a fetch_attempts row exists — the instant its
// backoff expires (0 when there is no row). Both lookups are primary
// key point queries; the torrents side rides the table's PK index.
func (s *Store) FetchAdmission(ctx context.Context, infoHash string) (stored bool, nextRetryAt int64, err error) {
	var storedN int
	err = s.queryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM torrents WHERE info_hash = ?),
			COALESCE((SELECT next_retry_at FROM fetch_attempts WHERE info_hash = ?), 0)`,
		infoHash, infoHash).Scan(&storedN, &nextRetryAt)
	if err != nil {
		return false, 0, err
	}
	return storedN > 0, nextRetryAt, nil
}

// FetchAttemptRow returns the backoff row for a hash; found is false
// when the hash has no recorded failure state.
func (s *Store) FetchAttemptRow(ctx context.Context, infoHash string) (att FetchAttempt, found bool, err error) {
	err = s.queryRowContext(ctx,
		`SELECT info_hash, fails, next_retry_at, last_outcome, updated_at
		 FROM fetch_attempts WHERE info_hash = ?`, infoHash).
		Scan(&att.InfoHash, &att.Fails, &att.NextRetryAt, &att.LastOutcome, &att.UpdatedAt)
	if err == sql.ErrNoRows {
		return FetchAttempt{}, false, nil
	}
	if err != nil {
		return FetchAttempt{}, false, err
	}
	return att, true, nil
}

// RecordFetchFailure records one failed fetch attempt for a hash:
// the consecutive-failure count grows by one and the next-retry
// instant is recomputed from the new count via FetchBackoff. It
// returns the new NextRetryAt. The read-modify-write runs in one
// short transaction of plain statements (no dialect-specific upsert
// spelling); on Postgres two fetchers racing the same hash can still
// interleave, which costs at most one failure increment — the
// backoff that results is never wrong, just occasionally one step
// shorter than the true count.
func (s *Store) RecordFetchFailure(ctx context.Context, infoHash, outcome string, now time.Time, maxBackoff time.Duration) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	nowUnix := now.Unix()
	if _, err := tx.ExecContext(ctx, s.d.rebind(
		`INSERT INTO fetch_attempts (info_hash, fails, next_retry_at, last_outcome, updated_at)
		 VALUES (?, 0, 0, '', ?)
		 ON CONFLICT(info_hash) DO NOTHING`), infoHash, nowUnix); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, s.d.rebind(
		`UPDATE fetch_attempts SET fails = fails + 1, last_outcome = ?, updated_at = ?
		 WHERE info_hash = ?`), outcome, nowUnix, infoHash); err != nil {
		return 0, err
	}
	var fails int64
	if err := tx.QueryRowContext(ctx, s.d.rebind(
		`SELECT fails FROM fetch_attempts WHERE info_hash = ?`), infoHash).Scan(&fails); err != nil {
		return 0, err
	}
	nextRetry := now.Add(FetchBackoff(fails, maxBackoff)).Unix()
	if _, err := tx.ExecContext(ctx, s.d.rebind(
		`UPDATE fetch_attempts SET next_retry_at = ? WHERE info_hash = ?`), nextRetry, infoHash); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return nextRetry, nil
}

// ClearFetchAttempt deletes a hash's failure state after a
// successful fetch: a live swarm starts over with a clean slate, so
// one success fully rehabilitates the hash instead of merely
// pausing a long backoff.
func (s *Store) ClearFetchAttempt(ctx context.Context, infoHash string) error {
	_, err := s.execContext(ctx, `DELETE FROM fetch_attempts WHERE info_hash = ?`, infoHash)
	return err
}

// PruneFetchAttempts deletes rows whose backoff expired before
// cutoff (unix seconds) and returns how many were removed. Rows are
// normally cleared by a success or refreshed by a new failure; what
// remains past expiry is hashes the network stopped re-reporting,
// and keeping them only bloats the admission lookup's table.
func (s *Store) PruneFetchAttempts(ctx context.Context, cutoff int64) (int64, error) {
	res, err := s.execContext(ctx, `DELETE FROM fetch_attempts WHERE next_retry_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
