package store

import (
	"context"
)

// Re-scrape candidate selection (see internal/rescrape for the
// runner). Swarm snapshots are written when a torrent is ingested
// and never refreshed on their own; this is the query that finds
// the ones worth re-checking.

// StalestSwarmHashes returns up to limit info hashes whose swarm
// snapshot is older than scrapedBefore (Unix seconds), stalest
// first. A torrent with no snapshot at all counts as scraped at
// the epoch: it sorts ahead of every scraped one and passes any
// age gate, because "never checked" is the stalest state there
// is — and the only way its health can ever leave unknown is a
// first scrape. Ties break on info_hash so the order is total and
// both backends agree. The statement is plain SQL over the two
// tables (COALESCE is spelled the same on both dialects), so
// there is one implementation, not one per backend.
func (s *Store) StalestSwarmHashes(ctx context.Context, scrapedBefore int64, limit int) ([]string, error) {
	if limit < 1 {
		return nil, nil
	}
	rows, err := s.queryContext(ctx,
		`SELECT t.info_hash FROM torrents t
		 LEFT JOIN swarm_stats w ON w.info_hash = t.info_hash
		 WHERE w.info_hash IS NULL OR w.scraped_at < ?
		 ORDER BY COALESCE(w.scraped_at, 0) ASC, t.info_hash ASC
		 LIMIT ?`, scrapedBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
