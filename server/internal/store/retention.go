package store

import (
	"context"
)

// Data-retention eligibility and deletion (see internal/retention
// for the runner). A torrent is eligible for cleanup when BOTH:
//
//   - its swarm health derives to HealthLikelyDead through
//     SwarmHealth — the one derivation every surface uses; this
//     file never restates the thresholds, it only prefilters a
//     superset of candidates in SQL and lets SwarmHealth judge
//     each one, and
//   - it was indexed before the caller's age cutoff
//     (torrents.created_at < cutoff).
//
// Deleting a torrent removes its torrents row (the files blob
// lives on it), its swarm_stats snapshot and its fetch_attempts
// backoff row. The blocked table is deliberately never touched:
// a blacklisted hash must stay blacklisted after its row is
// cleaned, and a cleaned hash that resurfaces later is simply
// re-ingested (its swarm evidence starts over).

// retentionCandidatePage bounds one candidate query. Candidates
// are the SQL prefilter's superset of the eligible set — in
// practice small, because a zero_count >= deadZeroStreakMin
// streak is rare — so a modest page keeps each query cheap while
// the keyset cursor walks the whole set without OFFSET drift.
const retentionCandidatePage = 256

// candidateQuery is the prefilter: every likely_dead row passes
// it (seeders and leechers both zero, a zero streak of at least
// deadZeroStreakMin with a recorded start), but not every row it
// returns is likely_dead — the 48h span half of the derivation is
// SwarmHealth's call, made in Go below.
const candidateQuery = `SELECT s.info_hash, s.seeders, s.leechers, s.completed, s.scraped_at, s.zero_since, s.zero_count
	FROM swarm_stats s JOIN torrents t ON t.info_hash = s.info_hash
	WHERE t.created_at < ? AND s.seeders = 0 AND s.leechers = 0
		AND s.zero_count >= ? AND s.zero_since > 0 AND s.info_hash > ?
	ORDER BY s.info_hash ASC LIMIT ?`

// ScanLikelyDead returns up to limit info hashes that are eligible
// for retention cleanup under the given created_at cutoff,
// starting strictly after cursor ("" walks from the beginning).
// nextCursor is the last candidate examined — pass it back to
// continue the walk; "" means the candidate set is exhausted.
// Hashes are returned in ascending order, and a hash the walk has
// passed is never returned again, so a caller deleting each batch
// as it goes cannot loop or skip.
func (s *Store) ScanLikelyDead(ctx context.Context, cutoff int64, cursor string, limit int) (hashes []string, nextCursor string, err error) {
	if limit < 1 {
		limit = 1
	}
	cur := cursor
	for len(hashes) < limit {
		rows, err := s.queryContext(ctx, candidateQuery,
			cutoff, int64(deadZeroStreakMin), cur, retentionCandidatePage)
		if err != nil {
			return nil, "", err
		}
		var page []SwarmStats
		for rows.Next() {
			var sw SwarmStats
			if err := rows.Scan(&sw.InfoHash, &sw.Seeders, &sw.Leechers,
				&sw.Completed, &sw.ScrapedAt, &sw.ZeroSince, &sw.ZeroCount); err != nil {
				rows.Close()
				return nil, "", err
			}
			page = append(page, sw)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, "", err
		}
		rows.Close()
		for _, sw := range page {
			cur = sw.InfoHash
			if SwarmHealth(&sw) == HealthLikelyDead {
				hashes = append(hashes, sw.InfoHash)
				if len(hashes) >= limit {
					// Stopped on the limit, not on exhaustion:
					// the cursor says where the walk resumes.
					return hashes, cur, nil
				}
			}
		}
		if len(page) < retentionCandidatePage {
			// A short page is the end of the candidate set.
			return hashes, "", nil
		}
	}
	return hashes, cur, nil
}

// CountLikelyDead counts the torrents eligible for retention
// cleanup under the given created_at cutoff, walking the same
// candidate path ScanLikelyDead deletes from — the count and the
// deletion can therefore never disagree about what "eligible"
// means.
func (s *Store) CountLikelyDead(ctx context.Context, cutoff int64) (int64, error) {
	var total int64
	cursor := ""
	for {
		hashes, next, err := s.ScanLikelyDead(ctx, cutoff, cursor, 1000)
		if err != nil {
			return 0, err
		}
		total += int64(len(hashes))
		if next == "" {
			return total, nil
		}
		cursor = next
	}
}

// DeleteRetentionBatch removes the given torrents and everything
// that belongs to them — the torrents row itself (files blob
// included), the swarm_stats snapshot and the fetch_attempts
// backoff row — in one transaction, and returns how many torrent
// rows were deleted. The blocked table is never touched (see the
// file comment). An empty batch is a no-op.
func (s *Store) DeleteRetentionBatch(ctx context.Context, hashes []string) (int64, error) {
	if len(hashes) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	args := make([]interface{}, len(hashes))
	for i, h := range hashes {
		args[i] = h
	}
	in := placeholders(len(hashes))
	res, err := tx.ExecContext(ctx, s.d.rebind(
		`DELETE FROM torrents WHERE info_hash IN (`+in+`)`), args...)
	if err != nil {
		return 0, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	for _, table := range []string{"swarm_stats", "fetch_attempts"} {
		if _, err := tx.ExecContext(ctx, s.d.rebind(
			`DELETE FROM `+table+` WHERE info_hash IN (`+in+`)`), args...); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}
