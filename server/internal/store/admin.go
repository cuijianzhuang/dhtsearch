package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
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

// Sort orders accepted by AdminListEx. The value set is the admin
// console's own (the public search's SearchEx sorts are a different,
// smaller set): the operator triages a 100k+ row table, so both
// directions of the triage-relevant columns are offered.
const (
	AdminSortNewest    = "newest"     // created_at DESC (the historical default)
	AdminSortOldest    = "oldest"     // created_at ASC
	AdminSortSizeDesc  = "size_desc"  // total_size DESC
	AdminSortSizeAsc   = "size_asc"   // total_size ASC
	AdminSortFilesDesc = "files_desc" // file_count DESC
	AdminSortSeeders   = "seeders"    // scraped seeder count DESC (see sortBySeeders)
)

// NormalizeAdminSort maps any input to a valid admin sort order:
// "" and unrecognized values fall back to AdminSortNewest, so a
// hand-typed or stale ?sort= can never break the list.
func NormalizeAdminSort(s string) string {
	switch s {
	case AdminSortNewest, AdminSortOldest, AdminSortSizeDesc,
		AdminSortSizeAsc, AdminSortFilesDesc, AdminSortSeeders:
		return s
	}
	return AdminSortNewest
}

// adminSortSQL is the ORDER BY clause for the sorts SQL can express.
// Every clause ends in a deterministic tiebreak (created_at, then
// info_hash) mirroring the in-memory comparator adminSortRows uses,
// so the SQL path and the in-memory path (category filter / seeders
// sort) order identically, on both dialects.
var adminSortSQL = map[string]string{
	AdminSortNewest:    `created_at DESC, info_hash ASC`,
	AdminSortOldest:    `created_at ASC, info_hash ASC`,
	AdminSortSizeDesc:  `total_size DESC, created_at DESC, info_hash ASC`,
	AdminSortSizeAsc:   `total_size ASC, created_at DESC, info_hash ASC`,
	AdminSortFilesDesc: `file_count DESC, created_at DESC, info_hash ASC`,
}

// Size filter buckets for AdminListEx, keyed by the ?size= value.
// Ranges are half-open [min, max) in bytes; a negative max means
// unbounded. The boundaries are binary (MiB/GiB), matching how the
// UI labels them (10 MB means 10 MiB, as everywhere sizes display).
const AdminSizeAll = ""

var adminSizeBuckets = map[string]struct{ min, max int64 }{
	"lt10m":    {0, 10 << 20},
	"10m_100m": {10 << 20, 100 << 20},
	"100m_1g":  {100 << 20, 1 << 30},
	"1g_10g":   {1 << 30, 10 << 30},
	"gt10g":    {10 << 30, -1},
}

// AdminSizeRange resolves a size bucket key to its byte range.
// ok is false for an unrecognized key ("" included: it means "all
// sizes" and needs no range at all).
func AdminSizeRange(bucket string) (min, max int64, ok bool) {
	b, ok := adminSizeBuckets[bucket]
	if !ok {
		return 0, 0, false
	}
	return b.min, b.max, true
}

// ValidAdminSize reports whether bucket is "" (all sizes) or a known
// bucket key.
func ValidAdminSize(bucket string) bool {
	if bucket == AdminSizeAll {
		return true
	}
	_, ok := adminSizeBuckets[bucket]
	return ok
}

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

// AdminListParams bundles the admin torrent list's knobs. Query and
// Status are the historical filters; Sort, Category and Size are the
// added ones. All filters combine with AND. Invalid Sort/Category/
// Size values are normalized to the defaults (newest / all / all)
// rather than rejected, mirroring NormalizeAdminSort.
type AdminListParams struct {
	Query    string
	Status   string // AdminStatus* constant
	Sort     string // AdminSort* constant; "" means newest
	Category string // CategoryOf key; "" means all categories
	Size     string // size bucket key; "" means all sizes
	Page     int
	PageSize int
}

// AdminList returns one page of torrents for the admin console, newest
// first, filtered by review status and the same keyword semantics as
// public Search. Files are not loaded; use AdminGet for one torrent.
// It is AdminListEx with the default sort and no category/size filter.
func (s *Store) AdminList(ctx context.Context, query, status string, page, pageSize int) (items []AdminTorrent, total int, err error) {
	return s.AdminListEx(ctx, AdminListParams{Query: query, Status: status, Page: page, PageSize: pageSize})
}

