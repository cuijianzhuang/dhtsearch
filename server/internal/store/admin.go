package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"dhtsearch/server/internal/filter"
)

// AdminTorrent is the management projection of a torrent: unlike Torrent
// it carries the raw name and the moderation state, because the admin
// console's whole job is reviewing what the public search displays.
// Name is the display name (cleaned title when one exists), RawName is
// the title as indexed.
type AdminTorrent struct {
	InfoHash   string        `json:"info_hash"`
	Name       string        `json:"name"`
	RawName    string        `json:"raw_name"`
	CleanName  string        `json:"clean_name,omitempty"`
	Alias      string        `json:"alias,omitempty"`
	TotalSize  int64         `json:"total_size"`
	FileCount  int           `json:"file_count"`
	Files      []filter.File `json:"files,omitempty"`
	CreatedAt  int64         `json:"created_at"`
	ReviewedAt int64         `json:"reviewed_at"`
}

// BlockedEntry is one row of the blocked table, shown in the admin
// console so a rejection can be undone (Unblock).
type BlockedEntry struct {
	InfoHash  string `json:"info_hash"`
	Reason    string `json:"reason"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
}

// Admin review-status filters for AdminList.
const (
	AdminStatusAll        = ""
	AdminStatusUnreviewed = "unreviewed"
	AdminStatusReviewed   = "reviewed"
)

// keywordWhere builds the same AND-of-keywords LIKE conditions Search
// uses (raw name, cleaned name and alias), for the admin list. The
// returned fragment has no leading WHERE.
func (s *Store) keywordWhere(query string) (string, []interface{}) {
	keywords := strings.Fields(query)
	if len(keywords) > maxKeywords {
		keywords = keywords[:maxKeywords]
	}
	if len(keywords) == 0 {
		return "", nil
	}
	op := s.d.likeOp()
	var conds []string
	var args []interface{}
	for _, kw := range keywords {
		conds = append(conds, "(name "+op+" ? ESCAPE '\\' OR clean_name "+op+" ? ESCAPE '\\' OR alias "+op+" ? ESCAPE '\\')")
		like := "%" + escapeLike(kw) + "%"
		args = append(args, like, like, like)
	}
	return strings.Join(conds, " AND "), args
}

// AdminList returns one page of torrents for the admin console, newest
// first, filtered by review status and the same keyword semantics as
// public Search. Files are not loaded; use AdminGet for one torrent.
func (s *Store) AdminList(ctx context.Context, query, status string, page, pageSize int) (items []AdminTorrent, total int, err error) {
	if page < 1 {
		page = 1
	}
	var conds []string
	var args []interface{}
	if kw, kwArgs := s.keywordWhere(query); kw != "" {
		conds = append(conds, kw)
		args = append(args, kwArgs...)
	}
	switch status {
	case AdminStatusUnreviewed:
		conds = append(conds, "reviewed_at = 0")
	case AdminStatusReviewed:
		conds = append(conds, "reviewed_at <> 0")
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	if err = s.queryRowContext(ctx, `SELECT COUNT(*) FROM torrents`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	// The display-name expression differs per dialect, so the name column
	// is selected twice: once raw, once through the expression.
	q := `SELECT info_hash, name, ` + s.d.displayNameExpr() + `, clean_name, alias, total_size, file_count, created_at, reviewed_at FROM torrents` +
		where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	rows, err := s.queryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	items, err = scanAdminList(rows)
	return items, total, err
}

// scanAdminList drains rows of the AdminList projection.
func scanAdminList(rows *sql.Rows) (items []AdminTorrent, err error) {
	defer rows.Close()
	for rows.Next() {
		var t AdminTorrent
		if err := rows.Scan(&t.InfoHash, &t.RawName, &t.Name, &t.CleanName, &t.Alias,
			&t.TotalSize, &t.FileCount, &t.CreatedAt, &t.ReviewedAt); err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return items, rows.Err()
}

// AdminGet returns one torrent with its file list, or (nil, nil) when the
// hash is not indexed.
func (s *Store) AdminGet(ctx context.Context, infoHash string) (*AdminTorrent, error) {
	row := s.queryRowContext(ctx,
		`SELECT info_hash, name, `+s.d.displayNameExpr()+`, clean_name, alias, total_size, file_count, files, created_at, reviewed_at FROM torrents WHERE info_hash = ?`,
		infoHash)
	var t AdminTorrent
	var fb []byte
	err := row.Scan(&t.InfoHash, &t.RawName, &t.Name, &t.CleanName, &t.Alias,
		&t.TotalSize, &t.FileCount, &fb, &t.CreatedAt, &t.ReviewedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	fj, err := decompressFiles(fb)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(fj, &t.Files); err != nil {
		return nil, err
	}
	return &t, nil
}

// AdminDelete removes one torrent WITHOUT recording it in blocked: the
// hash may be indexed again if the crawler rediscovers it. (Block is the
// permanent rejection.) Reports whether a row was deleted.
func (s *Store) AdminDelete(ctx context.Context, infoHash string) (bool, error) {
	res, err := s.execContext(ctx, `DELETE FROM torrents WHERE info_hash = ?`, infoHash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AdminUnblock removes one entry from blocked, so a future discovery of
// the hash can be indexed again. Reports whether an entry existed.
func (s *Store) AdminUnblock(ctx context.Context, infoHash string) (bool, error) {
	res, err := s.execContext(ctx, `DELETE FROM blocked WHERE info_hash = ?`, infoHash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AdminBlockedList returns one page of blocked entries, newest first.
func (s *Store) AdminBlockedList(ctx context.Context, page, pageSize int) (items []BlockedEntry, total int, err error) {
	if page < 1 {
		page = 1
	}
	if err = s.queryRowContext(ctx, `SELECT COUNT(*) FROM blocked`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.queryContext(ctx,
		`SELECT info_hash, reason, name, created_at FROM blocked ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var e BlockedEntry
		if err := rows.Scan(&e.InfoHash, &e.Reason, &e.Name, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// CountCreatedSince counts torrents indexed at or after the unix
// timestamp since (the admin overview's "added today" card passes local
// midnight).
func (s *Store) CountCreatedSince(ctx context.Context, since int64) (int, error) {
	var n int
	err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM torrents WHERE created_at >= ?`, since).Scan(&n)
	return n, err
}
