package store

import (
	"context"
)

// TrackerQuality is one row of the scraper's persisted
// tracker-quality snapshot (see internal/scraper's
// PersistedTracker, which this mirrors field for field; the store
// attaches no meaning to the weights beyond ordering by the
// success ratio they imply).
type TrackerQuality struct {
	Host      string  // "host:port" of the tracker
	SuccW     float64 // decayed response weight (prior included)
	FailW     float64 // decayed failure weight (prior included)
	SeededW   float64 // decayed weight of answered hashes it reported seeders for
	AskedW    float64 // decayed weight of hashes it answered for
	LatencyMs float64 // EWMA of successful scrape latency; 0 = none measured
	Attempts  int64   // raw lifetime observations at save time
	UpdatedAt int64   // unix seconds of the last observation
}

// SaveTrackerQuality replaces the whole snapshot with trackers.
// The scraper saves its proven set (at most one row per configured
// tracker), so replace-all in one transaction is both the simplest
// and the self-pruning semantics: trackers removed from the
// configuration do not linger in the table.
func (s *Store) SaveTrackerQuality(ctx context.Context, trackers []TrackerQuality) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.d.rebind(`DELETE FROM tracker_quality`)); err != nil {
		return err
	}
	ins, err := tx.Prepare(s.d.rebind(
		`INSERT INTO tracker_quality (host, succ_w, fail_w, seeded_w, asked_w, latency_ms, attempts, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`))
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, tr := range trackers {
		if _, err := ins.Exec(tr.Host, tr.SuccW, tr.FailW, tr.SeededW, tr.AskedW, tr.LatencyMs, tr.Attempts, tr.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadTrackerQuality returns the saved snapshot, best success
// ratio first. limit <= 0 selects every saved tracker (the
// configured tracker list is operator-sized, so the snapshot is
// small by construction). An empty table (no previous run, or
// persistence never landed) yields an empty slice and no error.
func (s *Store) LoadTrackerQuality(ctx context.Context, limit int) ([]TrackerQuality, error) {
	if limit <= 0 {
		limit = 4096
	}
	rows, err := s.queryContext(ctx,
		`SELECT host, succ_w, fail_w, seeded_w, asked_w, latency_ms, attempts, updated_at
		 FROM tracker_quality
		 ORDER BY succ_w / (succ_w + fail_w) DESC, updated_at DESC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackerQuality
	for rows.Next() {
		var tr TrackerQuality
		if err := rows.Scan(&tr.Host, &tr.SuccW, &tr.FailW, &tr.SeededW, &tr.AskedW, &tr.LatencyMs, &tr.Attempts, &tr.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}
