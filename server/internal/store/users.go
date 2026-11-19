package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
)

// Admin account roles. "admin" reaches every console surface;
// "reviewer" reaches only the moderation surfaces (overview, torrent
// review/block/delete, the blocked list) — never settings, never
// account management.
const (
	RoleAdmin    = "admin"
	RoleReviewer = "reviewer"
)

// Admin login sources: a hand-created local account, or an external
// identity linked on first federated login.
const (
	SourceLocal  = "local"
	SourceGitHub = "github"
	SourceOIDC   = "oidc"
)

// AdminUser is one admin console account. PasswordHash is a bcrypt
// hash for local accounts and empty for federated ones (they can
// never password-login). It is never serialized: API responses are
// built from explicit field lists, not from this struct.
type AdminUser struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string
	Source       string
	ExternalID   string
	Disabled     bool
	CreatedAt    int64
	UpdatedAt    int64
	LastLoginAt  int64
}

// AdminSession is one server-side admin session. Only the SHA-256 of
// the bearer token is stored. UserID is empty for sessions minted by
// the ADMIN_TOKEN recovery login.
type AdminSession struct {
	TokenHash string
	UserID    string
	Method    string // "token" | "password" | "github" | "oidc"
	CreatedAt int64
	ExpiresAt int64
}

// newID mints a random row id. Ids are generated here rather than by
// the database so the schema needs no AUTOINCREMENT/SERIAL split
// between the backends.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

const adminUserCols = `id, username, password_hash, role, source, external_id, disabled, created_at, updated_at, last_login_at`

func scanAdminUser(row interface{ Scan(...any) error }) (*AdminUser, error) {
	var u AdminUser
	var disabled int
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.Source,
		&u.ExternalID, &disabled, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt); err != nil {
		return nil, err
	}
	u.Disabled = disabled != 0
	return &u, nil
}