// AdminListEx is AdminList plus sorting and category/size filtering.
//
// The keyword, status and size filters and every sort except seeders
// are plain SQL, so the common case stays a COUNT + one paginated
// query. Two features cannot be expressed in SQL, exactly as in
// SearchEx: a torrent's category is computed from its file list
// (CategoryOf), and seeders live in swarm_stats. When either is in
// play, every match is loaded, classified / joined and sorted in
// memory with adminSortRows — the same total order adminSortSQL
// encodes — and paged there. The returned items never carry Files:
// classification reads the blobs into a scratch column only.
func (s *Store) AdminListEx(ctx context.Context, p AdminListParams) (items []AdminTorrent, total int, err error) {
	page := p.Page
	if page < 1 {
		page = 1
	}
	pageSize := p.PageSize
	if pageSize < 1 {
		pageSize = 20
	}
	sortOrder := NormalizeAdminSort(p.Sort)
	category := p.Category
	if !ValidCategory(category) {
		category = ""
	}
	size := p.Size
	if !ValidAdminSize(size) {
		size = AdminSizeAll
	}

	var conds []string
	var args []interface{}
	if kw, kwArgs := s.keywordWhere(p.Query); kw != "" {
		conds = append(conds, kw)
		args = append(args, kwArgs...)
	}
	switch p.Status {
	case AdminStatusUnreviewed:
		conds = append(conds, "reviewed_at = 0")
	case AdminStatusReviewed:
		conds = append(conds, "reviewed_at <> 0")
	}
	if size != AdminSizeAll {
		min, max, _ := AdminSizeRange(size)
		conds = append(conds, "total_size >= ?")
		args = append(args, min)
		if max >= 0 {
			conds = append(conds, "total_size < ?")
			args = append(args, max)
		}
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	// The display-name expression differs per dialect, so the name column
	// is selected twice: once raw, once through the expression.
	cols := `info_hash, name, ` + s.d.displayNameExpr() + `, clean_name, alias, total_size, file_count, created_at, reviewed_at`

	if category == "" && sortOrder != AdminSortSeeders {
		// SQL path: count, then one ordered page.
		if err = s.queryRowContext(ctx, `SELECT COUNT(*) FROM torrents`+where, args...).Scan(&total); err != nil {
			return nil, 0, err
		}
		q := `SELECT ` + cols + ` FROM torrents` + where +
			` ORDER BY ` + adminSortSQL[sortOrder] + ` LIMIT ? OFFSET ?`
		rows, err := s.queryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
		if err != nil {
			return nil, 0, err
		}
		items, err = scanAdminList(rows)
		return items, total, err
	}

	// In-memory path: load every match (with the files blob only when
	// classifying), filter by category, sort, then cut the page.
	if category != "" {
		cols += `, files`
	}
	rows, err := s.queryContext(ctx, `SELECT `+cols+` FROM torrents`+where, args...)
	if err != nil {
		return nil, 0, err
	}
	all, err := scanAdminListFiles(rows, category != "")
	if err != nil {
		return nil, 0, err
	}
	if category != "" {
		kept := all[:0]
		for _, t := range all {
			if t.category == category {
				kept = append(kept, t)
			}
		}
		all = kept
	}
	var swarm map[string]SwarmStats
	if sortOrder == AdminSortSeeders {
		hashes := make([]string, len(all))
		for i, t := range all {
			hashes[i] = t.InfoHash
		}
		if swarm, err = s.SwarmStatsFor(ctx, hashes); err != nil {
			return nil, 0, err
		}
	}
	sortAdminRows(all, sortOrder, swarm)
	total = len(all)
	start := (page - 1) * pageSize
	if start >= total {
		return []AdminTorrent{}, total, nil
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	items = make([]AdminTorrent, 0, end-start)
	for _, t := range all[start:end] {
		items = append(items, t.AdminTorrent)
	}
	return items, total, nil
}

// adminRow is an AdminTorrent plus the category computed from its
// file list, carried only through AdminListEx's in-memory path.
type adminRow struct {
	AdminTorrent
	category string
}

// scanAdminListFiles drains rows of the AdminList projection; with
// withFiles the projection's trailing files blob is decompressed per
// row and classified with CategoryOf (against the display name — the
// same input scanTorrents classifies by), then discarded.
func scanAdminListFiles(rows *sql.Rows, withFiles bool) (out []adminRow, err error) {
	defer rows.Close()
	for rows.Next() {
		var r adminRow
		if withFiles {
			var fb []byte
			if err := rows.Scan(&r.InfoHash, &r.RawName, &r.Name, &r.CleanName, &r.Alias,
				&r.TotalSize, &r.FileCount, &r.CreatedAt, &r.ReviewedAt, &fb); err != nil {
				return nil, err
			}
			fj, err := decompressFiles(fb)
			if err != nil {
				return nil, err
			}
			var files []filter.File
			if err := json.Unmarshal(fj, &files); err != nil {
				return nil, err
			}
			r.category = CategoryOf(files, r.Name)
		} else {
			if err := rows.Scan(&r.InfoHash, &r.RawName, &r.Name, &r.CleanName, &r.Alias,
				&r.TotalSize, &r.FileCount, &r.CreatedAt, &r.ReviewedAt); err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// sortAdminRows orders rows by the same total order adminSortSQL
// encodes for SQL-expressible sorts. The seeders case reuses
// sortBySeeders' rule — snapshot count descending, no snapshot below
// a scraped true zero, then created_at descending, then info_hash —
// applied to the admin projection.
func sortAdminRows(rows []adminRow, sortOrder string, swarm map[string]SwarmStats) {
	byCreatedHash := func(a, b *adminRow, createdDesc bool) bool {
		if a.CreatedAt != b.CreatedAt {
			if createdDesc {
				return a.CreatedAt > b.CreatedAt
			}
			return a.CreatedAt < b.CreatedAt
		}
		return a.InfoHash < b.InfoHash
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := &rows[i], &rows[j]
		switch sortOrder {
		case AdminSortOldest:
			return byCreatedHash(a, b, false)
		case AdminSortSizeDesc:
			if a.TotalSize != b.TotalSize {
				return a.TotalSize > b.TotalSize
			}
		case AdminSortSizeAsc:
			if a.TotalSize != b.TotalSize {
				return a.TotalSize < b.TotalSize
			}
		case AdminSortFilesDesc:
			if a.FileCount != b.FileCount {
				return a.FileCount > b.FileCount
			}
		case AdminSortSeeders:
			sa, sb := int64(-1), int64(-1)
			if sw, ok := swarm[a.InfoHash]; ok {
				sa = sw.Seeders
			}
			if sw, ok := swarm[b.InfoHash]; ok {
				sb = sw.Seeders
			}
			if sa != sb {
				return sa > sb
			}
		}
		// AdminSortNewest and every tie above: created_at DESC,
		// info_hash ASC.
		return byCreatedHash(a, b, true)
	})
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
	return s.adminGet(ctx, infoHash, true)
}

// AdminGetMeta is AdminGet without the file list: the detail endpoint
// serves metadata only and pages the files through FilesPage, so a
// torrent with thousands of files no longer pays a full decompress
// (or ships a ~300 KB response) just to render its header.
func (s *Store) AdminGetMeta(ctx context.Context, infoHash string) (*AdminTorrent, error) {
	return s.adminGet(ctx, infoHash, false)
}

func (s *Store) adminGet(ctx context.Context, infoHash string, withFiles bool) (*AdminTorrent, error) {
	cols := `info_hash, name, ` + s.d.displayNameExpr() + `, clean_name, alias, total_size, file_count, created_at, reviewed_at`
	if withFiles {
		cols = `info_hash, name, ` + s.d.displayNameExpr() + `, clean_name, alias, total_size, file_count, files, created_at, reviewed_at`
	}
	row := s.queryRowContext(ctx,
		`SELECT `+cols+` FROM torrents WHERE info_hash = ?`, infoHash)
	var t AdminTorrent
	var fb []byte
	var err error
	if withFiles {
		err = row.Scan(&t.InfoHash, &t.RawName, &t.Name, &t.CleanName, &t.Alias,
			&t.TotalSize, &t.FileCount, &fb, &t.CreatedAt, &t.ReviewedAt)
	} else {
		err = row.Scan(&t.InfoHash, &t.RawName, &t.Name, &t.CleanName, &t.Alias,
			&t.TotalSize, &t.FileCount, &t.CreatedAt, &t.ReviewedAt)
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if withFiles {
		fj, err := decompressFiles(fb)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(fj, &t.Files); err != nil {
			return nil, err
		}
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

// CreatedBuckets counts torrents per fixed-width time bucket over the
// window [start, start+buckets*bucketSec): bucket i covers
// [start+i*bucketSec, start+(i+1)*bucketSec). The result maps bucket
// index to count; empty buckets are absent, so callers materialise the
// full series themselves. The bucket index is integer division in SQL:
// for the non-negative offsets involved here (created_at >= start)
// SQLite and Postgres truncate identically, so one query serves both
// dialects — the trends endpoint depends on that parity.
func (s *Store) CreatedBuckets(ctx context.Context, start, bucketSec int64, buckets int) (map[int]int64, error) {
	rows, err := s.queryContext(ctx,
		`SELECT (created_at - ?) / ? AS b, COUNT(*) FROM torrents WHERE created_at >= ? AND created_at < ? GROUP BY b ORDER BY b`,
		start, bucketSec, start, start+bucketSec*int64(buckets))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]int64{}
	for rows.Next() {
		var b int
		var n int64
		if err := rows.Scan(&b, &n); err != nil {
			return nil, err
		}
		out[b] = n
	}
	return out, rows.Err()
}
