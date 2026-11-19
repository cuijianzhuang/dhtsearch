package store

import (
	"context"
	"database/sql"
)

// Report is one visitor report against an indexed torrent, worked from
// the admin console's report queue. Reason holds one of the preset keys
// the public API validates (adult/spam/infringement/other); Note is the
// reporter's free-text addition. Reporter is the rate-limit identity of
// the submitting client (see api.clientKey), kept for abuse forensics.
type Report struct {
	ID        int64  `json:"id"`
	InfoHash  string `json:"info_hash"`
	Name      string `json:"name"` // display name of the torrent; "" once it is gone
	Reason    string `json:"reason"`
	Note      string `json:"note,omitempty"`
	Status    int    `json:"status"`
	CreatedAt int64  `json:"created_at"`
}

// Report lifecycle states.
const (
	ReportPending   = 0
	ReportResolved  = 1
	ReportDismissed = 2
)

// ReportStatusAll is the AdminListReports filter for "any status".
const ReportStatusAll = -1

// AddReport records a visitor report. The API layer validates the
// reason and rate-limits per client+torrent before calling this.
func (s *Store) AddReport(ctx context.Context, infoHash, reason, note, reporter string, ts int64) error {
	_, err := s.execContext(ctx,
		`INSERT INTO reports (info_hash, reason, note, reporter, status, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		infoHash, reason, note, reporter, ReportPending, ts)
	return err
}

// ReportByID returns one report, or (nil, nil) when it does not exist.
func (s *Store) ReportByID(ctx context.Context, id int64) (*Report, error) {
	row := s.queryRowContext(ctx,
		`SELECT id, info_hash, reason, note, status, created_at FROM reports WHERE id = ?`, id)
	var r Report
	err := row.Scan(&r.ID, &r.InfoHash, &r.Reason, &r.Note, &r.Status, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// AdminListReports returns one page of the report queue, newest first.
// status is one of the Report* states or ReportStatusAll. The torrent's
// display name is joined in (raw + clean, resolved in Go the same way
// displayNameExpr does) and stays empty when the torrent has since
// been deleted or blocked.
func (s *Store) AdminListReports(ctx context.Context, status, page, pageSize int) (items []Report, total int, err error) {
	if page < 1 {
		page = 1
	}
	where, args := "", []interface{}{}
	if status != ReportStatusAll {
		where = " WHERE r.status = ?"
		args = append(args, status)
	}
	if err = s.queryRowContext(ctx, `SELECT COUNT(*) FROM reports r`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.queryContext(ctx,
		`SELECT r.id, r.info_hash, r.reason, r.note, r.status, r.created_at,
			COALESCE(t.name, ''), COALESCE(t.clean_name, '')
		 FROM reports r LEFT JOIN torrents t ON t.info_hash = r.info_hash`+
			where+` ORDER BY r.created_at DESC, r.id DESC LIMIT ? OFFSET ?`,
		append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Report
		var rawName, cleanName string
		if err := rows.Scan(&r.ID, &r.InfoHash, &r.Reason, &r.Note, &r.Status, &r.CreatedAt, &rawName, &cleanName); err != nil {
			return nil, 0, err
		}
		r.Name = rawName
		if cleanName != "" {
			r.Name = cleanName
		}
		items = append(items, r)
	}
	return items, total, rows.Err()
}

// AdminSetReportStatus moves a report to a new lifecycle state.
// Reports whether the report exists.
func (s *Store) AdminSetReportStatus(ctx context.Context, id int64, status int) (bool, error) {
	res, err := s.execContext(ctx, `UPDATE reports SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// PendingReportsCount returns how many reports await an operator.
func (s *Store) PendingReportsCount(ctx context.Context) (int, error) {
	var n int
	err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM reports WHERE status = ?`, ReportPending).Scan(&n)
	return n, err
}
