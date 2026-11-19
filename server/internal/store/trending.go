package store

import (
	"context"
	"database/sql"
	"errors"
)

// trendingStateKey is the single row the trending cache occupies in
// the trending_cache table. The value is an opaque blob owned by
// internal/trending (a JSON document); the store attaches no meaning
// to it, the same split as the settings table.
const trendingStateKey = "state"

// SaveTrendingState persists the trending service's last good state
// (snapshot plus resolved-name cache) so a restart can serve the
// charts immediately instead of waiting for a successful refetch.
func (s *Store) SaveTrendingState(ctx context.Context, data []byte) error {
	_, err := s.execContext(ctx,
		`INSERT INTO trending_cache (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		trendingStateKey, string(data))
	return err
}

// LoadTrendingState returns the blob SaveTrendingState last wrote, or
// (nil, nil) when no state has been persisted yet.
func (s *Store) LoadTrendingState(ctx context.Context) ([]byte, error) {
	var v string
	err := s.queryRowContext(ctx,
		`SELECT value FROM trending_cache WHERE key = ?`, trendingStateKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []byte(v), nil
}