// CountAdminUsers returns how many console accounts exist. Zero is
// the bootstrap state: only the recovery token (and any configured
// federated login) can get in.
func (s *Store) CountAdminUsers(ctx context.Context) (int, error) {
	var n int
	err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM admin_users`).Scan(&n)
	return n, err
}

// CountEnabledAdminUsers counts accounts that could administer the
// console right now: role admin and not disabled. The last-admin
// guard compares against this so the account system cannot lock
// itself out (the recovery token remains as the final fallback).
func (s *Store) CountEnabledAdminUsers(ctx context.Context) (int, error) {
	var n int
	err := s.queryRowContext(ctx,
		`SELECT COUNT(*) FROM admin_users WHERE role = ? AND disabled = 0`, RoleAdmin).Scan(&n)
	return n, err
}

// CreateAdminUser inserts u, filling ID and timestamps. A username
// that is already taken surfaces as a unique violation (see
// IsUniqueViolation).
func (s *Store) CreateAdminUser(ctx context.Context, u *AdminUser, ts int64) error {
	if u.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		u.ID = id
	}
	u.CreatedAt, u.UpdatedAt = ts, ts
	disabled := 0
	if u.Disabled {
		disabled = 1
	}
	_, err := s.execContext(ctx,
		`INSERT INTO admin_users (`+adminUserCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.Username, u.PasswordHash, u.Role, u.Source, u.ExternalID,
		disabled, u.CreatedAt, u.UpdatedAt, u.LastLoginAt)
	return err
}

// AdminUserByUsername looks an account up by its login name. Returns
// (nil, nil) when no such account exists.
func (s *Store) AdminUserByUsername(ctx context.Context, username string) (*AdminUser, error) {
	row := s.queryRowContext(ctx, `SELECT `+adminUserCols+` FROM admin_users WHERE username = ?`, username)
	u, err := scanAdminUser(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// AdminUserByID looks an account up by row id.
func (s *Store) AdminUserByID(ctx context.Context, id string) (*AdminUser, error) {
	row := s.queryRowContext(ctx, `SELECT `+adminUserCols+` FROM admin_users WHERE id = ?`, id)
	u, err := scanAdminUser(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// AdminUserByExternal looks an account up by its federated identity:
// (source, external_id), e.g. ("github", "12345678") or
// ("oidc", "<sub claim>").
func (s *Store) AdminUserByExternal(ctx context.Context, source, externalID string) (*AdminUser, error) {
	row := s.queryRowContext(ctx,
		`SELECT `+adminUserCols+` FROM admin_users WHERE source = ? AND external_id = ?`, source, externalID)
	u, err := scanAdminUser(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// ListAdminUsers returns every account, oldest first.
func (s *Store) ListAdminUsers(ctx context.Context) ([]AdminUser, error) {
	rows, err := s.queryContext(ctx, `SELECT `+adminUserCols+` FROM admin_users ORDER BY created_at, username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminUser
	for rows.Next() {
		u, err := scanAdminUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// UpdateAdminUser writes back role, disabled state and password hash
// for the row with u.ID. Callers load the row, mutate the fields and
// save; the username and identity columns are immutable.
func (s *Store) UpdateAdminUser(ctx context.Context, u *AdminUser, ts int64) error {
	disabled := 0
	if u.Disabled {
		disabled = 1
	}
	u.UpdatedAt = ts
	_, err := s.execContext(ctx,
		`UPDATE admin_users SET role = ?, disabled = ?, password_hash = ?, updated_at = ? WHERE id = ?`,
		u.Role, disabled, u.PasswordHash, ts, u.ID)
	return err
}

// DeleteAdminUser removes the account row. Sessions are the caller's
// job (DeleteAdminUserSessions) so the two deletions can be ordered.
func (s *Store) DeleteAdminUser(ctx context.Context, id string) (bool, error) {
	res, err := s.execContext(ctx, `DELETE FROM admin_users WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// TouchAdminUserLogin stamps the last successful login time.
func (s *Store) TouchAdminUserLogin(ctx context.Context, id string, ts int64) error {
	_, err := s.execContext(ctx, `UPDATE admin_users SET last_login_at = ? WHERE id = ?`, ts, id)
	return err
}

// CreateAdminSession records a freshly minted session.
func (s *Store) CreateAdminSession(ctx context.Context, sess AdminSession) error {
	_, err := s.execContext(ctx,
		`INSERT INTO admin_sessions (token_hash, user_id, method, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		sess.TokenHash, sess.UserID, sess.Method, sess.CreatedAt, sess.ExpiresAt)
	return err
}

// AdminSessionByHash returns the session for a token hash, or
// (nil, nil). Expired rows are returned as-is; the caller checks the
// clock so it can also delete the row.
func (s *Store) AdminSessionByHash(ctx context.Context, tokenHash string) (*AdminSession, error) {
	var sess AdminSession
	err := s.queryRowContext(ctx,
		`SELECT token_hash, user_id, method, created_at, expires_at FROM admin_sessions WHERE token_hash = ?`,
		tokenHash).Scan(&sess.TokenHash, &sess.UserID, &sess.Method, &sess.CreatedAt, &sess.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// DeleteAdminSession ends one session (logout, expiry cleanup).
func (s *Store) DeleteAdminSession(ctx context.Context, tokenHash string) error {
	_, err := s.execContext(ctx, `DELETE FROM admin_sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteAdminUserSessions ends every session of one account: the
// password-change / disable / delete broom.
func (s *Store) DeleteAdminUserSessions(ctx context.Context, userID string) error {
	_, err := s.execContext(ctx, `DELETE FROM admin_sessions WHERE user_id = ?`, userID)
	return err
}

// DeleteAdminUserSessionsExcept ends every session of one account
// except keepHash — a self-service password change keeps the caller
// logged in while kicking every other device.
func (s *Store) DeleteAdminUserSessionsExcept(ctx context.Context, userID, keepHash string) error {
	_, err := s.execContext(ctx,
		`DELETE FROM admin_sessions WHERE user_id = ? AND token_hash <> ?`, userID, keepHash)
	return err
}
