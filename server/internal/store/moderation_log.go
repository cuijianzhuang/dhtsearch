package store

import "context"

// ModerationRecord is one entry of the LLM moderation audit log: a
// torrent the pass classified, the label it received and what the
// pass did with it. Action is "kept" (label ok), "deleted" (removed
// and blocklisted) or "would_delete" (dry-run removal).
type ModerationRecord struct {
	InfoHash  string `json:"info_hash"`
	Name      string `json:"name"`
	Label     string `json:"label"`
	Action    string `json:"action"`
	CreatedAt int64  `json:"created_at"`
}

// moderationLogKeep bounds the audit log: only the newest this many
// rows are retained, pruned on every LogModeration batch. The log is
// an operator convenience, not a compliance record — the blocked
// table remains the durable list of what was removed.
const moderationLogKeep = 1000

// LogModeration appends one batch of classification records (all
// stamped ts) and prunes the log back to the newest
// moderationLogKeep rows.
func (s *Store) LogModeration(recs []ModerationRecord, ts int64) error {
	if len(recs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	ins, err := tx.Prepare(s.d.rebind(
		`INSERT INTO moderation_log (info_hash, name, label, action, created_at) VALUES (?, ?, ?, ?, ?)`))
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, r := range recs {
		if _, err := ins.Exec(r.InfoHash, r.Name, r.Label, r.Action, ts); err != nil {
			return err
		}
	}
	// Keep the newest moderationLogKeep rows: delete everything at or
	// below the id that sits that far down the newest-first order.
	// With fewer rows the subquery yields NULL and nothing matches.
	if _, err := tx.Exec(s.d.rebind(
		`DELETE FROM moderation_log WHERE id <= (
			SELECT id FROM moderation_log ORDER BY id DESC LIMIT 1 OFFSET ?
		)`), moderationLogKeep); err != nil {
		return err
	}
	return tx.Commit()
}

// RecentModeration returns the newest moderation log entries,
// newest first. limit is clamped to [1, 200].
func (s *Store) RecentModeration(ctx context.Context, limit int) ([]ModerationRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.queryContext(ctx,
		`SELECT info_hash, name, label, action, created_at FROM moderation_log
		 ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModerationRecord{}
	for rows.Next() {
		var r ModerationRecord
		if err := rows.Scan(&r.InfoHash, &r.Name, &r.Label, &r.Action, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
