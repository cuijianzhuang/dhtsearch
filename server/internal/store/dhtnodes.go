package store

import (
	"context"
)

// DHTNode is one row of the crawler's persisted node-quality snapshot
// (see internal/crawler's PersistedNode, which this mirrors field for
// field; the store attaches no meaning to the weights beyond ordering
// by the success ratio they imply).
type DHTNode struct {
	Addr      string  // host:port of the DHT node
	SuccW     float64 // decayed success weight (prior included)
	FailW     float64 // decayed failure weight (prior included)
	LatencyMs float64 // EWMA of successful query latency; 0 = none measured
	Attempts  int64   // raw lifetime observations at save time
	UpdatedAt int64   // unix seconds of the last observation
}

// SaveDHTNodes replaces the whole snapshot with nodes. The crawler
// saves its current top set (a few hundred rows at most), so
// replace-all in one transaction is both the simplest and the
// self-pruning semantics: nodes that fell out of the top set do not
// linger in the table.
func (s *Store) SaveDHTNodes(ctx context.Context, nodes []DHTNode) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(s.d.rebind(`DELETE FROM dht_nodes`)); err != nil {
		return err
	}
	ins, err := tx.Prepare(s.d.rebind(
		`INSERT INTO dht_nodes (addr, succ_w, fail_w, latency_ms, attempts, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`))
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, n := range nodes {
		if _, err := ins.Exec(n.Addr, n.SuccW, n.FailW, n.LatencyMs, n.Attempts, n.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// LoadDHTNodes returns the saved snapshot, best success ratio first.
// limit <= 0 selects the crawler's usual snapshot size. An empty
// table (no previous run, or persistence never landed) yields an
// empty slice and no error.
func (s *Store) LoadDHTNodes(ctx context.Context, limit int) ([]DHTNode, error) {
	if limit <= 0 {
		limit = 512
	}
	rows, err := s.queryContext(ctx,
		`SELECT addr, succ_w, fail_w, latency_ms, attempts, updated_at
		 FROM dht_nodes
		 ORDER BY succ_w / (succ_w + fail_w) DESC, updated_at DESC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DHTNode
	for rows.Next() {
		var n DHTNode
		if err := rows.Scan(&n.Addr, &n.SuccW, &n.FailW, &n.LatencyMs, &n.Attempts, &n.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
