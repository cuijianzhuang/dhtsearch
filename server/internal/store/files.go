package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"dhtsearch/server/internal/filter"
)

// FilesPage returns one page of a torrent's stored file list plus the
// total number of stored entries. found is false when the info hash is
// not indexed at all (the caller answers 404); a page past the end is
// not an error and simply yields an empty slice with the real total.
//
// Files live in a single compressed blob column, so paging cannot be
// pushed into SQL: the blob is decompressed and sliced here. That is
// still the right trade — the blob is one row either way, and what
// paging actually saves is the response body and the client's DOM,
// which were the measured costs (a 5000-file torrent produced a
// ~300 KB detail response and ~15k DOM nodes). The slice order is the
// stored order (metadata order), identical on both backends because
// both read the same blob.
func (s *Store) FilesPage(ctx context.Context, infoHash string, page, pageSize int) (files []filter.File, total int, found bool, err error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 1
	}
	var fb []byte
	err = s.queryRowContext(ctx, `SELECT files FROM torrents WHERE info_hash = ?`, infoHash).Scan(&fb)
	if err == sql.ErrNoRows {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	fj, err := decompressFiles(fb)
	if err != nil {
		return nil, 0, true, err
	}
	var all []filter.File
	if err := json.Unmarshal(fj, &all); err != nil {
		return nil, 0, true, err
	}
	total = len(all)
	start := (page - 1) * pageSize
	if start >= total {
		return []filter.File{}, total, true, nil
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return all[start:end], total, true, nil
}
