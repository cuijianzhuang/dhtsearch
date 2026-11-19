package store

import (
	"context"
	"fmt"
)

// LoadSettings returns every operator override row in the settings
// table as a key -> canonical string value map. It backs the settings
// registry's storage interface (internal/settings); the store itself
// attaches no meaning to the keys.
func (s *Store) LoadSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.queryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SaveSettings applies one batch of override changes atomically: keys
// in set are upserted with the given values, keys in del are removed
// (reverting those settings to their env/default layer). ts is the
// write time recorded for auditing; it plays no role in resolution.
func (s *Store) SaveSettings(ctx context.Context, set map[string]string, del []string, ts int64) error {
	if len(set) == 0 && len(del) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if len(set) > 0 {
		up, err := tx.Prepare(s.d.rebind(s.d.upsertSetting()))
		if err != nil {
			return err
		}
		defer up.Close()
		for k, v := range set {
			if _, err := up.Exec(k, v, ts); err != nil {
				return fmt.Errorf("save setting %s: %w", k, err)
			}
		}
	}
	if len(del) > 0 {
		st, err := tx.Prepare(s.d.rebind(`DELETE FROM settings WHERE key = ?`))
		if err != nil {
			return err
		}
		defer st.Close()
		for _, k := range del {
			if _, err := st.Exec(k); err != nil {
				return fmt.Errorf("delete setting %s: %w", k, err)
			}
		}
	}
	return tx.Commit()
}
