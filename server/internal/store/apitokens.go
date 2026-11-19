package store

import (
	"context"
	"database/sql"
	"errors"
)

// APIToken is one public-API credential as the store keeps it. The
// plaintext token is never stored: TokenHash is the hex SHA-256 of
// the full token string, Prefix is its display-safe head. The API
// layer generates the plaintext, hashes it and hands both in at
// creation time.
type APIToken struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	TokenHash  string `json:"-"`
	Enabled    bool   `json:"enabled"`
	RatePerMin int    `json:"rate_per_min"` // 0 = unlimited
	CallCount  int64  `json:"call_count"`
	LastUsedAt int64  `json:"last_used_at"` // unix seconds, 0 = never
	CreatedAt  int64  `json:"created_at"`
}

const apiTokenCols = `id, name, prefix, token_hash, enabled, rate_per_min, call_count, last_used_at, created_at`

func scanAPIToken(row interface{ Scan(...interface{}) error }) (APIToken, error) {
	var t APIToken
	var enabled int
	err := row.Scan(&t.ID, &t.Name, &t.Prefix, &t.TokenHash, &enabled,
		&t.RatePerMin, &t.CallCount, &t.LastUsedAt, &t.CreatedAt)
	t.Enabled = enabled != 0
	return t, err
}

// CreateAPIToken inserts a new token row and returns it with its
// assigned id. The read-back goes through the unique token_hash
// because LastInsertId is not portable (pgx stdlib does not support
// it).
func (s *Store) CreateAPIToken(ctx context.Context, name, prefix, tokenHash string, ratePerMin int, ts int64) (APIToken, error) {
	if _, err := s.execContext(ctx,
		`INSERT INTO api_tokens (name, prefix, token_hash, enabled, rate_per_min, call_count, last_used_at, created_at)
		 VALUES (?, ?, ?, 1, ?, 0, 0, ?)`,
		name, prefix, tokenHash, ratePerMin, ts); err != nil {
		return APIToken{}, err
	}
	tok, err := s.APITokenByHash(ctx, tokenHash)
	if err != nil {
		return APIToken{}, err
	}
	if tok == nil {
		return APIToken{}, errors.New("api token vanished after insert")
	}
	return *tok, nil
}

// APITokenByHash resolves a presented token's hash to its row, or
// (nil, nil) when no token matches. This is the per-request auth
// lookup; token_hash is unique-indexed on both backends.
func (s *Store) APITokenByHash(ctx context.Context, tokenHash string) (*APIToken, error) {
	t, err := scanAPIToken(s.queryRowContext(ctx,
		`SELECT `+apiTokenCols+` FROM api_tokens WHERE token_hash = ?`, tokenHash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// ListAPITokens returns every token, newest first, for the admin
// console. Token counts are operator-scale (tens, not thousands), so
// no pagination.
func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.queryContext(ctx,
		`SELECT `+apiTokenCols+` FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetAPITokenEnabled flips a token's enabled flag. Reports whether
// the token exists.
func (s *Store) SetAPITokenEnabled(ctx context.Context, id int64, enabled bool) (bool, error) {
	v := 0
	if enabled {
		v = 1
	}
	res, err := s.execContext(ctx, `UPDATE api_tokens SET enabled = ? WHERE id = ?`, v, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetAPITokenRate replaces a token's per-minute quota (0 =
// unlimited). Reports whether the token exists.
func (s *Store) SetAPITokenRate(ctx context.Context, id int64, ratePerMin int) (bool, error) {
	res, err := s.execContext(ctx, `UPDATE api_tokens SET rate_per_min = ? WHERE id = ?`, ratePerMin, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteAPIToken removes a token row outright; the credential stops
// working with the next request. Reports whether it existed.
func (s *Store) DeleteAPIToken(ctx context.Context, id int64) (bool, error) {
	res, err := s.execContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TouchAPIToken records one authenticated call: the lifetime counter
// goes up by one and last_used_at moves to ts. One atomic UPDATE on
// both backends, the same cost class as IncrStat.
func (s *Store) TouchAPIToken(ctx context.Context, id int64, ts int64) error {
	_, err := s.execContext(ctx,
		`UPDATE api_tokens SET call_count = call_count + 1, last_used_at = ? WHERE id = ?`, ts, id)
	return err
}
